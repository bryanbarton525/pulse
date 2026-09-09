/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package incident turns a storm of individual check failures into a small
// number of incidents, each with a named root cause.
package incident

import (
	"sort"
	"sync"

	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// Graph holds declared dependency edges between canaries.
//
// It is a UNION across every policy in the cluster, not a per-policy view. A
// service and its database are routinely owned by different teams and governed
// by different AnomalyPolicies, and those are exactly the pairs worth relating,
// so topology deliberately cuts across policy boundaries.
//
// Only DECLARED edges live here. Learned co-occurrence edges stay in Inference
// as proposals until a human promotes one into a policy — an inferred edge is a
// hypothesis, and acting on it silently would let a coincidence redirect blame
// onto an innocent service.
type Graph struct {
	mu sync.RWMutex

	// upstreams maps a canary to the canaries it depends on.
	upstreams map[string]map[string]struct{}
}

// NewGraph builds an empty dependency graph.
func NewGraph() *Graph {
	return &Graph{upstreams: map[string]map[string]struct{}{}}
}

// BuildGraph unions the declared topology of every probe's policy.
func BuildGraph(probes []proberunner.Probe) *Graph {
	graph := NewGraph()

	for _, probe := range probes {
		if probe.Intelligence == nil {
			continue
		}
		for _, dependency := range probe.Intelligence.Topology.DependsOn {
			graph.AddEdges(dependency.Canary, dependency.Upstream)
		}
	}

	return graph
}

// AddEdges records that canary depends on each of upstreams.
func (g *Graph) AddEdges(canary string, upstreams []string) {
	if canary == "" || len(upstreams) == 0 {
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.upstreams[canary] == nil {
		g.upstreams[canary] = map[string]struct{}{}
	}
	for _, upstream := range upstreams {
		if upstream != "" && upstream != canary {
			g.upstreams[canary][upstream] = struct{}{}
		}
	}
}

// Upstreams lists what a canary depends on, sorted for stable output.
func (g *Graph) Upstreams(canary string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	upstreams := make([]string, 0, len(g.upstreams[canary]))
	for upstream := range g.upstreams[canary] {
		upstreams = append(upstreams, upstream)
	}
	sort.Strings(upstreams)

	return upstreams
}

// Related reports whether a declared edge connects the two canaries in either
// direction. Direction does not matter for deciding that two failures belong to
// the same incident — only for deciding which one is to blame.
func (g *Graph) Related(left, right string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if _, found := g.upstreams[left][right]; found {
		return true
	}
	_, found := g.upstreams[right][left]
	return found
}

// Edges returns every declared edge as upstream/downstream pairs.
func (g *Graph) Edges() [][2]string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	edges := make([][2]string, 0, len(g.upstreams))
	for canary, upstreams := range g.upstreams {
		for upstream := range upstreams {
			edges = append(edges, [2]string{upstream, canary})
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i][0] != edges[j][0] {
			return edges[i][0] < edges[j][0]
		}
		return edges[i][1] < edges[j][1]
	})

	return edges
}

// RankRootCause picks which failing canary to blame.
//
// The rule is simple and explainable, which matters more here than cleverness:
// blame the failing canary that has no FAILING upstream. If payments-api
// depends on postgres-health and both are down, postgres-health has nothing
// failing beneath it, so it is the root cause and payments-api is a victim.
//
// Ties — several independent failures, or none of them connected — are broken
// by earliest onset, since whatever broke first is the better hypothesis.
// Candidates must already be sorted by onset time.
func (g *Graph) RankRootCause(failing []string) string {
	if len(failing) == 0 {
		return ""
	}

	failingSet := make(map[string]struct{}, len(failing))
	for _, canary := range failing {
		failingSet[canary] = struct{}{}
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	for _, canary := range failing {
		hasFailingUpstream := false
		for upstream := range g.upstreams[canary] {
			if _, found := failingSet[upstream]; found {
				hasFailingUpstream = true
				break
			}
		}
		if !hasFailingUpstream {
			return canary
		}
	}

	// Every candidate has a failing upstream, which means the declared edges
	// form a cycle. Fall back to earliest onset rather than looping forever.
	return failing[0]
}

// Roles labels each failing canary relative to the chosen root cause.
func (g *Graph) Roles(failing []string, rootCause string) map[string]string {
	roles := make(map[string]string, len(failing))
	for _, canary := range failing {
		if canary == rootCause {
			roles[canary] = RoleRootCause
			continue
		}
		roles[canary] = RoleDownstream
	}
	return roles
}
