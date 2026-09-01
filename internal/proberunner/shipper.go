package proberunner

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/bryanbarton525/pulse/internal/observation"
)

// HTTPShipper delivers observations to the incident engine.
//
// Shipping is asynchronous and lossy on purpose. A probe goroutine must never
// block on the engine being slow or briefly unreachable: monitoring that stops
// checking because its analysis tier is down is worse than monitoring that
// misses one correlation.
type HTTPShipper struct {
	endpoint string
	shard    string
	client   *http.Client
	logger   logr.Logger

	queue chan observation.Observation
	done  chan struct{}

	// mu guards stopped. A send on a closed channel panics even inside a
	// select with a default arm, so the guard -- not the select -- is what
	// makes Ship safe to call concurrently with Stop.
	mu        sync.RWMutex
	stopped   bool
	closeOnce sync.Once
}

// ShipperOptions configures an HTTPShipper.
type ShipperOptions struct {
	Endpoint string
	Shard    string
	Logger   logr.Logger

	// QueueSize bounds how many observations can be buffered before new ones
	// are dropped.
	QueueSize int

	// BatchWindow is how long to accumulate observations before sending.
	BatchWindow time.Duration
}

// NewHTTPShipper builds a shipper and starts its background sender.
func NewHTTPShipper(options ShipperOptions) *HTTPShipper {
	if options.QueueSize <= 0 {
		options.QueueSize = 1024
	}
	if options.BatchWindow <= 0 {
		options.BatchWindow = time.Second
	}

	shipper := &HTTPShipper{
		endpoint: options.Endpoint,
		shard:    options.Shard,
		client:   &http.Client{Timeout: 10 * time.Second},
		logger:   options.Logger,
		queue:    make(chan observation.Observation, options.QueueSize),
		done:     make(chan struct{}),
	}

	go shipper.run(options.BatchWindow)
	return shipper
}

// Ship queues one observation. It never blocks, and never panics.
//
// Holding the read lock for the duration of the send is what makes this safe:
// Stop takes the write lock before closing the queue, so a send can never be
// in progress when the close happens.
func (s *HTTPShipper) Ship(_ context.Context, signal observation.Observation) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.stopped {
		return
	}

	select {
	case s.queue <- signal:
	default:
		// The queue is full, which means the engine has been unreachable for a
		// while. Dropping is the right failure mode here — see the type docs.
		s.logger.V(1).Info("Dropping observation; the incident engine is not keeping up",
			"probe", signal.Probe, "kind", signal.Kind)
	}
}

// Stop drains and shuts down the sender.
//
// The stopped flag is set under the write lock before the channel is closed,
// so any concurrent Ship either completed its send already or will observe the
// flag and return without touching the channel.
func (s *HTTPShipper) Stop() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		s.mu.Unlock()

		close(s.queue)
	})

	<-s.done
}

func (s *HTTPShipper) run(window time.Duration) {
	defer close(s.done)

	ticker := time.NewTicker(window)
	defer ticker.Stop()

	var pending []observation.Observation

	flush := func() {
		if len(pending) == 0 {
			return
		}
		s.send(pending)
		pending = pending[:0]
	}

	for {
		select {
		case signal, open := <-s.queue:
			if !open {
				flush()
				return
			}
			pending = append(pending, signal)
			// Failures and recoveries decide whether someone gets paged, so
			// they go out immediately rather than waiting for the batch timer.
			if signal.Kind == observation.KindFailure || signal.Kind == observation.KindRecovery {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (s *HTTPShipper) send(signals []observation.Observation) {
	payload, err := json.Marshal(observation.Batch{
		Shard:        s.shard,
		Observations: signals,
	})
	if err != nil {
		s.logger.Error(err, "Failed to encode observations")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		s.logger.Error(err, "Failed to build the observation request")
		return
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := s.client.Do(request)
	if err != nil {
		s.logger.V(1).Info("Could not reach the incident engine", "error", err.Error())
		return
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		s.logger.V(1).Info("The incident engine rejected observations",
			"status", response.StatusCode, "count", len(signals))
	}
}

// NoopShipper discards observations. Used when no incident engine is
// configured, so drift and latency still move metrics locally.
type NoopShipper struct{}

// Ship implements Shipper.
func (NoopShipper) Ship(context.Context, observation.Observation) {}

// ResultBatch is one shard's full result snapshot, pushed to the incident
// engine so the controller has a single endpoint to poll no matter how many
// replicas are running.
type ResultBatch struct {
	Shard   string        `json:"shard"`
	Results []ProbeResult `json:"results"`
}

// ResultPusher periodically sends this shard's results to the incident engine.
//
// Push rather than pull: with sharding, the controller would otherwise have to
// discover and poll every replica through a headless Service. Pushing keeps
// the controller's single-endpoint model intact.
type ResultPusher struct {
	endpoint string
	shard    string
	runner   *Runner
	client   *http.Client
	logger   logr.Logger
	interval time.Duration
}

// NewResultPusher builds a pusher.
func NewResultPusher(
	endpoint, shard string,
	runner *Runner,
	logger logr.Logger,
	interval time.Duration,
) *ResultPusher {
	if interval <= 0 {
		interval = 5 * time.Second
	}

	return &ResultPusher{
		endpoint: endpoint,
		shard:    shard,
		runner:   runner,
		client:   &http.Client{Timeout: 10 * time.Second},
		logger:   logger,
		interval: interval,
	}
}

// Run pushes results until the context is cancelled.
func (p *ResultPusher) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.push(ctx)
		}
	}
}

func (p *ResultPusher) push(ctx context.Context) {
	payload, err := json.Marshal(ResultBatch{
		Shard:   p.shard,
		Results: p.runner.GetResults(),
	})
	if err != nil {
		p.logger.Error(err, "Failed to encode results")
		return
	}

	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, p.endpoint, bytes.NewReader(payload))
	if err != nil {
		p.logger.Error(err, "Failed to build the results request")
		return
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := p.client.Do(request)
	if err != nil {
		p.logger.V(1).Info("Could not push results to the incident engine", "error", err.Error())
		return
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		p.logger.V(1).Info("The incident engine rejected a results push",
			"status", response.StatusCode)
	}
}
