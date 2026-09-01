package incident

import (
	"testing"
	"time"

	"github.com/bryanbarton525/pulse/internal/proberunner"
)

func result(name string, healthy bool) proberunner.ProbeResult {
	return proberunner.ProbeResult{Name: name, Healthy: healthy, StatusCode: 200}
}

func TestAggregatorMergesEveryShardWithoutLossOrDuplication(t *testing.T) {
	t.Parallel()

	aggregator := NewAggregator(time.Minute)
	aggregator.Record(ResultBatch{Shard: "0", Results: []proberunner.ProbeResult{
		result("a/one", true), result("a/two", true),
	}})
	aggregator.Record(ResultBatch{Shard: "1", Results: []proberunner.ProbeResult{
		result("b/one", false),
	}})
	aggregator.Record(ResultBatch{Shard: "2", Results: []proberunner.ProbeResult{
		result("c/one", true), result("c/two", true),
	}})

	merged := aggregator.Results()
	if len(merged) != 5 {
		t.Fatalf("merged %d results, want 5", len(merged))
	}

	seen := map[string]int{}
	for _, entry := range merged {
		seen[entry.Name]++
	}
	for name, count := range seen {
		if count != 1 {
			t.Fatalf("result %s appears %d times, want once", name, count)
		}
	}
	// Sorted output keeps the controller's diffing stable.
	if merged[0].Name != "a/one" || merged[4].Name != "c/two" {
		t.Fatalf("results are not sorted by name: %v", merged)
	}
}

// A probe removed from a shard's config must disappear, not linger.
func TestAggregatorReplacesShardResultsWholesale(t *testing.T) {
	t.Parallel()

	aggregator := NewAggregator(time.Minute)
	aggregator.Record(ResultBatch{Shard: "0", Results: []proberunner.ProbeResult{
		result("a/one", true), result("a/two", true),
	}})
	aggregator.Record(ResultBatch{Shard: "0", Results: []proberunner.ProbeResult{
		result("a/one", true),
	}})

	merged := aggregator.Results()
	if len(merged) != 1 || merged[0].Name != "a/one" {
		t.Fatalf("merged = %v, want only the still-reported probe", merged)
	}
}

// A dead shard's results must expire, or the controller keeps reporting a
// healthy status for probes nobody is running.
func TestAggregatorForgetsSilentShards(t *testing.T) {
	t.Parallel()

	clock := time.Unix(1_700_000_000, 0)
	aggregator := NewAggregator(90 * time.Second)
	aggregator.now = func() time.Time { return clock }

	aggregator.Record(ResultBatch{Shard: "0", Results: []proberunner.ProbeResult{result("a/one", true)}})
	aggregator.Record(ResultBatch{Shard: "1", Results: []proberunner.ProbeResult{result("b/one", true)}})

	clock = clock.Add(60 * time.Second)
	aggregator.Record(ResultBatch{Shard: "0", Results: []proberunner.ProbeResult{result("a/one", true)}})

	clock = clock.Add(60 * time.Second)

	merged := aggregator.Results()
	if len(merged) != 1 || merged[0].Name != "a/one" {
		t.Fatalf("merged = %v, want only the shard still reporting", merged)
	}
	if shards := aggregator.Shards(); len(shards) != 1 || shards[0] != "0" {
		t.Fatalf("Shards() = %v, want only shard 0", shards)
	}
}

func TestAggregatorHandlesUnnamedShard(t *testing.T) {
	t.Parallel()

	aggregator := NewAggregator(time.Minute)
	aggregator.Record(ResultBatch{Results: []proberunner.ProbeResult{result("a/one", true)}})

	if got := len(aggregator.Results()); got != 1 {
		t.Fatalf("merged %d results, want 1", got)
	}
}
