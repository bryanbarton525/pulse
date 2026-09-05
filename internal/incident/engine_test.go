package incident

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/bryanbarton525/pulse/internal/embed"
	"github.com/bryanbarton525/pulse/internal/observation"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// probeDatabase is the shared upstream in these correlation scenarios.
const probeDatabase = "data/postgres"

// stubEmbedder maps known text to controlled vectors so tests can dictate
// similarity exactly.
type stubEmbedder struct {
	vectors map[string][]float32
}

type blockingNoveltyEmbedder struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
}

func (b *blockingNoveltyEmbedder) Space() string   { return embed.SpacePotion }
func (b *blockingNoveltyEmbedder) Dimensions() int { return 3 }
func (b *blockingNoveltyEmbedder) Close() error    { return nil }
func (b *blockingNoveltyEmbedder) Embed(ctx context.Context, texts []string) ([]embed.Vector, error) {
	b.mu.Lock()
	b.calls++
	call := b.calls
	b.mu.Unlock()
	if call == 2 {
		close(b.started)
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []embed.Vector{vector(1, 0, 0)}, nil
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
	mu         sync.Mutex
	incidents  []*Incident
	onDispatch func(*Incident)
}

func (r *recordingDispatcher) Dispatch(ctx context.Context, incident *Incident) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Record whether the context was already dead on arrival — a real action
	// would fail immediately with exactly this error.
	incident.dispatchErr = ctx.Err()
	if r.onDispatch != nil {
		r.onDispatch(incident)
	}
	r.incidents = append(r.incidents, incident)
}

func (r *recordingDispatcher) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.incidents)
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
		// Short, but still exercising the debounce rather than bypassing it.
		DispatchDelay: 40 * time.Millisecond,
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

func TestNoveltyEmbeddingDoesNotBlockReadsOrDispatchRecoveredIncident(t *testing.T) {
	t.Parallel()

	model := &blockingNoveltyEmbedder{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	dispatched := &recordingDispatcher{}
	engine := NewEngine(EngineOptions{
		Embedder:      model,
		Dispatcher:    dispatched,
		Logger:        logr.Discard(),
		DispatchDelay: 10 * time.Millisecond,
	})
	engine.LoadProbes([]proberunner.Probe{
		probeWithCorrelation("default/api", "pulse-system/app", nil),
	})
	now := time.Now()
	engine.Ingest(context.Background(), failure("default/api", "failed", now))

	select {
	case <-model.started:
	case <-time.After(time.Second):
		t.Fatal("novelty embedding did not start")
	}

	readDone := make(chan struct{})
	go func() {
		_ = engine.Open()
		close(readDone)
	}()
	select {
	case <-readDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Open blocked behind novelty embedding")
	}

	engine.Ingest(context.Background(), observation.Observation{
		Probe: "default/api", Kind: observation.KindRecovery, At: now.Add(time.Second),
	})
	close(model.release)
	time.Sleep(30 * time.Millisecond)
	if got := dispatched.count(); got != 0 {
		t.Fatalf("dispatched %d recovered incidents, want 0", got)
	}
}

func TestHealthyResultClosesIncidentAfterLostRecovery(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, _ := newTestEngine(t, nil, []proberunner.Probe{
		probeWithCorrelation("default/api", "pulse-system/app", nil),
	})
	reasserted := failure("default/api", "failed", now)
	reasserted.Reassert = true
	engine.Ingest(context.Background(), reasserted)
	if got := len(engine.Open()); got != 1 {
		t.Fatalf("open incidents = %d, want reasserted incident", got)
	}

	engine.ReconcileResults([]proberunner.ProbeResult{{
		Name: "default/api", Healthy: true, LastCheckTime: now.Add(time.Second),
	}})
	if got := len(engine.Open()); got != 0 {
		t.Fatalf("open incidents = %d after newer healthy result, want 0", got)
	}
}

func TestOlderHealthyResultCannotCloseNewerFailure(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, _ := newTestEngine(t, nil, []proberunner.Probe{
		probeWithCorrelation("default/api", "pulse-system/app", nil),
	})
	engine.Ingest(context.Background(), failure("default/api", "failed", now))
	engine.ReconcileResults([]proberunner.ProbeResult{{
		Name: "default/api", Healthy: true, LastCheckTime: now.Add(-time.Second),
	}})
	if got := len(engine.Open()); got != 1 {
		t.Fatalf("open incidents = %d after older healthy result, want 1", got)
	}
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
	waitForIncidents(t, dispatcher, 1) // three probes, one incident

	open := engine.Open()
	if len(open) != 1 {
		t.Fatalf("open incidents = %d, want 1: %+v", len(open), open)
	}
	if got := len(open[0].Members); got != 3 {
		t.Fatalf("incident members = %d, want 3", got)
	}
	if got := len(open[0].MergeEvidence); got != 3 {
		t.Fatalf("merge evidence = %d entries, want 3 pairwise reasons", got)
	}
	for _, evidence := range open[0].MergeEvidence {
		if evidence.Type != EvidenceSimilarity || evidence.Similarity == nil || evidence.Threshold == nil {
			t.Fatalf("similarity evidence is incomplete: %+v", evidence)
		}
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
					{Canary: "default/payments", Upstream: []string{probeDatabase}},
				}),
			probeWithCorrelation(probeDatabase, "pulse-system/platform-team", nil),
		})

	// The API is noticed first, but the database is to blame.
	engine.Ingest(context.Background(), failure("default/payments", "api 500", now))
	engine.Ingest(context.Background(), failure(probeDatabase, "connection refused", now.Add(time.Second)))
	waitForIncidents(t, dispatcher, 1) // two probes, one incident

	open := engine.Open()
	if len(open) != 1 {
		t.Fatalf("open incidents = %d, want 1", len(open))
	}

	incident := open[0]
	if incident.RootCause != probeDatabase {
		t.Fatalf("RootCause = %q, want data/postgres", incident.RootCause)
	}
	// Ownership follows the root cause across the policy boundary.
	if incident.Policy != "pulse-system/platform-team" {
		t.Fatalf("Policy = %q, want the root cause's policy pulse-system/platform-team", incident.Policy)
	}
	if len(incident.MergeEvidence) != 1 || incident.MergeEvidence[0].Type != EvidenceDeclaredEdge {
		t.Fatalf("MergeEvidence = %+v, want one declared-edge reason", incident.MergeEvidence)
	}
	if incident.MergeEvidence[0].Similarity != nil || incident.MergeEvidence[0].Threshold != nil {
		t.Fatalf("declared edge was presented as a measured similarity: %+v", incident.MergeEvidence[0])
	}

	for _, member := range incident.Members {
		wantRole := RoleDownstream
		if member.Probe == probeDatabase {
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
			probeWithCorrelation(probeDatabase, "pulse-system/platform-team", nil),
		})

	engine.Ingest(context.Background(), failure("default/payments", "same wall", now))
	engine.Ingest(context.Background(), failure(probeDatabase, "same wall", now.Add(time.Second)))
	waitForIncidents(t, dispatcher, 1) // two probes, one incident

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
	waitForIncidents(t, dispatcher, 1) // two probes, one incident

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
	waitForIncidents(t, dispatcher, 1) // ten drift reports, one incident

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

func TestReassertionRefreshesOpenIncidentWithoutRedispatch(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, dispatcher := newTestEngine(t,
		map[string][]float32{"upstream refused": {1, 0, 0}},
		[]proberunner.Probe{probeWithCorrelation("default/api", "pulse-system/app", nil)})

	engine.Ingest(context.Background(), failure("default/api", "upstream refused", now))
	waitForIncidents(t, dispatcher, 1)
	reasserted := failure("default/api", "upstream refused", now.Add(2*time.Minute))
	reasserted.Reassert = true
	engine.Ingest(context.Background(), reasserted)
	time.Sleep(3 * engine.dispatchDelay)
	if got := dispatcher.count(); got != 1 {
		t.Fatalf("dispatches = %d after reasserting an open incident, want 1", got)
	}
}

func TestReassertionRebuildsLostIncidentWithoutRedispatch(t *testing.T) {
	t.Parallel()

	engine, dispatcher := newTestEngine(t,
		map[string][]float32{"upstream refused": {1, 0, 0}},
		[]proberunner.Probe{probeWithCorrelation("default/api", "pulse-system/app", nil)})

	reasserted := failure("default/api", "upstream refused", time.Unix(1000, 0))
	reasserted.Reassert = true
	engine.Ingest(context.Background(), reasserted)
	time.Sleep(3 * engine.dispatchDelay)

	if got := dispatcher.count(); got != 0 {
		t.Fatalf("dispatches = %d for a reconstructed incident, want 0", got)
	}
	if got := len(engine.Open()); got != 1 {
		t.Fatalf("open incidents = %d after reconstruction, want 1", got)
	}
}

func TestNoveltyUsesRootCausePolicyAfterDebounce(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	root := probeWithCorrelation("data/catalogue", "pulse-system/root", nil)
	root.Intelligence.Triggers.FailureNovelty.ClusterThreshold = 0.95
	downstream := probeWithCorrelation("shop/checkout", "pulse-system/downstream",
		[]proberunner.ProbeDependency{{Canary: "shop/checkout", Upstream: []string{"data/catalogue"}}})
	downstream.Intelligence.Triggers.FailureNovelty.ClusterThreshold = 0.5

	engine, dispatcher := newTestEngine(t, map[string][]float32{
		"original root": {1, 0, 0},
		"changed root":  {0.9, 0.435, 0},
		"victim":        {0, 0, 1},
	}, []proberunner.Probe{root, downstream})

	engine.Ingest(context.Background(), failure(root.Name, "original root", now))
	waitForIncidents(t, dispatcher, 1)
	engine.Ingest(context.Background(), observation.Observation{
		Probe: root.Name, Kind: observation.KindRecovery, At: now.Add(time.Minute),
	})

	engine.Ingest(context.Background(), failure(downstream.Name, "victim", now.Add(2*time.Minute)))
	engine.Ingest(context.Background(), failure(root.Name, "changed root", now.Add(2*time.Minute+time.Second)))
	waitForIncidents(t, dispatcher, 2)
	if current := dispatcher.last(); current.RootCause != root.Name || !current.Novel {
		t.Fatalf("root=%q novel=%v, want root policy's stricter threshold to mark the changed root novel",
			current.RootCause, current.Novel)
	}
}

type leasedEmbedder struct {
	started chan struct{}
	release chan struct{}
}

func (l *leasedEmbedder) Space() string   { return embed.SpaceMiniLM }
func (l *leasedEmbedder) Dimensions() int { return 3 }
func (l *leasedEmbedder) Close() error    { return nil }
func (l *leasedEmbedder) Embed(context.Context, []string) ([]embed.Vector, error) {
	close(l.started)
	<-l.release
	return []embed.Vector{vector(1, 0, 0)}, nil
}

func TestSetEmbedderWaitsForInFlightEmbedding(t *testing.T) {
	t.Parallel()

	old := &leasedEmbedder{started: make(chan struct{}), release: make(chan struct{})}
	next := &stubEmbedder{vectors: map[string][]float32{}}
	engine := NewEngine(EngineOptions{Embedder: old, Logger: logr.Discard()})
	engine.LoadProbes([]proberunner.Probe{probeWithCorrelation("default/api", "pulse-system/app", nil)})

	go engine.Ingest(context.Background(), failure("default/api", "blocked", time.Now()))
	<-old.started
	swapped := make(chan embed.Embedder, 1)
	go func() { swapped <- engine.SetEmbedder(next) }()

	select {
	case <-swapped:
		t.Fatal("model swap completed while the old embedder was still in use")
	case <-time.After(20 * time.Millisecond):
	}
	close(old.release)
	select {
	case retired := <-swapped:
		if retired != old {
			t.Fatalf("retired embedder = %T, want the old model", retired)
		}
	case <-time.After(time.Second):
		t.Fatal("model swap did not complete after the embedding call finished")
	}
}

// Observations arrive over HTTP, so Ingest receives a request context that is
// cancelled the moment the handler returns. Dispatch happens in a goroutine
// afterwards, so a context that carries cancellation kills every action before
// it can reach Slack, a language model, or a log backend.
//
// This shipped: in a real cluster not one action request ever left the engine.
// The unit tests missed it because they called Dispatch with context.Background
// directly, never through Ingest.
func TestDispatchSurvivesACancelledIngestContext(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, dispatched := newTestEngine(t,
		map[string][]float32{"upstream refused": {1, 0, 0}},
		[]proberunner.Probe{probeWithCorrelation("default/api", "pulse-system/app", nil)})

	// Exactly what the HTTP handler hands over: a context already cancelled
	// by the time the dispatch goroutine runs.
	ctx, cancel := context.WithCancel(context.Background())
	engine.Ingest(ctx, failure("default/api", "upstream refused", now))
	cancel()

	waitForIncidents(t, dispatched, 1)

	current := dispatched.last()
	if current == nil {
		t.Fatal("no incident dispatched")
		return
	}
	if err := current.dispatchErr; err != nil {
		t.Fatalf("dispatch saw a cancelled context: %v", err)
	}
}

// A correlated incident gains members one observation at a time, and the root
// cause is often the LAST to report. Dispatching on each arrival sent one
// notification per member, and the first named whoever reported first as the
// root cause — in a real cluster that was three Slack messages for one outage,
// the first blaming the wrong service. The whole point of correlating is to
// produce one page.
func TestGrowingIncidentDispatchesOnceWithTheFinalRootCause(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, dispatched := newTestEngine(t,
		map[string][]float32{"upstream refused": {1, 0, 0}},
		[]proberunner.Probe{
			// The API depends on the database, so once both are failing the
			// database is the root cause — but it reports last.
			probeWithCorrelation("default/api", "pulse-system/app",
				[]proberunner.ProbeDependency{
					{Canary: "default/api", Upstream: []string{probeDatabase}},
				}),
			probeWithCorrelation(probeDatabase, "pulse-system/platform", nil),
		})

	engine.Ingest(context.Background(), failure("default/api", "upstream refused", now))
	engine.Ingest(context.Background(),
		failure(probeDatabase, "upstream refused", now.Add(time.Second)))

	waitForIncidents(t, dispatched, 1)
	time.Sleep(120 * time.Millisecond) // let any extra dispatch land

	if got := dispatched.count(); got != 1 {
		t.Fatalf("dispatched %d times for one incident, want 1", got)
	}

	current := dispatched.last()
	if current.RootCause != probeDatabase {
		t.Fatalf("RootCause = %q, want %q — dispatch fired before the incident settled",
			current.RootCause, probeDatabase)
	}
	if len(current.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(current.Members))
	}
}

// The llm action writes its analysis onto the incident it is handed. That is a
// snapshot, so without an explicit write-back the engine never learns it, it
// never appears on /incidents, and the operator never sees it on the CR.
func TestInvestigationIsCarriedBackOntoTheIncident(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, dispatched := newTestEngine(t,
		map[string][]float32{"boom": {1, 0, 0}},
		[]proberunner.Probe{probeWithCorrelation("default/api", "pulse-system/app", nil)})

	// Stand in for the llm action populating the analysis.
	dispatched.onDispatch = func(current *Incident) {
		current.Investigation = "**Assessment** — the upstream is refusing connections."
	}

	engine.Ingest(context.Background(), failure("default/api", "boom", now))
	waitForIncidents(t, dispatched, 1)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		open := engine.Open()
		if len(open) == 1 && open[0].Investigation != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("the investigation never reached the engine's incident, so it cannot reach CR status")
}

// A probe must never hold more than one open incident.
//
// byProbe maps a probe to a single incident, so when a probe that is already
// latency-shifted starts drifting, opening a second incident orphans the
// first: it stays in e.open, is never looked up again, and never closes. In a
// cluster this showed up as fourteen open incidents for five canaries.
func TestProbeHoldsAtMostOneOpenIncident(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, _ := newTestEngine(t, nil,
		[]proberunner.Probe{probeWithCorrelation("default/api", "pulse-system/app", nil)})

	for index := range 6 {
		kind := observation.KindBodyDrift
		if index%2 == 1 {
			kind = observation.KindLatencyShift
		}
		engine.Ingest(context.Background(), observation.Observation{
			Probe: "default/api", Kind: kind,
			DriftScore: 0.4, LatencyZScore: 4,
			At: now.Add(time.Duration(index) * time.Second),
		})
	}

	if got := len(engine.Open()); got != 1 {
		t.Fatalf("open incidents = %d after alternating drift and latency signals, want 1", got)
	}
}

// The runner closes a drift incident by reporting that the episode ended, but
// it tracks that in memory. A restart or a resharding loses it, and the
// incident is then stranded with nobody left to close it. The sweep is what
// makes the engine self-healing across the rollouts that cause this.
func TestSweepClosesStaleSingleProbeIncidents(t *testing.T) {
	t.Parallel()

	clock := time.Unix(1000, 0)
	dispatched := &recordingDispatcher{}
	engine := NewEngine(EngineOptions{
		Dispatcher:    dispatched,
		Logger:        logr.Discard(),
		Now:           func() time.Time { return clock },
		DispatchDelay: 20 * time.Millisecond,
	})
	engine.LoadProbes([]proberunner.Probe{
		probeWithCorrelation("default/api", "pulse-system/app", nil),
		probeWithCorrelation("default/other", "pulse-system/app", nil),
	})

	engine.Ingest(context.Background(), observation.Observation{
		Probe: "default/api", Kind: observation.KindBodyDrift, DriftScore: 0.4, At: clock,
	})
	// A correlation incident, which must survive the sweep: a long outage is
	// still a real outage.
	engine.Ingest(context.Background(), failure("default/other", "boom", clock))

	if got := len(engine.Open()); got != 2 {
		t.Fatalf("open incidents = %d, want 2", got)
	}

	// Nothing refreshes them; the runner that would have cleared the drift is
	// gone.
	clock = clock.Add(30 * time.Minute)
	if closed := engine.Sweep(5 * time.Minute); closed != 1 {
		t.Fatalf("Sweep closed %d incidents, want 1", closed)
	}

	open := engine.Open()
	if len(open) != 1 {
		t.Fatalf("open incidents = %d after the sweep, want 1", len(open))
	}
	if open[0].Trigger != TriggerFailureCorrelation {
		t.Fatalf("the sweep closed the %s incident; only single-probe ones should expire",
			open[0].Trigger)
	}

	// The probe must be released, or it can never open another incident.
	engine.Ingest(context.Background(), observation.Observation{
		Probe: "default/api", Kind: observation.KindBodyDrift, DriftScore: 0.4,
		At: clock.Add(time.Second),
	})
	if got := len(engine.Open()); got != 2 {
		t.Fatalf("open incidents = %d; the swept probe could not open a new incident", got)
	}
}

// A probe that is already drifting must still be able to join a correlated
// incident when it starts failing outright.
//
// Reusing whatever incident the probe already held meant an outage fragmented
// into one private drift incident per already-drifting probe, and only the
// probes that happened not to be drifting correlated together.
func TestFailureSupersedesAnOpenDriftIncident(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, dispatched := newTestEngine(t,
		map[string][]float32{"upstream refused": {1, 0, 0}},
		[]proberunner.Probe{
			probeWithCorrelation("default/api-a", "pulse-system/app", nil),
			probeWithCorrelation("default/api-b", "pulse-system/app", nil),
		})

	// Both are drifting while still passing.
	for _, probe := range []string{"default/api-a", "default/api-b"} {
		engine.Ingest(context.Background(), observation.Observation{
			Probe: probe, Kind: observation.KindBodyDrift, DriftScore: 0.4, At: now,
		})
	}
	if got := len(engine.Open()); got != 2 {
		t.Fatalf("open incidents = %d, want one drift incident per probe", got)
	}

	// Now the shared upstream dies and both fail with the same text.
	engine.Ingest(context.Background(),
		failure("default/api-a", "upstream refused", now.Add(time.Second)))
	engine.Ingest(context.Background(),
		failure("default/api-b", "upstream refused", now.Add(2*time.Second)))

	waitForIncidents(t, dispatched, 1)

	open := engine.Open()
	if len(open) != 1 {
		names := make([]string, 0, len(open))
		for _, i := range open {
			names = append(names, i.Trigger+":"+strings.Join(i.ProbeNames(), "+"))
		}
		t.Fatalf("open incidents = %d, want 1 correlated: %v", len(open), names)
	}
	if open[0].Trigger != TriggerFailureCorrelation {
		t.Fatalf("Trigger = %q, want %q", open[0].Trigger, TriggerFailureCorrelation)
	}
	if len(open[0].Members) != 2 {
		t.Fatalf("members = %d, want both probes", len(open[0].Members))
	}
}

// The runner re-reports a drift signal on every drifted check. Dispatching on
// each one notifies once per probe interval for as long as the drift lasts —
// in a cluster that was seven Slack messages and seven language-model calls
// for one ongoing drift. Nothing changed between them, so there was nothing
// new to say.
func TestOngoingDriftNotifiesOnlyOnce(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, dispatched := newTestEngine(t, nil,
		[]proberunner.Probe{probeWithCorrelation("default/api", "pulse-system/app", nil)})

	for index := range 12 {
		engine.Ingest(context.Background(), observation.Observation{
			Probe: "default/api", Kind: observation.KindBodyDrift, DriftScore: 0.42,
			At: now.Add(time.Duration(index) * 5 * time.Second),
		})
		time.Sleep(8 * time.Millisecond)
	}

	waitForIncidents(t, dispatched, 1)
	time.Sleep(150 * time.Millisecond) // let any further dispatch land

	if got := dispatched.count(); got != 1 {
		t.Fatalf("dispatched %d times for one ongoing drift, want 1", got)
	}
	if got := len(engine.Open()); got != 1 {
		t.Fatalf("open incidents = %d, want 1", got)
	}
}

// A drift that turns into a latency shift is a different statement about the
// probe and is worth re-announcing.
func TestChangedTriggerNotifiesAgain(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	engine, dispatched := newTestEngine(t, nil,
		[]proberunner.Probe{probeWithCorrelation("default/api", "pulse-system/app", nil)})

	engine.Ingest(context.Background(), observation.Observation{
		Probe: "default/api", Kind: observation.KindBodyDrift, DriftScore: 0.42, At: now,
	})
	waitForIncidents(t, dispatched, 1)

	engine.Ingest(context.Background(), observation.Observation{
		Probe: "default/api", Kind: observation.KindLatencyShift, LatencyZScore: 4.1,
		At: now.Add(5 * time.Second),
	})
	waitForIncidents(t, dispatched, 2)

	if got := dispatched.last().Trigger; got != TriggerLatencyShift {
		t.Fatalf("Trigger = %q, want %q", got, TriggerLatencyShift)
	}
}

// Restating an ongoing failure lets the engine rebuild its picture after a
// restart, but it must not be counted as a new onset: the dependency learner
// measures how often one canary fails just before another, and heartbeats
// would let a single long outage manufacture confidence from nothing.
func TestReassertedFailuresDoNotFeedTheDependencyLearner(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	probes := []proberunner.Probe{
		probeWithCorrelation("data/db", "pulse-system/app", nil),
		probeWithCorrelation("default/api", "pulse-system/app", nil),
	}
	for i := range probes {
		probes[i].Intelligence.Topology.InferDependencies = true
		probes[i].Intelligence.Topology.InferMinObservations = 2
		probes[i].Intelligence.Topology.InferMinConfidence = 0.5
	}

	engine, _ := newTestEngine(t, map[string][]float32{"boom": {1, 0, 0}}, probes)

	// One real outage, then a long stream of restatements.
	engine.Ingest(context.Background(), failure("data/db", "boom", now))
	engine.Ingest(context.Background(), failure("default/api", "boom", now.Add(time.Second)))

	for tick := 1; tick <= 20; tick++ {
		at := now.Add(time.Duration(tick) * 2 * time.Minute)
		for _, probe := range []string{"data/db", "default/api"} {
			signal := failure(probe, "boom", at)
			signal.Reassert = true
			engine.Ingest(context.Background(), signal)
		}
	}

	// A single co-occurrence is below the two-observation floor, so nothing
	// should be proposed. Counting the restatements would sail past it.
	if got := engine.Proposals(); len(got) != 0 {
		t.Fatalf("Proposals() = %+v; restatements were counted as onsets", got)
	}
}
