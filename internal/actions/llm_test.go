package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/bryanbarton525/pulse/internal/proberunner"
)

func describeProbes(probes map[string]proberunner.Probe) ProbeDescriber {
	return func(name string) (proberunner.Probe, bool) {
		probe, found := probes[name]
		return probe, found
	}
}

func TestLLMSendsChatCompletionRequest(t *testing.T) {
	t.Parallel()

	server, got := captureServer(t, http.StatusOK,
		`{"choices":[{"message":{"role":"assistant","content":"**Assessment** — postgres is down."}}]}`)

	action := NewLLMAction("investigate", proberunner.ProbeLLMAction{
		Endpoint:           server.URL,
		Model:              "gemma-3-27b-it",
		APIKeyCredentialID: "key",
		MaxTokens:          512,
		TimeoutSeconds:     30,
	}, CredentialMap{"key": "sglang-token"}, nil, nil)

	investigation, err := action.Fire(context.Background(), testIncident())
	if err != nil {
		t.Fatalf("Fire() error = %v", err)
	}
	if !strings.Contains(investigation, "postgres is down") {
		t.Fatalf("investigation = %q, want the model's content", investigation)
	}

	if auth := got.headers.Get("Authorization"); auth != "Bearer sglang-token" {
		t.Fatalf("Authorization = %q, want bearer auth", auth)
	}

	var payload chatRequest
	if err := json.Unmarshal(got.body, &payload); err != nil {
		t.Fatalf("body is not a chat request: %v", err)
	}
	if payload.Model != "gemma-3-27b-it" || payload.MaxTokens != 512 {
		t.Fatalf("request = %+v, want the configured model and token budget", payload)
	}
	if len(payload.Messages) != 2 || payload.Messages[0].Role != "system" {
		t.Fatalf("messages = %+v, want a system prompt then a user prompt", payload.Messages)
	}
	if payload.Stream {
		t.Fatal("request set stream=true; the action reads a single response")
	}
}

// The prompt carries the WHOLE incident. That is the payoff of correlating
// first — the model sees the blast radius rather than one symptom.
func TestLLMPromptDescribesEveryMemberAndTopology(t *testing.T) {
	t.Parallel()

	server, got := captureServer(t, http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`)

	probes := map[string]proberunner.Probe{
		"data/postgres": {
			Name: "data/postgres", Type: "grpc", URL: "postgres.data.svc:5432",
			Intelligence: &proberunner.ProbeIntelligence{},
		},
		"default/payments": {
			Name: "default/payments", Type: "http", Method: "GET",
			URL: "https://payments.example.com/health", ExpectedStatus: 200,
			Intelligence: &proberunner.ProbeIntelligence{
				Topology: proberunner.ProbeTopology{
					DependsOn: []proberunner.ProbeDependency{
						{Canary: "default/payments", Upstream: []string{"data/postgres"}},
					},
				},
			},
		},
	}

	history := func(probe string, limit int) []string {
		return []string{"check 1 ok", "check 2 failed"}
	}

	action := NewLLMAction("investigate", proberunner.ProbeLLMAction{
		Endpoint: server.URL, ContextChecks: 5, TimeoutSeconds: 30,
	}, CredentialMap{}, describeProbes(probes), history)

	if _, err := action.Fire(context.Background(), testIncident()); err != nil {
		t.Fatalf("Fire() error = %v", err)
	}

	var payload chatRequest
	_ = json.Unmarshal(got.body, &payload)
	prompt := payload.Messages[1].Content

	for _, want := range []string{
		"data/postgres",
		"default/payments",
		"rootCause",
		"downstream",
		"https://payments.example.com/health",
		"Declared upstream: data/postgres",
		"check 2 failed",
		"connection refused",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt is missing %q:\n%s", want, prompt)
		}
	}
}

// This payload leaves the cluster. Credentials must never be in it.
func TestLLMPromptExcludesCredentials(t *testing.T) {
	t.Parallel()

	server, got := captureServer(t, http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`)

	probes := map[string]proberunner.Probe{
		"data/postgres": {
			Name: "data/postgres", URL: "postgres.data.svc:5432",
			Auth: &proberunner.ProbeAuth{
				Type:              "bearer",
				TokenCredentialID: "pulse-system__policy__bearer-token",
			},
			Headers:      map[string]string{"X-Api-Key": "super-secret-header-value"},
			Intelligence: &proberunner.ProbeIntelligence{},
		},
	}

	action := NewLLMAction("investigate", proberunner.ProbeLLMAction{
		Endpoint: server.URL, TimeoutSeconds: 30,
	}, CredentialMap{}, describeProbes(probes), nil)

	if _, err := action.Fire(context.Background(), testIncident()); err != nil {
		t.Fatalf("Fire() error = %v", err)
	}

	for _, secret := range []string{
		"super-secret-header-value",
		"pulse-system__policy__bearer-token",
	} {
		if strings.Contains(string(got.body), secret) {
			t.Fatalf("prompt leaked %q", secret)
		}
	}
}

func TestLLMUsesDefaultSystemPromptWhenUnset(t *testing.T) {
	t.Parallel()

	server, got := captureServer(t, http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`)
	action := NewLLMAction("investigate", proberunner.ProbeLLMAction{
		Endpoint: server.URL, TimeoutSeconds: 30,
	}, CredentialMap{}, nil, nil)

	if _, err := action.Fire(context.Background(), testIncident()); err != nil {
		t.Fatalf("Fire() error = %v", err)
	}

	var payload chatRequest
	_ = json.Unmarshal(got.body, &payload)
	if !strings.Contains(payload.Messages[0].Content, "site reliability engineer") {
		t.Fatalf("system prompt = %q, want the built-in default", payload.Messages[0].Content)
	}
}

func TestLLMSurfacesEndpointErrors(t *testing.T) {
	t.Parallel()

	server, _ := captureServer(t, http.StatusServiceUnavailable, `{"error":{"message":"no capacity"}}`)
	action := NewLLMAction("investigate", proberunner.ProbeLLMAction{
		Endpoint: server.URL, TimeoutSeconds: 30,
	}, CredentialMap{}, nil, nil)

	_, err := action.Fire(context.Background(), testIncident())
	if err == nil {
		t.Fatal("Fire() error = nil, want an endpoint error")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("Fire() error = %v, want it to carry the status code", err)
	}
}

func TestLLMRejectsEmptyChoices(t *testing.T) {
	t.Parallel()

	server, _ := captureServer(t, http.StatusOK, `{"choices":[]}`)
	action := NewLLMAction("investigate", proberunner.ProbeLLMAction{
		Endpoint: server.URL, TimeoutSeconds: 30,
	}, CredentialMap{}, nil, nil)

	if _, err := action.Fire(context.Background(), testIncident()); err == nil {
		t.Fatal("Fire() error = nil, want an error for an empty response")
	}
}

// The result is stored on a CRD status field capped at 8KiB.
func TestLLMTruncatesOversizedInvestigation(t *testing.T) {
	t.Parallel()

	huge, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]string{"content": strings.Repeat("x", 20000)}},
		},
	})
	server, _ := captureServer(t, http.StatusOK, string(huge))

	action := NewLLMAction("investigate", proberunner.ProbeLLMAction{
		Endpoint: server.URL, TimeoutSeconds: 30,
	}, CredentialMap{}, nil, nil)

	investigation, err := action.Fire(context.Background(), testIncident())
	if err != nil {
		t.Fatalf("Fire() error = %v", err)
	}
	if len(investigation) > InvestigationMaxBytes+64 {
		t.Fatalf("investigation length = %d, want it clipped near %d",
			len(investigation), InvestigationMaxBytes)
	}
	if !strings.Contains(investigation, "[truncated]") {
		t.Fatal("truncated investigation does not say so")
	}
}
