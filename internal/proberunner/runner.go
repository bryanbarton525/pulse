package proberunner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
)

// ProbeResult holds the outcome of the most recent check for one probe.
// The /results endpoint serializes a map of these to JSON.
type ProbeResult struct {
	Name           string    `json:"name"`
	Healthy        bool      `json:"healthy"`
	StatusCode     int       `json:"statusCode"`
	LastCheckTime  time.Time `json:"lastCheckTime"`
	Message        string    `json:"message"`
	URL            string    `json:"url"`
	ExpectedStatus int       `json:"expectedStatus"`
}

// Runner manages the lifecycle of all probe goroutines.
//
// Architecture:
//   - One goroutine per Probe, each with its own ticker
//   - Results stored in a thread-safe map
//   - Reload() cancels all goroutines and starts fresh from new config
//   - The HTTP server reads results via GetResults() (concurrent-safe)
type Runner struct {
	// mu protects the results map.
	// RWMutex allows multiple readers (HTTP server serving /results)
	// without blocking each other — only writers (probe goroutines
	// recording a check result) need exclusive access.
	mu      sync.RWMutex
	results map[string]*ProbeResult
	emitMu  sync.Mutex

	// cancel stops all running probe goroutines.
	// Called during Reload() or shutdown.
	cancel context.CancelFunc

	logger logr.Logger

	// Prometheus metrics — registered once, updated by every check.
	checkTotal    *prometheus.CounterVec
	checkDuration *prometheus.HistogramVec
	checkHealthy  *prometheus.GaugeVec
	stdoutWriter  io.Writer
}

// NewRunner creates a Runner and registers Prometheus metrics.
//
// Why register metrics here and not globally?
// Because the runner owns the check lifecycle — it's the only thing
// that should be recording check metrics. If we registered globally,
// we'd risk double-registration panics if NewRunner is called twice.
func NewRunner(logger logr.Logger, reg prometheus.Registerer) *Runner {
	checkTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pulse_canary_checks_total",
			Help: "Total number of canary checks executed, labeled by probe name and result.",
		},
		[]string{"probe", "result"}, // result: "success" or "failure"
	)

	checkDuration := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "pulse_canary_check_duration_seconds",
			Help:    "Duration of canary HTTP checks in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"probe"},
	)

	checkHealthy := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pulse_canary_healthy",
			Help: "Whether the canary is currently healthy (1) or unhealthy (0).",
		},
		[]string{"probe"},
	)

	reg.MustRegister(checkTotal, checkDuration, checkHealthy)

	return &Runner{
		results:       make(map[string]*ProbeResult),
		logger:        logger,
		checkTotal:    checkTotal,
		checkDuration: checkDuration,
		checkHealthy:  checkHealthy,
		stdoutWriter:  os.Stdout,
	}
}

// Start launches a goroutine for each probe in the config.
//
// Each goroutine:
//  1. Runs the check immediately (don't wait for the first tick)
//  2. Then ticks every Interval seconds
//  3. Stops when ctx is cancelled (via Reload or shutdown)
func (r *Runner) Start(ctx context.Context, config *ProbeConfig) {
	// Create a cancellable child context so we can stop all probes on reload.
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	for _, probe := range config.Probes {
		// IMPORTANT: capture the loop variable.
		// Without this, all goroutines would share the same `probe` pointer
		// and would all check the last probe in the slice.
		p := probe
		go r.runProbe(ctx, p)
	}

	r.logger.Info("Started probe runner", "probeCount", len(config.Probes))
}

// Reload stops all current probes and starts new ones from fresh config.
//
// This is called when the ConfigMap file changes (detected by a file watcher
// or periodic re-read). All old goroutines are cancelled, results are cleared,
// and new goroutines are started.
func (r *Runner) Reload(ctx context.Context, config *ProbeConfig) {
	r.logger.Info("Reloading probe configuration", "probeCount", len(config.Probes))

	// Stop all existing probe goroutines.
	if r.cancel != nil {
		r.cancel()
	}

	// Clear stale results — probes that were removed shouldn't linger.
	r.mu.Lock()
	r.results = make(map[string]*ProbeResult)
	r.mu.Unlock()

	r.Start(ctx, config)
}

// GetResults returns a snapshot of all current probe results.
// Called by the HTTP server when serving GET /results.
//
// We return a copy (slice, not the map reference) so the caller
// can serialize to JSON without holding the lock.
func (r *Runner) GetResults() []ProbeResult {
	r.mu.RLock()
	defer r.mu.RUnlock()

	results := make([]ProbeResult, 0, len(r.results))
	for _, result := range r.results {
		results = append(results, *result)
	}
	return results
}

// Stop cancels all running probe goroutines.
func (r *Runner) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
}

// runProbe executes a single probe's check loop.
// Runs in its own goroutine. Exits when ctx is cancelled.
func (r *Runner) runProbe(ctx context.Context, probe Probe) {
	logger := r.logger.WithValues("probe", probe.Name, "url", probe.URL)
	logger.Info("Starting probe")

	// Run immediately on startup — don't wait for the first tick.
	r.executeCheck(probe)

	ticker := time.NewTicker(time.Duration(probe.Interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("Probe stopped")
			return
		case <-ticker.C:
			r.executeCheck(probe)
		}
	}
}

// executeCheck performs one HTTP check and records the result.
func (r *Runner) executeCheck(probe Probe) {
	logger := r.logger.WithValues("probe", probe.Name)

	start := time.Now()
	httpClient, err := newHTTPClient()
	if err != nil {
		result := &ProbeResult{
			Name:           probe.Name,
			Healthy:        false,
			StatusCode:     0,
			LastCheckTime:  time.Now(),
			Message:        fmt.Sprintf("Failed to create HTTP client: %v", err),
			URL:            probe.URL,
			ExpectedStatus: probe.ExpectedStatus,
		}
		r.recordResult(probe.Name, result)
		r.emitResult(probe, result, time.Since(start))
		logger.Info("Check failed", "error", err, "duration", time.Since(start))
		return
	}

	result := r.executeProbe(httpClient, probe)
	duration := time.Since(start)
	r.recordResult(probe.Name, result)
	r.emitResult(probe, result, duration)
	if result.Healthy {
		logger.Info("Check passed", "status", result.StatusCode, "duration", duration)
		return
	}

	logger.Info("Check failed", "status", result.StatusCode, "message", result.Message, "duration", duration)
}

func (r *Runner) emitResult(probe Probe, result *ProbeResult, duration time.Duration) {
	if shouldEmitPrometheus(probe.Outputs) {
		r.checkDuration.WithLabelValues(probe.Name).Observe(duration.Seconds())
		if result.Healthy {
			r.checkTotal.WithLabelValues(probe.Name, "success").Inc()
			r.checkHealthy.WithLabelValues(probe.Name).Set(1)
		} else {
			r.checkTotal.WithLabelValues(probe.Name, "failure").Inc()
			r.checkHealthy.WithLabelValues(probe.Name).Set(0)
		}
	}

	if shouldEmitStdout(probe.Outputs) {
		r.writeStdoutResult(result)
	}
}

func (r *Runner) writeStdoutResult(result *ProbeResult) {
	payload, err := json.Marshal(result)
	if err != nil {
		r.logger.Error(err, "Failed to marshal probe result for stdout", "probe", result.Name)
		return
	}

	r.emitMu.Lock()
	defer r.emitMu.Unlock()

	if _, err := fmt.Fprintln(r.stdoutWriter, string(payload)); err != nil {
		r.logger.Error(err, "Failed to write probe result to stdout", "probe", result.Name)
	}
}

func shouldEmitPrometheus(outputs []ProbeOutput) bool {
	if len(outputs) == 0 {
		return true
	}

	for _, output := range outputs {
		if output.Type == ProbeOutputPrometheus {
			return true
		}
	}

	return false
}

func shouldEmitStdout(outputs []ProbeOutput) bool {
	for _, output := range outputs {
		if output.Type == ProbeOutputStdout {
			return true
		}
	}

	return false
}

func (r *Runner) executeProbe(httpClient *http.Client, probe Probe) *ProbeResult {
	if len(probe.Journey) > 0 {
		return r.executeJourney(httpClient, probe)
	}

	return executeRequest(probe.Name, httpClient, probe.URL, probe.Method, probe.Headers, probe.Body,
		probe.ExpectedStatus, probe.ContainsText)
}

func (r *Runner) executeJourney(httpClient *http.Client, probe Probe) *ProbeResult {
	lastStatus := 0
	for index, step := range probe.Journey {
		result := executeRequest(probe.Name, httpClient, step.URL, step.Method, step.Headers, step.Body,
			step.ExpectedStatus, step.ContainsText)
		lastStatus = result.StatusCode
		if !result.Healthy {
			result.URL = probe.URL
			result.ExpectedStatus = step.ExpectedStatus
			result.Message = fmt.Sprintf("Step %d (%s) failed: %s", index+1, step.Name, result.Message)
			return result
		}
	}

	return &ProbeResult{
		Name:           probe.Name,
		Healthy:        true,
		StatusCode:     lastStatus,
		LastCheckTime:  time.Now(),
		Message:        fmt.Sprintf("Synthetic journey succeeded (%d steps)", len(probe.Journey)),
		URL:            probe.URL,
		ExpectedStatus: probe.ExpectedStatus,
	}
}

func executeRequest(
	probeName string,
	httpClient *http.Client,
	url string,
	method string,
	headers map[string]string,
	body string,
	expectedStatus int,
	containsText string,
) *ProbeResult {
	request, err := http.NewRequest(normalizeMethod(method), url, bytes.NewBufferString(body))
	if err != nil {
		return &ProbeResult{
			Name:           probeName,
			Healthy:        false,
			StatusCode:     0,
			LastCheckTime:  time.Now(),
			Message:        fmt.Sprintf("Failed to build request: %v", err),
			URL:            url,
			ExpectedStatus: expectedStatus,
		}
	}

	for key, value := range headers {
		request.Header.Set(key, value)
	}

	response, err := httpClient.Do(request)
	if err != nil {
		return &ProbeResult{
			Name:           probeName,
			Healthy:        false,
			StatusCode:     0,
			LastCheckTime:  time.Now(),
			Message:        fmt.Sprintf("HTTP request failed: %v", err),
			URL:            url,
			ExpectedStatus: expectedStatus,
		}
	}
	defer func() {
		_ = response.Body.Close()
	}()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return &ProbeResult{
			Name:           probeName,
			Healthy:        false,
			StatusCode:     response.StatusCode,
			LastCheckTime:  time.Now(),
			Message:        fmt.Sprintf("Failed to read response body: %v", err),
			URL:            url,
			ExpectedStatus: expectedStatus,
		}
	}

	if response.StatusCode != expectedStatus {
		return &ProbeResult{
			Name:           probeName,
			Healthy:        false,
			StatusCode:     response.StatusCode,
			LastCheckTime:  time.Now(),
			Message:        fmt.Sprintf("Expected %d but got %d", expectedStatus, response.StatusCode),
			URL:            url,
			ExpectedStatus: expectedStatus,
		}
	}

	if containsText != "" && !strings.Contains(string(responseBody), containsText) {
		return &ProbeResult{
			Name:           probeName,
			Healthy:        false,
			StatusCode:     response.StatusCode,
			LastCheckTime:  time.Now(),
			Message:        fmt.Sprintf("Response body did not contain %q", containsText),
			URL:            url,
			ExpectedStatus: expectedStatus,
		}
	}

	message := fmt.Sprintf("Got expected status %d", response.StatusCode)
	if containsText != "" {
		message = fmt.Sprintf("Got expected status %d and matched response text", response.StatusCode)
	}

	return &ProbeResult{
		Name:           probeName,
		Healthy:        true,
		StatusCode:     response.StatusCode,
		LastCheckTime:  time.Now(),
		Message:        message,
		URL:            url,
		ExpectedStatus: expectedStatus,
	}
}

func (r *Runner) recordResult(name string, result *ProbeResult) {
	r.mu.Lock()
	r.results[name] = result
	r.mu.Unlock()
}

func newHTTPClient() (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}

	return &http.Client{Timeout: 10 * time.Second, Jar: jar}, nil
}

func normalizeMethod(method string) string {
	if method == "" {
		return http.MethodGet
	}

	return strings.ToUpper(method)
}
