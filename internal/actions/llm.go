package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/bryanbarton525/pulse/internal/incident"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// DefaultSystemPrompt is used when a policy does not supply its own.
//
// It asks for a specific, bounded answer. An open-ended "explain this" prompt
// produces prose nobody reads at three in the morning.
const DefaultSystemPrompt = `You are a senior site reliability engineer triaging a live incident.

You are given a correlated group of synthetic monitoring failures, the dependency
relationships between them, and which check Pulse believes is the root cause.

Respond in Markdown, under 400 words, with exactly these sections:
- **Assessment** — one paragraph on what is most likely happening.
- **Evidence** — the specific observations that support it.
- **Next steps** — concrete commands or checks, most useful first.

If the evidence does not support a confident conclusion, say so plainly and list
what to check to narrow it down. Do not invent log lines, metrics, or resource
names that were not provided.`

// InvestigationMaxBytes caps what is stored back on the canary status, which
// the CRD limits to 8KiB.
const InvestigationMaxBytes = 8192

// LLMAction escalates an incident to a language model for root-cause analysis.
//
// One call per INCIDENT, not per canary. That is the entire point of
// correlating first: five canaries failing off one backend produce a single
// investigation carrying the whole blast radius, rather than five separate
// analyses that each see one symptom.
type LLMAction struct {
	name       string
	config     proberunner.ProbeLLMAction
	apiKey     string
	client     *http.Client
	describe   ProbeDescriber
	historyFor HistoryLookup
}

// ProbeDescriber returns the safe, non-secret description of a probe for
// inclusion in a prompt.
type ProbeDescriber func(probe string) (proberunner.Probe, bool)

// HistoryLookup returns recent human-readable check messages for a probe.
type HistoryLookup func(probe string, limit int) []string

// NewLLMAction builds an llm action.
func NewLLMAction(
	name string,
	config proberunner.ProbeLLMAction,
	credentials Credentials,
	describe ProbeDescriber,
	history HistoryLookup,
) *LLMAction {
	return &LLMAction{
		name:       name,
		config:     config,
		apiKey:     credentials.Lookup(config.APIKeyCredentialID),
		client:     defaultClient(time.Duration(config.TimeoutSeconds) * time.Second),
		describe:   describe,
		historyFor: history,
	}
}

// Name implements Action.
func (a *LLMAction) Name() string { return a.name }

// Type implements Action.
func (a *LLMAction) Type() string { return "llm" }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model     string        `json:"model,omitempty"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
	Stream    bool          `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Fire implements Action, returning the model's Markdown analysis.
func (a *LLMAction) Fire(ctx context.Context, current *incident.Incident) (string, error) {
	systemPrompt := a.config.SystemPrompt
	if strings.TrimSpace(systemPrompt) == "" {
		systemPrompt = DefaultSystemPrompt
	}

	payload, err := json.Marshal(chatRequest{
		Model:     a.config.Model,
		MaxTokens: a.config.MaxTokens,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: a.buildPrompt(current)},
		},
	})
	if err != nil {
		return "", fmt.Errorf("encoding chat request: %w", err)
	}

	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, a.config.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("building chat request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if a.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+a.apiKey)
	}

	response, err := a.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("calling chat endpoint: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("reading chat response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("chat endpoint returned %d: %s", response.StatusCode, clip(string(body), 256))
	}

	var decoded chatResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("decoding chat response: %w", err)
	}
	if decoded.Error != nil {
		return "", fmt.Errorf("chat endpoint error: %s", decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("chat endpoint returned no choices")
	}

	return clip(strings.TrimSpace(decoded.Choices[0].Message.Content), InvestigationMaxBytes), nil
}

// buildPrompt assembles the incident into a briefing.
//
// Everything here is either operator-authored configuration or already-redacted
// observation text. Resolved credentials, auth headers, and raw response bodies
// are deliberately excluded: this payload leaves the cluster.
func (a *LLMAction) buildPrompt(current *incident.Incident) string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "# Incident %s\n\n", current.ID)
	fmt.Fprintf(&builder, "- Detected by: %s\n", current.Trigger)
	fmt.Fprintf(&builder, "- Suspected root cause: %s\n", current.RootCause)
	fmt.Fprintf(&builder, "- Affected checks: %d\n", len(current.Members))
	if current.Novel {
		builder.WriteString("- This failure shape has not been seen before.\n")
	} else {
		builder.WriteString("- This failure shape has been seen before.\n")
	}
	builder.WriteString("\n")

	members := append([]incident.Member(nil), current.Members...)
	sort.Slice(members, func(i, j int) bool {
		if members[i].Role != members[j].Role {
			// Root cause first, so the most relevant context leads.
			return members[i].Role == incident.RoleRootCause
		}
		return members[i].Probe < members[j].Probe
	})

	for _, member := range members {
		fmt.Fprintf(&builder, "## %s (%s)\n", member.Probe, member.Role)

		if a.describe != nil {
			if probe, found := a.describe(member.Probe); found {
				fmt.Fprintf(&builder, "- Target: %s %s\n", orDefault(probe.Method, "GET"), probe.URL)
				fmt.Fprintf(&builder, "- Check type: %s\n", probe.Type)
				if probe.ExpectedStatus > 0 {
					fmt.Fprintf(&builder, "- Expected status: %d\n", probe.ExpectedStatus)
				}
				for _, dependency := range probe.Intelligence.DependenciesFor(member.Probe) {
					fmt.Fprintf(&builder, "- Declared upstream: %s\n", dependency)
				}
			}
		}

		signal := member.Signal
		if signal.StatusCode > 0 {
			fmt.Fprintf(&builder, "- Observed status: %d\n", signal.StatusCode)
		}
		if signal.Message != "" {
			fmt.Fprintf(&builder, "- Message: %s\n", signal.Message)
		}
		if signal.DriftScore > 0 {
			fmt.Fprintf(&builder, "- Response body drift score: %.3f\n", signal.DriftScore)
		}
		if signal.LatencyZScore > 0 {
			fmt.Fprintf(&builder, "- Latency z-score: %.2f\n", signal.LatencyZScore)
		}
		fmt.Fprintf(&builder, "- Observed at: %s\n", signal.At.UTC().Format(time.RFC3339))

		if a.historyFor != nil && a.config.ContextChecks > 0 {
			history := a.historyFor(member.Probe, a.config.ContextChecks)
			if len(history) > 0 {
				builder.WriteString("- Recent checks (oldest first):\n")
				for _, entry := range history {
					fmt.Fprintf(&builder, "  - %s\n", entry)
				}
			}
		}

		builder.WriteString("\n")
	}

	return builder.String()
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "\n\n[truncated]"
}
