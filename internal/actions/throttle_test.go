package actions

import (
	"testing"
	"time"
)

// fixedClock lets throttle tests advance time without sleeping.
type fixedClock struct{ at time.Time }

func (c *fixedClock) now() time.Time          { return c.at }
func (c *fixedClock) advance(d time.Duration) { c.at = c.at.Add(d) }

func newTestThrottle(cooldown time.Duration, maxPerHour int) (*Throttle, *fixedClock) {
	clock := &fixedClock{at: time.Unix(1_700_000_000, 0)}
	throttle := NewThrottle(cooldown, maxPerHour)
	throttle.now = clock.now
	return throttle, clock
}

func TestThrottleAllowsFirstThenBlocksWithinCooldown(t *testing.T) {
	t.Parallel()

	throttle, clock := newTestThrottle(15*time.Minute, 0)

	if !throttle.Allow("sig", "notify") {
		t.Fatal("the first attempt was blocked")
	}

	clock.advance(5 * time.Minute)
	if throttle.Allow("sig", "notify") {
		t.Fatal("an attempt inside the cooldown was allowed")
	}

	clock.advance(11 * time.Minute)
	if !throttle.Allow("sig", "notify") {
		t.Fatal("an attempt after the cooldown was blocked")
	}
}

func TestThrottleEnforcesHourlyCap(t *testing.T) {
	t.Parallel()

	throttle, clock := newTestThrottle(0, 3)

	for attempt := range 3 {
		if !throttle.Allow("sig", "notify") {
			t.Fatalf("attempt %d was blocked below the cap", attempt+1)
		}
		clock.advance(time.Minute)
	}

	if throttle.Allow("sig", "notify") {
		t.Fatal("an attempt over the hourly cap was allowed")
	}

	// The window slides: once the earliest attempt ages out, room reopens.
	clock.advance(58 * time.Minute)
	if !throttle.Allow("sig", "notify") {
		t.Fatal("an attempt was blocked after the hourly window slid")
	}
}

// Separate actions on the same incident must not throttle each other — Slack
// being rate-limited should not suppress the Datadog record.
func TestThrottleIsPerActionAndPerSignature(t *testing.T) {
	t.Parallel()

	throttle, _ := newTestThrottle(15*time.Minute, 0)

	if !throttle.Allow("sig", "notify") {
		t.Fatal("first action blocked")
	}
	if !throttle.Allow("sig", "ship") {
		t.Fatal("a different action on the same incident was blocked")
	}
	if !throttle.Allow("other-sig", "notify") {
		t.Fatal("the same action on a different incident was blocked")
	}
	if throttle.Allow("sig", "notify") {
		t.Fatal("a repeat of the same action and signature was allowed")
	}
}

func TestThrottleWithNoLimitsAlwaysAllows(t *testing.T) {
	t.Parallel()

	throttle, _ := newTestThrottle(0, 0)
	for attempt := range 100 {
		if !throttle.Allow("sig", "notify") {
			t.Fatalf("attempt %d was blocked by an unlimited throttle", attempt+1)
		}
	}
}

func TestThrottleForgetClearsHistory(t *testing.T) {
	t.Parallel()

	throttle, _ := newTestThrottle(time.Hour, 0)

	if !throttle.Allow("sig", "notify") {
		t.Fatal("first attempt blocked")
	}
	if throttle.Allow("sig", "notify") {
		t.Fatal("second attempt allowed inside the cooldown")
	}

	throttle.Forget("sig")

	if !throttle.Allow("sig", "notify") {
		t.Fatal("an attempt after Forget was still blocked")
	}
}
