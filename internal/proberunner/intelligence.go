package proberunner

import (
	"context"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bryanbarton525/pulse/internal/anomaly"
	"github.com/bryanbarton525/pulse/internal/embed"
	"github.com/bryanbarton525/pulse/internal/observation"
)

// Shipper delivers observations to the incident engine.
type Shipper interface {
	Ship(ctx context.Context, signal observation.Observation)
}

// Intelligence is the runner-side half of the feature.
//
// It owns everything that needs the raw material of a check: the response body,
// the exact duration, the previous result. All of that stays in this process —
// only normalized, redacted text and scalar scores are ever shipped onward.
//
// The two evaluations here run on the hot path, on every check, so both are
// cheap by construction: drift is a static-embedding lookup plus one cosine,
// and latency is arithmetic.
type Intelligence struct {
	mu          sync.RWMutex
	normalizers map[string]*anomaly.Normalizer
	sampleCount map[string]int

	// reportedHits and reportedMisses are the cache totals already published
	// to Prometheus, so RecordCacheStats can publish only the delta.
	reportedHits   uint64
	reportedMisses uint64

	embedder embed.Embedder
	drift    *anomaly.DriftDetector
	latency  *anomaly.LatencyDetector
	shipper  Shipper
	logger   logr.Logger
	metrics  *IntelligenceMetrics
}

// IntelligenceMetrics are the runner's Prometheus collectors for this feature.
type IntelligenceMetrics struct {
	driftScore    *prometheus.GaugeVec
	latencyZScore *prometheus.GaugeVec
	signals       *prometheus.CounterVec
	embedSeconds  prometheus.Histogram
	cacheHits     prometheus.Counter
	cacheMisses   prometheus.Counter
}

// NewIntelligenceMetrics registers the runner-side collectors.
func NewIntelligenceMetrics(registerer prometheus.Registerer) *IntelligenceMetrics {
	metrics := &IntelligenceMetrics{
		driftScore: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pulse_canary_drift_score",
			Help: "Cosine distance between the latest passing response body and the learned baseline.",
		}, []string{"probe", "policy"}),
		latencyZScore: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pulse_canary_latency_zscore",
			Help: "Standard deviations above the rolling mean for the latest passing check.",
		}, []string{"probe", "policy"}),
		signals: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pulse_canary_signals_total",
			Help: "Signals shipped to the incident engine, labeled by kind.",
		}, []string{"probe", "kind"}),
		embedSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "pulse_embed_duration_seconds",
			Help:    "Time spent embedding response bodies on the hot path.",
			Buckets: []float64{.0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1},
		}),
		cacheHits: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "pulse_embed_cache_hits_total",
			Help: "Body embeddings served from cache rather than recomputed.",
		}),
		cacheMisses: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "pulse_embed_cache_misses_total",
			Help: "Body embeddings that had to be computed.",
		}),
	}

	registerer.MustRegister(
		metrics.driftScore, metrics.latencyZScore, metrics.signals,
		metrics.embedSeconds, metrics.cacheHits, metrics.cacheMisses)

	return metrics
}

// NewIntelligence builds the runner-side evaluator. A nil embedder disables
// body drift while leaving latency and failure reporting working.
func NewIntelligence(
	embedder embed.Embedder,
	shipper Shipper,
	logger logr.Logger,
	metrics *IntelligenceMetrics,
) *Intelligence {
	return &Intelligence{
		normalizers: map[string]*anomaly.Normalizer{},
		sampleCount: map[string]int{},
		embedder:    embedder,
		drift:       anomaly.NewDriftDetector(),
		latency:     anomaly.NewLatencyDetector(),
		shipper:     shipper,
		logger:      logger,
		metrics:     metrics,
	}
}

// Evaluate runs every enabled trigger for one completed check.
//
// previous is the result this probe reported last, or nil on the first check.
// It is what makes onset detection possible without any extra state.
func (i *Intelligence) Evaluate(probe Probe, result *ProbeResult, previous *ProbeResult, duration time.Duration) {
	if probe.Intelligence == nil || i.shipper == nil {
		return
	}

	if !result.Healthy {
		i.reportFailure(probe, result, previous)
		return
	}

	// A check that just recovered closes out its incident membership.
	if previous != nil && !previous.Healthy {
		i.ship(probe.Name, observation.Observation{
			Probe: probe.Name,
			Kind:  observation.KindRecovery,
			At:    result.LastCheckTime,
		})
	}

	i.evaluateDrift(probe, result)
	i.evaluateLatency(probe, result, duration)
}

// reportFailure ships only the ONSET of a failure, not every failing tick.
//
// A probe down for an hour at a thirty-second interval is one event, not a
// hundred and twenty. Shipping each tick would flood the engine, and — worse —
// would let a single long outage manufacture overwhelming confidence in the
// dependency learner.
func (i *Intelligence) reportFailure(probe Probe, result *ProbeResult, previous *ProbeResult) {
	if previous != nil && !previous.Healthy {
		return
	}

	normalizer := i.normalizerFor(probe)
	text := normalizer.FailureText(anomaly.Failure{
		ProbeType:      orHTTP(probe.Type),
		StatusCode:     result.StatusCode,
		ExpectedStatus: result.ExpectedStatus,
		Message:        result.Message,
	})

	i.ship(probe.Name, observation.Observation{
		Probe:          probe.Name,
		Kind:           observation.KindFailure,
		Text:           text,
		Labels:         probe.Labels,
		ProbeType:      orHTTP(probe.Type),
		URL:            probe.URL,
		StatusCode:     result.StatusCode,
		ExpectedStatus: result.ExpectedStatus,
		Message:        normalizer.Normalize(result.Message),
		At:             result.LastCheckTime,
	})
}

// evaluateDrift scores a PASSING check's response body.
//
// The body itself never leaves this function: it is embedded here, compared
// here, and only the resulting score is shipped.
func (i *Intelligence) evaluateDrift(probe Probe, result *ProbeResult) {
	config := probe.Intelligence.Triggers.BodyDrift
	if config == nil || i.embedder == nil || result.BodySnippet == "" {
		return
	}

	// Sampling is the pressure valve if the hot path ever becomes CPU-bound.
	// Drift moves over hours, so coarse sampling costs little.
	if config.SampleEvery > 1 && !i.shouldSample(probe.Name, config.SampleEvery) {
		return
	}

	normalizer := i.normalizerFor(probe)
	text := normalizer.BodyText(result.BodySnippet, config.MaxBodyBytes)
	if text == "" {
		return
	}

	started := time.Now()
	vectors, err := i.embedder.Embed(context.Background(), []string{text})
	if i.metrics != nil {
		i.metrics.embedSeconds.Observe(time.Since(started).Seconds())
	}
	if err != nil || len(vectors) == 0 {
		if err != nil {
			i.logger.Error(err, "Body embedding failed", "probe", probe.Name)
		}
		return
	}

	outcome := i.drift.Observe(probe.Name, vectors[0], anomaly.DriftConfig{
		Threshold:           config.Threshold,
		WarmupChecks:        config.WarmupChecks,
		ConsecutiveBreaches: config.ConsecutiveBreaches,
	})

	result.DriftScore = outcome.Score
	if i.metrics != nil && !outcome.Warming {
		i.metrics.driftScore.WithLabelValues(probe.Name, probe.Intelligence.Policy).Set(outcome.Score)
	}

	if !outcome.Drifted {
		return
	}

	i.ship(probe.Name, observation.Observation{
		Probe:      probe.Name,
		Kind:       observation.KindBodyDrift,
		Labels:     probe.Labels,
		ProbeType:  orHTTP(probe.Type),
		URL:        probe.URL,
		StatusCode: result.StatusCode,
		Message: "Response body changed meaning while the check was still passing " +
			"(cosine distance from baseline).",
		DriftScore: outcome.Score,
		At:         result.LastCheckTime,
	})
}

func (i *Intelligence) evaluateLatency(probe Probe, result *ProbeResult, duration time.Duration) {
	config := probe.Intelligence.Triggers.LatencyShift
	if config == nil {
		return
	}

	outcome := i.latency.Observe(probe.Name, duration, anomaly.LatencyConfig{
		ZScoreThreshold:     config.ZScoreThreshold,
		WarmupChecks:        config.WarmupChecks,
		ConsecutiveBreaches: config.ConsecutiveBreaches,
	})

	result.LatencyZScore = outcome.ZScore
	if i.metrics != nil && !outcome.Warming {
		i.metrics.latencyZScore.WithLabelValues(probe.Name, probe.Intelligence.Policy).Set(outcome.ZScore)
	}

	if !outcome.Shifted {
		return
	}

	i.ship(probe.Name, observation.Observation{
		Probe:         probe.Name,
		Kind:          observation.KindLatencyShift,
		Labels:        probe.Labels,
		ProbeType:     orHTTP(probe.Type),
		URL:           probe.URL,
		StatusCode:    result.StatusCode,
		Message:       "Check is passing but materially slower than its rolling baseline.",
		LatencyZScore: outcome.ZScore,
		At:            result.LastCheckTime,
	})
}

func (i *Intelligence) ship(probe string, signal observation.Observation) {
	if i.metrics != nil {
		i.metrics.signals.WithLabelValues(probe, signal.Kind).Inc()
	}
	i.shipper.Ship(context.Background(), signal)
}

// shouldSample reports whether this check is the Nth for its probe.
func (i *Intelligence) shouldSample(probe string, every int) bool {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.sampleCount[probe]++
	if i.sampleCount[probe] >= every {
		i.sampleCount[probe] = 0
		return true
	}
	return false
}

// normalizerFor returns the normalizer for a probe's policy, compiling the
// policy's redaction patterns once and caching per policy rather than per probe.
func (i *Intelligence) normalizerFor(probe Probe) *anomaly.Normalizer {
	policy := probe.Intelligence.Policy

	i.mu.RLock()
	cached, found := i.normalizers[policy]
	i.mu.RUnlock()
	if found {
		return cached
	}

	var redact []string
	if drift := probe.Intelligence.Triggers.BodyDrift; drift != nil {
		redact = drift.Redact
	}

	normalizer, err := anomaly.NewNormalizer(redact)
	if err != nil {
		// A bad redaction pattern must never mean "carry on without redacting":
		// fall back to a normalizer that still masks everything built in, and
		// say so loudly.
		i.logger.Error(err, "Invalid redact pattern; using built-in masking only", "policy", policy)
		normalizer, _ = anomaly.NewNormalizer(nil)
	}

	i.mu.Lock()
	i.normalizers[policy] = normalizer
	i.mu.Unlock()

	return normalizer
}

// Retain drops per-probe state for probes that no longer exist.
//
// This is called on config reload INSTEAD of clearing everything: editing one
// canary must not blind every other probe for a full warmup period.
func (i *Intelligence) Retain(keep map[string]struct{}) {
	i.drift.Retain(keep)
	i.latency.Retain(keep)

	i.mu.Lock()
	defer i.mu.Unlock()

	// Redaction patterns can change with a policy edit, so recompile them.
	i.normalizers = map[string]*anomaly.Normalizer{}

	for probe := range i.sampleCount {
		if _, found := keep[probe]; !found {
			delete(i.sampleCount, probe)
		}
	}
}

// RecordCacheStats copies embedding cache counters into Prometheus.
//
// The cache exposes running totals while Prometheus counters take increments,
// so the last-reported totals are tracked here and only the delta is added.
func (i *Intelligence) RecordCacheStats() {
	if i.metrics == nil {
		return
	}

	caching, ok := i.embedder.(*embed.CachingEmbedder)
	if !ok {
		return
	}

	hits, misses := caching.Stats()

	i.mu.Lock()
	hitDelta := hits - i.reportedHits
	missDelta := misses - i.reportedMisses
	i.reportedHits = hits
	i.reportedMisses = misses
	i.mu.Unlock()

	i.metrics.cacheHits.Add(float64(hitDelta))
	i.metrics.cacheMisses.Add(float64(missDelta))
}

func orHTTP(probeType string) string {
	if probeType == "" {
		return ProbeTypeHTTP
	}
	return probeType
}
