package incident

import (
	"testing"
	"time"
)

const inferWindow = 2 * time.Minute

// Repeatedly seeing postgres fail just before payments should produce a
// high-confidence proposal.
func TestInferenceProposesRepeatedCooccurrence(t *testing.T) {
	t.Parallel()

	inference := NewInference()
	base := time.Now()

	for round := range 6 {
		at := base.Add(time.Duration(round) * time.Hour)
		inference.RecordOnset("data/postgres", at, inferWindow)
		inference.RecordOnset("default/payments", at.Add(5*time.Second), inferWindow)
	}

	proposals := inference.Proposals(5, 0.85)
	if len(proposals) != 1 {
		t.Fatalf("Proposals() returned %d entries, want 1: %+v", len(proposals), proposals)
	}

	got := proposals[0]
	if got.From != "data/postgres" || got.To != "default/payments" {
		t.Fatalf("proposal = %s -> %s, want data/postgres -> default/payments", got.From, got.To)
	}
	if got.Confidence < 0.99 {
		t.Fatalf("Confidence = %v, want ~1.0", got.Confidence)
	}
	if got.Observations != 6 {
		t.Fatalf("Observations = %d, want 6", got.Observations)
	}
}

// One coincidence is not a dependency.
func TestInferenceRequiresMinimumObservations(t *testing.T) {
	t.Parallel()

	inference := NewInference()
	base := time.Now()
	inference.RecordOnset("data/postgres", base, inferWindow)
	inference.RecordOnset("default/payments", base.Add(time.Second), inferWindow)

	if got := inference.Proposals(5, 0.85); len(got) != 0 {
		t.Fatalf("Proposals() = %+v, want none below the observation floor", got)
	}
}

// A probe that fails on its own most of the time should not acquire a
// high-confidence upstream from occasional overlap.
func TestInferenceConfidenceIsAnchoredOnTheDownstreamProbe(t *testing.T) {
	t.Parallel()

	inference := NewInference()
	base := time.Now()

	// payments fails 20 times; postgres precedes it only 5 of those.
	for round := range 20 {
		at := base.Add(time.Duration(round) * time.Hour)
		if round < 5 {
			inference.RecordOnset("data/postgres", at, inferWindow)
		}
		inference.RecordOnset("default/payments", at.Add(5*time.Second), inferWindow)
	}

	if got := inference.Proposals(5, 0.85); len(got) != 0 {
		t.Fatalf("Proposals() = %+v, want none at 5/20 confidence", got)
	}

	proposals := inference.Proposals(5, 0.2)
	if len(proposals) != 1 {
		t.Fatalf("Proposals() returned %d entries at a low bar, want 1", len(proposals))
	}
	if confidence := proposals[0].Confidence; confidence < 0.24 || confidence > 0.26 {
		t.Fatalf("Confidence = %v, want ~0.25 (5 of 20)", confidence)
	}
}

// Failures far apart in time are unrelated, however often they both happen.
func TestInferenceIgnoresOnsetsOutsideTheWindow(t *testing.T) {
	t.Parallel()

	inference := NewInference()
	base := time.Now()

	for round := range 10 {
		at := base.Add(time.Duration(round) * time.Hour)
		inference.RecordOnset("data/postgres", at, inferWindow)
		// An hour later — far outside the correlation window.
		inference.RecordOnset("default/payments", at.Add(30*time.Minute), inferWindow)
	}

	if got := inference.Proposals(2, 0.5); len(got) != 0 {
		t.Fatalf("Proposals() = %+v, want none for onsets outside the window", got)
	}
}

// A probe failing for hours must count as ONE onset, or a single outage would
// manufacture overwhelming confidence.
func TestInferenceCountsOnsetsNotTicks(t *testing.T) {
	t.Parallel()

	inference := NewInference()
	base := time.Now()

	// Two genuine incidents, each recorded once.
	for round := range 2 {
		at := base.Add(time.Duration(round) * time.Hour)
		inference.RecordOnset("data/postgres", at, inferWindow)
		inference.RecordOnset("default/payments", at.Add(time.Second), inferWindow)
	}

	proposals := inference.Proposals(1, 0.5)
	if len(proposals) != 1 {
		t.Fatalf("Proposals() returned %d entries, want 1", len(proposals))
	}
	if proposals[0].Observations != 2 {
		t.Fatalf("Observations = %d, want 2 — one per incident", proposals[0].Observations)
	}
}

// Within a single window a candidate is credited once, not once per repetition.
func TestInferenceCreditsEachCandidateOncePerEvent(t *testing.T) {
	t.Parallel()

	inference := NewInference()
	base := time.Now()

	inference.RecordOnset("data/postgres", base, inferWindow)
	inference.RecordOnset("data/postgres", base.Add(time.Second), inferWindow)
	inference.RecordOnset("data/postgres", base.Add(2*time.Second), inferWindow)
	inference.RecordOnset("default/payments", base.Add(3*time.Second), inferWindow)

	proposals := inference.Proposals(1, 0.1)
	if len(proposals) != 1 {
		t.Fatalf("Proposals() returned %d entries, want 1", len(proposals))
	}
	if proposals[0].Observations != 1 {
		t.Fatalf("Observations = %d, want 1 despite three upstream onsets in the window",
			proposals[0].Observations)
	}
}

// The critical safety property: a learned edge is a suggestion. It must not
// change how failures merge until a human promotes it into a policy.
func TestProposalsDoNotAffectMergingUntilPromoted(t *testing.T) {
	t.Parallel()

	inference := NewInference()
	base := time.Now()
	for round := range 10 {
		at := base.Add(time.Duration(round) * time.Hour)
		inference.RecordOnset("data/postgres", at, inferWindow)
		inference.RecordOnset("default/payments", at.Add(time.Second), inferWindow)
	}

	proposals := inference.Proposals(5, 0.85)
	if len(proposals) == 0 {
		t.Fatal("expected a proposal to exist for this test to be meaningful")
	}

	// The graph is built only from DECLARED topology, so the learned edge is
	// absent and dissimilar failures still do not merge.
	graph := NewGraph()
	now := time.Now()
	decision := Evaluate(
		candidate("data/postgres", now, 1, 0, 0),
		candidate("default/payments", now, 0, 1, 0),
		graph, 0.85)

	if decision.Merge {
		t.Fatal("an unpromoted inferred edge changed the merge decision")
	}

	// After a human promotes it into spec.topology.dependsOn, it takes effect.
	graph.AddEdges(proposals[0].To, []string{proposals[0].From})
	promoted := Evaluate(
		candidate("data/postgres", now, 1, 0, 0),
		candidate("default/payments", now, 0, 1, 0),
		graph, 0.85)

	if !promoted.Merge {
		t.Fatal("the promoted edge did not take effect")
	}
}

func TestInferenceForgetRemovesAProbeEntirely(t *testing.T) {
	t.Parallel()

	inference := NewInference()
	base := time.Now()
	for round := range 6 {
		at := base.Add(time.Duration(round) * time.Hour)
		inference.RecordOnset("data/postgres", at, inferWindow)
		inference.RecordOnset("default/payments", at.Add(time.Second), inferWindow)
	}

	inference.Forget("data/postgres")

	if got := inference.Proposals(1, 0.1); len(got) != 0 {
		t.Fatalf("Proposals() = %+v after Forget, want none", got)
	}
}
