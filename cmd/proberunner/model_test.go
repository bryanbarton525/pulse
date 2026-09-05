package main

import (
	"testing"

	"github.com/bryanbarton525/pulse/internal/proberunner"
)

func TestResolveHotModelIsDeterministicAcrossProbeOrder(t *testing.T) {
	probe := func(name, policy, path string) proberunner.Probe {
		return proberunner.Probe{
			Name: name,
			Intelligence: &proberunner.ProbeIntelligence{
				Policy:   policy,
				Model:    proberunner.ProbeModelConfig{Hot: proberunner.ProbeHotModel{Backend: "potion", ModelPath: path}},
				Triggers: proberunner.ProbeTriggers{BodyDrift: &proberunner.ProbeBodyDriftTrigger{}},
			},
		}
	}
	a := probe("z/probe", "z/policy", "/z.bin")
	b := probe("a/probe", "a/policy", "/a.bin")

	first, conflicts := resolveHotModel([]proberunner.Probe{a, b}, "/default.bin", "/vocab.txt")
	second, _ := resolveHotModel([]proberunner.Probe{b, a}, "/default.bin", "/vocab.txt")
	if first.ModelPath != "/a.bin" || second.ModelPath != "/a.bin" {
		t.Fatalf("resolved paths = %q and %q, want alphabetical policy's /a.bin", first.ModelPath, second.ModelPath)
	}
	if len(conflicts) != 1 || conflicts[0] != "z/policy" {
		t.Fatalf("conflicts = %v, want [z/policy]", conflicts)
	}
}
