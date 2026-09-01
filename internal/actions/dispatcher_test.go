package actions

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bryanbarton525/pulse/internal/incident"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// fakeAction records invocations and can pretend to be any action type.
type fakeAction struct {
	mu     sync.Mutex
	name   string
	kind   string
	result string
	err    error
	calls  int
	seen   []*incident.Incident
	order  *[]string
}

func (f *fakeAction) Name() string { return f.name }
func (f *fakeAction) Type() string { return f.kind }

func (f *fakeAction) Fire(_ context.Context, current *incident.Incident) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	snapshot := *current
	f.seen = append(f.seen, &snapshot)
	if f.order != nil {
		*f.order = append(*f.order, f.name)
	}
	return f.result, f.err
}

func (f *fakeAction) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newTestDispatcher(t *testing.T) *Dispatcher {
	t.Helper()

	return NewDispatcher(DispatcherOptions{
		Metrics: NewMetrics(prometheus.NewRegistry()),
		Logger:  logr.Discard(),
		Timeout: 5 * time.Second,
	})
}

// installPolicy wires compiled actions directly, bypassing config parsing.
func installPolicy(d *Dispatcher, policy string, throttle *Throttle, actions ...Action) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.byPolicy[policy] = actions
	d.throttles[policy] = throttle
}

// Order matters: an llm action must run before slack so the notification can
// carry the analysis.
func TestDispatcherRunsActionsInDeclaredOrderAndChainsInvestigation(t *testing.T) {
	t.Parallel()

	var order []string
	llm := &fakeAction{name: "investigate", kind: "llm", result: "**Assessment** — db is down.", order: &order}
	slack := &fakeAction{name: "notify", kind: "slack", order: &order}

	dispatcher := newTestDispatcher(t)
	installPolicy(dispatcher, "pulse-system/app", NewThrottle(0, 0), llm, slack)

	current := testIncident()
	current.Policy = "pulse-system/app"
	dispatcher.Dispatch(context.Background(), current)

	if len(order) != 2 || order[0] != "investigate" || order[1] != "notify" {
		t.Fatalf("action order = %v, want [investigate notify]", order)
	}
	if got := slack.seen[0].Investigation; got != "**Assessment** — db is down." {
		t.Fatalf("slack saw Investigation = %q, want the llm result chained in", got)
	}
}

// One broken sink must not silence the others.
func TestDispatcherContinuesAfterAnActionFails(t *testing.T) {
	t.Parallel()

	failing := &fakeAction{name: "notify", kind: "slack", err: errors.New("slack is down")}
	working := &fakeAction{name: "ship", kind: "observability"}

	dispatcher := newTestDispatcher(t)
	installPolicy(dispatcher, "pulse-system/app", NewThrottle(0, 0), failing, working)

	current := testIncident()
	current.Policy = "pulse-system/app"
	dispatcher.Dispatch(context.Background(), current)

	if working.count() != 1 {
		t.Fatal("a later action was skipped because an earlier one failed")
	}
}

// A flapping canary must not page repeatedly. The throttle keys on the incident
// signature, which is stable across recurrences of the same problem.
func TestDispatcherThrottlesRepeatedIncidents(t *testing.T) {
	t.Parallel()

	slack := &fakeAction{name: "notify", kind: "slack"}
	dispatcher := newTestDispatcher(t)
	installPolicy(dispatcher, "pulse-system/app", NewThrottle(15*time.Minute, 4), slack)

	current := testIncident()
	current.Policy = "pulse-system/app"

	for range 5 {
		dispatcher.Dispatch(context.Background(), current)
	}

	if slack.count() != 1 {
		t.Fatalf("slack fired %d times for the same signature, want 1 within the cooldown", slack.count())
	}
}

// Different problems must not throttle each other.
func TestDispatcherThrottlesPerSignature(t *testing.T) {
	t.Parallel()

	slack := &fakeAction{name: "notify", kind: "slack"}
	dispatcher := newTestDispatcher(t)
	installPolicy(dispatcher, "pulse-system/app", NewThrottle(15*time.Minute, 4), slack)

	for _, signature := range []string{"sig-a", "sig-b", "sig-c"} {
		current := testIncident()
		current.Policy = "pulse-system/app"
		current.Signature = signature
		dispatcher.Dispatch(context.Background(), current)
	}

	if slack.count() != 3 {
		t.Fatalf("slack fired %d times, want 3 — distinct problems must not throttle each other",
			slack.count())
	}
}

// The default: victims are silent. That is what turns five pages into one.
func TestDispatcherSuppressesDownstreamByDefault(t *testing.T) {
	t.Parallel()

	rootAction := &fakeAction{name: "notify", kind: "slack"}
	victimAction := &fakeAction{name: "notify", kind: "slack"}

	dispatcher := newTestDispatcher(t)
	installPolicy(dispatcher, "pulse-system/platform", NewThrottle(0, 0), rootAction)
	installPolicy(dispatcher, "pulse-system/app", NewThrottle(0, 0), victimAction)

	dispatcher.mu.Lock()
	dispatcher.probes = map[string]proberunner.Probe{
		"default/payments": {
			Name: "default/payments",
			Intelligence: &proberunner.ProbeIntelligence{
				Policy:    "pulse-system/app",
				Incidents: proberunner.ProbeIncidents{NotifyOnDownstream: false},
			},
		},
	}
	dispatcher.mu.Unlock()

	current := testIncident()
	current.Policy = "pulse-system/platform"
	dispatcher.Dispatch(context.Background(), current)

	if rootAction.count() != 1 {
		t.Fatalf("root cause policy fired %d times, want 1", rootAction.count())
	}
	if victimAction.count() != 0 {
		t.Fatalf("downstream policy fired %d times, want 0 by default", victimAction.count())
	}
}

// Opting in gets a terse reference, not a second full page.
func TestDispatcherSendsTerseNoticeWhenDownstreamOptsIn(t *testing.T) {
	t.Parallel()

	rootAction := &fakeAction{name: "notify", kind: "slack"}
	victimAction := &fakeAction{name: "notify", kind: "slack"}

	dispatcher := newTestDispatcher(t)
	installPolicy(dispatcher, "pulse-system/platform", NewThrottle(0, 0), rootAction)
	installPolicy(dispatcher, "pulse-system/app", NewThrottle(0, 0), victimAction)

	dispatcher.mu.Lock()
	dispatcher.probes = map[string]proberunner.Probe{
		"default/payments": {
			Name: "default/payments",
			Intelligence: &proberunner.ProbeIntelligence{
				Policy:    "pulse-system/app",
				Incidents: proberunner.ProbeIncidents{NotifyOnDownstream: true},
			},
		},
	}
	dispatcher.mu.Unlock()

	current := testIncident()
	current.Policy = "pulse-system/platform"
	current.Investigation = "the full analysis"
	dispatcher.Dispatch(context.Background(), current)

	if victimAction.count() != 1 {
		t.Fatalf("downstream policy fired %d times, want 1", victimAction.count())
	}

	notice := victimAction.seen[0]
	if notice.DownstreamFor != "default/payments" {
		t.Fatalf("DownstreamFor = %q, want default/payments", notice.DownstreamFor)
	}
	if notice.Investigation != "" {
		t.Fatal("the downstream notice carried the full investigation; it should be a pointer only")
	}
}

// If the victim happens to share the root cause's policy, it was already told.
func TestDispatcherDoesNotDoubleNotifyWithinOnePolicy(t *testing.T) {
	t.Parallel()

	action := &fakeAction{name: "notify", kind: "slack"}
	dispatcher := newTestDispatcher(t)
	installPolicy(dispatcher, "pulse-system/app", NewThrottle(0, 0), action)

	dispatcher.mu.Lock()
	dispatcher.probes = map[string]proberunner.Probe{
		"default/payments": {
			Name: "default/payments",
			Intelligence: &proberunner.ProbeIntelligence{
				Policy:    "pulse-system/app",
				Incidents: proberunner.ProbeIncidents{NotifyOnDownstream: true},
			},
		},
	}
	dispatcher.mu.Unlock()

	current := testIncident()
	current.Policy = "pulse-system/app"
	dispatcher.Dispatch(context.Background(), current)

	if action.count() != 1 {
		t.Fatalf("action fired %d times, want 1 — the victim shares the root cause's policy",
			action.count())
	}
}

// A malformed policy must not take down alerting for every other policy.
func TestLoadIsolatesBrokenPolicies(t *testing.T) {
	t.Parallel()

	dispatcher := newTestDispatcher(t)
	err := dispatcher.Load([]proberunner.Probe{
		{
			Name: "default/good",
			Intelligence: &proberunner.ProbeIntelligence{
				Policy: "pulse-system/good",
				Actions: []proberunner.ProbeAction{
					{Name: "local", Type: "metric"},
				},
			},
		},
		{
			Name: "default/bad",
			Intelligence: &proberunner.ProbeIntelligence{
				Policy: "pulse-system/bad",
				Actions: []proberunner.ProbeAction{
					// A slack action with no slack block.
					{Name: "notify", Type: "slack"},
				},
			},
		},
	}, CredentialMap{}, nil)

	if err == nil {
		t.Fatal("Load() error = nil, want the broken policy reported")
	}

	dispatcher.mu.Lock()
	_, goodLoaded := dispatcher.byPolicy["pulse-system/good"]
	_, badLoaded := dispatcher.byPolicy["pulse-system/bad"]
	dispatcher.mu.Unlock()

	if !goodLoaded {
		t.Fatal("a valid policy was dropped because another policy was broken")
	}
	if badLoaded {
		t.Fatal("the broken policy was loaded anyway")
	}
}

// Policies are compiled once, not once per canary.
func TestLoadCompilesEachPolicyOnce(t *testing.T) {
	t.Parallel()

	probes := make([]proberunner.Probe, 0, 500)
	for index := range 500 {
		probes = append(probes, proberunner.Probe{
			Name: "default/probe-" + string(rune('a'+index%26)) + string(rune('a'+index/26)),
			Intelligence: &proberunner.ProbeIntelligence{
				Policy:  "pulse-system/shared",
				Actions: []proberunner.ProbeAction{{Name: "local", Type: "metric"}},
			},
		})
	}

	dispatcher := newTestDispatcher(t)
	if err := dispatcher.Load(probes, CredentialMap{}, nil); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	dispatcher.mu.Lock()
	policies := len(dispatcher.byPolicy)
	dispatcher.mu.Unlock()

	if policies != 1 {
		t.Fatalf("compiled %d policies for 500 probes sharing one, want 1", policies)
	}
}
