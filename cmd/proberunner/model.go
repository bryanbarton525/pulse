package main

import (
	"reflect"
	"sort"
	"sync"

	"github.com/go-logr/logr"

	"github.com/bryanbarton525/pulse/internal/embed"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// hotModelState owns the one cluster-wide body-drift embedding space. Every
// shard resolves from the full, unsharded probe list so policy placement cannot
// make replicas choose different vector spaces.
type hotModelState struct {
	mu        sync.Mutex
	resolved  *proberunner.ProbeHotModel
	embedder  embed.Embedder
	modelPath string
	vocabPath string
	cacheSize int
}

func (m *hotModelState) reload(probes []proberunner.Probe, logger logr.Logger) embed.Embedder {
	embedder, _ := m.reloadIfChanged(probes, logger)
	return embedder
}

func (m *hotModelState) reloadIfChanged(
	probes []proberunner.Probe,
	logger logr.Logger,
) (embed.Embedder, bool) {
	resolved, conflicts := resolveHotModel(probes, m.modelPath, m.vocabPath)

	m.mu.Lock()
	unchanged := reflect.DeepEqual(resolved, m.resolved)
	current := m.embedder
	m.mu.Unlock()
	if unchanged {
		return current, false
	}

	for _, policy := range conflicts {
		logger.Info("Ignoring a conflicting body-drift model; the hot embedding space is cluster-wide",
			"policy", policy)
	}

	var built embed.Embedder
	if resolved != nil {
		loaded, err := embed.LoadPotion(
			resolved.ModelPath, resolved.VocabPath, resolved.MaxSequenceLength)
		if err != nil {
			logger.Error(err, "Could not load the body-drift model; drift scoring is disabled",
				"model", resolved.ModelPath)
		} else {
			logger.Info("Loaded the body-drift model", "model", resolved.ModelPath,
				"dimensions", loaded.Dimensions())
			built = embed.NewCachingEmbedder(loaded, m.cacheSize)
		}
	}

	m.mu.Lock()
	m.resolved = resolved
	m.embedder = built
	m.mu.Unlock()
	return built, true
}

func (m *hotModelState) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.embedder != nil {
		_ = m.embedder.Close()
		m.embedder = nil
	}
}

func resolveHotModel(
	probes []proberunner.Probe,
	defaultModelPath, defaultVocabPath string,
) (*proberunner.ProbeHotModel, []string) {
	byPolicy := map[string]proberunner.ProbeHotModel{}
	for _, probe := range probes {
		if probe.Intelligence == nil || probe.Intelligence.Triggers.BodyDrift == nil {
			continue
		}
		model := probe.Intelligence.Model.Hot
		if model.Backend == "" {
			model.Backend = "potion"
		}
		if model.ModelPath == "" {
			model.ModelPath = defaultModelPath
		}
		if model.VocabPath == "" {
			model.VocabPath = defaultVocabPath
		}
		if _, found := byPolicy[probe.Intelligence.Policy]; !found {
			byPolicy[probe.Intelligence.Policy] = model
		}
	}
	if len(byPolicy) == 0 {
		return nil, nil
	}

	policies := make([]string, 0, len(byPolicy))
	for policy := range byPolicy {
		policies = append(policies, policy)
	}
	sort.Strings(policies)
	chosen := byPolicy[policies[0]]
	conflicts := make([]string, 0)
	for _, policy := range policies[1:] {
		if !reflect.DeepEqual(chosen, byPolicy[policy]) {
			conflicts = append(conflicts, policy)
		}
	}
	return &chosen, conflicts
}
