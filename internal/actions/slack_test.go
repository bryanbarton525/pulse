package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/bryanbarton525/pulse/internal/incident"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

func TestSlackWebhookPostsMessage(t *testing.T) {
	t.Parallel()

	server, got := captureServer(t, http.StatusOK, "ok")
	action, err := NewSlackAction("notify", proberunner.ProbeSlackAction{
		WebhookCredentialID: "hook",
	}, CredentialMap{"hook": server.URL})
	if err != nil {
		t.Fatalf("NewSlackAction() error = %v", err)
	}

	if _, err := action.Fire(context.Background(), testIncident()); err != nil {
		t.Fatalf("Fire() error = %v", err)
	}

	var payload map[string]string
	if err := json.Unmarshal(got.body, &payload); err != nil {
		t.Fatalf("body is not a webhook payload: %v", err)
	}

	text := payload["text"]
	if !strings.Contains(text, "data/postgres") {
		t.Fatalf("message = %q, want it to name the root cause", text)
	}
	if !strings.Contains(text, "default/payments") {
		t.Fatalf("message = %q, want it to list the affected checks", text)
	}
	if !strings.Contains(text, "2 checks failing together") {
		t.Fatalf("message = %q, want it to say how many checks are involved", text)
	}
}

func TestSlackBotTokenPostsToEachChannel(t *testing.T) {
	t.Parallel()

	var channels []string
	server, _ := captureServer(t, http.StatusOK, `{"ok":true}`)

	// Re-wrap so every request is recorded, not just the last.
	handler := http.NewServeMux()
	handler.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		channels = append(channels, payload["channel"].(string))
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	server.Config.Handler = handler

	action, err := NewSlackAction("notify", proberunner.ProbeSlackAction{
		BotTokenCredentialID: "bot",
		Channels:             []string{"#sre-alerts", "#platform"},
	}, CredentialMap{"bot": "xoxb-token"})
	if err != nil {
		t.Fatalf("NewSlackAction() error = %v", err)
	}
	action.postMessageURL = server.URL

	if _, err := action.Fire(context.Background(), testIncident()); err != nil {
		t.Fatalf("Fire() error = %v", err)
	}

	if len(channels) != 2 || channels[0] != "#sre-alerts" || channels[1] != "#platform" {
		t.Fatalf("posted to %v, want both configured channels", channels)
	}
}

// chat.postMessage answers 200 even when it rejects the message, so the body
// has to be inspected or every failure would look like a success.
func TestSlackDetectsPostMessageFailureBehindHTTP200(t *testing.T) {
	t.Parallel()

	server, _ := captureServer(t, http.StatusOK, `{"ok":false,"error":"channel_not_found"}`)
	action, err := NewSlackAction("notify", proberunner.ProbeSlackAction{
		BotTokenCredentialID: "bot",
		Channels:             []string{"#nope"},
	}, CredentialMap{"bot": "xoxb-token"})
	if err != nil {
		t.Fatalf("NewSlackAction() error = %v", err)
	}
	action.postMessageURL = server.URL

	_, err = action.Fire(context.Background(), testIncident())
	if err == nil {
		t.Fatal("Fire() error = nil, want the Slack-level error to surface")
	}
	if !strings.Contains(err.Error(), "channel_not_found") {
		t.Fatalf("Fire() error = %v, want it to carry Slack's error code", err)
	}
}

func TestSlackIncludesInvestigationWhenRequested(t *testing.T) {
	t.Parallel()

	server, got := captureServer(t, http.StatusOK, "ok")
	action, err := NewSlackAction("notify", proberunner.ProbeSlackAction{
		WebhookCredentialID:  "hook",
		IncludeInvestigation: true,
	}, CredentialMap{"hook": server.URL})
	if err != nil {
		t.Fatalf("NewSlackAction() error = %v", err)
	}

	current := testIncident()
	current.Investigation = "**Assessment** — postgres is refusing connections."

	if _, err := action.Fire(context.Background(), current); err != nil {
		t.Fatalf("Fire() error = %v", err)
	}

	var payload map[string]string
	_ = json.Unmarshal(got.body, &payload)
	if !strings.Contains(payload["text"], "postgres is refusing connections") {
		t.Fatalf("message = %q, want the investigation appended", payload["text"])
	}
}

func TestSlackOmitsInvestigationByDefault(t *testing.T) {
	t.Parallel()

	server, got := captureServer(t, http.StatusOK, "ok")
	action, err := NewSlackAction("notify", proberunner.ProbeSlackAction{
		WebhookCredentialID: "hook",
	}, CredentialMap{"hook": server.URL})
	if err != nil {
		t.Fatalf("NewSlackAction() error = %v", err)
	}

	current := testIncident()
	current.Investigation = "SHOULD NOT APPEAR"

	if _, err := action.Fire(context.Background(), current); err != nil {
		t.Fatalf("Fire() error = %v", err)
	}

	var payload map[string]string
	_ = json.Unmarshal(got.body, &payload)
	if strings.Contains(payload["text"], "SHOULD NOT APPEAR") {
		t.Fatalf("message = %q, want no investigation without includeInvestigation", payload["text"])
	}
}

func TestSlackCustomTemplate(t *testing.T) {
	t.Parallel()

	server, got := captureServer(t, http.StatusOK, "ok")
	action, err := NewSlackAction("notify", proberunner.ProbeSlackAction{
		WebhookCredentialID: "hook",
		Template:            "{{.Trigger}} on {{.RootCause}}",
	}, CredentialMap{"hook": server.URL})
	if err != nil {
		t.Fatalf("NewSlackAction() error = %v", err)
	}

	if _, err := action.Fire(context.Background(), testIncident()); err != nil {
		t.Fatalf("Fire() error = %v", err)
	}

	var payload map[string]string
	_ = json.Unmarshal(got.body, &payload)
	want := incident.TriggerFailureCorrelation + " on data/postgres"
	if payload["text"] != want {
		t.Fatalf("message = %q, want %q", payload["text"], want)
	}
}

// A broken template must fail at construction, not in the middle of an outage.
func TestNewSlackActionRejectsBadTemplate(t *testing.T) {
	t.Parallel()

	_, err := NewSlackAction("notify", proberunner.ProbeSlackAction{
		WebhookCredentialID: "hook",
		Template:            "{{ .Unclosed",
	}, CredentialMap{"hook": "http://example.invalid"})
	if err == nil {
		t.Fatal("NewSlackAction() error = nil, want a template parse error")
	}
}

func TestSlackDriftMessageReadsAsStillPassing(t *testing.T) {
	t.Parallel()

	server, got := captureServer(t, http.StatusOK, "ok")
	action, err := NewSlackAction("notify", proberunner.ProbeSlackAction{
		WebhookCredentialID: "hook",
	}, CredentialMap{"hook": server.URL})
	if err != nil {
		t.Fatalf("NewSlackAction() error = %v", err)
	}

	current := testIncident()
	current.Trigger = incident.TriggerBodyDrift
	current.Members = current.Members[:1]

	if _, err := action.Fire(context.Background(), current); err != nil {
		t.Fatalf("Fire() error = %v", err)
	}

	var payload map[string]string
	_ = json.Unmarshal(got.body, &payload)
	if !strings.Contains(payload["text"], "still passing") {
		t.Fatalf("message = %q, want it to make clear the check is still green", payload["text"])
	}
}
