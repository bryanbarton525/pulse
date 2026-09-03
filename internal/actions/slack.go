package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/bryanbarton525/pulse/internal/incident"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// slackPostMessageURL is used when a policy supplies a bot token and channels
// rather than an incoming webhook.
const slackPostMessageURL = "https://slack.com/api/chat.postMessage"

// SlackAction posts an incident to Slack.
type SlackAction struct {
	name     string
	config   proberunner.ProbeSlackAction
	webhook  string
	botToken string
	client   *http.Client
	template *template.Template

	// postMessageURL is overridable so tests can point at a local server.
	postMessageURL string
}

// NewSlackAction builds a slack action, compiling any custom template up front
// so a broken template surfaces at construction rather than mid-incident.
func NewSlackAction(
	name string,
	config proberunner.ProbeSlackAction,
	credentials Credentials,
) (*SlackAction, error) {
	action := &SlackAction{
		name:           name,
		config:         config,
		webhook:        credentials.Lookup(config.WebhookCredentialID),
		botToken:       credentials.Lookup(config.BotTokenCredentialID),
		client:         defaultClient(15 * time.Second),
		postMessageURL: slackPostMessageURL,
	}

	if strings.TrimSpace(config.Template) != "" {
		compiled, err := template.New("slack").Parse(config.Template)
		if err != nil {
			return nil, fmt.Errorf("parsing slack template for action %q: %w", name, err)
		}
		action.template = compiled
	}

	return action, nil
}

// Name implements Action.
func (a *SlackAction) Name() string { return a.name }

// Type implements Action.
func (a *SlackAction) Type() string { return TypeSlack }

// Fire implements Action.
func (a *SlackAction) Fire(ctx context.Context, current *incident.Incident) (string, error) {
	text, err := a.render(current)
	if err != nil {
		return "", err
	}

	if len(a.config.Channels) > 0 && a.botToken != "" {
		for _, channel := range a.config.Channels {
			if err := a.postMessage(ctx, channel, text); err != nil {
				return "", err
			}
		}
		return text, nil
	}

	if a.webhook == "" {
		return "", fmt.Errorf("slack action %q has no webhook or bot token", a.name)
	}

	return text, a.postWebhook(ctx, text)
}

// render builds the message body. The default is plain Markdown rather than
// rich blocks: it renders identically in a webhook, in chat.postMessage, and in
// an email digest, and it is what an operator can read on a phone.
func (a *SlackAction) render(current *incident.Incident) (string, error) {
	if a.template != nil {
		var rendered bytes.Buffer
		if err := a.template.Execute(&rendered, current); err != nil {
			return "", fmt.Errorf("rendering slack template: %w", err)
		}
		return rendered.String(), nil
	}

	var builder strings.Builder

	// A downstream notice is deliberately terse: it exists so a team is not
	// mystified by their own red check, not to re-page them for someone
	// else's outage.
	if current.DownstreamFor != "" {
		fmt.Fprintf(&builder, "*Pulse: `%s` is affected by another incident*\n", current.DownstreamFor)
		fmt.Fprintf(&builder, "Root cause: `%s` (owned by `%s`)\n", current.RootCause, current.Policy)
		fmt.Fprintf(&builder, "No action needed from you unless `%s` stays red after it recovers.\n",
			current.DownstreamFor)
		fmt.Fprintf(&builder, "\nIncident `%s`", current.ID)
		return builder.String(), nil
	}

	headline := "Pulse incident"
	switch current.Trigger {
	case incident.TriggerBodyDrift:
		headline = "Pulse: response body drifted while the check was still passing"
	case incident.TriggerLatencyShift:
		headline = "Pulse: check is passing but getting slower"
	case incident.TriggerFailureCorrelation:
		if len(current.Members) > 1 {
			headline = fmt.Sprintf("Pulse: %d checks failing together", len(current.Members))
		} else {
			headline = "Pulse: check failing"
		}
	}

	fmt.Fprintf(&builder, "*%s*\n", headline)
	fmt.Fprintf(&builder, "Root cause: `%s`\n", current.RootCause)

	if len(current.Members) > 1 {
		builder.WriteString("Also affected:")
		for _, member := range current.Members {
			if member.Probe == current.RootCause {
				continue
			}
			fmt.Fprintf(&builder, " `%s`", member.Probe)
		}
		builder.WriteString("\n")
	}

	if signal := current.RootCauseSignal(); signal.Message != "" {
		fmt.Fprintf(&builder, "Detail: %s\n", signal.Message)
	}
	if current.Trigger == incident.TriggerBodyDrift {
		fmt.Fprintf(&builder, "Drift score: %.3f\n", current.RootCauseSignal().DriftScore)
	}
	if current.Trigger == incident.TriggerLatencyShift {
		fmt.Fprintf(&builder, "Latency z-score: %.2f\n", current.RootCauseSignal().LatencyZScore)
	}

	if !current.Novel {
		builder.WriteString("_This failure shape has been seen before._\n")
	}

	if a.config.IncludeInvestigation && current.Investigation != "" {
		builder.WriteString("\n")
		builder.WriteString(current.Investigation)
		builder.WriteString("\n")
	}

	fmt.Fprintf(&builder, "\nIncident `%s`", current.ID)

	return builder.String(), nil
}

func (a *SlackAction) postWebhook(ctx context.Context, text string) error {
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return fmt.Errorf("encoding slack payload: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.webhook, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("building slack request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	return a.send(request, false)
}

func (a *SlackAction) postMessage(ctx context.Context, channel, text string) error {
	payload, err := json.Marshal(map[string]any{
		"channel": channel,
		"text":    text,
		"mrkdwn":  true,
	})
	if err != nil {
		return fmt.Errorf("encoding slack payload: %w", err)
	}

	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, a.postMessageURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("building slack request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Authorization", "Bearer "+a.botToken)

	return a.send(request, true)
}

// send posts the request. chat.postMessage returns HTTP 200 even for failures,
// with the real outcome in an "ok" field, so that body must be inspected.
func (a *SlackAction) send(request *http.Request, checkBody bool) error {
	response, err := a.client.Do(request)
	if err != nil {
		return fmt.Errorf("calling slack: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("slack returned %d: %s", response.StatusCode, clip(string(body), 200))
	}

	if !checkBody {
		return nil
	}

	var decoded struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fmt.Errorf("decoding slack response: %w", err)
	}
	if !decoded.OK {
		return fmt.Errorf("slack rejected the message: %s", decoded.Error)
	}

	return nil
}
