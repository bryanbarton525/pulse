package integration

import (
	"math/rand"

	"github.com/bryanbarton525/pulse/internal/anomaly"
)

// These thin aliases keep the threshold regression test readable without
// importing anomaly's names into every scenario above.

type driftDetector = *anomaly.DriftDetector

func newDriftDetector() driftDetector { return anomaly.NewDriftDetector() }

func anomalyNormalizer() (*anomaly.Normalizer, error) { return anomaly.NewNormalizer(nil) }

func driftConfig(threshold float64, warmup int) anomaly.DriftConfig {
	return anomaly.DriftConfig{
		Threshold:           threshold,
		WarmupChecks:        warmup,
		ConsecutiveBreaches: 2,
	}
}

func newSeededRandom(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }
