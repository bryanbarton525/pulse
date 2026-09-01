package incident

import (
	"sort"
	"sync"
	"time"

	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// ResultBatch is one shard's view of its own probes.
//
// It is defined in the proberunner package, which owns ProbeResult, so the
// runner can build one without importing this package.
type ResultBatch = proberunner.ResultBatch

// Aggregator merges probe results from every shard into one view.
//
// With sharding, no single probe runner knows the whole cluster's state, but
// the controller's StatusSyncer still wants exactly one endpoint to poll. The
// engine already receives traffic from every shard, so it is the natural place
// to reassemble that picture — and it keeps StatusSyncer unchanged.
//
// State is kept PER SHARD rather than as one merged map so that a shard going
// away takes its probes with it. A single merged map would keep serving stale
// results for probes whose runner is gone.
type Aggregator struct {
	mu      sync.RWMutex
	shards  map[string]shardState
	timeout time.Duration
	now     func() time.Time

	// history keeps a short trail of each probe's check messages, which is the
	// context a language model needs to tell "just broke" from "flapping all
	// morning". It is bounded per probe so a long outage cannot grow it.
	history      map[string][]string
	historyDepth int
}

type shardState struct {
	results  map[string]proberunner.ProbeResult
	reported time.Time
}

// NewAggregator builds an aggregator that forgets a shard after timeout with
// no report from it.
func NewAggregator(timeout time.Duration) *Aggregator {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}

	return &Aggregator{
		shards:       map[string]shardState{},
		timeout:      timeout,
		now:          time.Now,
		history:      map[string][]string{},
		historyDepth: 32,
	}
}

// Record replaces a shard's results wholesale.
//
// Replacing rather than merging is deliberate: a probe deleted from a shard's
// config disappears from its next report, and that is how the result should
// disappear here too.
func (a *Aggregator) Record(batch ResultBatch) {
	shard := batch.Shard
	if shard == "" {
		shard = "default"
	}

	results := make(map[string]proberunner.ProbeResult, len(batch.Results))
	for _, result := range batch.Results {
		results[result.Name] = result
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	previous := a.shards[shard].results
	a.shards[shard] = shardState{results: results, reported: a.now()}

	// Record a history entry only when a probe's message actually changes, so
	// the trail reads as a sequence of events rather than the same line
	// repeated once per poll.
	for name, result := range results {
		if earlier, found := previous[name]; found && earlier.Message == result.Message {
			continue
		}
		a.appendHistoryLocked(name, result)
	}
}

func (a *Aggregator) appendHistoryLocked(probe string, result proberunner.ProbeResult) {
	state := "ok"
	if !result.Healthy {
		state = "FAILED"
	}

	entry := result.LastCheckTime.UTC().Format(time.RFC3339) + " " + state + ": " + result.Message

	trail := append(a.history[probe], entry)
	if len(trail) > a.historyDepth {
		trail = trail[len(trail)-a.historyDepth:]
	}
	a.history[probe] = trail
}

// History returns the most recent check messages for a probe, oldest first.
func (a *Aggregator) History(probe string, limit int) []string {
	if limit <= 0 {
		return nil
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	trail := a.history[probe]
	if len(trail) > limit {
		trail = trail[len(trail)-limit:]
	}

	return append([]string(nil), trail...)
}

// Results returns every live probe result, sorted by name for stable output.
func (a *Aggregator) Results() []proberunner.ProbeResult {
	a.mu.RLock()
	defer a.mu.RUnlock()

	cutoff := a.now().Add(-a.timeout)
	merged := make([]proberunner.ProbeResult, 0, a.sizeLocked())

	for _, state := range a.shards {
		// A shard that has stopped reporting is presumed gone; keeping its
		// results would show a healthy status for probes nobody is running.
		if state.reported.Before(cutoff) {
			continue
		}
		for _, result := range state.results {
			merged = append(merged, result)
		}
	}

	sort.Slice(merged, func(i, j int) bool { return merged[i].Name < merged[j].Name })
	return merged
}

func (a *Aggregator) sizeLocked() int {
	total := 0
	for _, state := range a.shards {
		total += len(state.results)
	}
	return total
}

// Shards lists the shards currently reporting.
func (a *Aggregator) Shards() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	cutoff := a.now().Add(-a.timeout)
	shards := make([]string, 0, len(a.shards))
	for shard, state := range a.shards {
		if state.reported.Before(cutoff) {
			continue
		}
		shards = append(shards, shard)
	}

	sort.Strings(shards)
	return shards
}
