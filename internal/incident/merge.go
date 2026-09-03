package incident

import (
	"time"

	"github.com/bryanbarton525/pulse/internal/embed"
)

// Why two failures were judged to belong to the same incident.
const (
	// EvidenceDeclaredEdge means an operator declared a dependency between the
	// two canaries — or promoted a learned edge into a policy, which is the
	// same thing once it lands in spec.topology.
	EvidenceDeclaredEdge = "declaredEdge"

	// EvidenceSimilarity means the two failures read as the same failure. This
	// is where the model earns its place: two canaries with no declared
	// relationship, both reporting an identical dial timeout to the same
	// address, are telling you they share an upstream nobody wrote down.
	EvidenceSimilarity = "similarity"

	// EvidenceNone means the failures merely happened at the same time, which
	// is not evidence of anything.
	EvidenceNone = "none"
)

// Candidate is one failing probe eligible for correlation.
type Candidate struct {
	Probe  string
	Vector embed.Vector
	Labels map[string]string
	At     time.Time
}

// MergeDecision records whether two failures belong to one incident, and why.
// The reason is kept because an operator looking at a merged incident needs to
// be able to see why Pulse thought these things were related.
type MergeDecision struct {
	Merge      bool
	Evidence   string
	Similarity float64
}

// Evaluate decides whether two concurrent failures belong to the same incident.
//
// The central design choice of this feature is here: simultaneity is NOT
// evidence. In any cluster of a few thousand canaries, unrelated things fail at
// the same moment constantly, and merging on timing alone would produce one
// enormous meaningless incident. A merge requires an actual reason — a declared
// dependency, or failure text that says the two probes hit the same wall.
//
// This is also why correlation does not need canaries to share an AnomalyPolicy.
// Policies follow team ownership; outages follow infrastructure. Gating on
// evidence rather than on configuration lets a payments canary and a database
// canary owned by different teams still land in one incident.
func Evaluate(left, right Candidate, graph *Graph, similarityThreshold float64) MergeDecision {
	if left.Probe == right.Probe {
		return MergeDecision{Evidence: EvidenceNone}
	}

	// A declared edge is the strongest signal available and needs no model.
	if graph != nil && graph.Related(left.Probe, right.Probe) {
		return MergeDecision{Merge: true, Evidence: EvidenceDeclaredEdge, Similarity: 1}
	}

	// Without vectors — a gRPC failure with no text, or an embedder outage —
	// fall back to declared topology alone rather than guessing.
	if len(left.Vector.Values) == 0 || len(right.Vector.Values) == 0 {
		return MergeDecision{Evidence: EvidenceNone}
	}
	if left.Vector.Space != right.Vector.Space {
		return MergeDecision{Evidence: EvidenceNone}
	}

	similarity := embed.Cosine(left.Vector, right.Vector)
	if similarity >= similarityThreshold {
		return MergeDecision{Merge: true, Evidence: EvidenceSimilarity, Similarity: similarity}
	}

	return MergeDecision{Evidence: EvidenceNone, Similarity: similarity}
}

// MatchesSelector reports whether a probe's labels satisfy a candidate
// selector.
//
// The selector is a guardrail for hard boundaries — never correlate dev with
// prod — and not the primary mechanism. An empty selector matches everything,
// which is the default: the candidate set is the whole cluster and evidence
// does the filtering.
func MatchesSelector(selector Selector, labels map[string]string) bool {
	if selector == nil {
		return true
	}
	return selector.Matches(labels)
}

// Selector abstracts label matching so this package does not depend on
// Kubernetes machinery for what is, at this point, a pure predicate.
type Selector interface {
	Matches(labels map[string]string) bool
}
