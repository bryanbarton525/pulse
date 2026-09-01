package anomaly

import (
	"math/rand"
	"testing"
	"time"
)

func defaultLatencyConfig() LatencyConfig {
	return LatencyConfig{ZScoreThreshold: 3.0, WarmupChecks: 30, ConsecutiveBreaches: 3}
}

// warmLatency feeds jittered samples around a target so the variance is
// non-zero, as it would be against a real service.
func warmLatency(detector *LatencyDetector, probe string, config LatencyConfig, base time.Duration) {
	random := rand.New(rand.NewSource(1))
	for range config.WarmupChecks {
		jitter := time.Duration(random.NormFloat64() * float64(base) * 0.05)
		detector.Observe(probe, base+jitter, config)
	}
}

func TestLatencySuppressesScoringDuringWarmup(t *testing.T) {
	t.Parallel()

	detector := NewLatencyDetector()
	config := defaultLatencyConfig()

	for round := range config.WarmupChecks {
		if result := detector.Observe("ns/probe", 100*time.Millisecond, config); !result.Warming {
			t.Fatalf("round %d: Warming = false, want true during warmup", round)
		}
	}

	if result := detector.Observe("ns/probe", 100*time.Millisecond, config); result.Warming {
		t.Fatal("Warming = true after the warmup budget was met")
	}
}

func TestLatencyIgnoresNormalJitter(t *testing.T) {
	t.Parallel()

	detector := NewLatencyDetector()
	config := defaultLatencyConfig()
	warmLatency(detector, "ns/probe", config, 100*time.Millisecond)

	random := rand.New(rand.NewSource(2))
	for round := range 100 {
		jitter := time.Duration(random.NormFloat64() * float64(100*time.Millisecond) * 0.05)
		if result := detector.Observe("ns/probe", 100*time.Millisecond+jitter, config); result.Shifted {
			t.Fatalf("round %d: Shifted = true for ordinary jitter (z=%v)", round, result.ZScore)
		}
	}
}

func TestLatencyDetectsSustainedSlowdown(t *testing.T) {
	t.Parallel()

	detector := NewLatencyDetector()
	config := defaultLatencyConfig()
	warmLatency(detector, "ns/probe", config, 100*time.Millisecond)

	var shifted bool
	for range config.ConsecutiveBreaches {
		result := detector.Observe("ns/probe", 900*time.Millisecond, config)
		shifted = result.Shifted
	}

	if !shifted {
		t.Fatal("Shifted = false after a sustained 9x slowdown")
	}
}

func TestLatencyDebounceHoldsSingleSpike(t *testing.T) {
	t.Parallel()

	detector := NewLatencyDetector()
	config := defaultLatencyConfig()
	warmLatency(detector, "ns/probe", config, 100*time.Millisecond)

	if result := detector.Observe("ns/probe", 900*time.Millisecond, config); result.Shifted {
		t.Fatal("Shifted = true on a single spike, want the debounce to hold it")
	}

	// Recovering resets the counter, so an isolated spike later must not fire
	// on its own either.
	detector.Observe("ns/probe", 100*time.Millisecond, config)
	if result := detector.Observe("ns/probe", 900*time.Millisecond, config); result.Shifted {
		t.Fatal("Shifted = true on a spike after recovery, want the counter to have reset")
	}
}

// A faster-than-usual check is not a problem and must never fire.
func TestLatencyIgnoresSpeedups(t *testing.T) {
	t.Parallel()

	detector := NewLatencyDetector()
	config := defaultLatencyConfig()
	warmLatency(detector, "ns/probe", config, 100*time.Millisecond)

	result := detector.Observe("ns/probe", 5*time.Millisecond, config)
	if result.Shifted {
		t.Fatal("Shifted = true for a speedup")
	}
	if result.ZScore > 0 {
		t.Fatalf("ZScore = %v for a speedup, want it negative", result.ZScore)
	}
}

// Same reasoning as drift: a sustained slowdown must not quietly become the
// new baseline.
func TestLatencyBaselineDoesNotAbsorbSustainedSlowdown(t *testing.T) {
	t.Parallel()

	detector := NewLatencyDetector()
	config := defaultLatencyConfig()
	warmLatency(detector, "ns/probe", config, 100*time.Millisecond)

	var last LatencyResult
	for range 200 {
		last = detector.Observe("ns/probe", 900*time.Millisecond, config)
	}

	if !last.Shifted {
		t.Fatal("Shifted = false during a sustained slowdown")
	}
	if last.ZScore < config.ZScoreThreshold {
		t.Fatalf("ZScore decayed to %v, want it above the threshold %v",
			last.ZScore, config.ZScoreThreshold)
	}
}

func TestLatencyRetainKeepsSurvivingProbes(t *testing.T) {
	t.Parallel()

	detector := NewLatencyDetector()
	config := defaultLatencyConfig()
	warmLatency(detector, "ns/keep", config, 100*time.Millisecond)
	warmLatency(detector, "ns/drop", config, 100*time.Millisecond)

	detector.Retain(map[string]struct{}{"ns/keep": {}})

	if result := detector.Observe("ns/keep", 100*time.Millisecond, config); result.Warming {
		t.Fatal("the retained probe lost its statistics")
	}
	if result := detector.Observe("ns/drop", 100*time.Millisecond, config); !result.Warming {
		t.Fatal("the dropped probe kept its statistics")
	}
}
