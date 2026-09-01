package actions

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bryanbarton525/pulse/internal/incident"
)

// Metrics are the incident engine's Prometheus collectors.
//
// The metric action needs no credentials and no network, so it is the sink
// that always works — a policy with nothing but `type: metric` is a complete,
// useful configuration.
type Metrics struct {
	incidents *prometheus.CounterVec
	members   *prometheus.CounterVec
	actions   *prometheus.CounterVec
	throttled *prometheus.CounterVec
}

// NewMetrics registers the engine's collectors.
func NewMetrics(registerer prometheus.Registerer) *Metrics {
	metrics := &Metrics{
		incidents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pulse_incidents_total",
			Help: "Incidents opened, labeled by trigger and whether the failure shape was new.",
		}, []string{"trigger", "novel"}),
		members: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pulse_incident_members_total",
			Help: "Canaries drawn into an incident, labeled by their role.",
		}, []string{"trigger", "role"}),
		actions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pulse_incident_actions_total",
			Help: "Incident actions attempted, labeled by action, type and result.",
		}, []string{"action", "type", "result"}),
		throttled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pulse_incident_actions_throttled_total",
			Help: "Incident actions suppressed by the policy throttle.",
		}, []string{"action", "type"}),
	}

	registerer.MustRegister(metrics.incidents, metrics.members, metrics.actions, metrics.throttled)
	return metrics
}

// RecordIncident counts an incident and its members.
func (m *Metrics) RecordIncident(current *incident.Incident) {
	if m == nil {
		return
	}

	novel := "false"
	if current.Novel {
		novel = "true"
	}
	m.incidents.WithLabelValues(current.Trigger, novel).Inc()

	for _, member := range current.Members {
		m.members.WithLabelValues(current.Trigger, member.Role).Inc()
	}
}

// RecordAction counts one action attempt.
func (m *Metrics) RecordAction(action Action, err error) {
	if m == nil {
		return
	}

	result := "success"
	if err != nil {
		result = "failure"
	}
	m.actions.WithLabelValues(action.Name(), action.Type(), result).Inc()
}

// RecordThrottled counts one suppressed action.
func (m *Metrics) RecordThrottled(action Action) {
	if m == nil {
		return
	}
	m.throttled.WithLabelValues(action.Name(), action.Type()).Inc()
}

// MetricAction is the credential-free sink. The counters are already recorded
// by the dispatcher, so firing it is a no-op that exists to make "just give me
// a metric and a Kubernetes Event" an explicit, valid policy.
type MetricAction struct{ name string }

// NewMetricAction builds a metric action.
func NewMetricAction(name string) *MetricAction { return &MetricAction{name: name} }

// Name implements Action.
func (a *MetricAction) Name() string { return a.name }

// Type implements Action.
func (a *MetricAction) Type() string { return "metric" }

// Fire implements Action.
func (a *MetricAction) Fire(_ context.Context, _ *incident.Incident) (string, error) {
	return "", nil
}
