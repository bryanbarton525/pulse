package actions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bryanbarton525/pulse/internal/incident"
	"github.com/bryanbarton525/pulse/internal/observation"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

type captured struct {
	path    string
	headers http.Header
	body    []byte
}

// captureServer records exactly one request for assertion.
func captureServer(t *testing.T, status int, response string) (*httptest.Server, *captured) {
	t.Helper()

	got := &captured{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.headers = r.Header.Clone()
		got.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)

	return server, got
}

func testIncident() *incident.Incident {
	at := time.Unix(1_700_000_000, 0).UTC()
	return &incident.Incident{
		ID:        "inc-1",
		Signature: "sig-1",
		Trigger:   incident.TriggerFailureCorrelation,
		RootCause: "data/postgres",
		Policy:    "pulse-system/platform",
		Novel:     true,
		UpdatedAt: at,
		Members: []incident.Member{
			{
				Probe: "data/postgres", Role: incident.RoleRootCause,
				Signal: observation.Observation{
					Probe: "data/postgres", Message: "connection refused", At: at,
				},
			},
			{Probe: "default/payments", Role: incident.RoleDownstream},
		},
	}
}

func fireObservability(
	t *testing.T,
	config proberunner.ProbeObservabilityAction,
	credential string,
) *captured {
	t.Helper()

	server, got := captureServer(t, http.StatusOK, `{}`)
	config.Endpoint = server.URL

	action, err := NewObservabilityAction("ship", config, CredentialMap{"cred": credential})
	if err != nil {
		t.Fatalf("NewObservabilityAction() error = %v", err)
	}
	if _, err := action.Fire(context.Background(), testIncident()); err != nil {
		t.Fatalf("Fire() error = %v", err)
	}

	return got
}

func TestObservabilityDatadogShape(t *testing.T) {
	t.Parallel()

	got := fireObservability(t, proberunner.ProbeObservabilityAction{
		Provider:     ProviderDatadog,
		CredentialID: "cred",
		Tags:         map[string]string{"env": "prod", "team": "sre"},
	}, "dd-api-key")

	if got.path != "/api/v2/logs" {
		t.Fatalf("path = %q, want /api/v2/logs", got.path)
	}
	if key := got.headers.Get("DD-API-KEY"); key != "dd-api-key" {
		t.Fatalf("DD-API-KEY = %q, want dd-api-key", key)
	}

	var payload []map[string]any
	if err := json.Unmarshal(got.body, &payload); err != nil {
		t.Fatalf("body is not a Datadog log array: %v", err)
	}
	if len(payload) != 1 {
		t.Fatalf("payload length = %d, want 1", len(payload))
	}
	tags, _ := payload[0]["ddtags"].(string)
	for _, want := range []string{"env:prod", "team:sre", "trigger:failureCorrelation"} {
		if !strings.Contains(tags, want) {
			t.Fatalf("ddtags = %q, want it to contain %q", tags, want)
		}
	}
	if status := payload[0]["status"]; status != "error" {
		t.Fatalf("status = %v, want error for a failure incident", status)
	}
}

func TestObservabilityLokiShape(t *testing.T) {
	t.Parallel()

	got := fireObservability(t, proberunner.ProbeObservabilityAction{
		Provider:     ProviderLoki,
		CredentialID: "cred",
		Username:     "tenant-user",
		Index:        "team-a",
		Tags:         map[string]string{"env": "prod"},
	}, "loki-password")

	if got.path != "/loki/api/v1/push" {
		t.Fatalf("path = %q, want /loki/api/v1/push", got.path)
	}
	if org := got.headers.Get("X-Scope-OrgID"); org != "team-a" {
		t.Fatalf("X-Scope-OrgID = %q, want team-a", org)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("tenant-user:loki-password"))
	if auth := got.headers.Get("Authorization"); auth != wantAuth {
		t.Fatalf("Authorization = %q, want basic auth", auth)
	}

	var payload struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(got.body, &payload); err != nil {
		t.Fatalf("body is not a Loki push payload: %v", err)
	}
	if len(payload.Streams) != 1 || len(payload.Streams[0].Values) != 1 {
		t.Fatalf("payload = %+v, want one stream with one value", payload)
	}

	// Loki labels are indexed. A per-incident label would create an unbounded
	// number of streams and eventually take the cluster down.
	labels := payload.Streams[0].Stream
	for _, forbidden := range []string{"incident", "root_cause", "probe"} {
		if _, found := labels[forbidden]; found {
			t.Fatalf("Loki labels include high-cardinality key %q: %v", forbidden, labels)
		}
	}
	if labels["env"] != "prod" || labels["job"] != "pulse" {
		t.Fatalf("Loki labels = %v, want env and job present", labels)
	}
}

func TestObservabilityElasticsearchShape(t *testing.T) {
	t.Parallel()

	got := fireObservability(t, proberunner.ProbeObservabilityAction{
		Provider:     ProviderElasticsearch,
		CredentialID: "cred",
		Index:        "pulse-prod",
	}, "es-api-key")

	if got.path != "/pulse-prod/_doc" {
		t.Fatalf("path = %q, want /pulse-prod/_doc", got.path)
	}
	if auth := got.headers.Get("Authorization"); auth != "ApiKey es-api-key" {
		t.Fatalf("Authorization = %q, want ApiKey auth", auth)
	}

	var document map[string]any
	if err := json.Unmarshal(got.body, &document); err != nil {
		t.Fatalf("body is not a flat document: %v", err)
	}
	if _, found := document["@timestamp"]; !found {
		t.Fatalf("document is missing @timestamp: %v", document)
	}
	if document["rootCause"] != "data/postgres" {
		t.Fatalf("document rootCause = %v, want data/postgres", document["rootCause"])
	}
}

func TestObservabilitySplunkShape(t *testing.T) {
	t.Parallel()

	got := fireObservability(t, proberunner.ProbeObservabilityAction{
		Provider:     ProviderSplunk,
		CredentialID: "cred",
		Index:        "main",
	}, "hec-token")

	if got.path != "/services/collector/event" {
		t.Fatalf("path = %q, want /services/collector/event", got.path)
	}
	if auth := got.headers.Get("Authorization"); auth != "Splunk hec-token" {
		t.Fatalf("Authorization = %q, want Splunk auth", auth)
	}

	var payload map[string]any
	if err := json.Unmarshal(got.body, &payload); err != nil {
		t.Fatalf("body is not a HEC event: %v", err)
	}
	if payload["sourcetype"] != "pulse:incident" {
		t.Fatalf("sourcetype = %v, want pulse:incident", payload["sourcetype"])
	}
	if _, found := payload["event"]; !found {
		t.Fatalf("HEC payload has no event field: %v", payload)
	}
}

func TestObservabilityOTLPShape(t *testing.T) {
	t.Parallel()

	got := fireObservability(t, proberunner.ProbeObservabilityAction{
		Provider:     ProviderOTLP,
		CredentialID: "cred",
	}, "otlp-token")

	if got.path != "/v1/logs" {
		t.Fatalf("path = %q, want /v1/logs", got.path)
	}
	if auth := got.headers.Get("Authorization"); auth != "Bearer otlp-token" {
		t.Fatalf("Authorization = %q, want bearer auth", auth)
	}

	var payload struct {
		ResourceLogs []struct {
			ScopeLogs []struct {
				LogRecords []map[string]any `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := json.Unmarshal(got.body, &payload); err != nil {
		t.Fatalf("body is not OTLP: %v", err)
	}
	if len(payload.ResourceLogs) != 1 ||
		len(payload.ResourceLogs[0].ScopeLogs) != 1 ||
		len(payload.ResourceLogs[0].ScopeLogs[0].LogRecords) != 1 {
		t.Fatalf("OTLP payload shape is wrong: %+v", payload)
	}
}

func TestObservabilityGenericUsesTemplate(t *testing.T) {
	t.Parallel()

	got := fireObservability(t, proberunner.ProbeObservabilityAction{
		Provider:     ProviderGeneric,
		CredentialID: "cred",
		Headers:      map[string]string{"X-Custom": "yes"},
		BodyTemplate: `{"who":"{{.RootCause}}","why":"{{.Trigger}}"}`,
	}, "token")

	if custom := got.headers.Get("X-Custom"); custom != "yes" {
		t.Fatalf("X-Custom = %q, want yes", custom)
	}

	var payload map[string]string
	if err := json.Unmarshal(got.body, &payload); err != nil {
		t.Fatalf("rendered body is not JSON: %v (%s)", err, got.body)
	}
	if payload["who"] != "data/postgres" || payload["why"] != incident.TriggerFailureCorrelation {
		t.Fatalf("rendered body = %v, want the incident fields substituted", payload)
	}
}

func TestNewObservabilityActionRejectsBadConfiguration(t *testing.T) {
	t.Parallel()

	t.Run("generic without template", func(t *testing.T) {
		t.Parallel()
		_, err := NewObservabilityAction("x",
			proberunner.ProbeObservabilityAction{Provider: ProviderGeneric, Endpoint: "http://x"},
			CredentialMap{})
		if err == nil {
			t.Fatal("NewObservabilityAction() error = nil, want a missing-template error")
		}
	})

	t.Run("unparseable template", func(t *testing.T) {
		t.Parallel()
		_, err := NewObservabilityAction("x", proberunner.ProbeObservabilityAction{
			Provider: ProviderGeneric, Endpoint: "http://x", BodyTemplate: "{{ .Unclosed",
		}, CredentialMap{})
		if err == nil {
			t.Fatal("NewObservabilityAction() error = nil, want a template parse error")
		}
	})

	t.Run("unknown provider", func(t *testing.T) {
		t.Parallel()
		action, err := NewObservabilityAction("x",
			proberunner.ProbeObservabilityAction{Provider: "nonesuch", Endpoint: "http://x"},
			CredentialMap{})
		if err != nil {
			t.Fatalf("NewObservabilityAction() error = %v", err)
		}
		if _, err := action.Fire(context.Background(), testIncident()); err == nil {
			t.Fatal("Fire() error = nil, want an unsupported-provider error")
		}
	})
}

func TestObservabilitySurfacesBackendErrors(t *testing.T) {
	t.Parallel()

	server, _ := captureServer(t, http.StatusForbidden, `{"error":"invalid api key"}`)
	action, err := NewObservabilityAction("ship", proberunner.ProbeObservabilityAction{
		Provider: ProviderDatadog, Endpoint: server.URL, CredentialID: "cred",
	}, CredentialMap{"cred": "wrong"})
	if err != nil {
		t.Fatalf("NewObservabilityAction() error = %v", err)
	}

	_, err = action.Fire(context.Background(), testIncident())
	if err == nil {
		t.Fatal("Fire() error = nil, want a backend error")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("Fire() error = %v, want it to carry the status code", err)
	}
}

// Drift fires while the check is still passing, so it is not an outage.
func TestObservabilityDatadogUsesWarningForPassingChecks(t *testing.T) {
	t.Parallel()

	server, got := captureServer(t, http.StatusOK, `{}`)
	action, err := NewObservabilityAction("ship", proberunner.ProbeObservabilityAction{
		Provider: ProviderDatadog, Endpoint: server.URL, CredentialID: "cred",
	}, CredentialMap{"cred": "key"})
	if err != nil {
		t.Fatalf("NewObservabilityAction() error = %v", err)
	}

	drift := testIncident()
	drift.Trigger = incident.TriggerBodyDrift
	if _, err := action.Fire(context.Background(), drift); err != nil {
		t.Fatalf("Fire() error = %v", err)
	}

	var payload []map[string]any
	if err := json.Unmarshal(got.body, &payload); err != nil {
		t.Fatalf("body is not a Datadog log array: %v", err)
	}
	if payload[0]["status"] != "warning" {
		t.Fatalf("status = %v, want warning for a still-passing check", payload[0]["status"])
	}
}
