package incident

import (
	"sort"
	"sync"
	"time"
)

// Window is a rolling buffer of recent failures across every shard.
//
// It is cluster-wide by design. Probe runners shard by probe name, so two
// canaries hitting the same broken backend usually land on different replicas;
// correlating within a shard would miss exactly the incidents correlation
// exists to catch. All failures funnel here so the whole picture is in one
// place.
//
// Capacity is bounded rather than unbounded-with-TTL: during a total outage
// every probe in the cluster fails inside one window, and the buffer must not
// grow without limit while the engine is trying to reason about it.
type Window struct {
	mu       sync.Mutex
	entries  []Candidate
	capacity int
}

// NewWindow builds a window holding at most capacity failures.
func NewWindow(capacity int) *Window {
	if capacity <= 0 {
		capacity = 4096
	}
	return &Window{capacity: capacity}
}

// Add records a failure, evicting the oldest entry when full.
func (w *Window) Add(candidate Candidate) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Replace any earlier entry for the same probe: a probe failing on every
	// tick should occupy one slot, not fill the buffer by itself.
	for index := range w.entries {
		if w.entries[index].Probe == candidate.Probe {
			w.entries[index] = candidate
			return
		}
	}

	w.entries = append(w.entries, candidate)
	if len(w.entries) > w.capacity {
		w.entries = w.entries[len(w.entries)-w.capacity:]
	}
}

// Remove drops a probe's entry, used when it recovers.
func (w *Window) Remove(probe string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	remaining := w.entries[:0]
	for _, entry := range w.entries {
		if entry.Probe != probe {
			remaining = append(remaining, entry)
		}
	}
	w.entries = remaining
}

// Recent returns failures within the window, oldest first, excluding a probe.
// Ordering matters: root-cause ranking breaks ties by earliest onset.
func (w *Window) Recent(now time.Time, within time.Duration, exclude string) []Candidate {
	w.mu.Lock()
	defer w.mu.Unlock()

	cutoff := now.Add(-within)
	recent := make([]Candidate, 0, len(w.entries))
	for _, entry := range w.entries {
		if entry.Probe == exclude || entry.At.Before(cutoff) {
			continue
		}
		recent = append(recent, entry)
	}

	sort.Slice(recent, func(i, j int) bool { return recent[i].At.Before(recent[j].At) })
	return recent
}

// Prune drops entries older than the cutoff.
func (w *Window) Prune(cutoff time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()

	remaining := w.entries[:0]
	for _, entry := range w.entries {
		if !entry.At.Before(cutoff) {
			remaining = append(remaining, entry)
		}
	}
	w.entries = remaining
}

// Len reports how many failures are currently buffered.
func (w *Window) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.entries)
}
