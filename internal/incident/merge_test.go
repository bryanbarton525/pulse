package incident

import (
	"math"
	"testing"
	"time"

	"github.com/bryanbarton525/pulse/internal/embed"
)

func vector(values ...float32) embed.Vector {
	var sum float64
	for _, value := range values {
		sum += float64(value) * float64(value)
	}
	if sum > 0 {
		scale := float32(1 / math.Sqrt(sum))
		for index := range values {
			values[index] *= scale
		}
	}
	return embed.Vector{Space: embed.SpacePotion, Values: values}
}

func candidate(probe string, at time.Time, values ...float32) Candidate {
	return Candidate{Probe: probe, Vector: vector(values...), At: at}
}

// THE central guarantee: two things failing at the same moment, with different
// failure modes and no declared relationship, are two incidents. Without this
// the whole feature degrades into "alert when several things are red".
func TestEvaluateDoesNotMergeUnrelatedConcurrentFailures(t *testing.T) {
	t.Parallel()

	now := time.Now()
	dbTimeout := candidate("default/payments", now, 1, 0, 0)
	tlsExpired := candidate("edge/marketing", now, 0, 1, 0)

	decision := Evaluate(dbTimeout, tlsExpired, NewGraph(), 0.85)

	if decision.Merge {
		t.Fatalf("merged unrelated failures on evidence %q", decision.Evidence)
	}
	if decision.Evidence != EvidenceNone {
		t.Fatalf("Evidence = %q, want %q", decision.Evidence, EvidenceNone)
	}
}

// Two canaries reporting the same failure text share an upstream even if
// nobody declared one.
func TestEvaluateMergesOnFailureSimilarity(t *testing.T) {
	t.Parallel()

	now := time.Now()
	first := candidate("default/payments", now, 1, 0.02, 0)
	second := candidate("default/orders", now, 1, 0, 0)

	decision := Evaluate(first, second, NewGraph(), 0.85)

	if !decision.Merge {
		t.Fatalf("did not merge near-identical failures (similarity %v)", decision.Similarity)
	}
	if decision.Evidence != EvidenceSimilarity {
		t.Fatalf("Evidence = %q, want %q", decision.Evidence, EvidenceSimilarity)
	}
}

// A declared dependency merges failures even when their text looks nothing
// alike — a database returning "connection refused" and an API returning
// "500 internal server error" are one incident if one depends on the other.
func TestEvaluateMergesOnDeclaredEdgeDespiteDissimilarText(t *testing.T) {
	t.Parallel()

	now := time.Now()
	graph := NewGraph()
	graph.AddEdges("default/payments", []string{"data/postgres"})

	api := candidate("default/payments", now, 1, 0, 0)
	database := candidate("data/postgres", now, 0, 1, 0)

	decision := Evaluate(api, database, graph, 0.85)

	if !decision.Merge {
		t.Fatal("did not merge across a declared dependency edge")
	}
	if decision.Evidence != EvidenceDeclaredEdge {
		t.Fatalf("Evidence = %q, want %q", decision.Evidence, EvidenceDeclaredEdge)
	}
}

// The edge is a relationship, not a direction, for merge purposes.
func TestEvaluateDeclaredEdgeWorksInBothDirections(t *testing.T) {
	t.Parallel()

	now := time.Now()
	graph := NewGraph()
	graph.AddEdges("default/payments", []string{"data/postgres"})

	forward := Evaluate(
		candidate("default/payments", now, 1, 0, 0),
		candidate("data/postgres", now, 0, 1, 0), graph, 0.85)
	reverse := Evaluate(
		candidate("data/postgres", now, 0, 1, 0),
		candidate("default/payments", now, 1, 0, 0), graph, 0.85)

	if !forward.Merge || !reverse.Merge {
		t.Fatalf("edge is directional for merging: forward=%v reverse=%v",
			forward.Merge, reverse.Merge)
	}
}

func TestEvaluateRespectsSimilarityThreshold(t *testing.T) {
	t.Parallel()

	now := time.Now()
	first := candidate("a/one", now, 1, 0.5, 0)
	second := candidate("b/two", now, 1, 0, 0)

	if decision := Evaluate(first, second, NewGraph(), 0.99); decision.Merge {
		t.Fatalf("merged at a strict threshold (similarity %v)", decision.Similarity)
	}
	if decision := Evaluate(first, second, NewGraph(), 0.5); !decision.Merge {
		t.Fatal("did not merge at a permissive threshold")
	}
}

// A gRPC failure carries no text to embed. Rather than guessing, correlation
// falls back to declared topology alone.
func TestEvaluateWithoutVectorsFallsBackToTopology(t *testing.T) {
	t.Parallel()

	now := time.Now()
	bare := Candidate{Probe: "default/grpc", At: now}
	other := candidate("default/api", now, 1, 0, 0)

	if decision := Evaluate(bare, other, NewGraph(), 0.85); decision.Merge {
		t.Fatal("merged without vectors and without a declared edge")
	}

	graph := NewGraph()
	graph.AddEdges("default/api", []string{"default/grpc"})
	if decision := Evaluate(bare, other, graph, 0.85); !decision.Merge {
		t.Fatal("did not merge on a declared edge when vectors were absent")
	}
}

// Comparing a hot-path vector to a cold-path vector is meaningless; the merge
// path must refuse rather than panic on a live signal.
func TestEvaluateRefusesCrossSpaceComparison(t *testing.T) {
	t.Parallel()

	now := time.Now()
	hot := Candidate{
		Probe: "a/one", At: now,
		Vector: embed.Vector{Space: embed.SpacePotion, Values: []float32{1, 0}},
	}
	cold := Candidate{
		Probe: "b/two", At: now,
		Vector: embed.Vector{Space: embed.SpaceMiniLM, Values: []float32{1, 0}},
	}

	if decision := Evaluate(hot, cold, NewGraph(), 0.85); decision.Merge {
		t.Fatal("merged vectors from different embedding spaces")
	}
}

func TestEvaluateNeverMergesAProbeWithItself(t *testing.T) {
	t.Parallel()

	now := time.Now()
	self := candidate("a/one", now, 1, 0, 0)

	if decision := Evaluate(self, self, NewGraph(), 0.85); decision.Merge {
		t.Fatal("merged a probe with itself")
	}
}
