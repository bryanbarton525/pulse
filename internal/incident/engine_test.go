package incident

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/bryanbarton525/pulse/internal/embed"
	"github.com/bryanbarton525/pulse/internal/observation"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// stubEmbedder maps known text to controlled vectors so tests can dictate
// similarity exactly.
type stubEmbedder struct {
	vectors map[string][]float32
}

func (s *stubEmbedder) Space() string   { return embed.SpacePotion }
func (s *stubEmbedder) Dimensions() int { return 3 }
func (s *stubEmbedder) Close() error    { return nil }

func (s *stubEmbedder) Embed(_ context.Context, texts []string) ([]embed.Vector, error) {
	out := make([]embed.Vector, len(texts))
	for index, text := range texts {
		values, found := s.vectors[text]
		if !found {
			values = []float32{0, 0, 1}
		}
		out[index] = vector(append([]float32(nil), values...)...)
	}
	return out, nil
}

// recordingDispatcher captures dispatched incidents instead of calling out.
type recordingDispatcher struct {
	mu        sync.Mutex
	incidents []*Incident
}

func (r *recordingDispatcher) Dispatch(_ context.Context, incident *Incident) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.incidents = append(r.incidents, incident)
}

func (r *recordingDispatcher) last() *Incident {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.incidents) == 0 {
		return nil
	}
	return r.incidents[len(r.incidents)-1]
}

// probeWithCorrelation builds a probe opted into correlation and novelty.
func probeWithCorrelation(name, policy string, dependsOn []proberunner.ProbeDependency) proberunner.Probe {
	return proberunner.Probe{
		Name: name,
		Type: proberunner.ProbeTypeHTTP,
		Intelligence: &proberunner.ProbeIntelligence{
			Policy: policy,
			Triggers: proberunner.ProbeTriggers{
				FailureCorrelation: &proberunner.ProbeFailureCorrelationTrigger{
					WindowSeconds:       120,
					SimilarityThreshold: 0.85,
				},
				FailureNovelty: &proberunner.ProbeFailureNoveltyTrigger{
					ClusterThreshold:      0.8,
					SettlingPeriodSeconds: 0,
				},
			},
			Topology: proberunner.ProbeTopology{DependsOn: dependsOn},
		},
	}
}

func newTestEngine(t *testing.T, vectors map[string][]float32, probes []proberunner.Probe) (*Engine, *recordingDispatcher) {
	t.Helper()

	dispatcher := &recordingDispatcher{}
	engine := NewEngine(EngineOptions{
		Embedder:   &stubEmbedder{vectors: vectors},
		Dispatcher: dispatcher,
		Logger:     logr.Discard(),
		Now:        func() time.Time { return time.Unix(0, 0) },
	})
	engine.LoadProbes(probes)

	return engine, dispatcher
}

func failure(probe, text string, at time.Time) observation.Observation {
	return observation.Observation{
		Probe: probe, Kind: observation.KindFailure, Text: text, At: at,
	}
}

// waitForIncidents blocks until the dispatcher has seen at least n incidents.
// Dispatch is asynchronous so a slow action never stalls the engine.
func waitForIncidents(t *testing.T, dispatcher *recordingDispatcher, count int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dispatcher.mu.Lock()
		got := len(dispatcher.incidents)
		dispatcher.mu.Unlock()
		if got >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d dispatches", count)
}

// THE negative control. Two unrelated services failing at the same moment with
// different failure modes must stay two incidents. If this ever merges, the
// whole feature has degraded into "alert when several things are red".
func TestEngineKeepsUnrelatedConcurrentFailuresSeparate(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, dispatcher := newTestEngine(t,
		map[string][]float32{
			"db timeout":  {1, 0, 0},
			"tls expired": {0, 1, 0},
		},
		[]proberunner.Probe{
			probeWithCorrelation("default/payments", "pulse-system/app", nil),
			probeWithCorrelation("edge/marketing", "pulse-system/edge", nil),
		})

	engine.Ingest(context.Background(), failure("default/payments", "db timeout", now))
	engine.Ingest(context.Background(), failure("edge/marketing", "tls expired", now.Add(time.Second)))
	waitForIncidents(t, dispatcher, 2)

	open := engine.Open()
	if len(open) != 2 {
		t.Fatalf("open incidents = %d, want 2 separate incidents: %+v", len(open), open)
	}
	for _, incident := range open {
		if len(incident.Members) != 1 {
			t.Fatalf("incident %s has %d members, want 1", incident.ID, len(incident.Members))
		}
	}
}

// The payoff: three canaries hitting one dead backend become ONE incident.
func TestEngineMergesIdenticalFailuresIntoOneIncident(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	shared := []float32{1, 0, 0}
	engine, dispatcher := newTestEngine(t,
		map[string][]float32{"upstream refused": shared},
		[]proberunner.Probe{
			probeWithCorrelation("default/payments", "pulse-system/app", nil),
			probeWithCorrelation("default/orders", "pulse-system/app", nil),
			probeWithCorrelation("default/shipping", "pulse-system/app", nil),
		})

	for index, probe := range []string{"default/payments", "default/orders", "default/shipping"} {
		engine.Ingest(context.Background(),
			failure(probe, "upstream refused", now.Add(time.Duration(index)*time.Second)))
	}
	waitForIncidents(t, dispatcher, 3)

	open := engine.Open()
	if len(open) != 1 {
		t.Fatalf("open incidents = %d, want 1: %+v", len(open), open)
	}
	if got := len(open[0].Members); got != 3 {
		t.Fatalf("incident members = %d, want 3", got)
	}
}

// Root cause and victims must be labelled from declared topology, so the page
// goes to whoever owns what actually broke.
func TestEngineNamesRootCauseFromDeclaredTopology(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, dispatcher := newTestEngine(t,
		map[string][]float32{
			"api 500":            {1, 0, 0},
			"connection refused": {0, 1, 0},
		},
		[]proberunner.Probe{
			probeWithCorrelation("default/payments", "pulse-system/app-team",
				[]proberunner.ProbeDependency{
					{Canary: "default/payments", Upstream: []string{"data/postgres"}},
				}),
			probeWithCorrelation("data/postgres", "pulse-system/platform-team", nil),
		})

	// The API is noticed first, but the database is to blame.
	engine.Ingest(context.Background(), failure("default/payments", "api 500", now))
	engine.Ingest(context.Background(), failure("data/postgres", "connection refused", now.Add(time.Second)))
	waitForIncidents(t, dispatcher, 2)

	open := engine.Open()
	if len(open) != 1 {
		t.Fatalf("open incidents = %d, want 1", len(open))
	}

	incident := open[0]
	if incident.RootCause != "data/postgres" {
		t.Fatalf("RootCause = %q, want data/postgres", incident.RootCause)
	}
	// Ownership follows the root cause across the policy boundary.
	if incident.Policy != "pulse-system/platform-team" {
		t.Fatalf("Policy = %q, want the root cause's policy pulse-system/platform-team", incident.Policy)
	}

	for _, member := range incident.Members {
		wantRole := RoleDownstream
		if member.Probe == "data/postgres" {
			wantRole = RoleRootCause
		}
		if member.Role != wantRole {
			t.Fatalf("%s role = %q, want %q", member.Probe, member.Role, wantRole)
		}
	}
}

// Correlation must work across canaries governed by DIFFERENT policies —
// a service and its database are rarely owned by the same team.
func TestEngineCorrelatesAcrossPolicyBoundaries(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, dispatcher := newTestEngine(t,
		map[string][]float32{"same wall": {1, 0, 0}},
		[]proberunner.Probe{
			probeWithCorrelation("default/payments", "pulse-system/app-team", nil),
			probeWithCorrelation("data/postgres", "pulse-system/platform-team", nil),
		})

	engine.Ingest(context.Background(), failure("default/payments", "same wall", now))
	engine.Ingest(context.Background(), failure("data/postgres", "same wall", now.Add(time.Second)))
	waitForIncidents(t, dispatcher, 2)

	if open := engine.Open(); len(open) != 1 {
		t.Fatalf("open incidents = %d, want 1 spanning two policies", len(open))
	}
}

// Recovery closes an incident once every member is healthy again.
func TestEngineClosesIncidentOnRecovery(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, dispatcher := newTestEngine(t,
		map[string][]float32{"upstream refused": {1, 0, 0}},
		[]proberunner.Probe{
			probeWithCorrelation("default/payments", "pulse-system/app", nil),
			probeWithCorrelation("default/orders", "pulse-system/app", nil),
		})

	engine.Ingest(context.Background(), failure("default/payments", "upstream refused", now))
	engine.Ingest(context.Background(), failure("default/orders", "upstream refused", now.Add(time.Second)))
	waitForIncidents(t, dispatcher, 2)

	if got := len(engine.Open()); got != 1 {
		t.Fatalf("open incidents = %d, want 1", got)
	}

	recover := func(probe string, at time.Time) observation.Observation {
		return observation.Observation{Probe: probe, Kind: observation.KindRecovery, At: at}
	}

	engine.Ingest(context.Background(), recover("default/payments", now.Add(time.Minute)))
	if got := len(engine.Open()); got != 1 {
		t.Fatalf("open incidents = %d after one recovery, want the incident to stay open", got)
	}

	engine.Ingest(context.Background(), recover("default/orders", now.Add(2*time.Minute)))
	if got := len(engine.Open()); got != 0 {
		t.Fatalf("open incidents = %d after full recovery, want 0", got)
	}
}

// Drift concerns exactly one endpoint, so it must never pull in other probes.
func TestEngineTreatsDriftAsSingleProbeIncident(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, dispatcher := newTestEngine(t, nil,
		[]proberunner.Probe{probeWithCorrelation("default/api", "pulse-system/app", nil)})

	engine.Ingest(context.Background(), observation.Observation{
		Probe: "default/api", Kind: observation.KindBodyDrift, DriftScore: 0.62, At: now,
	})
	waitForIncidents(t, dispatcher, 1)

	incident := dispatcher.last()
	if incident.Trigger != TriggerBodyDrift {
		t.Fatalf("Trigger = %q, want %q", incident.Trigger, TriggerBodyDrift)
	}
	if len(incident.Members) != 1 || incident.RootCause != "default/api" {
		t.Fatalf("drift incident = %+v, want a single self-caused member", incident)
	}

	// Dispatching is not enough. An incident that is not REGISTERED never
	// appears in /incidents, so it never reaches the canary's status and an
	// operator sees a drift metric moving with nothing to explain it. This
	// assertion is the one that was missing when exactly that shipped.
	open := engine.Open()
	if len(open) != 1 {
		t.Fatalf("open incidents = %d, want the drift incident to be registered", len(open))
	}
	if open[0].Trigger != TriggerBodyDrift || open[0].RootCause != "default/api" {
		t.Fatalf("registered incident = %+v, want the drift incident", open[0])
	}
}

// A probe that keeps drifting reports on every check. Those must fold into one
// open incident, not produce a fresh incident per interval.
func TestEngineDeduplicatesRepeatedDriftIntoOneIncident(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, dispatcher := newTestEngine(t, nil,
		[]proberunner.Probe{probeWithCorrelation("default/api", "pulse-system/app", nil)})

	for index := range 10 {
		engine.Ingest(context.Background(), observation.Observation{
			Probe: "default/api", Kind: observation.KindBodyDrift,
			DriftScore: 0.62, At: now.Add(time.Duration(index) * time.Second),
		})
	}
	waitForIncidents(t, dispatcher, 10)

	open := engine.Open()
	if len(open) != 1 {
		t.Fatalf("open incidents = %d after 10 drift reports, want 1", len(open))
	}

	first := open[0].ID
	if open[0].UpdatedAt.Equal(open[0].OpenedAt) {
		t.Fatal("the incident was never refreshed by the later reports")
	}
	_ = first
}

// Drift fires while the check PASSES, so there is no failure to recover from.
// The runner sends an explicit recovery when drift stops; without it the
// incident would stay open forever.
func TestEngineClosesDriftIncidentWhenDriftStops(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, dispatcher := newTestEngine(t, nil,
		[]proberunner.Probe{probeWithCorrelation("default/api", "pulse-system/app", nil)})

	engine.Ingest(context.Background(), observation.Observation{
		Probe: "default/api", Kind: observation.KindBodyDrift, DriftScore: 0.62, At: now,
	})
	waitForIncidents(t, dispatcher, 1)

	if got := len(engine.Open()); got != 1 {
		t.Fatalf("open incidents = %d, want 1", got)
	}

	engine.Ingest(context.Background(), observation.Observation{
		Probe: "default/api", Kind: observation.KindRecovery, At: now.Add(time.Minute),
	})

	if got := len(engine.Open()); got != 0 {
		t.Fatalf("open incidents = %d after drift stopped, want 0", got)
	}
}

// A probe that never opted in must be ignored entirely.
func TestEngineIgnoresProbesWithoutIntelligence(t *testing.T) {
	t.Parallel()

	engine, dispatcher := newTestEngine(t, nil,
		[]proberunner.Probe{{Name: "default/plain", Type: proberunner.ProbeTypeHTTP}})

	engine.Ingest(context.Background(), failure("default/plain", "anything", time.Unix(1000, 0)))
	time.Sleep(20 * time.Millisecond)

	if got := len(engine.Open()); got != 0 {
		t.Fatalf("open incidents = %d, want 0 for a probe that never opted in", got)
	}
	if dispatcher.last() != nil {
		t.Fatal("dispatched an incident for a probe that never opted in")
	}
}

// The same failure shape recurring must not read as novel the second time —
// that is what keeps repeat outages from re-invoking a language model.
func TestEngineMarksRepeatFailureShapesAsNotNovel(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, dispatcher := newTestEngine(t,
		map[string][]float32{"upstream refused": {1, 0, 0}},
		[]proberunner.Probe{probeWithCorrelation("default/api", "pulse-system/app", nil)})

	engine.Ingest(context.Background(), failure("default/api", "upstream refused", now))
	waitForIncidents(t, dispatcher, 1)
	if first := dispatcher.last(); !first.Novel {
		t.Fatal("the first sighting of a failure shape was not marked novel")
	}

	engine.Ingest(context.Background(), observation.Observation{
		Probe: "default/api", Kind: observation.KindRecovery, At: now.Add(time.Minute),
	})
	engine.Ingest(context.Background(), failure("default/api", "upstream refused", now.Add(2*time.Minute)))
	waitForIncidents(t, dispatcher, 2)

	if second := dispatcher.last(); second.Novel {
		t.Fatal("a repeat of a known failure shape was marked novel")
	}
}
