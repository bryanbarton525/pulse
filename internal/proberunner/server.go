package proberunner

import (
	"encoding/json"
	"net/http"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewServeMux creates the HTTP handler with /metrics and /results routes.
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
