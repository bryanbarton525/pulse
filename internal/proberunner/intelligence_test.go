package proberunner

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bryanbarton525/pulse/internal/embed"
	"github.com/bryanbarton525/pulse/internal/observation"
)

// captureShipper records everything the runner tries to send.
type captureShipper struct {
	mu      sync.Mutex
	signals []observation.Observation
}

func (c *captureShipper) Ship(_ context.Context, signal observation.Observation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.signals = append(c.signals, signal)
}

func (c *captureShipper) kinds() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	kinds := make([]string, 0, len(c.signals))
	for _, signal := range c.signals {
		kinds = append(kinds, signal.Kind)
	}
	return kinds
}

// bodyEmbedder maps a body to a vector so drift can be driven deterministically.
type bodyEmbedder struct{ vectors map[string][]float32 }

func (b *bodyEmbedder) Space() string   { return embed.SpacePotion }
func (b *bodyEmbedder) Dimensions() int { return 3 }
func (b *bodyEmbedder) Close() error    { return nil }

func (b *bodyEmbedder) Embed(_ context.Context, texts []string) ([]embed.Vector, error) {
	out := make([]embed.Vector, len(texts))
	for index, text := range texts {
		values := []float32{0, 0, 1}
		for key, candidate := range b.vectors {
			if strings.Contains(text, key) {
				values = candidate
				break
			}
		}
		out[index] = embed.Vector{Space: embed.SpacePotion, Values: append([]float32(nil), values...)}
	}
	return out, nil
}

func testIntelligence(t *testing.T, embedder embed.Embedder) (*Intelligence, *captureShipper) {
	t.Helper()

	shipper := &captureShipper{}
	intelligence := NewIntelligence(
		embedder, shipper, logr.Discard(), NewIntelligenceMetrics(prometheus.NewRegistry()))
	return intelligence, shipper
}

func intelligentProbe(triggers ProbeTriggers) Probe {
	return Probe{
		Name:           "default/api",
		Type:           ProbeTypeHTTP,
		URL:            "https://api.example.com/health",
		ExpectedStatus: 200,
		Intelligence: &ProbeIntelligence{
			Policy:   "pulse-system/prod",
			Triggers: triggers,
		},
	}
}

func healthy(body string) *ProbeResult {
	return &ProbeResult{
		Name: "default/api", Healthy: true, StatusCode: 200, ExpectedStatus: 200,
		Message: "Got expected status 200", LastCheckTime: time.Now(), BodySnippet: body,
	}
}

func failed(message string) *ProbeResult {
	return &ProbeResult{
		Name: "default/api", Healthy: false, StatusCode: 503, ExpectedStatus: 200,
		Message: message, LastCheckTime: time.Now(),
	}
}

// A probe down for an hour is ONE event. Shipping every failing tick would
// flood the engine and inflate the dependency learner's confidence.
func TestIntelligenceShipsFailureOnsetOnly(t *testing.T) {
	t.Parallel()

	intelligence, shipper := testIntelligence(t, nil)
	probe := intelligentProbe(ProbeTriggers{})

	first := failed("Expected 200 but got 503")
	intelligence.Evaluate(probe, first, nil, time.Millisecond)

	for range 10 {
		next := failed("Expected 200 but got 503")
		intelligence.Evaluate(probe, next, first, time.Millisecond)
		first = next
	}

	kinds := shipper.kinds()
	if len(kinds) != 1 || kinds[0] != observation.KindFailure {
		t.Fatalf("shipped %v, want exactly one failure onset", kinds)
	}
}

func TestIntelligenceShipsRecovery(t *testing.T) {
	t.Parallel()

	intelligence, shipper := testIntelligence(t, nil)
	probe := intelligentProbe(ProbeTriggers{})

	failure := failed("Expected 200 but got 503")
	intelligence.Evaluate(probe, failure, nil, time.Millisecond)
	intelligence.Evaluate(probe, healthy(""), failure, time.Millisecond)

	kinds := shipper.kinds()
	if len(kinds) != 2 || kinds[1] != observation.KindRecovery {
		t.Fatalf("shipped %v, want a failure then a recovery", kinds)
	}
}

// A steadily healthy probe must produce no traffic at all.
func TestIntelligenceIsSilentWhileHealthy(t *testing.T) {
	t.Parallel()

	intelligence, shipper := testIntelligence(t, nil)
	probe := intelligentProbe(ProbeTriggers{})

	previous := healthy("")
	for range 20 {
		next := healthy("")
		intelligence.Evaluate(probe, next, previous, time.Millisecond)
		previous = next
	}

	if kinds := shipper.kinds(); len(kinds) != 0 {
		t.Fatalf("shipped %v while healthy, want nothing", kinds)
	}
}

// The signal drift exists for: the check passes, but the payload changed.
func TestIntelligenceShipsDriftForPassingChecks(t *testing.T) {
	t.Parallel()

	embedder := &bodyEmbedder{vectors: map[string][]float32{
		"widget": {1, 0, 0},
		"items":  {0, 1, 0},
	}}
	intelligence, shipper := testIntelligence(t, embedder)
	probe := intelligentProbe(ProbeTriggers{
		BodyDrift: &ProbeBodyDriftTrigger{
			Threshold: 0.35, WarmupChecks: 5, ConsecutiveBreaches: 2,
			MaxBodyBytes: 4096, SampleEvery: 1,
		},
	})

	normal := `{"items":[{"id":1,"name":"widget"}]}`
	previous := healthy(normal)
	for range 6 {
		next := healthy(normal)
		intelligence.Evaluate(probe, next, previous, time.Millisecond)
		previous = next
	}
	if kinds := shipper.kinds(); len(kinds) != 0 {
		t.Fatalf("shipped %v during warmup, want nothing", kinds)
	}

	// The endpoint now returns an empty result set while still answering 200.
	empty := `{"items":[]}`
	for range 2 {
		next := healthy(empty)
		intelligence.Evaluate(probe, next, previous, time.Millisecond)
		previous = next
	}

	kinds := shipper.kinds()
	if len(kinds) != 1 || kinds[0] != observation.KindBodyDrift {
		t.Fatalf("shipped %v, want one bodyDrift signal", kinds)
	}
}

// The whole privacy design: the body is embedded in this process and the
// signal that leaves carries a score, never the payload.
func TestIntelligenceNeverShipsResponseBodies(t *testing.T) {
	t.Parallel()

	embedder := &bodyEmbedder{vectors: map[string][]float32{
		"alice": {1, 0, 0},
		"empty": {0, 1, 0},
	}}
	intelligence, shipper := testIntelligence(t, embedder)
	probe := intelligentProbe(ProbeTriggers{
		BodyDrift: &ProbeBodyDriftTrigger{
			Threshold: 0.35, WarmupChecks: 3, ConsecutiveBreaches: 1,
			MaxBodyBytes: 4096, SampleEvery: 1,
		},
	})

	secret := `{"customer":"alice@example.com","balance":41999,"ssn":"123-45-6789"}`
	previous := healthy(secret)
	for range 4 {
		next := healthy(secret)
		intelligence.Evaluate(probe, next, previous, time.Millisecond)
		previous = next
	}
	intelligence.Evaluate(probe, healthy(`{"result":"empty"}`), previous, time.Millisecond)

	shipper.mu.Lock()
	encoded, err := json.Marshal(shipper.signals)
	shipper.mu.Unlock()
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	for _, leaked := range []string{"alice@example.com", "123-45-6789", "41999"} {
		if strings.Contains(string(encoded), leaked) {
			t.Fatalf("a shipped observation leaked %q: %s", leaked, encoded)
		}
	}
	if len(shipper.signals) == 0 {
		t.Fatal("expected a drift signal for this test to be meaningful")
	}
}

// ProbeResult crosses /results to the controller, so the body must not be in
// its serialized form either.
func TestProbeResultDoesNotSerializeBody(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(healthy(`{"ssn":"123-45-6789"}`))
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "123-45-6789") {
		t.Fatalf("ProbeResult serialized the response body: %s", encoded)
	}
}

// Redaction patterns must be applied before any text is shipped.
func TestIntelligenceAppliesPolicyRedaction(t *testing.T) {
	t.Parallel()

	intelligence, shipper := testIntelligence(t, nil)
	probe := intelligentProbe(ProbeTriggers{
		BodyDrift: &ProbeBodyDriftTrigger{
			Threshold: 0.35, WarmupChecks: 5, ConsecutiveBreaches: 2,
			MaxBodyBytes: 4096, SampleEvery: 1,
			Redact: []string{`token=[A-Za-z0-9]+`},
		},
	})

	intelligence.Evaluate(probe, failed("auth rejected: token=abc123SECRET"), nil, time.Millisecond)

	shipper.mu.Lock()
	defer shipper.mu.Unlock()
	encoded, _ := json.Marshal(shipper.signals)
	if strings.Contains(string(encoded), "abc123SECRET") {
		t.Fatalf("a shipped observation leaked a redacted token: %s", encoded)
	}
}

// A canary that never opted in must produce nothing.
func TestIntelligenceIgnoresProbesWithoutPolicy(t *testing.T) {
	t.Parallel()

	intelligence, shipper := testIntelligence(t, nil)
	probe := Probe{Name: "default/plain", Type: ProbeTypeHTTP}

	intelligence.Evaluate(probe, failed("boom"), nil, time.Millisecond)

	if kinds := shipper.kinds(); len(kinds) != 0 {
		t.Fatalf("shipped %v for a probe without intelligence, want nothing", kinds)
	}
}

// Editing one canary must not reset every other probe's warmup.
func TestIntelligenceRetainKeepsSurvivingProbeState(t *testing.T) {
	t.Parallel()

	embedder := &bodyEmbedder{vectors: map[string][]float32{"stable": {1, 0, 0}}}
	intelligence, _ := testIntelligence(t, embedder)

	keep := intelligentProbe(ProbeTriggers{
		BodyDrift: &ProbeBodyDriftTrigger{
			Threshold: 0.35, WarmupChecks: 3, ConsecutiveBreaches: 1,
			MaxBodyBytes: 4096, SampleEvery: 1,
		},
	})

	previous := healthy("stable")
	for range 5 {
		next := healthy("stable")
		intelligence.Evaluate(keep, next, previous, time.Millisecond)
		previous = next
	}

	intelligence.Retain(map[string]struct{}{"default/api": {}})

	// Still warmed: a matching body scores, rather than restarting warmup.
	result := healthy("stable")
	intelligence.Evaluate(keep, result, previous, time.Millisecond)
	if result.DriftScore != 0 {
		t.Fatalf("DriftScore = %v for an unchanged body, want 0", result.DriftScore)
	}

	intelligence.Retain(map[string]struct{}{"other/probe": {}})
	// Dropped: the baseline is gone, so nothing can fire on the next check.
	dropped := healthy("stable")
	intelligence.Evaluate(keep, dropped, previous, time.Millisecond)
	if dropped.DriftScore != 0 {
		t.Fatalf("DriftScore = %v right after a reset, want 0 while warming", dropped.DriftScore)
	}
}
