package anomaly

import (
	"math"
	"testing"

	"github.com/bryanbarton525/pulse/internal/embed"
)

func potionVector(values ...float32) embed.Vector {
	normalize(values)
	return embed.Vector{Space: embed.SpacePotion, Values: values}
}

func defaultDriftConfig() DriftConfig {
	return DriftConfig{Threshold: 0.35, WarmupChecks: 10, ConsecutiveBreaches: 2}
}

// While warming, scores are not trusted and nothing can fire — otherwise the
// first check after a rollout would page someone.
func TestDriftSuppressesScoringDuringWarmup(t *testing.T) {
	t.Parallel()

	detector := NewDriftDetector()
	config := defaultDriftConfig()
	normal := potionVector(1, 0, 0)

	for round := range config.WarmupChecks {
		result := detector.Observe("ns/probe", normal, config)
		if !result.Warming {
			t.Fatalf("round %d: Warming = false, want true during warmup", round)
		}
		if result.Drifted {
			t.Fatalf("round %d: Drifted = true during warmup", round)
		}
	}

	if result := detector.Observe("ns/probe", normal, config); result.Warming {
		t.Fatal("Warming = true after the warmup budget was met")
	}
}

func TestDriftScoresStableBodiesNearZero(t *testing.T) {
	t.Parallel()

	detector := NewDriftDetector()
	config := defaultDriftConfig()
	normal := potionVector(1, 0, 0)

	for range config.WarmupChecks {
		detector.Observe("ns/probe", normal, config)
	}

	result := detector.Observe("ns/probe", normal, config)
	if result.Score > 1e-5 {
		t.Fatalf("Score = %v for an unchanged body, want ~0", result.Score)
	}
	if result.Drifted {
		t.Fatal("Drifted = true for an unchanged body")
	}
}

// The signal itself: a body that changed meaning scores far from the baseline.
func TestDriftDetectsChangedBodyAfterDebounce(t *testing.T) {
	t.Parallel()

	detector := NewDriftDetector()
	config := defaultDriftConfig()
	normal := potionVector(1, 0, 0)
	changed := potionVector(0, 1, 0)

	for range config.WarmupChecks {
		detector.Observe("ns/probe", normal, config)
	}

	first := detector.Observe("ns/probe", changed, config)
	if math.Abs(first.Score-1) > 1e-5 {
		t.Fatalf("Score = %v for an orthogonal body, want ~1", first.Score)
	}
	if first.Drifted {
		t.Fatal("Drifted = true on the first breach, want the debounce to hold it")
	}

	second := detector.Observe("ns/probe", changed, config)
	if !second.Drifted {
		t.Fatal("Drifted = false after ConsecutiveBreaches breaches")
	}
}

// A single odd response between normal ones must not fire.
func TestDriftDebounceResetsOnRecovery(t *testing.T) {
	t.Parallel()

	detector := NewDriftDetector()
	config := defaultDriftConfig()
	normal := potionVector(1, 0, 0)
	changed := potionVector(0, 1, 0)

	for range config.WarmupChecks {
		detector.Observe("ns/probe", normal, config)
	}

	if result := detector.Observe("ns/probe", changed, config); result.Drifted {
		t.Fatal("Drifted = true on a single blip")
	}
	detector.Observe("ns/probe", normal, config)

	if result := detector.Observe("ns/probe", changed, config); result.Drifted {
		t.Fatal("Drifted = true immediately after the counter should have reset")
	}
}

// The failure mode this guards against: if a drifted body kept updating the
// baseline, a sustained change would become the new normal and the signal
// would fade out exactly when it matters.
func TestDriftBaselineDoesNotAbsorbSustainedChange(t *testing.T) {
	t.Parallel()

	detector := NewDriftDetector()
	config := defaultDriftConfig()
	normal := potionVector(1, 0, 0)
	changed := potionVector(0, 1, 0)

	for range config.WarmupChecks {
		detector.Observe("ns/probe", normal, config)
	}

	var last DriftResult
	for range 200 {
		last = detector.Observe("ns/probe", changed, config)
	}

	if last.Score < 0.9 {
		t.Fatalf("Score decayed to %v over a sustained outage, want it to stay near 1", last.Score)
	}
	if !last.Drifted {
		t.Fatal("Drifted = false during a sustained change")
	}
}

// A slow legitimate change (a deploy adding a field) should eventually be
// accepted as the new normal rather than alerting forever.
func TestDriftBaselineTracksGradualChange(t *testing.T) {
	t.Parallel()

	detector := NewDriftDetector()
	config := defaultDriftConfig()

	for range config.WarmupChecks {
		detector.Observe("ns/probe", potionVector(1, 0, 0), config)
	}

	// Rotate the body a little at a time, each step well under the threshold.
	var last DriftResult
	for step := 1; step <= 60; step++ {
		angle := float64(step) * 0.02
		last = detector.Observe(
			"ns/probe",
			potionVector(float32(math.Cos(angle)), float32(math.Sin(angle)), 0),
			config,
		)
	}

	if last.Drifted {
		t.Fatal("Drifted = true for gradual, legitimate change")
	}
	if last.Score > config.Threshold {
		t.Fatalf("Score = %v, want the baseline to have followed the drift", last.Score)
	}
}

func TestDriftIsolatesProbesFromEachOther(t *testing.T) {
	t.Parallel()

	detector := NewDriftDetector()
	config := defaultDriftConfig()

	for range config.WarmupChecks {
		detector.Observe("ns/a", potionVector(1, 0, 0), config)
		detector.Observe("ns/b", potionVector(0, 1, 0), config)
	}

	if result := detector.Observe("ns/a", potionVector(1, 0, 0), config); result.Score > 1e-5 {
		t.Fatalf("probe a Score = %v, want ~0 — baselines leaked between probes", result.Score)
	}
	if result := detector.Observe("ns/b", potionVector(0, 1, 0), config); result.Score > 1e-5 {
		t.Fatalf("probe b Score = %v, want ~0 — baselines leaked between probes", result.Score)
	}
}

// Swapping the model in a policy makes the old centroid meaningless. Comparing
// across spaces would panic, so the baseline must reset instead.
func TestDriftResetsBaselineWhenEmbeddingSpaceChanges(t *testing.T) {
	t.Parallel()

	detector := NewDriftDetector()
	config := defaultDriftConfig()

	for range config.WarmupChecks {
		detector.Observe("ns/probe", potionVector(1, 0, 0), config)
	}

	result := detector.Observe("ns/probe", embed.Vector{
		Space: embed.SpaceMiniLM, Values: []float32{1, 0, 0},
	}, config)

	if !result.Warming {
		t.Fatal("Warming = false after the embedding space changed, want a reset baseline")
	}
	if result.Samples != 1 {
		t.Fatalf("Samples = %d after a space change, want 1", result.Samples)
	}
}

// Editing one canary must not blind every other probe for a full warmup.
func TestDriftRetainKeepsSurvivingProbes(t *testing.T) {
	t.Parallel()

	detector := NewDriftDetector()
	config := defaultDriftConfig()

	for range config.WarmupChecks {
		detector.Observe("ns/keep", potionVector(1, 0, 0), config)
		detector.Observe("ns/drop", potionVector(0, 1, 0), config)
	}
	if got := detector.Tracked(); got != 2 {
		t.Fatalf("Tracked() = %d, want 2", got)
	}

	detector.Retain(map[string]struct{}{"ns/keep": {}})

	if got := detector.Tracked(); got != 1 {
		t.Fatalf("Tracked() = %d after Retain, want 1", got)
	}
	if result := detector.Observe("ns/keep", potionVector(1, 0, 0), config); result.Warming {
		t.Fatal("the retained probe lost its baseline")
	}
	if result := detector.Observe("ns/drop", potionVector(0, 1, 0), config); !result.Warming {
		t.Fatal("the dropped probe kept its baseline")
	}
}
