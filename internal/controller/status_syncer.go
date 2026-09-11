package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"

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

	// ProbeRunnerURL overrides only the runner fallback source. It keeps the
	// engine-first behavior intact and makes fallback integration-testable.
	ProbeRunnerURL string

	// IncidentEngineURL overrides the default in-cluster address of the
	// incident engine, for the same local-development reason.
	IncidentEngineURL string

	// Recorder emits Kubernetes Events for incidents. The probe runner and the
	// incident engine deliberately hold no Kubernetes client, so this is the
	// only component that can put an incident on `kubectl describe`.
	Recorder events.EventRecorder

	// TokenSource supplies the operational API bearer token. Tests may inject a
	// rotating or failing source; production reads the controller-owned Secret.
	TokenSource func(context.Context) (string, error)
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
	token, err := s.operationalToken(ctx)
	if err != nil {
		logger.Error(err, "Could not read operational API token")
		return
	}

	// ── Poll the probe runner ────────────────────────────
	results, err := s.fetchResultsWithToken(token)
	if err != nil {
		// Best-effort. The runner might not be ready yet (Deployment
		// still starting, no CRs created yet, etc.). We'll retry
		// on the next tick.
		logger.Info("Could not fetch probe results", "error", err)
		s.syncIncidents(ctx, token)
		s.syncProposals(ctx, token)
		return
	}

	if len(results) == 0 {
		s.syncIncidents(ctx, token)
		s.syncProposals(ctx, token)
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
	}

	// ── List all GrpcCanary CRs ──────────────────────────
	var grpcCanaryList canaryv1alpha1.GrpcCanaryList
	if err := s.List(ctx, &grpcCanaryList); err != nil {
		logger.Error(err, "Failed to list GrpcCanary resources for status sync")
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
		if !s.statusChanged(canary, res) {
			continue
		}

		if err := s.updateHTTPResultStatus(ctx, types.NamespacedName{Namespace: canary.Namespace, Name: canary.Name}, res); err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			logger.Error(err, "Failed to update status", "canary", key)
			continue
		}
		updated++
	}

	for i := range grpcCanaryList.Items {
		canary := &grpcCanaryList.Items[i]
		key := fmt.Sprintf("%s/%s", canary.Namespace, canary.Name)

		res, found := resultMap[key]
		if !found {
			continue
		}
		if !s.grpcStatusChanged(canary, res) {
			continue
		}

		if err := s.updateGRPCResultStatus(ctx, types.NamespacedName{Namespace: canary.Namespace, Name: canary.Name}, res); err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			logger.Error(err, "Failed to update status", "grpccanary", key)
			continue
		}
		updated++
	}

	// Incidents are synced after results so a canary's phase and its incident
	// membership land in the same cycle.
	s.syncIncidents(ctx, token)
	s.syncProposals(ctx, token)

	logger.Info("Status sync complete",
		"resultsReceived", len(results),
		"httpCanariesChecked", len(canaryList.Items),
		"grpcCanariesChecked", len(grpcCanaryList.Items),
		"statusesUpdated", updated,
	)
}

func (s *StatusSyncer) updateHTTPResultStatus(ctx context.Context, key types.NamespacedName, res proberunner.ProbeResult) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current canaryv1alpha1.HttpCanary
		if err := s.Get(ctx, key, &current); err != nil {
			return err
		}
		if !s.statusChanged(&current, res) {
			return nil
		}
		applyResultStatus(&current.Status.Phase, &current.Status.LastStatus, &current.Status.Message, &current.Status.LastCheckTime, res)
		return s.Status().Update(ctx, &current)
	})
}

func (s *StatusSyncer) updateGRPCResultStatus(ctx context.Context, key types.NamespacedName, res proberunner.ProbeResult) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current canaryv1alpha1.GrpcCanary
		if err := s.Get(ctx, key, &current); err != nil {
			return err
		}
		if !s.grpcStatusChanged(&current, res) {
			return nil
		}
		applyResultStatus(&current.Status.Phase, &current.Status.LastStatus, &current.Status.Message, &current.Status.LastCheckTime, res)
		return s.Status().Update(ctx, &current)
	})
}

func applyResultStatus(phase *string, lastStatus *int, message *string, lastCheckTime **metav1.Time, res proberunner.ProbeResult) {
	*phase = canaryv1alpha1.PhaseUnhealthy
	if res.Healthy {
		*phase = canaryv1alpha1.PhaseHealthy
	}
	*lastStatus = res.StatusCode
	*message = res.Message
	checkTime := metav1.NewTime(res.LastCheckTime)
	*lastCheckTime = &checkTime
}

// statusChanged returns true if the probe result differs from the CR's
// current status. This prevents writing unchanged statuses to the API
// server on every sync cycle.
//
// At scale, this is critical: 1,000 CRs × 4 syncs/minute = 4,000 potential
// writes/minute. If most probes are stable (Healthy → Healthy), skipping
// unchanged statuses drops this to near zero during steady state.
func (s *StatusSyncer) statusChanged(canary *canaryv1alpha1.HttpCanary, res proberunner.ProbeResult) bool {
	expectedPhase := canaryv1alpha1.PhaseUnhealthy
	if res.Healthy {
		expectedPhase = canaryv1alpha1.PhaseHealthy
	}

	return canary.Status.Phase != expectedPhase ||
		canary.Status.LastStatus != res.StatusCode ||
		canary.Status.Message != res.Message
}

func (s *StatusSyncer) grpcStatusChanged(canary *canaryv1alpha1.GrpcCanary, res proberunner.ProbeResult) bool {
	expectedPhase := canaryv1alpha1.PhaseUnhealthy
	if res.Healthy {
		expectedPhase = canaryv1alpha1.PhaseHealthy
	}

	return canary.Status.Phase != expectedPhase ||
		canary.Status.LastStatus != res.StatusCode ||
		canary.Status.Message != res.Message
}

// fetchResults retrieves a COMPLETE view of probe results.
//
// Completeness is the whole point of this function, and it must not depend on
// whether the intelligence feature is enabled. Three sources, in order:
//
//  1. an explicit override, for local development against a port-forward;
//  2. the incident engine, which aggregates every shard's pushed snapshot;
//  3. the probe runners themselves -- the ClusterIP Service when there is a
//     single replica, or every replica's own address when sharded.
//
// The engine is preferred but never required: it is only deployed when a canary
// opts into intelligence, and it must not become a hidden dependency of plain
// status reporting.
func (s *StatusSyncer) fetchResults() ([]proberunner.ProbeResult, error) {
	return s.fetchResultsWithToken("")
}

func (s *StatusSyncer) fetchResultsWithToken(token string) ([]proberunner.ProbeResult, error) {
	if s.ResultsURL != "" {
		return s.fetchResultsFromWithToken(s.ResultsURL, token)
	}

	if results, err := s.fetchResultsFromWithToken(s.incidentEngineURL()+"/results", token); err == nil && len(results) > 0 {
		return results, nil
	}

	shards := probeRunnerShards()
	if shards <= 1 {
		return s.fetchResultsFromWithToken(s.probeRunnerResultsURL(), token)
	}

	return s.fetchShardedResultsWithToken(int(shards), token)
}

// fetchShardedResults polls every replica and merges what comes back.
//
// A replica that cannot be reached is logged and skipped rather than failing
// the cycle. Missing results are safe: syncAllStatuses only touches canaries it
// has a result for, so an unreachable shard leaves its canaries at their
// previous status instead of corrupting them. Falling back to a single
// arbitrary shard, by contrast, would silently starve every other shard's
// canaries of updates.
func (s *StatusSyncer) fetchShardedResultsWithToken(shards int, token string) ([]proberunner.ProbeResult, error) {
	merged := make(map[string]proberunner.ProbeResult)
	reached := 0
	var lastErr error

	for _, url := range s.shardResultsURLs(shards) {
		results, err := s.fetchResultsFromWithToken(url, token)
		if err != nil {
			lastErr = err
			continue
		}

		reached++
		for _, result := range results {
			merged[result.Name] = result
		}
	}

	if reached == 0 {
		if lastErr != nil {
			return nil, fmt.Errorf("no probe runner replica could be reached: %w", lastErr)
		}
		return nil, nil
	}

	combined := make([]proberunner.ProbeResult, 0, len(merged))
	for _, result := range merged {
		combined = append(combined, result)
	}
	sort.Slice(combined, func(i, j int) bool { return combined[i].Name < combined[j].Name })

	return combined, nil
}

func (s *StatusSyncer) fetchResultsFrom(url string) ([]proberunner.ProbeResult, error) {
	return s.fetchResultsFromWithToken(url, "")
}

func (s *StatusSyncer) fetchResultsFromWithToken(url, token string) ([]proberunner.ProbeResult, error) {
	httpClient := &http.Client{Timeout: 5 * time.Second}
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building GET %s: %w", url, err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(request)
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

func (s *StatusSyncer) operationalToken(ctx context.Context) (string, error) {
	if s.TokenSource != nil {
		return s.TokenSource(ctx)
	}
	var secret corev1.Secret
	if err := s.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: ProbeAuthName}, &secret); err != nil {
		return "", fmt.Errorf("reading %s: %w", ProbeAuthName, err)
	}
	if token := string(secret.Data[ProbeInternalTokenKey]); token != "" {
		return token, nil
	}
	var authStore proberunner.AuthStore
	if err := yaml.Unmarshal(secret.Data[ProbeAuthFile], &authStore); err != nil {
		return "", fmt.Errorf("decoding %s: %w", ProbeAuthFile, err)
	}
	if token := authStore.Values[proberunner.InternalAuthTokenKey]; token != "" {
		return token, nil
	}
	return "", fmt.Errorf("%s contains no internal token", ProbeAuthName)
}

func (s *StatusSyncer) probeRunnerResultsURL() string {
	if s.ProbeRunnerURL != "" {
		return s.ProbeRunnerURL
	}
	if s.ResultsURL != "" {
		return s.ResultsURL
	}

	return fmt.Sprintf("http://%s.%s.svc:%d/results",
		ProbeRunnerName, s.Namespace, ProbeRunnerAPIPort)
}

// shardResultsURLs addresses every probe runner replica directly.
//
// StatefulSet pods have predictable DNS -- <name>-<ordinal>.<headless>.<ns>.svc
// -- so the full set can be derived from the replica count with no endpoint
// discovery.
//
// This exists because the ClusterIP Service is the WRONG source once sharding
// is on: it load-balances to one arbitrary replica, and a replica only knows
// its own slice of the probes. Polling it leaves the canaries owned by every
// other shard updating only when the balancer happens to pick their pod.
func (s *StatusSyncer) shardResultsURLs(shards int) []string {
	urls := make([]string, 0, shards)
	for ordinal := range shards {
		urls = append(urls, fmt.Sprintf("http://%s-%d.%s.%s.svc:%d/results",
			ProbeRunnerName, ordinal, ProbeRunnerHeadlessName, s.Namespace, ProbeRunnerAPIPort))
	}
	return urls
}
