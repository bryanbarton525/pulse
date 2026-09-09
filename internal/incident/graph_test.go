package incident

import (
	"reflect"
	"testing"

	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// The canonical case: an API depends on a database, both are down, the
// database is to blame and the API is a victim.
func TestRankRootCausePicksTheNodeWithNoFailingUpstream(t *testing.T) {
	t.Parallel()

	graph := NewGraph()
	graph.AddEdges("default/payments", []string{probeDatabase})

	// Candidates arrive sorted by onset; the API was noticed first.
	got := graph.RankRootCause([]string{"default/payments", probeDatabase})

	if got != probeDatabase {
		t.Fatalf("RankRootCause() = %q, want data/postgres", got)
	}
}

func TestRankRootCauseWalksMultipleHops(t *testing.T) {
	t.Parallel()

	graph := NewGraph()
	graph.AddEdges("edge/web", []string{"default/payments"})
	graph.AddEdges("default/payments", []string{probeDatabase})

	got := graph.RankRootCause([]string{"edge/web", "default/payments", probeDatabase})

	if got != probeDatabase {
		t.Fatalf("RankRootCause() = %q, want the deepest failing node data/postgres", got)
	}
}

// An upstream that is HEALTHY does not absolve its downstream. Only failing
// upstreams shift the blame.
func TestRankRootCauseIgnoresHealthyUpstreams(t *testing.T) {
	t.Parallel()

	graph := NewGraph()
	graph.AddEdges("default/payments", []string{probeDatabase})

	// postgres is not in the failing set, so payments is the root cause.
	got := graph.RankRootCause([]string{"default/payments"})

	if got != "default/payments" {
		t.Fatalf("RankRootCause() = %q, want default/payments", got)
	}
}

// With no topology at all, the earliest failure is the best hypothesis.
func TestRankRootCauseFallsBackToEarliestOnset(t *testing.T) {
	t.Parallel()

	got := NewGraph().RankRootCause([]string{"first/probe", "second/probe", "third/probe"})

	if got != "first/probe" {
		t.Fatalf("RankRootCause() = %q, want the earliest failure", got)
	}
}

// A cycle in declared topology must terminate rather than spin.
func TestRankRootCauseTerminatesOnCyclicTopology(t *testing.T) {
	t.Parallel()

	graph := NewGraph()
	graph.AddEdges("a/one", []string{"b/two"})
	graph.AddEdges("b/two", []string{"a/one"})

	got := graph.RankRootCause([]string{"a/one", "b/two"})

	if got != "a/one" {
		t.Fatalf("RankRootCause() = %q, want the earliest failure as the cycle fallback", got)
	}
}

func TestRankRootCauseWithNoCandidates(t *testing.T) {
	t.Parallel()

	if got := NewGraph().RankRootCause(nil); got != "" {
		t.Fatalf("RankRootCause(nil) = %q, want empty", got)
	}
}

func TestRolesLabelsEveryMember(t *testing.T) {
	t.Parallel()

	failing := []string{probeDatabase, "default/payments", "edge/web"}
	roles := NewGraph().Roles(failing, probeDatabase)

	if roles[probeDatabase] != RoleRootCause {
		t.Fatalf("root cause role = %q, want %q", roles[probeDatabase], RoleRootCause)
	}
	for _, victim := range []string{"default/payments", "edge/web"} {
		if roles[victim] != RoleDownstream {
			t.Fatalf("%s role = %q, want %q", victim, roles[victim], RoleDownstream)
		}
	}
}

func TestGraphIgnoresSelfEdgesAndEmptyNames(t *testing.T) {
	t.Parallel()

	graph := NewGraph()
	graph.AddEdges("a/one", []string{"a/one", ""})
	graph.AddEdges("", []string{"b/two"})

	if got := graph.Upstreams("a/one"); len(got) != 0 {
		t.Fatalf("Upstreams() = %v, want none", got)
	}
	if got := graph.Edges(); len(got) != 0 {
		t.Fatalf("Edges() = %v, want none", got)
	}
}

// Topology must union across policies: a service and its database are usually
// owned by different teams and governed by different AnomalyPolicies.
func TestBuildGraphUnionsTopologyAcrossPolicies(t *testing.T) {
	t.Parallel()

	probes := []proberunner.Probe{
		{
			Name: "default/payments",
			Intelligence: &proberunner.ProbeIntelligence{
				Policy: "pulse-system/app-team",
				Topology: proberunner.ProbeTopology{
					DependsOn: []proberunner.ProbeDependency{
						{Canary: "default/payments", Upstream: []string{probeDatabase}},
					},
				},
			},
		},
		{
			Name: probeDatabase,
			Intelligence: &proberunner.ProbeIntelligence{
				Policy: "pulse-system/platform-team",
				Topology: proberunner.ProbeTopology{
					DependsOn: []proberunner.ProbeDependency{
						{Canary: probeDatabase, Upstream: []string{"data/storage"}},
					},
				},
			},
		},
		// A probe that never opted in contributes nothing and must not panic.
		{Name: "default/plain"},
	}

	graph := BuildGraph(probes)

	want := [][2]string{
		{probeDatabase, "default/payments"},
		{"data/storage", probeDatabase},
	}
	if got := graph.Edges(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Edges() = %v, want %v", got, want)
	}
	if got := graph.RankRootCause([]string{"default/payments", probeDatabase, "data/storage"}); got != "data/storage" {
		t.Fatalf("RankRootCause() = %q, want data/storage across the unioned graph", got)
	}
}
