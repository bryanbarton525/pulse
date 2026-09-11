package proberunner

import (
	"encoding/json"
	"net/http"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bryanbarton525/pulse/internal/authn"
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
func NewAPIServeMux(runner *Runner, logger logr.Logger, auth authn.Policy) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /results", func(w http.ResponseWriter, r *http.Request) {
		if !auth.Authorized(r) {
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
