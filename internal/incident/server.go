package incident

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bryanbarton525/pulse/internal/authn"
	"github.com/bryanbarton525/pulse/internal/observation"
)

// maxRequestBytes bounds a single push from a probe runner.
const maxRequestBytes = 16 << 20

const (
	maxObservationBatch = 2048
	maxResultBatch      = 10000
)

// NewMetricsServeMux creates the unauthenticated metrics and liveness surface.
func NewMetricsServeMux(logger logr.Logger, gatherer prometheus.Gatherer) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// NewAPIServeMux creates the authenticated operational API surface.
func NewAPIServeMux(
	engine *Engine,
	aggregator *Aggregator,
	logger logr.Logger,
	auth authn.Policy,
) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /observations", func(w http.ResponseWriter, r *http.Request) {
		if !auth.Authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var batch observation.Batch
		if !decode(w, r, logger, &batch) {
			return
		}
		if len(batch.Observations) > maxObservationBatch {
			http.Error(w, "observation batch too large", http.StatusRequestEntityTooLarge)
			return
		}
		for _, signal := range batch.Observations {
			engine.Ingest(r.Context(), signal)
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /results", func(w http.ResponseWriter, r *http.Request) {
		if !auth.Authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var batch ResultBatch
		if !decode(w, r, logger, &batch) {
			return
		}
		if len(batch.Results) > maxResultBatch {
			http.Error(w, "result batch too large", http.StatusRequestEntityTooLarge)
			return
		}
		engine.ReconcileResults(batch.Results)
		aggregator.Record(batch)
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("GET /results", func(w http.ResponseWriter, r *http.Request) {
		if !auth.Authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, logger, aggregator.Results())
	})
	mux.HandleFunc("GET /incidents", func(w http.ResponseWriter, r *http.Request) {
		if !auth.Authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, logger, engine.Open())
	})
	mux.HandleFunc("GET /topology", func(w http.ResponseWriter, r *http.Request) {
		if !auth.Authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, logger, map[string]any{
			"declared": engine.DeclaredEdges(), "proposals": engine.Proposals(),
		})
	})
	return mux
}

func decode(w http.ResponseWriter, r *http.Request, logger logr.Logger, target any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		logger.Error(err, "Failed to read a request body")
		http.Error(w, "could not read request", http.StatusBadRequest)
		return false
	}

	if err := json.Unmarshal(body, target); err != nil {
		logger.Error(err, "Failed to decode a request body")
		http.Error(w, "could not decode request", http.StatusBadRequest)
		return false
	}

	return true
}

func writeJSON(w http.ResponseWriter, logger logr.Logger, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logger.Error(err, "Failed to write a JSON response")
	}
}
