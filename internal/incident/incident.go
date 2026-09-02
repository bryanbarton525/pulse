package incident

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"github.com/bryanbarton525/pulse/internal/observation"
)

// Roles a member can hold within an incident.
const (
	RoleRootCause  = "rootCause"
	RoleDownstream = "downstream"
)

// Triggers that can open an incident. Correlation and novelty are decided by
// the engine; drift and latency arrive already decided by a runner.
const (
	TriggerFailureCorrelation = "failureCorrelation"
	TriggerFailureNovelty     = "failureNovelty"
	TriggerBodyDrift          = "bodyDrift"
	TriggerLatencyShift       = "latencyShift"
)

// Member is one canary caught up in an incident.
type Member struct {
	Probe  string                  `json:"probe"`
	Role   string                  `json:"role"`
	Signal observation.Observation `json:"signal"`
}

// Incident groups signals that share a cause.
//
// A single-member incident is the common case: one canary drifted, or one
// canary failed with nothing else nearby. Multi-member incidents are the
// payoff — five canaries failing off one dead backend become one page instead
// of five.
type Incident struct {
	ID string `json:"id"`

	// Signature keys throttling and deduplication. It is derived from the
	// SHAPE of the incident — the root cause and its failure cluster — not
	// from its timing, so a canary flapping every thirty seconds produces the
	// same signature each time and gets suppressed rather than paging repeatedly.
	Signature string `json:"signature"`

	Trigger   string   `json:"trigger"`
	Members   []Member `json:"members"`
	RootCause string   `json:"rootCause"`

	// Policy is the root cause's AnomalyPolicy. It owns action dispatch: one
	// investigation and one notification per incident, sent to whoever owns
	// the thing that actually broke.
	Policy string `json:"policy"`

	// Novel reports that this failure shape has not been seen before. Known
	// shapes skip expensive escalation.
	Novel bool `json:"novel"`

	OpenedAt  time.Time `json:"openedAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// Investigation is the language model's root-cause analysis, filled in by
	// the llm action if the policy declares one.
	Investigation string `json:"investigation,omitempty"`

	// dispatchErr records the state of the context the dispatcher was handed,
	// so tests can assert actions are not born cancelled.
	dispatchErr error

	// DownstreamFor names the probe this notice is about when the incident is
	// being reported to the owner of a VICTIM rather than of the root cause.
	// Suppressing victims is the whole point of correlating, but a team whose
	// service is red still deserves to know why — as a one-line reference to
	// somebody else's incident, not a second page.
	DownstreamFor string `json:"downstreamFor,omitempty"`
}

// ProbeNames lists every member probe, sorted.
func (i *Incident) ProbeNames() []string {
	names := make([]string, 0, len(i.Members))
	for _, member := range i.Members {
		names = append(names, member.Probe)
	}
	sort.Strings(names)
	return names
}

// Member returns the member for a probe, if present.
func (i *Incident) Member(probe string) (Member, bool) {
	for _, member := range i.Members {
		if member.Probe == probe {
			return member, true
		}
	}
	return Member{}, false
}

// RootCauseSignal returns the observation belonging to the root cause.
func (i *Incident) RootCauseSignal() observation.Observation {
	if member, found := i.Member(i.RootCause); found {
		return member.Signal
	}
	if len(i.Members) > 0 {
		return i.Members[0].Signal
	}
	return observation.Observation{}
}

// Removes a probe from the incident, reporting whether anything remains.
func (i *Incident) remove(probe string) bool {
	remaining := i.Members[:0]
	for _, member := range i.Members {
		if member.Probe != probe {
			remaining = append(remaining, member)
		}
	}
	i.Members = remaining
	return len(i.Members) > 0
}

// computeSignature hashes the incident's shape so that recurrences of the same
// problem collapse onto one throttle key.
//
// The member set is included because "the database is down" and "the database
// and the cache are down" are different situations worth telling someone about
// separately. Timestamps and scores are excluded, because those change on
// every occurrence and would defeat the throttle entirely.
func computeSignature(trigger, rootCause, clusterID string, members []string) string {
	sorted := append([]string(nil), members...)
	sort.Strings(sorted)

	hash := sha256.New()
	_, _ = hash.Write([]byte(trigger))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(rootCause))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(clusterID))
	for _, member := range sorted {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(member))
	}

	return hex.EncodeToString(hash.Sum(nil))[:16]
}
