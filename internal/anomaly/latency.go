package anomaly

import (
	"math"
	"sync"
	"time"
)

// LatencyConfig is the per-probe tuning for latency-shift detection.
type LatencyConfig struct {
	ZScoreThreshold     float64
	WarmupChecks        int
	ConsecutiveBreaches int
}

// LatencyResult is the outcome of observing one check's duration.
type LatencyResult struct {
	// ZScore is how many standard deviations above the rolling mean this
	// check landed. Negative values mean faster than usual.
	ZScore float64

	// Warming reports that too few samples exist to trust the statistics.
	Warming bool

	// Shifted reports a sustained slowdown.
	Shifted bool

	Samples int
}

// latencyStats holds an exponentially weighted mean and variance.
type latencyStats struct {
	mean        float64
	variance    float64
	samples     int
	consecutive int
}

// LatencyDetector watches check duration for probes that are still passing.
//
// No model is involved. This is the cheap companion to body drift: together
// they cover "green but wrong" from two directions — the payload changed
// meaning, or the endpoint is degrading.
type LatencyDetector struct {
	mu    sync.Mutex
	stats map[string]*latencyStats
}

// NewLatencyDetector builds an empty detector.
func NewLatencyDetector() *LatencyDetector {
	return &LatencyDetector{stats: map[string]*latencyStats{}}
}

// Observe records one PASSING check's duration and scores it.
//
// Failing checks must not be fed here. A failure is frequently a timeout, so
// its duration is the client's timeout ceiling rather than the service's real
// latency; absorbing those would inflate the mean until genuine slowdowns stop
// registering.
func (l *LatencyDetector) Observe(probe string, duration time.Duration, config LatencyConfig) LatencyResult {
	warmup := max(config.WarmupChecks, 2)
	breaches := max(config.ConsecutiveBreaches, 1)

	seconds := duration.Seconds()

	l.mu.Lock()
	defer l.mu.Unlock()

	current, found := l.stats[probe]
	if !found {
		current = &latencyStats{}
		l.stats[probe] = current
	}

	if current.samples < warmup {
		current.absorb(seconds, warmup)
		return LatencyResult{Warming: true, Samples: current.samples}
	}

	deviation := math.Sqrt(current.variance)
	zScore := 0.0
	if deviation > 0 {
		zScore = (seconds - current.mean) / deviation
	}

	if zScore > config.ZScoreThreshold {
		current.consecutive++
		// Not absorbed, for the same reason drift does not absorb a drifted
		// body: a sustained slowdown would otherwise become the new normal.
		return LatencyResult{
			ZScore:  zScore,
			Shifted: current.consecutive >= breaches,
			Samples: current.samples,
		}
	}

	current.consecutive = 0
	current.absorb(seconds, warmup)

	return LatencyResult{ZScore: zScore, Samples: current.samples}
}

func (s *latencyStats) absorb(value float64, warmup int) {
	s.samples++

	if s.samples == 1 {
		s.mean = value
		return
	}

	alpha := max(1/float64(s.samples), 1/float64(warmup))

	delta := value - s.mean
	s.mean += alpha * delta
	// Exponentially weighted variance, tracking the same window as the mean.
	s.variance = (1 - alpha) * (s.variance + alpha*delta*delta)
}

// Forget drops a probe's latency statistics.
func (l *LatencyDetector) Forget(probe string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.stats, probe)
}

// Retain drops statistics for probes not in keep, so a config reload does not
// reset every probe's warmup.
func (l *LatencyDetector) Retain(keep map[string]struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for probe := range l.stats {
		if _, found := keep[probe]; !found {
			delete(l.stats, probe)
		}
	}
}
