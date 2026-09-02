// Package integration wires the real components together — the real embedding
// model, the real drift detector, the real correlation engine — and drives the
// scenarios the feature exists for.
//
// The unit tests verify each piece against controlled inputs. These verify that
// the pieces agree with each other, and that the real potion weights actually
// separate the cases the thresholds assume they separate.
package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bryanbarton525/pulse/internal/embed"
	"github.com/bryanbarton525/pulse/internal/incident"
	"github.com/bryanbarton525/pulse/internal/observation"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// pipeline is a probe runner and an incident engine wired directly together.
type pipeline struct {
	intelligence *proberunner.Intelligence
	engine       *incident.Engine
	dispatched   *recordingDispatcher
}

// directShipper hands observations straight to the engine, standing in for the
// HTTP hop between the two processes.
type directShipper struct{ engine *incident.Engine }

func (d *directShipper) Ship(ctx context.Context, signal observation.Observation) {
	d.engine.Ingest(ctx, signal)
}

type recordingDispatcher struct {
	mu        sync.Mutex
	incidents []*incident.Incident
}

func (r *recordingDispatcher) Dispatch(_ context.Context, current *incident.Incident) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.incidents = append(r.incidents, current)
}

func (r *recordingDispatcher) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.incidents)
}

func (r *recordingDispatcher) last() *incident.Incident {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.incidents) == 0 {
		return nil
	}
	return r.incidents[len(r.incidents)-1]
}

// realEmbedder loads the converted potion weights, skipping when absent.
func realEmbedder(t *testing.T) embed.Embedder {
	t.Helper()

	root := os.Getenv("PULSE_MODELS_DIR")
	if root == "" {
		root = filepath.Join("..", "..", "hack", "models")
	}

	modelPath := filepath.Join(root, "potion", "model.bin")
	if _, err := os.Stat(modelPath); err != nil {
		t.Skipf("real model not present (run `make fetch-models`): %v", err)
	}

	embedder, err := embed.LoadPotion(modelPath, filepath.Join(root, "potion", "vocab.txt"), 256)
	if err != nil {
		t.Fatalf("LoadPotion() error = %v", err)
	}

	return embed.NewCachingEmbedder(embedder, 1024)
}

func newPipeline(t *testing.T, probes []proberunner.Probe) *pipeline {
	t.Helper()

	embedder := realEmbedder(t)
	dispatched := &recordingDispatcher{}

	engine := incident.NewEngine(incident.EngineOptions{
		// Both tiers use the real model here. In production the cold path uses
		// MiniLM, but correlation only needs a consistent space, and this keeps
		// the test free of ONNX Runtime.
		Embedder:   embedder,
		Dispatcher: dispatched,
		Logger:     logr.Discard(),
	})
	engine.LoadProbes(probes)

	intelligence := proberunner.NewIntelligence(
		embedder,
		&directShipper{engine: engine},
		logr.Discard(),
		proberunner.NewIntelligenceMetrics(prometheus.NewRegistry()),
	)

	return &pipeline{intelligence: intelligence, engine: engine, dispatched: dispatched}
}

func correlatingProbe(name, policy string, dependsOn []proberunner.ProbeDependency) proberunner.Probe {
	return proberunner.Probe{
		Name:           name,
		Type:           proberunner.ProbeTypeHTTP,
		URL:            "https://" + name,
		ExpectedStatus: 200,
		Intelligence: &proberunner.ProbeIntelligence{
			Policy: policy,
			Triggers: proberunner.ProbeTriggers{
				BodyDrift: &proberunner.ProbeBodyDriftTrigger{
					Threshold: 0.15, WarmupChecks: 10, ConsecutiveBreaches: 2,
					MaxBodyBytes: 4096, SampleEvery: 1,
				},
				FailureCorrelation: &proberunner.ProbeFailureCorrelationTrigger{
					WindowSeconds: 120, SimilarityThreshold: 0.85,
				},
				FailureNovelty: &proberunner.ProbeFailureNoveltyTrigger{
					ClusterThreshold: 0.80, SettlingPeriodSeconds: 0,
				},
			},
			Topology: proberunner.ProbeTopology{DependsOn: dependsOn},
		},
	}
}

func passing(name, body string, at time.Time) *proberunner.ProbeResult {
	return &proberunner.ProbeResult{
		Name: name, Healthy: true, StatusCode: 200, ExpectedStatus: 200,
		Message: "Got expected status 200", LastCheckTime: at, BodySnippet: body,
	}
}

func failing(name, message string, status int, at time.Time) *proberunner.ProbeResult {
	return &proberunner.ProbeResult{
		Name: name, Healthy: false, StatusCode: status, ExpectedStatus: 200,
		Message: message, LastCheckTime: at,
	}
}

// settle waits for the engine's asynchronous dispatch to catch up.
func settle(t *testing.T, dispatched *recordingDispatcher, want int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if dispatched.count() >= want {
			// Let any extra dispatches land so over-firing is detectable.
			time.Sleep(50 * time.Millisecond)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d dispatches; got %d", want, dispatched.count())
}

// Three services behind one dead backend produce ONE incident, with the
// database named as the root cause and the two APIs marked downstream.
func TestCorrelatedOutageProducesOneIncident(t *testing.T) {
	t.Parallel()

	probes := []proberunner.Probe{
		correlatingProbe("default/payments", "pulse-system/app-team",
			[]proberunner.ProbeDependency{
				{Canary: "default/payments", Upstream: []string{"data/postgres"}},
			}),
		correlatingProbe("default/orders", "pulse-system/app-team",
			[]proberunner.ProbeDependency{
				{Canary: "default/orders", Upstream: []string{"data/postgres"}},
			}),
		// The database is governed by a DIFFERENT policy, as it would be in a
		// real cluster where platform and app teams own separate policies.
		correlatingProbe("data/postgres", "pulse-system/platform-team", nil),
	}

	line := newPipeline(t, probes)
	now := time.Unix(1_700_000_000, 0)

	// The APIs notice first; the database check notices a moment later.
	line.intelligence.Evaluate(probes[0],
		failing("default/payments", "dial tcp 10.96.0.5:5432: i/o timeout", 0, now),
		nil, 2*time.Second)
	line.intelligence.Evaluate(probes[1],
		failing("default/orders", "dial tcp 10.96.0.5:5432: i/o timeout", 0, now.Add(time.Second)),
		nil, 2*time.Second)
	line.intelligence.Evaluate(probes[2],
		failing("data/postgres", "connection refused", 0, now.Add(2*time.Second)),
		nil, time.Second)

	settle(t, line.dispatched, 3)

	open := line.engine.Open()
	if len(open) != 1 {
		t.Fatalf("open incidents = %d, want 1: %+v", len(open), open)
	}

	current := open[0]
	if len(current.Members) != 3 {
		t.Fatalf("incident members = %d, want 3: %v", len(current.Members), current.ProbeNames())
	}
	if current.RootCause != "data/postgres" {
		t.Fatalf("RootCause = %q, want data/postgres", current.RootCause)
	}
	// Ownership follows the root cause across the policy boundary.
	if current.Policy != "pulse-system/platform-team" {
		t.Fatalf("Policy = %q, want the platform team's policy", current.Policy)
	}

	for _, member := range current.Members {
		want := incident.RoleDownstream
		if member.Probe == "data/postgres" {
			want = incident.RoleRootCause
		}
		if member.Role != want {
			t.Fatalf("%s role = %q, want %q", member.Probe, member.Role, want)
		}
	}
}

// The negative control, run through the REAL model: two unrelated services
// failing at the same moment with different failure modes stay two incidents.
func TestUnrelatedSimultaneousFailuresStaySeparate(t *testing.T) {
	t.Parallel()

	probes := []proberunner.Probe{
		correlatingProbe("default/payments", "pulse-system/app-team", nil),
		correlatingProbe("edge/marketing", "pulse-system/edge-team", nil),
	}

	line := newPipeline(t, probes)
	now := time.Unix(1_700_000_000, 0)

	line.intelligence.Evaluate(probes[0],
		failing("default/payments", "dial tcp 10.96.0.5:5432: i/o timeout", 0, now),
		nil, 2*time.Second)
	line.intelligence.Evaluate(probes[1],
		failing("edge/marketing", "x509: certificate has expired or is not yet valid", 0, now),
		nil, 2*time.Second)

	settle(t, line.dispatched, 2)

	open := line.engine.Open()
	if len(open) != 2 {
		t.Fatalf("open incidents = %d, want 2 separate incidents: %+v", len(open), open)
	}
	for _, current := range open {
		if len(current.Members) != 1 {
			t.Fatalf("incident %s has %d members, want 1", current.ID, len(current.Members))
		}
	}
}

// Two canaries with no declared relationship, both reporting the same failure,
// merge on the strength of the failure text alone. This is the case that
// justifies having a model in the correlation path at all.
func TestUndeclaredSharedUpstreamMergesOnFailureText(t *testing.T) {
	t.Parallel()

	probes := []proberunner.Probe{
		correlatingProbe("default/service-a", "pulse-system/team-a", nil),
		correlatingProbe("other/service-b", "pulse-system/team-b", nil),
	}

	line := newPipeline(t, probes)
	now := time.Unix(1_700_000_000, 0)

	// Identical failure shape, differing only in the volatile bits that
	// normalization masks away.
	line.intelligence.Evaluate(probes[0],
		failing("default/service-a", "dial tcp 10.96.0.9:6379: i/o timeout after 1.2s", 0, now),
		nil, 2*time.Second)
	line.intelligence.Evaluate(probes[1],
		failing("other/service-b", "dial tcp 10.96.0.9:6379: i/o timeout after 900ms", 0,
			now.Add(time.Second)),
		nil, 2*time.Second)

	settle(t, line.dispatched, 2)

	open := line.engine.Open()
	if len(open) != 1 {
		t.Fatalf("open incidents = %d, want 1 merged on failure similarity", len(open))
	}
	if len(open[0].Members) != 2 {
		t.Fatalf("incident members = %d, want 2", len(open[0].Members))
	}
}

// Green-but-wrong, driven through the real model: the check keeps returning
// 200, but the payload became an empty result set.
func TestBodyDriftFiresWhileTheCheckStillPasses(t *testing.T) {
	t.Parallel()

	probes := []proberunner.Probe{correlatingProbe("default/api", "pulse-system/app-team", nil)}
	line := newPipeline(t, probes)
	now := time.Unix(1_700_000_000, 0)

	normal := func(index int) string {
		return fmt.Sprintf(
			`{"items":[{"id":%d,"name":"widget","status":"active"},`+
				`{"id":%d,"name":"gadget","status":"active"}],"total":2,"generated":"2026-08-%02dT10:00:00Z"}`,
			index, index+1, (index%27)+1)
	}

	var previous *proberunner.ProbeResult
	for index := range 12 {
		result := passing("default/api", normal(index), now.Add(time.Duration(index)*time.Second))
		line.intelligence.Evaluate(probes[0], result, previous, 40*time.Millisecond)
		previous = result
	}

	if got := line.dispatched.count(); got != 0 {
		t.Fatalf("dispatched %d incidents during a healthy warmup, want 0", got)
	}

	// The endpoint starts returning nothing, while still answering 200.
	for index := range 3 {
		result := passing("default/api", `{"items":[],"total":0}`,
			now.Add(time.Duration(20+index)*time.Second))
		line.intelligence.Evaluate(probes[0], result, previous, 40*time.Millisecond)
		previous = result
	}

	settle(t, line.dispatched, 1)

	current := line.dispatched.last()
	if current.Trigger != incident.TriggerBodyDrift {
		t.Fatalf("Trigger = %q, want %q", current.Trigger, incident.TriggerBodyDrift)
	}
	if current.RootCause != "default/api" {
		t.Fatalf("RootCause = %q, want default/api", current.RootCause)
	}
	if previous.DriftScore < 0.15 {
		t.Fatalf("DriftScore = %.4f, want it above the 0.15 threshold", previous.DriftScore)
	}
	t.Logf("empty result set scored %.4f against the learned baseline", previous.DriftScore)
}

// A healthy endpoint whose payload only varies in timestamps and counters must
// never fire. This is the false-positive guard for the whole drift feature.
func TestStableEndpointNeverFiresDrift(t *testing.T) {
	t.Parallel()

	probes := []proberunner.Probe{correlatingProbe("default/health", "pulse-system/app-team", nil)}
	line := newPipeline(t, probes)
	now := time.Unix(1_700_000_000, 0)

	var previous *proberunner.ProbeResult
	for index := range 200 {
		body := fmt.Sprintf(
			`{"status":"ok","uptimeSeconds":%d,"checkedAt":"2026-08-31T%02d:%02d:00Z",`+
				`"requestId":"%08x"}`,
			1000+index*30, index%24, index%60, index*7919)

		result := passing("default/health", body, now.Add(time.Duration(index)*30*time.Second))
		line.intelligence.Evaluate(probes[0], result, previous, 40*time.Millisecond)
		previous = result
	}

	if got := line.dispatched.count(); got != 0 {
		t.Fatalf("dispatched %d incidents for a stable endpoint, want 0", got)
	}
}

// Recovering must close the incident, so a resolved outage does not linger in
// canary status forever.
func TestRecoveryClosesTheIncident(t *testing.T) {
	t.Parallel()

	probes := []proberunner.Probe{
		correlatingProbe("default/payments", "pulse-system/app-team", nil),
		correlatingProbe("default/orders", "pulse-system/app-team", nil),
	}

	line := newPipeline(t, probes)
	now := time.Unix(1_700_000_000, 0)

	down := []*proberunner.ProbeResult{
		failing("default/payments", "dial tcp 10.96.0.5:5432: i/o timeout", 0, now),
		failing("default/orders", "dial tcp 10.96.0.5:5432: i/o timeout", 0, now.Add(time.Second)),
	}
	line.intelligence.Evaluate(probes[0], down[0], nil, 2*time.Second)
	line.intelligence.Evaluate(probes[1], down[1], nil, 2*time.Second)

	settle(t, line.dispatched, 2)
	if got := len(line.engine.Open()); got != 1 {
		t.Fatalf("open incidents = %d, want 1", got)
	}

	line.intelligence.Evaluate(probes[0],
		passing("default/payments", `{"ok":true}`, now.Add(time.Minute)), down[0], 40*time.Millisecond)
	line.intelligence.Evaluate(probes[1],
		passing("default/orders", `{"ok":true}`, now.Add(time.Minute)), down[1], 40*time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(line.engine.Open()) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("open incidents = %d after full recovery, want 0", len(line.engine.Open()))
}

// A sustained outage must report ONE failure onset, not one per check. This is
// what keeps a long outage from flooding the engine and inflating the
// dependency learner's confidence.
func TestSustainedOutageReportsASingleOnset(t *testing.T) {
	t.Parallel()

	probes := []proberunner.Probe{correlatingProbe("default/api", "pulse-system/app-team", nil)}
	line := newPipeline(t, probes)
	now := time.Unix(1_700_000_000, 0)

	var previous *proberunner.ProbeResult
	for index := range 120 {
		result := failing("default/api", "Expected 200 but got 503", 503,
			now.Add(time.Duration(index)*30*time.Second))
		line.intelligence.Evaluate(probes[0], result, previous, 100*time.Millisecond)
		previous = result
	}

	settle(t, line.dispatched, 1)

	if got := line.dispatched.count(); got != 1 {
		t.Fatalf("dispatched %d incidents across a 60-minute outage, want 1", got)
	}
	if got := len(line.engine.Open()); got != 1 {
		t.Fatalf("open incidents = %d, want 1", got)
	}
}

// The default drift threshold is a tuned number, not a guess. This pins the
// measurements it was derived from, so a change to normalization, tokenization,
// or the bundled model cannot silently invalidate it.
//
// The gap that matters is between the WORST healthy score and the WEAKEST real
// failure. If a future change narrows it, this fails and the default needs
// revisiting rather than the test being relaxed.
func TestDriftThresholdSeparatesHealthyVariationFromRealFailures(t *testing.T) {
	t.Parallel()

	const (
		defaultThreshold = 0.15
		warmupChecks     = 20
	)

	embedder := realEmbedder(t)
	normalizer, err := anomalyNormalizer()
	if err != nil {
		t.Fatalf("NewNormalizer() error = %v", err)
	}
	ctx := context.Background()
	config := driftConfig(defaultThreshold, warmupChecks)

	names := []string{"widget", "gadget", "sprocket", "flange", "bracket", "gasket", "washer", "bolt"}
	random := newSeededRandom(42)

	// A healthy payload that genuinely varies: item count, names, statuses,
	// identifiers, and timestamps all change between checks.
	varying := func() string {
		count := 1 + random.Intn(8)

		var body strings.Builder
		body.WriteString(`{"items":[`)
		for index := range count {
			if index > 0 {
				body.WriteString(",")
			}
			status := "active"
			if random.Intn(4) == 0 {
				status = "pending"
			}
			fmt.Fprintf(&body, `{"id":%d,"name":%q,"status":%q}`,
				random.Intn(10000), names[random.Intn(len(names))], status)
		}
		fmt.Fprintf(&body, `],"total":%d,"generated":"2026-08-31T10:%02d:00Z"}`,
			count, random.Intn(60))

		return body.String()
	}

	score := func(detector driftDetector, body string) float64 {
		vectors, err := embedder.Embed(ctx, []string{normalizer.BodyText(body, 4096)})
		if err != nil {
			t.Fatalf("Embed() error = %v", err)
		}
		return detector.Observe("probe", vectors[0], config).Score
	}

	warmed := func() driftDetector {
		detector := newDriftDetector()
		for range warmupChecks {
			score(detector, varying())
		}
		return detector
	}

	// ── False-positive side ──────────────────────────────
	detector := warmed()
	worstHealthy := 0.0
	for range 300 {
		if got := score(detector, varying()); got > worstHealthy {
			worstHealthy = got
		}
	}

	if worstHealthy >= defaultThreshold {
		t.Fatalf("healthy variation peaked at %.4f, at or above the %.2f default — "+
			"the default would produce false positives", worstHealthy, defaultThreshold)
	}

	// ── Detection side ───────────────────────────────────
	failures := map[string]string{
		"empty result set": `{"items":[],"total":0}`,
		"null collection":  `{"items":null,"total":0}`,
		"truncated json":   `{"items":[{"id":1,"name":"widg`,
		"error object":     `{"error":"unauthorized","message":"token expired"}`,
		"stack trace":      `{"error":"panic: runtime error: index out of range","stack":"goroutine 1"}`,
		"maintenance page": `<html><body><h1>Service Temporarily Unavailable</h1></body></html>`,
		"empty body":       ``,
	}

	weakest := 1e9
	weakestName := ""
	for name, body := range failures {
		got := score(warmed(), body)
		if got <= defaultThreshold {
			t.Errorf("%s scored %.4f, at or below the %.2f default — it would not be detected",
				name, got, defaultThreshold)
		}
		if got < weakest {
			weakest, weakestName = got, name
		}
	}

	margin := weakest / worstHealthy
	t.Logf("worst healthy %.4f, weakest failure %.4f (%s), separation %.1fx",
		worstHealthy, weakest, weakestName, margin)

	if margin < 2.0 {
		t.Fatalf("separation is only %.1fx (healthy %.4f vs failure %.4f) — "+
			"too narrow for a safe default threshold", margin, worstHealthy, weakest)
	}
}
