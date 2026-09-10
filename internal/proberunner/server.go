package proberunner

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewMetricsServeMux creates the unauthenticated metrics and liveness surface.
func NewMetricsServeMux(logger logr.Logger, gatherer prometheus.Gatherer) *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("GET /metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			logger.Info("Failed to write health response", "error", err)
		}
	})
	return mux
}

// NewAPIServeMux creates the authenticated operational surface.
func NewAPIServeMux(runner *Runner, logger logr.Logger, tokenSource func() string) *http.ServeMux {
	mux := http.NewServeMux()
	token := func() string { return "" }
	if tokenSource != nil {
		token = tokenSource
	}
	mux.HandleFunc("GET /results", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, token()) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		results := runner.GetResults()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(results); err != nil {
			logger.Error(err, "Failed to encode results")
			http.Error(w, "Failed to encode results", http.StatusInternalServerError)
		}
	})
	return mux
}

// NewServeMux creates the legacy combined HTTP handler. Production binaries use
// NewMetricsServeMux and NewAPIServeMux on separate listeners.
//
// Why a separate function instead of a method on Runner?
// Because the HTTP server is a distinct concern from probe execution.
// The runner stores results; this just reads and serves them.
// Keeping them separate makes both easier to test independently.
func NewServeMux(runner *Runner, logger logr.Logger, gatherer prometheus.Gatherer) *http.ServeMux {
	mux := http.NewServeMux()

	// /metrics — Prometheus exposition format.
	//
	// promhttp.HandlerFor reads all registered metrics from the Gatherer
	// (which is the same Registry we registered our metrics on in NewRunner)
	// and serializes them in the text-based Prometheus format:
	//
	//   # HELP pulse_canary_checks_total Total number of canary checks executed.
	//   # TYPE pulse_canary_checks_total counter
	//   pulse_canary_checks_total{probe="default/check-api",result="success"} 42
	//
	// Prometheus scrapes this endpoint at its own interval (usually 15-30s).
	mux.Handle("GET /metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))

	// /results — JSON array of latest probe results.
	//
	// This is an internal endpoint consumed only by the Pulse controller.
	// It returns the most recent check result for every active probe:
	//
	//   [
	//     {"name":"default/check-api","healthy":true,"statusCode":200,...},
	//     {"name":"staging/check-web","healthy":false,"statusCode":503,...}
	//   ]
	//
	// The controller maps each result back to an HttpCanary CR using the
	// "name" field (which is "namespace/crname") and updates .status.
	mux.HandleFunc("GET /results", func(w http.ResponseWriter, r *http.Request) {
		results := runner.GetResults()

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(results); err != nil {
			logger.Error(err, "Failed to encode results")
			http.Error(w, "Failed to encode results", http.StatusInternalServerError)
		}
	})

	// /healthz — basic liveness probe for Kubernetes.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			logger.Info("Failed to write health response", "error", err)
		}
	})

	return mux
}

func authorized(request *http.Request, token string) bool {
	if token == "" {
		return true
	}
	provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	return len(provided) == len(token) &&
		subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}
