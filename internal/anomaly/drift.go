package anomaly

import (
	"math"
	"sync"

	"github.com/bryanbarton525/pulse/internal/embed"
)

// DriftConfig is the per-probe tuning for body-drift detection.
type DriftConfig struct {
	Threshold           float64
	WarmupChecks        int
	ConsecutiveBreaches int
}

// DriftResult is the outcome of scoring one passing check.
type DriftResult struct {
	// Score is the cosine distance from the baseline, in [0, 2].
	Score float64

	// Warming reports that the baseline has too few samples to trust. While
	// warming, Score is meaningless and Drifted is always false.
	Warming bool

	// Drifted reports a sustained departure from the baseline: the score
	// exceeded the threshold on ConsecutiveBreaches checks in a row.
	Drifted bool

	// Samples is how many observations the baseline has absorbed.
	Samples int
}

// baseline is one probe's learned notion of a normal response body.
//
// It holds a single running-average vector rather than a window of past
// vectors: at a few thousand probes that is about 1.5KB each, and it never
// needs to be re-scanned. Nothing here is persisted — a restart re-warms from
// scratch, which is why the runner front-loads warmup samples in a burst.
type baseline struct {
	centroid    embed.Vector
	samples     int
	consecutive int
}

// DriftDetector tracks a body baseline per probe.
//
// It is safe for concurrent use: every probe runs in its own goroutine and they
// all report here.
type DriftDetector struct {
	mu        sync.Mutex
	baselines map[string]*baseline
}

// NewDriftDetector builds an empty detector.
func NewDriftDetector() *DriftDetector {
	return &DriftDetector{baselines: map[string]*baseline{}}
}

// Observe scores one PASSING check's body against the probe's baseline.
//
// Only passing checks belong here. Drift exists to catch a canary that is green
// while its payload has quietly changed meaning; a failing check is already
// reported through the normal path and its body is usually an error page that
// would corrupt the baseline.
func (d *DriftDetector) Observe(probe string, vector embed.Vector, config DriftConfig) DriftResult {
	warmup := config.WarmupChecks
	if warmup < 2 {
		warmup = 2
	}
	breaches := config.ConsecutiveBreaches
	if breaches < 1 {
		breaches = 1
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	current, found := d.baselines[probe]
	if !found {
		current = &baseline{}
		d.baselines[probe] = current
	}

	// A policy edit can swap the embedding model underneath a live baseline.
	// The old centroid is meaningless in the new space, so start over rather
	// than comparing across spaces.
	if current.samples > 0 && current.centroid.Space != vector.Space {
		*current = baseline{}
	}

	if current.samples < warmup {
		current.absorb(vector, warmup)
		return DriftResult{Warming: true, Samples: current.samples}
	}

	score := embed.Distance(current.centroid, vector)

	if score > config.Threshold {
		current.consecutive++
		// Deliberately NOT absorbed. If a drifted body kept updating the
		// baseline, a sustained change would train itself into "normal" and
		// the signal would fade out exactly when it mattered most.
		return DriftResult{
			Score:   score,
			Drifted: current.consecutive >= breaches,
			Samples: current.samples,
		}
	}

	current.consecutive = 0
	current.absorb(vector, warmup)

	return DriftResult{Score: score, Samples: current.samples}
}

// absorb folds a vector into the running average.
//
// The weight starts at 1/n so the first few samples converge quickly, then
// floors at 1/warmup so the baseline keeps tracking slow legitimate change
// (a deploy that adds a field) instead of freezing after warmup.
func (b *baseline) absorb(vector embed.Vector, warmup int) {
	b.samples++

	if len(b.centroid.Values) == 0 {
		b.centroid = embed.Vector{
			Space:  vector.Space,
			Values: append([]float32(nil), vector.Values...),
		}
		return
	}

	alpha := 1 / float64(b.samples)
	if floor := 1 / float64(warmup); alpha < floor {
		alpha = floor
	}

	for index := range b.centroid.Values {
		blended := float64(b.centroid.Values[index])*(1-alpha) + float64(vector.Values[index])*alpha
		b.centroid.Values[index] = float32(blended)
	}

	normalize(b.centroid.Values)
}

// Forget drops a probe's baseline.
func (d *DriftDetector) Forget(probe string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.baselines, probe)
}

// Retain drops every baseline for a probe not in keep.
//
// Config reloads must call this rather than clearing everything: editing one
// canary should not blind every other probe in the cluster for a full warmup
// period.
func (d *DriftDetector) Retain(keep map[string]struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for probe := range d.baselines {
		if _, found := keep[probe]; !found {
			delete(d.baselines, probe)
		}
	}
}

// Tracked reports how many probes have a baseline.
func (d *DriftDetector) Tracked() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.baselines)
}

func normalize(values []float32) {
	var sum float64
	for _, value := range values {
		sum += float64(value) * float64(value)
	}
	if sum == 0 {
		return
	}

	scale := float32(1 / math.Sqrt(sum))
	for index := range values {
		values[index] *= scale
	}
}
