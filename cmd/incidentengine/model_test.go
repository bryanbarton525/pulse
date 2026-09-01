package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"

	"github.com/bryanbarton525/pulse/internal/proberunner"
)

func coldProbe(name, policy string, model proberunner.ProbeColdModel) proberunner.Probe {
	return proberunner.Probe{
		Name: name,
		Intelligence: &proberunner.ProbeIntelligence{
			Policy: policy,
			Model:  proberunner.ProbeModelConfig{Cold: model},
			Triggers: proberunner.ProbeTriggers{
				FailureCorrelation: &proberunner.ProbeFailureCorrelationTrigger{
					WindowSeconds: 120, SimilarityThreshold: 0.85,
				},
			},
		},
	}
}

func httpColdModel(endpoint, credentialID string) proberunner.ProbeColdModel {
	return proberunner.ProbeColdModel{
		Backend: "http",
		HTTP: proberunner.ProbeHTTPEmbedModel{
			Endpoint:           endpoint,
			Model:              "bge-small",
			APIKeyCredentialID: credentialID,
		},
	}
}

// The whole credential chain exists to end up in an Authorization header. It
// was fully plumbed -- CRD field, Secret resolution, credential ID on the wire
// -- and then dropped at the final hop, so authenticated endpoints got
// anonymous requests.
func TestRemoteEmbedderSendsTheResolvedAPIKey(t *testing.T) {
	t.Parallel()

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"index": 0, "embedding": []float32{1, 0, 0}}},
		})
	}))
	defer server.Close()

	probes := []proberunner.Probe{
		coldProbe("default/api", "pulse-system/prod", httpColdModel(server.URL, "cred-id")),
	}
	authStore := proberunner.AuthStore{Values: map[string]string{"cred-id": "secret-token"}}

	embedder := buildColdEmbedder(probes, authStore, logr.Discard())
	if embedder == nil {
		t.Fatal("buildColdEmbedder() = nil, want a remote embedder")
	}

	if _, err := embedder.Embed(context.Background(), []string{"boom"}); err != nil {
		t.Fatalf("Embed() error = %v", err)
	}

	if gotAuth != "Bearer secret-token" {
		t.Fatalf("Authorization = %q, want the resolved API key", gotAuth)
	}
}

func TestRemoteEmbedderWithoutCredentialSendsNoAuth(t *testing.T) {
	t.Parallel()

	var hadAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"index": 0, "embedding": []float32{1, 0, 0}}},
		})
	}))
	defer server.Close()

	probes := []proberunner.Probe{
		coldProbe("default/api", "pulse-system/prod", httpColdModel(server.URL, "")),
	}

	embedder := buildColdEmbedder(probes, proberunner.AuthStore{}, logr.Discard())
	if _, err := embedder.Embed(context.Background(), []string{"boom"}); err != nil {
		t.Fatalf("Embed() error = %v", err)
	}

	if hadAuth {
		t.Fatal("an Authorization header was sent with no credential configured")
	}
}

// The failure-path model is cluster-global on purpose: correlation compares
// failure vectors ACROSS policies, and vectors from different models are not
// comparable. Resolution must therefore be deterministic rather than dependent
// on map iteration order.
func TestColdModelResolutionIsDeterministic(t *testing.T) {
	t.Parallel()

	probes := []proberunner.Probe{
		coldProbe("z/last", "pulse-system/zeta", httpColdModel("http://zeta", "")),
		coldProbe("a/first", "pulse-system/alpha", httpColdModel("http://alpha", "")),
		coldProbe("m/mid", "pulse-system/mu", httpColdModel("http://mu", "")),
	}

	for range 20 {
		resolved, conflicts := resolveColdModel(probes)
		if resolved == nil {
			t.Fatal("resolveColdModel() = nil")
		}
		if resolved.HTTP.Endpoint != "http://alpha" {
			t.Fatalf("resolved endpoint = %q, want the first policy in sorted order",
				resolved.HTTP.Endpoint)
		}
		if len(conflicts) != 2 {
			t.Fatalf("conflicts = %v, want the two disagreeing policies", conflicts)
		}
	}
}

func TestColdModelResolutionReportsNoConflictWhenPoliciesAgree(t *testing.T) {
	t.Parallel()

	model := httpColdModel("http://shared", "cred")
	probes := []proberunner.Probe{
		coldProbe("a/one", "pulse-system/alpha", model),
		coldProbe("b/two", "pulse-system/beta", model),
	}

	_, conflicts := resolveColdModel(probes)
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none when every policy agrees", conflicts)
	}
}

// Probes that use neither correlation nor novelty do not need this tier.
func TestColdModelResolutionIgnoresProbesThatDoNotNeedIt(t *testing.T) {
	t.Parallel()

	driftOnly := proberunner.Probe{
		Name: "default/drift",
		Intelligence: &proberunner.ProbeIntelligence{
			Policy: "pulse-system/drift",
			Triggers: proberunner.ProbeTriggers{
				BodyDrift: &proberunner.ProbeBodyDriftTrigger{Threshold: 0.15},
			},
		},
	}

	if resolved, _ := resolveColdModel([]proberunner.Probe{driftOnly}); resolved != nil {
		t.Fatalf("resolveColdModel() = %+v, want nil when nothing needs the model", resolved)
	}
	if resolved, _ := resolveColdModel(nil); resolved != nil {
		t.Fatal("resolveColdModel(nil) should be nil")
	}
}

// A policy edit that changes the model must take effect on reload. Previously
// the embedder was built once at startup and the new configuration ignored.
func TestModelStateRebuildsWhenTheModelChanges(t *testing.T) {
	t.Parallel()

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"index": 0, "embedding": []float32{1, 0}}},
		})
	}))
	defer first.Close()

	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"index": 0, "embedding": []float32{0, 1}}},
		})
	}))
	defer second.Close()

	models := &modelState{}
	store := proberunner.AuthStore{Values: map[string]string{}}

	original := []proberunner.Probe{
		coldProbe("default/api", "pulse-system/prod", httpColdModel(first.URL, "")),
	}
	if _, changed := models.reloadIfChanged(original, store, logr.Discard()); !changed {
		t.Fatal("the first load should report a change")
	}

	// An unrelated reload must NOT rebuild, or every edit throws away the
	// warm novelty index.
	if _, changed := models.reloadIfChanged(original, store, logr.Discard()); changed {
		t.Fatal("an unchanged configuration rebuilt the model")
	}

	updated := []proberunner.Probe{
		coldProbe("default/api", "pulse-system/prod", httpColdModel(second.URL, "")),
	}
	if _, changed := models.reloadIfChanged(updated, store, logr.Discard()); !changed {
		t.Fatal("a changed endpoint did not rebuild the model")
	}

	models.close()
}

// Rotating the credential alone must also rebuild, since the key is captured
// inside the embedder at construction.
func TestModelStateRebuildsWhenOnlyTheCredentialChanges(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"index": 0, "embedding": []float32{1, 0}}},
		})
	}))
	defer server.Close()

	models := &modelState{}
	probes := []proberunner.Probe{
		coldProbe("default/api", "pulse-system/prod", httpColdModel(server.URL, "cred")),
	}

	before := proberunner.AuthStore{Values: map[string]string{"cred": "old-token"}}
	after := proberunner.AuthStore{Values: map[string]string{"cred": "rotated-token"}}

	models.reloadIfChanged(probes, before, logr.Discard())
	if _, changed := models.reloadIfChanged(probes, after, logr.Discard()); !changed {
		t.Fatal("a rotated credential did not rebuild the model")
	}

	models.close()
}
