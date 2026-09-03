package actions

import (
	"sync"
	"time"
)

// Throttle bounds how often one incident shape can fire one action.
//
// Without it, a canary flapping on a thirty-second interval would post to Slack
// a hundred and twenty times an hour and re-invoke a language model every time.
// The key is the incident SIGNATURE rather than its ID, so the same problem
// recurring is recognized as the same problem even though each occurrence opens
// a fresh incident.
type Throttle struct {
	mu      sync.Mutex
	history map[string][]time.Time

	cooldown   time.Duration
	maxPerHour int
	now        func() time.Time
}

// NewThrottle builds a throttle. A cooldown or cap of zero disables that limit.
func NewThrottle(cooldown time.Duration, maxPerHour int) *Throttle {
	return &Throttle{
		history:    map[string][]time.Time{},
		cooldown:   cooldown,
		maxPerHour: maxPerHour,
		now:        time.Now,
	}
}

// Allow reports whether an action may fire now, and records it if so.
func (t *Throttle) Allow(signature, action string) bool {
	key := signature + "\x00" + action
	now := t.now()

	t.mu.Lock()
	defer t.mu.Unlock()

	// Drop anything older than an hour; that is the longest window any limit
	// here looks back over.
	cutoff := now.Add(-time.Hour)
	kept := t.history[key][:0]
	for _, at := range t.history[key] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	t.history[key] = kept

	if t.cooldown > 0 && len(kept) > 0 {
		if now.Sub(kept[len(kept)-1]) < t.cooldown {
			return false
		}
	}

	if t.maxPerHour > 0 && len(kept) >= t.maxPerHour {
		return false
	}

	t.history[key] = append(t.history[key], now)
	return true
}

// Forget clears history for a signature, so a genuinely new occurrence after a
// long quiet period is not held back by stale bookkeeping.
func (t *Throttle) Forget(signature string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for key := range t.history {
		if len(key) > len(signature) && key[:len(signature)] == signature {
			delete(t.history, key)
		}
	}
}
