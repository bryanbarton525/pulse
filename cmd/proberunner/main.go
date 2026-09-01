package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/bryanbarton525/pulse/internal/embed"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

func main() {
	var configPath string
	var authFilePath string
	var listenAddr string
	var incidentEngineURL string
	var hotModelPath string
	var hotVocabPath string
	var embedCacheSize int
	flag.StringVar(&configPath, "config", "/etc/pulse/probes.yaml",
		"Path to the probe config file (mounted from ConfigMap).")
	flag.StringVar(&authFilePath, "auth-file", "/etc/pulse-auth/auth.yaml",
		"Path to the auth file (mounted from Secret).")
	flag.StringVar(&listenAddr, "listen", ":9090", "Address to serve /metrics and /results on.")
	flag.StringVar(&incidentEngineURL, "incident-engine", "",
		"Base URL of the incident engine. Empty disables correlation and action dispatch.")
	flag.StringVar(&hotModelPath, "hot-model", proberunner.DefaultHotModelPath,
		"Path to the static embedding model used for body-drift scoring.")
	flag.StringVar(&hotVocabPath, "hot-vocab", proberunner.DefaultHotVocabPath,
		"Path to the vocabulary for the static embedding model.")
	flag.IntVar(&embedCacheSize, "embed-cache-size", 4096,
		"How many normalized response bodies to memoize. Most checks return an identical body every time.")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	// Use the same logging framework as the controller (controller-runtime's zap logger).
	// This ensures consistent log format across both binaries.
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logger := ctrl.Log.WithName("proberunner")

	// ── Load config ──────────────────────────────────────────
	//
	// The config file is the mounted ConfigMap that the controller writes.
	// It contains a YAML list of all active probes.
	config, err := proberunner.LoadConfigFromFile(configPath)
	if err != nil {
		logger.Error(err, "Failed to load probe config")
		os.Exit(1)
	}
	authStore, err := proberunner.LoadAuthStoreFromFile(authFilePath)
	if err != nil {
		logger.Error(err, "Failed to load probe auth store")
		os.Exit(1)
	}
	// ── Take this replica's share of the probes ──────────────
	//
	// Sharding is a stable hash of the probe name against the StatefulSet
	// ordinal, so every replica reaches the same split from the same ConfigMap
	// with no coordination. A single replica owns everything, which is the
	// default and reproduces the original behavior exactly.
	ordinal, shards := proberunner.ShardFromEnvironment()
	shardName := strconv.Itoa(ordinal)
	config = proberunner.Shard(config, ordinal, shards)

	logger.Info("Loaded probe config",
		"probeCount", len(config.Probes), "shard", ordinal, "shards", shards)

	// ── Set up Prometheus registry ───────────────────────────
	//
	// We create our own registry instead of using prometheus.DefaultRegisterer.
	// This avoids polluting the default registry with our metrics and
	// prevents conflicts if other libraries also register default metrics.
	registry := prometheus.NewRegistry()

	// ── Create and start the runner ──────────────────────────
	//
	// The runner spawns one goroutine per probe. Each goroutine
	// runs its check loop independently and records results.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner := proberunner.NewRunner(logger, registry, *authStore)

	// ── Attach the model-driven evaluator ────────────────────
	//
	// Built only when some probe actually opted in, so a cluster with no
	// AnomalyPolicy never loads a model and behaves exactly as it did before
	// this feature existed.
	var shipper *proberunner.HTTPShipper
	if incidentEngineURL != "" && anyProbeWantsIntelligence(config.Probes) {
		shipper = proberunner.NewHTTPShipper(proberunner.ShipperOptions{
			Endpoint: strings.TrimRight(incidentEngineURL, "/") + "/observations",
			Shard:    shardName,
			Logger:   logger,
		})

		embedder := buildHotEmbedder(config.Probes, hotModelPath, hotVocabPath, embedCacheSize, logger)
		runner.SetIntelligence(proberunner.NewIntelligence(
			embedder, shipper, logger, proberunner.NewIntelligenceMetrics(registry)))

		go proberunner.NewResultPusher(
			strings.TrimRight(incidentEngineURL, "/")+"/results",
			shardName, runner, logger, 5*time.Second,
		).Run(ctx)
	}

	runner.Start(ctx, config)

	// ── Start HTTP server ────────────────────────────────────
	//
	// Serves /metrics (Prometheus), /results (controller), and /healthz (k8s).
	mux := proberunner.NewServeMux(runner, logger, registry)
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

	// ── Watch for config changes ─────────────────────────────
	//
	// When the controller updates the ConfigMap, Kubernetes remounts
	// the file. We detect this by polling the file's modification time.
	//
	// Why poll instead of fsnotify?
	// ConfigMap volume mounts use symlinks that get atomically swapped.
	// fsnotify doesn't reliably detect symlink target changes across
	// all platforms. Polling every 5s is simple and reliable.
	go watchConfigReload(ctx, configPath, authFilePath, runner)

	// ── Graceful shutdown ────────────────────────────────────
	//
	// Wait for SIGTERM (what Kubernetes sends) or SIGINT (Ctrl+C).
	// Then stop all probes and drain the HTTP server.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	logger.Info("Received shutdown signal", "signal", sig)

	// Stop blocks until in-flight checks finish, so the shipper is still alive
	// for anything they report on the way out.
	drained := runner.Stop()
	if !drained {
		logger.Info("Some probes were still running at shutdown; " +
			"stopping the observation shipper anyway")
	}
	if shipper != nil {
		shipper.Stop()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error(err, "HTTP server shutdown error")
	}

	logger.Info("Probe runner stopped")
}

// watchConfigReload polls the config file for changes and reloads the runner.
//
// ConfigMap volume mounts work like this:
//
//	/etc/pulse/probes.yaml → ..data/probes.yaml → ..2026_03_31.../probes.yaml
//
// When the ConfigMap is updated, Kubernetes creates a new timestamped directory,
// then atomically swaps the ..data symlink. The file's ModTime changes, which
// we detect here.
func watchConfigReload(ctx context.Context, configPath string, authFilePath string, runner *proberunner.Runner) {
	logger := ctrl.Log.WithName("proberunner")
	var configModTime time.Time
	var authModTime time.Time

	// Get initial mod time.
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
				logger.Error(err, "Failed to stat config file")
				continue
			}
			authInfo, err := os.Stat(authFilePath)
			if err != nil {
				logger.Error(err, "Failed to stat auth file")
				continue
			}

			if configInfo.ModTime().After(configModTime) || authInfo.ModTime().After(authModTime) {
				logger.Info("Runtime files changed, reloading")
				configModTime = configInfo.ModTime()
				authModTime = authInfo.ModTime()

				newConfig, err := proberunner.LoadConfigFromFile(configPath)
				if err != nil {
					logger.Error(err, "Failed to reload config — keeping current probes")
					continue
				}
				newAuthStore, err := proberunner.LoadAuthStoreFromFile(authFilePath)
				if err != nil {
					logger.Error(err, "Failed to reload auth store — keeping current probes")
					continue
				}

				ordinal, shards := proberunner.ShardFromEnvironment()
				newConfig = proberunner.Shard(newConfig, ordinal, shards)

				runner.Reload(ctx, newConfig, *newAuthStore)
				logger.Info("Config reloaded", "probeCount", len(newConfig.Probes), "shard", ordinal)
			}
		}
	}
}

// anyProbeWantsIntelligence reports whether this shard has any opted-in canary.
func anyProbeWantsIntelligence(probes []proberunner.Probe) bool {
	for _, probe := range probes {
		if probe.Intelligence != nil {
			return true
		}
	}
	return false
}

// buildHotEmbedder loads the static model used for body-drift scoring.
//
// This runs on EVERY passing check, so it must be nearly free: static
// Model2Vec embeddings are a token lookup and a mean, with no transformer
// forward pass and no cgo. A cache in front of it absorbs the common case
// where an endpoint returns an identical body every time.
//
// A nil return disables drift while leaving latency and failure reporting
// working — degrading one trigger beats failing to start.
func buildHotEmbedder(
	probes []proberunner.Probe,
	modelPath, vocabPath string,
	cacheSize int,
	logger logr.Logger,
) embed.Embedder {
	wanted := false
	for _, probe := range probes {
		if probe.Intelligence != nil && probe.Intelligence.Triggers.BodyDrift != nil {
			wanted = true
			if probe.Intelligence.Model.Hot.ModelPath != "" {
				modelPath = probe.Intelligence.Model.Hot.ModelPath
			}
			if probe.Intelligence.Model.Hot.VocabPath != "" {
				vocabPath = probe.Intelligence.Model.Hot.VocabPath
			}
			break
		}
	}

	if !wanted {
		return nil
	}

	embedder, err := embed.LoadPotion(modelPath, vocabPath, 0)
	if err != nil {
		logger.Error(err, "Could not load the body-drift model; drift scoring is disabled",
			"model", modelPath)
		return nil
	}

	logger.Info("Loaded the body-drift model", "model", modelPath, "dimensions", embedder.Dimensions())
	return embed.NewCachingEmbedder(embedder, cacheSize)
}
