package proberunner

import (
	"fmt"
	"testing"
)

func namedProbes(count int) *ProbeConfig {
	config := &ProbeConfig{}
	for index := range count {
		config.Probes = append(config.Probes, Probe{Name: fmt.Sprintf("ns/probe-%d", index)})
	}
	return config
}

// Every probe must be owned by exactly one shard. A gap means an unmonitored
// endpoint; an overlap means duplicate checks and double-counted metrics.
func TestShardPartitionsProbesExactlyOnce(t *testing.T) {
	t.Parallel()

	const total = 5
	config := namedProbes(500)

	owners := map[string]int{}
	for ordinal := range total {
		for _, probe := range Shard(config, ordinal, total).Probes {
			owners[probe.Name]++
		}
	}

	if len(owners) != 500 {
		t.Fatalf("covered %d probes across all shards, want 500", len(owners))
	}
	for name, count := range owners {
		if count != 1 {
			t.Fatalf("probe %s is owned by %d shards, want exactly 1", name, count)
		}
	}
}

func TestShardIsStableAcrossCalls(t *testing.T) {
	t.Parallel()

	config := namedProbes(100)
	first := Shard(config, 2, 4)
	second := Shard(config, 2, 4)

	if len(first.Probes) != len(second.Probes) {
		t.Fatalf("shard sizes differ between calls: %d and %d", len(first.Probes), len(second.Probes))
	}
	for index := range first.Probes {
		if first.Probes[index].Name != second.Probes[index].Name {
			t.Fatal("shard assignment is not deterministic")
		}
	}
}

func TestShardDistributesReasonablyEvenly(t *testing.T) {
	t.Parallel()

	const total = 4
	config := namedProbes(4000)

	for ordinal := range total {
		got := len(Shard(config, ordinal, total).Probes)
		// A stable hash will not be perfectly even; badly lopsided would mean
		// one replica carrying most of the load.
		if got < 800 || got > 1200 {
			t.Fatalf("shard %d owns %d of 4000 probes, want roughly 1000", ordinal, got)
		}
	}
}

// The default must reproduce today's single-Deployment behavior exactly.
func TestShardWithSingleReplicaOwnsEverything(t *testing.T) {
	t.Parallel()

	config := namedProbes(50)
	if got := Shard(config, 0, 1); got != config {
		t.Fatal("a single shard should return the config unchanged")
	}
	if !OwnsProbe("anything", 0, 1) {
		t.Fatal("a single shard should own every probe")
	}
}

func TestOrdinalFromPodName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		podName string
		want    int
		wantErr bool
	}{
		{podName: "pulse-probe-runner-0", want: 0},
		{podName: "pulse-probe-runner-7", want: 7},
		{podName: "pulse-probe-runner-13", want: 13},
		{podName: "pulse-probe-runner", wantErr: true},
		{podName: "pulse-probe-runner-", wantErr: true},
		{podName: "", wantErr: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.podName, func(t *testing.T) {
			t.Parallel()

			got, err := OrdinalFromPodName(testCase.podName)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("OrdinalFromPodName(%q) error = nil, want an error", testCase.podName)
				}
				return
			}
			if err != nil {
				t.Fatalf("OrdinalFromPodName(%q) error = %v", testCase.podName, err)
			}
			if got != testCase.want {
				t.Fatalf("OrdinalFromPodName(%q) = %d, want %d", testCase.podName, got, testCase.want)
			}
		})
	}
}

// A misconfigured shard must monitor everything rather than nothing.
func TestShardFromEnvironmentFailsClosedToOwningEverything(t *testing.T) {
	t.Setenv("PULSE_PROBE_RUNNER_SHARDS", "4")
	t.Setenv("POD_NAME", "not-a-statefulset-pod")

	ordinal, total := ShardFromEnvironment()
	if ordinal != 0 || total != 1 {
		t.Fatalf("ShardFromEnvironment() = (%d, %d), want (0, 1) when the ordinal is unknowable",
			ordinal, total)
	}
}

func TestShardFromEnvironmentReadsPodOrdinal(t *testing.T) {
	t.Setenv("PULSE_PROBE_RUNNER_SHARDS", "4")
	t.Setenv("POD_NAME", "pulse-probe-runner-2")

	ordinal, total := ShardFromEnvironment()
	if ordinal != 2 || total != 4 {
		t.Fatalf("ShardFromEnvironment() = (%d, %d), want (2, 4)", ordinal, total)
	}
}
