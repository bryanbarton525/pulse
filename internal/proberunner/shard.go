package proberunner

import (
	"fmt"
	"hash/fnv"
	"os"
	"strconv"
	"strings"
)

// Shard filters a config down to the probes this replica owns.
//
// Assignment is a stable hash of the probe name, so every replica reaches the
// same conclusion from the same ConfigMap with no coordination, no leases, and
// no shared state. Adding a replica reshuffles roughly 1/N of the probes; the
// ones that move re-warm their drift and latency baselines via the burst
// warm-start, which is why rebalancing is safe.
//
// A total of one returns the config unchanged, which is the default and
// reproduces the single-Deployment behavior exactly.
func Shard(config *ProbeConfig, ordinal, total int) *ProbeConfig {
	if config == nil {
		return nil
	}
	if total <= 1 {
		return config
	}

	owned := ProbeConfig{Probes: make([]Probe, 0, len(config.Probes)/total+1)}
	for _, probe := range config.Probes {
		if OwnsProbe(probe.Name, ordinal, total) {
			owned.Probes = append(owned.Probes, probe)
		}
	}

	return &owned
}

// OwnsProbe reports whether the given shard is responsible for a probe.
func OwnsProbe(name string, ordinal, total int) bool {
	if total <= 1 {
		return true
	}

	hash := fnv.New32a()
	_, _ = hash.Write([]byte(name))

	return int(hash.Sum32()%uint32(total)) == ordinal
}

// OrdinalFromPodName extracts a StatefulSet ordinal from a pod name such as
// "pulse-probe-runner-2".
//
// A StatefulSet is what makes sharding workable: Deployment pods have random
// names, so a replica could not know which slice of the probes it owns without
// leader election or a coordination service.
func OrdinalFromPodName(podName string) (int, error) {
	index := strings.LastIndex(podName, "-")
	if index < 0 || index == len(podName)-1 {
		return 0, fmt.Errorf("pod name %q has no StatefulSet ordinal suffix", podName)
	}

	ordinal, err := strconv.Atoi(podName[index+1:])
	if err != nil {
		return 0, fmt.Errorf("pod name %q has a non-numeric ordinal: %w", podName, err)
	}
	if ordinal < 0 {
		return 0, fmt.Errorf("pod name %q has a negative ordinal", podName)
	}

	return ordinal, nil
}

// ShardFromEnvironment resolves this replica's shard identity.
//
// It fails closed to shard 0 of 1 — a single replica owning everything — so a
// misconfigured deployment monitors too much rather than silently monitoring
// nothing.
func ShardFromEnvironment() (ordinal, total int) {
	total = 1
	if raw := os.Getenv("PULSE_PROBE_RUNNER_SHARDS"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			total = parsed
		}
	}

	if total == 1 {
		return 0, 1
	}

	if parsed, err := OrdinalFromPodName(os.Getenv("POD_NAME")); err == nil && parsed < total {
		return parsed, total
	}

	return 0, 1
}
