/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Command incidentengine correlates failures from every probe runner shard
// into incidents and performs each policy's actions.
//
// It is the ONLY Pulse binary that carries a transformer. Probe runners scale
// to N replicas and stay cgo-free; this one runs a single replica and can
// afford ONNX Runtime, because it only embeds on failures — three orders of
// magnitude rarer than the checks themselves.
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/bryanbarton525/pulse/internal/actions"
	"github.com/bryanbarton525/pulse/internal/embed"
	"github.com/bryanbarton525/pulse/internal/incident"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

func main() {
	var configPath string
	var authFilePath string
	var listenAddr string
	var onnxLibraryPath string

	flag.StringVar(&configPath, "config", "/etc/pulse/probes.yaml",
		"Path to the probe config file (mounted from the same ConfigMap the runners read).")
	flag.StringVar(&authFilePath, "auth-file", "/etc/pulse-auth/auth.yaml",
		"Path to the auth file (mounted from the same Secret the runners read).")
	flag.StringVar(&listenAddr, "listen", ":9090", "Address to serve the HTTP API on.")
	flag.StringVar(&onnxLibraryPath, "onnxruntime-lib", "",
		"Path to libonnxruntime.so. Defaults to the ONNXRUNTIME_SHARED_LIBRARY_PATH env var.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logger := ctrl.Log.WithName("incidentengine")

	if onnxLibraryPath != "" {
		if err := os.Setenv("ONNXRUNTIME_SHARED_LIBRARY_PATH", onnxLibraryPath); err != nil {
			logger.Error(err, "Failed to set the ONNX Runtime library path")
		}
	}

	// ── Load the shared probe configuration ──────────────────
	//
	// The engine mounts the SAME ConfigMap and Secret as the probe runners.
	// That is why observations can stay lean: every policy, action, and
	// credential is already here, so none of it has to travel over the wire.
	config, err := proberunner.LoadConfigFromFile(configPath)
	if err != nil {
		logger.Error(err, "Failed to load probe config")
		os.Exit(1)
	}
	authStore, err := proberunner.LoadAuthStoreFromFile(authFilePath)
	if err != nil {
		logger.Error(err, "Failed to load the auth store")
		os.Exit(1)
	}

	registry := prometheus.NewRegistry()
	metrics := actions.NewMetrics(registry)

	aggregator := incident.NewAggregator(2 * time.Minute)

	dispatcher := actions.NewDispatcher(actions.DispatcherOptions{
		Metrics: metrics,
		Logger:  logger,
		Timeout: 2 * time.Minute,
	})

	// models tracks what is currently loaded so a configuration reload can tell
	// a real model change from an unrelated edit.
	models := &modelState{}
	embedder := models.reload(config.Probes, *authStore, logger)
	defer models.close()

	engine := incident.NewEngine(incident.EngineOptions{
		Embedder:   embedder,
		Dispatcher: dispatcher,
		Parse:      parseSelector,
		Logger:     logger,
	})

	engine.SetEmbedder(embedder)
	applyConfig(engine, dispatcher, aggregator, models, config, *authStore, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux := incident.NewServeMux(engine, aggregator, logger, registry)
	server := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("Starting HTTP server", "addr", listenAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error(err, "HTTP server failed")
			os.Exit(1)
		}
	}()

	// The reload loop takes only a context; the logger rides inside it, because
	// a function taking both forces callers to reason about which one wins.
	go watchConfigReload(logr.NewContext(ctx, logger), configPath, authFilePath,
		engine, dispatcher, aggregator, models)
	go sweepStaleIncidents(logr.NewContext(ctx, logger), engine)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	logger.Info("Received shutdown signal", "signal", sig)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error(err, "HTTP server shutdown error")
	}

	logger.Info("Incident engine stopped")
}

// buildColdEmbedder loads the failure-path model, but only if some probe
// actually needs it. A cluster with no AnomalyPolicy never pays for a model.
func buildColdEmbedder(
	probes []proberunner.Probe,
	authStore proberunner.AuthStore,
	logger logr.Logger,
) embed.Embedder {
	model, conflicts := resolveColdModel(probes)
	if model == nil {
		logger.Info("No probe needs the failure-path model; skipping model load")
		return nil
	}

	// Correlation deliberately spans policies, and vectors from different
	// models are not comparable, so the failure-path model is cluster-global.
	// Policies that disagree are reported rather than silently honored.
	for _, conflict := range conflicts {
		logger.Info("Ignoring a conflicting failure-path model; this setting is cluster-wide "+
			"because correlation compares failures across policies", "policy", conflict)
	}

	switch model.Backend {
	case "http":
		apiKey := authStore.Values[model.HTTP.APIKeyCredentialID]
		if model.HTTP.APIKeyCredentialID != "" && apiKey == "" {
			logger.Info("The configured embeddings API key could not be resolved; "+
				"calls will be sent unauthenticated",
				"credentialID", model.HTTP.APIKeyCredentialID)
		}
		logger.Info("Using a remote embeddings endpoint",
			"endpoint", model.HTTP.Endpoint, "authenticated", apiKey != "")
		return embed.NewHTTPEmbedder(
			model.HTTP.Endpoint, model.HTTP.Model, apiKey, embed.SpaceMiniLM, 30*time.Second)

	default:
		embedder, err := embed.LoadONNX(model.ONNX.ModelPath, model.ONNX.VocabPath, model.ONNX.MaxSequenceLength)
		if err != nil {
			// Correlation degrades to declared topology only, which is worse
			// but still correct — far better than refusing to start and losing
			// alerting entirely.
			logger.Error(err, "Could not load the failure-path model; "+
				"correlation will use declared topology only")
			return nil
		}
		logger.Info("Loaded the failure-path model", "model", model.ONNX.ModelPath)
		return embedder
	}
}

// applyConfig pushes a freshly loaded configuration into every component.
func applyConfig(
	engine *incident.Engine,
	dispatcher *actions.Dispatcher,
	aggregator *incident.Aggregator,
	models *modelState,
	config *proberunner.ProbeConfig,
	authStore proberunner.AuthStore,
	logger logr.Logger,
) {
	engine.LoadProbes(config.Probes)

	// A policy edit can change the model. Rebuild only when it actually
	// differs, so an unrelated edit does not throw away a warm novelty index.
	if embedder, changed := models.reloadIfChanged(config.Probes, authStore, logger); changed {
		engine.SetEmbedder(embedder)
	}

	history := func(probe string, limit int) []string {
		return aggregator.History(probe, limit)
	}

	if err := dispatcher.Load(config.Probes, actions.CredentialMap(authStore.Values), history); err != nil {
		// Load isolates broken policies, so this reports what was skipped
		// rather than meaning nothing loaded.
		logger.Error(err, "Some policies could not be loaded")
	}

	logger.Info("Configuration applied", "probeCount", len(config.Probes))
}

// parseSelector converts a serialized label selector into a predicate.
func parseSelector(serialized string) (incident.Selector, error) {
	parsed, err := labels.Parse(serialized)
	if err != nil {
		return nil, err
	}
	return labelSelector{parsed}, nil
}

type labelSelector struct{ inner labels.Selector }

func (l labelSelector) Matches(values map[string]string) bool {
	return l.inner.Matches(labels.Set(values))
}

// watchConfigReload polls the mounted files, matching how the probe runner
// detects ConfigMap updates.
func watchConfigReload(
	ctx context.Context,
	configPath, authFilePath string,
	engine *incident.Engine,
	dispatcher *actions.Dispatcher,
	aggregator *incident.Aggregator,
	models *modelState,
) {
	logger := logr.FromContextOrDiscard(ctx)

	var configModTime, authModTime time.Time
	if info, err := os.Stat(configPath); err == nil {
		configModTime = info.ModTime()
	}
	if info, err := os.Stat(authFilePath); err == nil {
		authModTime = info.ModTime()
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			configInfo, err := os.Stat(configPath)
			if err != nil {
				continue
			}
			authInfo, err := os.Stat(authFilePath)
			if err != nil {
				continue
			}
			if !configInfo.ModTime().After(configModTime) && !authInfo.ModTime().After(authModTime) {
				continue
			}

			configModTime = configInfo.ModTime()
			authModTime = authInfo.ModTime()

			newConfig, err := proberunner.LoadConfigFromFile(configPath)
			if err != nil {
				logger.Error(err, "Failed to reload the probe config — keeping the current one")
				continue
			}
			newAuth, err := proberunner.LoadAuthStoreFromFile(authFilePath)
			if err != nil {
				logger.Error(err, "Failed to reload the auth store — keeping the current one")
				continue
			}

			applyConfig(engine, dispatcher, aggregator, models, newConfig, *newAuth, logger)
		}
	}
}

// staleIncidentIdle is how long a drift or latency incident may go without a
// refresh before the engine closes it. Generous next to any sane probe
// interval, so a slow probe is never closed out from under itself.
const staleIncidentIdle = 5 * time.Minute

// sweepStaleIncidents closes single-probe incidents the runner stopped
// refreshing, which is what happens to every open drift incident when a probe
// runner restarts or the shards are rebalanced.
func sweepStaleIncidents(ctx context.Context, engine *incident.Engine) {
	logger := logr.FromContextOrDiscard(ctx)

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if closed := engine.Sweep(staleIncidentIdle); closed > 0 {
				logger.Info("Closed stale single-probe incidents", "count", closed)
			}
		}
	}
}
