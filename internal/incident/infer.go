package incident

import (
	"sort"
	"sync"
	"time"
)

// Proposal is a dependency edge the engine learned from co-occurrence.
//
// A proposal is a HYPOTHESIS and never affects correlation. It is surfaced in
// AnomalyPolicy status for a human to review and promote into
// spec.topology.dependsOn. The reason is straightforward: co-occurrence is not
// causation, and an edge that silently redirected blame onto an innocent
// service would be worse than no edge at all — an operator would be sent to
// debug the wrong system during an outage.
type Proposal struct {
	From         string    `json:"from"`
	To           string    `json:"to"`
	Confidence   float64   `json:"confidence"`
	Observations int       `json:"observations"`
	LastObserved time.Time `json:"lastObserved"`
}

type onset struct {
	probe string
	at    time.Time
}

// Inference learns which canaries tend to fail just before which others.
type Inference struct {
	mu sync.Mutex

	// onsets counts how many times each probe STARTED failing. Only onsets
	// count, not every failing check: a probe failing for an hour at a 30s
	// interval is one event, not 120, and counting ticks would manufacture
	// enormous confidence from a single outage.
	onsets map[string]int

	// cooccurrences[from][to] counts how often `from` began failing shortly
	// before `to` did.
	cooccurrences map[string]map[string]int

	lastObserved map[string]time.Time

	// recent is the sliding list of onsets used to find candidate causes.
	recent []onset
}

// NewInference builds an empty co-occurrence learner.
func NewInference() *Inference {
	return &Inference{
		onsets:        map[string]int{},
		cooccurrences: map[string]map[string]int{},
		lastObserved:  map[string]time.Time{},
	}
}

// RecordOnset notes that a probe just started failing, and credits any probe
// that started failing shortly before it as a candidate cause.
func (i *Inference) RecordOnset(probe string, at time.Time, window time.Duration) {
	i.mu.Lock()
	defer i.mu.Unlock()

	cutoff := at.Add(-window)

	// Drop onsets that have aged out of the window.
	kept := i.recent[:0]
	for _, entry := range i.recent {
		if !entry.at.Before(cutoff) {
			kept = append(kept, entry)
		}
	}
	i.recent = kept

	i.onsets[probe]++

	seen := map[string]struct{}{}
	for _, entry := range i.recent {
		if entry.probe == probe {
			continue
		}
		// Credit each candidate once per event, even if it appears repeatedly
		// in the window.
		if _, found := seen[entry.probe]; found {
			continue
		}
		seen[entry.probe] = struct{}{}

		if i.cooccurrences[entry.probe] == nil {
			i.cooccurrences[entry.probe] = map[string]int{}
		}
		i.cooccurrences[entry.probe][probe]++
		i.lastObserved[entry.probe+"\x00"+probe] = at
	}

	i.recent = append(i.recent, onset{probe: probe, at: at})
}

// Proposals returns learned edges that clear both thresholds, strongest first.
//
// Confidence is the fraction of `to`'s failures that were preceded by a `from`
// failure. Anchoring on the downstream probe is what makes the number
// meaningful: an upstream that fails constantly would otherwise appear to cause
// everything.
func (i *Inference) Proposals(minObservations int, minConfidence float64) []Proposal {
	if minObservations < 1 {
		minObservations = 1
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	var proposals []Proposal
	for from, downstreams := range i.cooccurrences {
		for to, count := range downstreams {
			total := i.onsets[to]
			if total == 0 || count < minObservations {
				continue
			}

			confidence := float64(count) / float64(total)
			if confidence < minConfidence {
				continue
			}

			proposals = append(proposals, Proposal{
				From:         from,
				To:           to,
				Confidence:   confidence,
				Observations: count,
				LastObserved: i.lastObserved[from+"\x00"+to],
			})
		}
	}

	sort.Slice(proposals, func(a, b int) bool {
		if proposals[a].Confidence != proposals[b].Confidence {
			return proposals[a].Confidence > proposals[b].Confidence
		}
		if proposals[a].From != proposals[b].From {
			return proposals[a].From < proposals[b].From
		}
		return proposals[a].To < proposals[b].To
	})

	return proposals
}

// Forget removes a probe from the learner, used when a canary is deleted.
func (i *Inference) Forget(probe string) {
	i.mu.Lock()
	defer i.mu.Unlock()

	delete(i.onsets, probe)
	delete(i.cooccurrences, probe)
	for from, downstreams := range i.cooccurrences {
		delete(downstreams, probe)
		if len(downstreams) == 0 {
			delete(i.cooccurrences, from)
		}
	}

	kept := i.recent[:0]
	for _, entry := range i.recent {
		if entry.probe != probe {
			kept = append(kept, entry)
		}
	}
	i.recent = kept
}
