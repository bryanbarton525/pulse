package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// shardServer stands in for one probe runner replica, serving only the probes
// that replica owns.
func shardServer(t *testing.T, names []string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		results := make([]proberunner.ProbeResult, 0, len(names))
		for _, name := range names {
			results = append(results, proberunner.ProbeResult{
				Name: name, Healthy: true, StatusCode: 200,
			})
		}
		_ = json.NewEncoder(w).Encode(results)
	}))
	t.Cleanup(server.Close)

	return server
}

// A sharded deployment must produce a COMPLETE view even with no incident
// engine, which is the case whenever intelligence is not enabled.
//
// Polling the ClusterIP Service instead would land on one arbitrary replica,
// and a replica only knows its own slice -- so every other shard's canaries
// would update only when the load balancer happened to pick their pod.
func TestShardedResultsMergeEveryReplica(t *testing.T) {
	t.Parallel()

	shards := []*httptest.Server{
		shardServer(t, []string{"default/a", "default/b"}),
		shardServer(t, []string{"default/c"}),
		shardServer(t, []string{"default/d", "default/e"}),
	}

	syncer := &StatusSyncer{Namespace: "pulse-system"}
	urls := make([]string, 0, len(shards))
	for _, shard := range shards {
		urls = append(urls, shard.URL)
	}

	merged := map[string]bool{}
	for _, url := range urls {
		results, err := syncer.fetchResultsFrom(url)
		if err != nil {
			t.Fatalf("fetchResultsFrom(%s) error = %v", url, err)
		}
		for _, result := range results {
			merged[result.Name] = true
		}
	}

	for _, want := range []string{"default/a", "default/b", "default/c", "default/d", "default/e"} {
		if !merged[want] {
			t.Fatalf("merged view is missing %s", want)
		}
	}
}

// One unreachable replica must not fail the cycle. Its canaries simply keep
// their previous status, because syncAllStatuses only touches canaries it has
// a result for.
func TestShardedResultsToleratesAnUnreachableReplica(t *testing.T) {
	t.Parallel()

	live := shardServer(t, []string{"default/a"})

	syncer := &StatusSyncer{Namespace: "pulse-system"}

	reached := 0
	merged := map[string]bool{}
	for _, url := range []string{live.URL, "http://127.0.0.1:1/results"} {
		results, err := syncer.fetchResultsFrom(url)
		if err != nil {
			continue
		}
		reached++
		for _, result := range results {
			merged[result.Name] = true
		}
	}

	if reached != 1 {
		t.Fatalf("reached %d replicas, want 1", reached)
	}
	if !merged["default/a"] {
		t.Fatal("results from the reachable replica were lost")
	}
}

// Per-replica addresses have to be derived from StatefulSet DNS, which is what
// makes fan-out possible without endpoint discovery.
func TestShardResultsURLsUseStatefulSetPodDNS(t *testing.T) {
	t.Parallel()

	syncer := &StatusSyncer{Namespace: "pulse-system"}
	urls := syncer.shardResultsURLs(3)

	if len(urls) != 3 {
		t.Fatalf("got %d URLs for 3 shards: %v", len(urls), urls)
	}

	for ordinal, url := range urls {
		want := fmt.Sprintf("%s-%d.%s.", ProbeRunnerName, ordinal, ProbeRunnerHeadlessName)
		if !strings.Contains(url, want) {
			t.Fatalf("URL %q does not address pod ordinal %d (want %q)", url, ordinal, want)
		}
		if !strings.HasSuffix(url, "/results") {
			t.Fatalf("URL %q does not end in /results", url)
		}
	}
}

// An explicit override is for local development and must bypass discovery.
func TestExplicitResultsURLWins(t *testing.T) {
	t.Parallel()

	server := shardServer(t, []string{"default/only"})
	syncer := &StatusSyncer{Namespace: "pulse-system", ResultsURL: server.URL}

	results, err := syncer.fetchResults()
	if err != nil {
		t.Fatalf("fetchResults() error = %v", err)
	}
	if len(results) != 1 || results[0].Name != "default/only" {
		t.Fatalf("fetchResults() = %v, want the overridden source", results)
	}
}
