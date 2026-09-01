package incident

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bryanbarton525/pulse/internal/observation"
)

// maxRequestBytes bounds a single push from a probe runner.
const maxRequestBytes = 16 << 20

// NewServeMux builds the incident engine's HTTP surface.
//
// Mirrors the probe runner's server so both binaries look and behave the same:
//
//	POST /observations  a shard reporting failures, drift, and recoveries
//	POST /results       a shard reporting its full result snapshot
//	GET  /results       the merged view, polled by the controller's StatusSyncer
//	GET  /incidents     open incidents, for status and for humans
//	GET  /topology      learned dependency proposals awaiting review
//	GET  /metrics       Prometheus
//	GET  /healthz       liveness
func NewServeMux(
	engine *Engine,
	aggregator *Aggregator,
	logger logr.Logger,
	gatherer prometheus.Gatherer,
) *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("GET /metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("POST /observations", func(w http.ResponseWriter, r *http.Request) {
		var batch observation.Batch
		if !decode(w, r, logger, &batch) {
			return
		}

		for _, signal := range batch.Observations {
			engine.Ingest(r.Context(), signal)
		}

		w.WriteHeader(http.StatusAccepted)
	})

	mux.HandleFunc("POST /results", func(w http.ResponseWriter, r *http.Request) {
		var batch ResultBatch
		if !decode(w, r, logger, &batch) {
			return
		}

		aggregator.Record(batch)
		w.WriteHeader(http.StatusAccepted)
	})

	mux.HandleFunc("GET /results", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, logger, aggregator.Results())
	})

	mux.HandleFunc("GET /incidents", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, logger, engine.Open())
	})

	mux.HandleFunc("GET /topology", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, logger, map[string]any{
			// Declared edges are what actually drives correlation; proposals
			// are hypotheses awaiting a human. Keeping them in separate fields
			// makes that distinction impossible to miss.
			"declared":  engine.DeclaredEdges(),
			"proposals": engine.Proposals(),
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
