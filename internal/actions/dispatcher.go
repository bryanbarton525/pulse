package actions

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/bryanbarton525/pulse/internal/incident"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// Dispatcher performs a policy's actions for an incident.
//
// Ownership rule: the ROOT CAUSE's policy owns the incident and fires the full
// action set. Downstream victims are suppressed — collapsing a storm into one
// page is the entire reason correlation exists — unless their own policy opts
// into a brief downstream notice.
type Dispatcher struct {
	mu sync.Mutex

	// byPolicy holds the compiled actions for each policy, built once when
	// configuration loads so a broken template or a missing credential is
	// reported at load time and not during an outage.
	byPolicy map[string][]Action

	throttles map[string]*Throttle
	probes    map[string]proberunner.Probe

	metrics *Metrics
	logger  logr.Logger

	// timeout bounds a single action so one unreachable endpoint cannot pin a
	// goroutine indefinitely.
	timeout time.Duration
}

// DispatcherOptions configures a Dispatcher.
type DispatcherOptions struct {
	Metrics *Metrics
	Logger  logr.Logger
	Timeout time.Duration
}

// NewDispatcher builds an empty dispatcher; call Load to populate it.
func NewDispatcher(options DispatcherOptions) *Dispatcher {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}

	return &Dispatcher{
		byPolicy:  map[string][]Action{},
		throttles: map[string]*Throttle{},
		probes:    map[string]proberunner.Probe{},
		metrics:   options.Metrics,
		logger:    options.Logger,
		timeout:   timeout,
	}
}

// Load compiles every distinct policy's actions from the probe configuration.
//
// Policies are compiled once, not once per probe: thousands of canaries
// typically share a handful of policies, and each compiled action holds a
// resolved credential and an HTTP client.
func (d *Dispatcher) Load(
	probes []proberunner.Probe,
	credentials Credentials,
	history HistoryLookup,
) error {
	byPolicy := map[string][]Action{}
	throttles := map[string]*Throttle{}
	indexed := make(map[string]proberunner.Probe, len(probes))

	describe := func(name string) (proberunner.Probe, bool) {
		probe, found := indexed[name]
		return probe, found
	}

	for _, probe := range probes {
		indexed[probe.Name] = probe
	}

	var problems []error
	for _, probe := range probes {
		if probe.Intelligence == nil {
			continue
		}
		policy := probe.Intelligence.Policy
		if _, done := byPolicy[policy]; done {
			continue
		}

		compiled, err := buildActions(probe.Intelligence.Actions, credentials, describe, history)
		if err != nil {
			// One malformed policy must not disable every other policy's
			// alerting, so record it and carry on.
			problems = append(problems, fmt.Errorf("policy %s: %w", policy, err))
			continue
		}

		byPolicy[policy] = compiled
		throttles[policy] = NewThrottle(
			time.Duration(probe.Intelligence.Throttle.CooldownSeconds)*time.Second,
			probe.Intelligence.Throttle.MaxPerHour,
		)
	}

	d.mu.Lock()
	d.byPolicy = byPolicy
	d.throttles = throttles
	d.probes = indexed
	d.mu.Unlock()

	if len(problems) > 0 {
		return fmt.Errorf("%d policies failed to load: %v", len(problems), problems)
	}
	return nil
}

func buildActions(
	configured []proberunner.ProbeAction,
	credentials Credentials,
	describe ProbeDescriber,
	history HistoryLookup,
) ([]Action, error) {
	compiled := make([]Action, 0, len(configured))

	for _, action := range configured {
		switch action.Type {
		case TypeMetric:
			compiled = append(compiled, NewMetricAction(action.Name))

		case TypeLLM:
			if action.LLM == nil {
				return nil, fmt.Errorf("action %q is missing its llm block", action.Name)
			}
			compiled = append(compiled,
				NewLLMAction(action.Name, *action.LLM, credentials, describe, history))

		case TypeSlack:
			if action.Slack == nil {
				return nil, fmt.Errorf("action %q is missing its slack block", action.Name)
			}
			built, err := NewSlackAction(action.Name, *action.Slack, credentials)
			if err != nil {
				return nil, err
			}
			compiled = append(compiled, built)

		case TypeObservability:
			if action.Observability == nil {
				return nil, fmt.Errorf("action %q is missing its observability block", action.Name)
			}
			built, err := NewObservabilityAction(action.Name, *action.Observability, credentials)
			if err != nil {
				return nil, err
			}
			compiled = append(compiled, built)

		default:
			return nil, fmt.Errorf("action %q has unsupported type %q", action.Name, action.Type)
		}
	}

	return compiled, nil
}

// Dispatch implements incident.Dispatcher.
func (d *Dispatcher) Dispatch(ctx context.Context, current *incident.Incident) {
	d.metrics.RecordIncident(current)

	d.fireForPolicy(ctx, current.Policy, current)
	d.notifyDownstream(ctx, current)
}

// fireForPolicy runs a policy's actions in declared order.
//
// Order is meaningful: an llm action earlier in the list populates
// Investigation, which a later slack action can include. Running them
// concurrently would make that impossible, so they run in sequence.
func (d *Dispatcher) fireForPolicy(ctx context.Context, policy string, current *incident.Incident) {
	d.mu.Lock()
	compiled := d.byPolicy[policy]
	throttle := d.throttles[policy]
	d.mu.Unlock()

	if len(compiled) == 0 {
		return
	}

	logger := d.logger.WithValues(
		"incident", current.ID, "policy", policy, "rootCause", current.RootCause)

	for _, action := range compiled {
		if throttle != nil && !throttle.Allow(current.Signature, action.Name()) {
			d.metrics.RecordThrottled(action)
			logger.V(1).Info("Action throttled", "action", action.Name())
			continue
		}

		actionCtx, cancel := context.WithTimeout(ctx, d.timeout)
		result, err := action.Fire(actionCtx, current)
		cancel()

		d.metrics.RecordAction(action, err)
		if err != nil {
			// One failing sink must not stop the others. If Slack is down, the
			// Datadog record should still be written.
			logger.Error(err, "Incident action failed", "action", action.Name())
			continue
		}

		if action.Type() == TypeLLM && result != "" {
			current.Investigation = result
		}
	}
}

// notifyDownstream sends a brief reference to victims whose policy asked for it.
func (d *Dispatcher) notifyDownstream(ctx context.Context, current *incident.Incident) {
	for _, member := range current.Members {
		if member.Role != incident.RoleDownstream {
			continue
		}

		d.mu.Lock()
		probe, found := d.probes[member.Probe]
		d.mu.Unlock()

		if !found || probe.Intelligence == nil {
			continue
		}
		if !probe.Intelligence.Incidents.NotifyOnDownstream {
			continue
		}
		// The root cause's own policy already reported this in full.
		if probe.Intelligence.Policy == current.Policy {
			continue
		}

		notice := *current
		notice.DownstreamFor = member.Probe
		// A victim's team gets the pointer, not the analysis.
		notice.Investigation = ""

		d.fireForPolicy(ctx, probe.Intelligence.Policy, &notice)
	}
}
