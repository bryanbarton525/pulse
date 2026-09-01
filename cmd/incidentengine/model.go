package main

import (
	"reflect"
	"sort"
	"sync"

	"github.com/go-logr/logr"

	"github.com/bryanbarton525/pulse/internal/embed"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// resolveColdModel picks the single failure-path model for the whole cluster.
//
// This setting is deliberately NOT per-policy. Correlation merges failures
// across canaries governed by different policies -- a service and its database
// rarely share one -- by comparing their failure vectors. Vectors from
// different models occupy different embedding spaces and have no meaningful
// cosine distance between them, which the embed package treats as a
// panic-level wiring error. Honoring per-policy models would therefore make
// cross-policy correlation either silently wrong or impossible.
//
// Selection is deterministic: policies are considered in sorted name order, so
// the same configuration always resolves to the same model regardless of map
// iteration order. Any policy asking for something different is returned as a
// conflict for the caller to report.
func resolveColdModel(probes []proberunner.Probe) (*proberunner.ProbeColdModel, []string) {
	type candidate struct {
		policy string
		model  proberunner.ProbeColdModel
	}

	seen := map[string]candidate{}
	for _, probe := range probes {
		if probe.Intelligence == nil {
			continue
		}
		triggers := probe.Intelligence.Triggers
		// Only correlation and novelty use this tier.
		if triggers.FailureCorrelation == nil && triggers.FailureNovelty == nil {
			continue
		}
		policy := probe.Intelligence.Policy
		if _, found := seen[policy]; !found {
			seen[policy] = candidate{policy: policy, model: probe.Intelligence.Model.Cold}
		}
	}

	if len(seen) == 0 {
		return nil, nil
	}

	policies := make([]string, 0, len(seen))
	for policy := range seen {
		policies = append(policies, policy)
	}
	sort.Strings(policies)

	chosen := seen[policies[0]].model

	var conflicts []string
	for _, policy := range policies[1:] {
		if !reflect.DeepEqual(seen[policy].model, chosen) {
			conflicts = append(conflicts, policy)
		}
	}

	return &chosen, conflicts
}

// modelState owns the currently loaded failure-path embedder.
//
// It exists so a configuration reload can rebuild the model when it genuinely
// changed and leave it alone otherwise. Rebuilding unconditionally would
// discard a warm novelty index on every unrelated policy edit.
type modelState struct {
	mu       sync.Mutex
	resolved *proberunner.ProbeColdModel
	apiKey   string
	embedder embed.Embedder
}

// reload builds the embedder for the given configuration, replacing any
// previous one.
func (m *modelState) reload(
	probes []proberunner.Probe,
	authStore proberunner.AuthStore,
	logger logr.Logger,
) embed.Embedder {
	embedder, _ := m.reloadIfChanged(probes, authStore, logger)
	return embedder
}

// reloadIfChanged rebuilds only when the resolved model or its credential
// differs, reporting whether a swap happened.
func (m *modelState) reloadIfChanged(
	probes []proberunner.Probe,
	authStore proberunner.AuthStore,
	logger logr.Logger,
) (embed.Embedder, bool) {
	resolved, conflicts := resolveColdModel(probes)

	apiKey := ""
	if resolved != nil {
		apiKey = authStore.Values[resolved.HTTP.APIKeyCredentialID]
	}

	m.mu.Lock()
	unchanged := reflect.DeepEqual(resolved, m.resolved) && apiKey == m.apiKey
	current := m.embedder
	m.mu.Unlock()

	if unchanged {
		return current, false
	}

	// buildColdEmbedder reports any conflicting policies as it resolves.
	_ = conflicts
	embedder := buildColdEmbedder(probes, authStore, logger)

	m.mu.Lock()
	previous := m.embedder
	m.resolved = resolved
	m.apiKey = apiKey
	m.embedder = embedder
	m.mu.Unlock()

	// Close the old model only after the new one is in place, so no caller can
	// observe a closed session.
	if previous != nil && previous != embedder {
		_ = previous.Close()
	}

	return embedder, true
}

func (m *modelState) close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.embedder != nil {
		_ = m.embedder.Close()
		m.embedder = nil
	}
}
