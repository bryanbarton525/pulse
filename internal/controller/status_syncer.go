package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	canaryv1alpha1 "github.com/bryanbarton525/pulse/api/v1alpha1"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// StatusSyncer is a manager.Runnable that periodically polls the probe
// runner's /results endpoint and updates each HttpCanary CR's .status.
//
// WHY THIS IS SEPARATE FROM THE RECONCILER:
//
// Status sync needs to run on a fixed timer (every 15s), regardless of
// whether any CRs changed. If we put this in Reconcile() with RequeueAfter,
// every CR would independently requeue:
//
//	1,000 CRs × RequeueAfter(15s) = ~67 reconciles/second
//	Each polls /results + updates 1,000 statuses = 67,000 writes/sec
//
// As a standalone Runnable, it runs ONCE per interval:
//
//	1 poll + 1,000 status writes per 15s = ~67 writes/sec
//
// That's a 1,000x reduction in API server load.
//
// HOW IT INTEGRATES WITH THE MANAGER:
//
// controller-runtime's Manager has an Add() method that accepts any
// Runnable (anything with a Start(ctx) error method). When mgr.Start()
// is called, it starts all registered Runnables in separate goroutines.
// When the manager shuts down, it cancels the context, and our loop exits.
type StatusSyncer struct {
	// Client talks to the Kubernetes API server.
	client.Client

	// Namespace is the operator namespace (where the probe runner Service lives).
	Namespace string

	// Interval is how often to poll /results and update statuses.
	Interval time.Duration

	// ResultsURL overrides the default in-cluster Service URL for the probe
	// runner. This is primarily useful for local controller runs that need to
	// talk to a port-forwarded or otherwise externally reachable runner.
	ResultsURL string
}

// Start implements manager.Runnable. The manager calls this in a goroutine
// when mgr.Start() runs. The context is cancelled when the manager shuts down.
func (s *StatusSyncer) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("status-syncer")
	logger.Info("Starting status syncer", "interval", s.Interval)

	// time.NewTicker fires immediately? No — it fires AFTER the first interval.
	// So we do one sync immediately, then enter the ticker loop.
	s.syncAllStatuses(ctx)

	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Manager is shutting down. Exit cleanly.
			logger.Info("Status syncer stopped")
			return nil
		case <-ticker.C:
			s.syncAllStatuses(ctx)
		}
	}
}

// syncAllStatuses does one full cycle: poll /results, list CRs, update statuses.
func (s *StatusSyncer) syncAllStatuses(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("status-syncer")

	// ── Poll the probe runner ────────────────────────────
	results, err := s.fetchResults()
	if err != nil {
		// Best-effort. The runner might not be ready yet (Deployment
		// still starting, no CRs created yet, etc.). We'll retry
		// on the next tick.
		logger.Info("Could not fetch probe results", "error", err)
		return
	}

	if len(results) == 0 {
		return
	}

	// ── Build lookup map: "namespace/name" → result ──────
	resultMap := make(map[string]proberunner.ProbeResult, len(results))
	for _, res := range results {
		resultMap[res.Name] = res
	}

	// ── List all HttpCanary CRs ──────────────────────────
	var canaryList canaryv1alpha1.HttpCanaryList
	if err := s.List(ctx, &canaryList); err != nil {
		logger.Error(err, "Failed to list HttpCanary resources for status sync")
		return
	}

	// ── Update each CR's status ──────────────────────────
	updated := 0
	for i := range canaryList.Items {
		canary := &canaryList.Items[i]
		key := fmt.Sprintf("%s/%s", canary.Namespace, canary.Name)

		res, found := resultMap[key]
		if !found {
			continue
		}

		// Only update if something actually changed. This avoids
		// unnecessary writes to the API server, which matters at
		// scale (1,000+ CRs).
		if !s.statusChanged(canary, res) {
			continue
		}

		if res.Healthy {
			canary.Status.Phase = "Healthy"
		} else {
			canary.Status.Phase = "Unhealthy"
		}
		canary.Status.LastStatus = res.StatusCode
		canary.Status.Message = res.Message

		checkTime := metav1.NewTime(res.LastCheckTime)
		canary.Status.LastCheckTime = &checkTime

		if err := s.Status().Update(ctx, canary); err != nil {
			if errors.IsNotFound(err) {
				// CR was deleted between our List and this Update. Normal.
				continue
			}
			logger.Error(err, "Failed to update status", "canary", key)
			continue
		}
		updated++
	}

	logger.Info("Status sync complete",
		"resultsReceived", len(results),
		"canariesChecked", len(canaryList.Items),
		"statusesUpdated", updated,
	)
}

// statusChanged returns true if the probe result differs from the CR's
// current status. This prevents writing unchanged statuses to the API
// server on every sync cycle.
//
// At scale, this is critical: 1,000 CRs × 4 syncs/minute = 4,000 potential
// writes/minute. If most probes are stable (Healthy → Healthy), skipping
// unchanged statuses drops this to near zero during steady state.
func (s *StatusSyncer) statusChanged(canary *canaryv1alpha1.HttpCanary, res proberunner.ProbeResult) bool {
	expectedPhase := "Unhealthy"
	if res.Healthy {
		expectedPhase = "Healthy"
	}

	return canary.Status.Phase != expectedPhase ||
		canary.Status.LastStatus != res.StatusCode ||
		canary.Status.Message != res.Message
}

// fetchResults calls GET /results on the probe runner Service.
func (s *StatusSyncer) fetchResults() ([]proberunner.ProbeResult, error) {
	url := s.probeRunnerResultsURL()

	httpClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned status %d", url, resp.StatusCode)
	}

	var results []proberunner.ProbeResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decoding results: %w", err)
	}

	return results, nil
}

func (s *StatusSyncer) probeRunnerResultsURL() string {
	if s.ResultsURL != "" {
		return s.ResultsURL
	}

	return fmt.Sprintf("http://%s.%s.svc:%d/results",
		ProbeRunnerName, s.Namespace, ProbeRunnerPort)
}
