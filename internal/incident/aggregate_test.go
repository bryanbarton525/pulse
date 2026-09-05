package incident

import (
	"testing"
	"time"

	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// probeA is the fixture probe these tests assert on most often.
const probeA = "a/one"

func result(name string, healthy bool) proberunner.ProbeResult {
	return proberunner.ProbeResult{Name: name, Healthy: healthy, StatusCode: 200}
}

func TestAggregatorMergesEveryShardWithoutLossOrDuplication(t *testing.T) {
	t.Parallel()

	aggregator := NewAggregator(time.Minute)
	aggregator.Record(ResultBatch{Shard: "0", Results: []proberunner.ProbeResult{
		result(probeA, true), result("a/two", true),
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
	if merged[0].Name != probeA || merged[4].Name != "c/two" {
		t.Fatalf("results are not sorted by name: %v", merged)
	}
}

// A probe removed from a shard's config must disappear, not linger.
func TestAggregatorReplacesShardResultsWholesale(t *testing.T) {
	t.Parallel()

	aggregator := NewAggregator(time.Minute)
	aggregator.Record(ResultBatch{Shard: "0", Results: []proberunner.ProbeResult{
		result(probeA, true), result("a/two", true),
	}})
	aggregator.Record(ResultBatch{Shard: "0", Results: []proberunner.ProbeResult{
		result(probeA, true),
	}})

	merged := aggregator.Results()
	if len(merged) != 1 || merged[0].Name != probeA {
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

	aggregator.Record(ResultBatch{Shard: "0", Results: []proberunner.ProbeResult{result(probeA, true)}})
	aggregator.Record(ResultBatch{Shard: "1", Results: []proberunner.ProbeResult{result("b/one", true)}})

	clock = clock.Add(60 * time.Second)
	aggregator.Record(ResultBatch{Shard: "0", Results: []proberunner.ProbeResult{result(probeA, true)}})

	clock = clock.Add(60 * time.Second)

	merged := aggregator.Results()
	if len(merged) != 1 || merged[0].Name != probeA {
		t.Fatalf("merged = %v, want only the shard still reporting", merged)
	}
	if shards := aggregator.Shards(); len(shards) != 1 || shards[0] != "0" {
		t.Fatalf("Shards() = %v, want only shard 0", shards)
	}
}

func TestAggregatorHandlesUnnamedShard(t *testing.T) {
	t.Parallel()

	aggregator := NewAggregator(time.Minute)
	aggregator.Record(ResultBatch{Results: []proberunner.ProbeResult{result(probeA, true)}})

	if got := len(aggregator.Results()); got != 1 {
		t.Fatalf("merged %d results, want 1", got)
	}
}

func TestAggregatorComputesLiveAgeWithItsOwnClock(t *testing.T) {
	t.Parallel()

	clock := time.Unix(1_700_000_000, 0)
	aggregator := NewAggregator(time.Minute)
	aggregator.now = func() time.Time { return clock }
	entry := result(probeA, true)
	entry.LastCheckTime = clock.Add(-7 * time.Second)
	aggregator.Record(ResultBatch{Results: []proberunner.ProbeResult{entry}})

	merged := aggregator.Results()
	if len(merged) != 1 || merged[0].LiveAgeSeconds != 7 {
		t.Fatalf("live age = %v, want 7 seconds", merged)
	}
}

func TestAggregatorBoundsHistoryForRemovedProbes(t *testing.T) {
	t.Parallel()

	clock := time.Unix(1_700_000_000, 0)
	aggregator := NewAggregator(time.Minute)
	aggregator.now = func() time.Time { return clock }

	first := result(probeA, false)
	first.Message = "failed"
	aggregator.Record(ResultBatch{Shard: "0", Results: []proberunner.ProbeResult{first}})
	if got := len(aggregator.History(probeA, 10)); got != 1 {
		t.Fatalf("initial history length = %d, want 1", got)
	}

	clock = clock.Add(2 * time.Minute)
	aggregator.Record(ResultBatch{Shard: "0", Results: []proberunner.ProbeResult{
		result("b/one", true),
	}})
	if got := aggregator.History(probeA, 10); len(got) != 0 {
		t.Fatalf("removed probe history was retained: %v", got)
	}
}

func TestAggregatorKeepsHistoryWhileProbeMovesShards(t *testing.T) {
	t.Parallel()

	clock := time.Unix(1_700_000_000, 0)
	aggregator := NewAggregator(time.Minute)
	aggregator.now = func() time.Time { return clock }
	first := result(probeA, false)
	first.Message = "failed"
	aggregator.Record(ResultBatch{Shard: "0", Results: []proberunner.ProbeResult{first}})

	clock = clock.Add(30 * time.Second)
	aggregator.Record(ResultBatch{Shard: "1", Results: []proberunner.ProbeResult{first}})
	aggregator.Record(ResultBatch{Shard: "0"})
	if got := len(aggregator.History(probeA, 10)); got == 0 {
		t.Fatal("probe history was lost during resharding")
	}
}
