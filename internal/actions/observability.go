package actions

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/bryanbarton525/pulse/internal/incident"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// Supported observability providers.
const (
	ProviderDatadog       = "datadog"
	ProviderLoki          = "loki"
	ProviderElasticsearch = "elasticsearch"
	ProviderSplunk        = "splunk"
	ProviderOTLP          = "otlp"
	ProviderGeneric       = "generic"
)

// ObservabilityAction ships an incident to a logging or metrics backend.
//
// This is one HTTP JSON poster with per-provider shaping rather than a separate
// integration each. The providers differ only in three ways — where the request
// goes, how it authenticates, and what the JSON looks like — so the differences
// live in one switch and everything else is shared.
type ObservabilityAction struct {
	name       string
	config     proberunner.ProbeObservabilityAction
	credential string
	client     *http.Client
	template   *template.Template

	// source names Pulse in emitted records.
	source string
}

// NewObservabilityAction builds an observability action.
func NewObservabilityAction(
	name string,
	config proberunner.ProbeObservabilityAction,
	credentials Credentials,
) (*ObservabilityAction, error) {
	action := &ObservabilityAction{
		name:       name,
		config:     config,
		credential: credentials.Lookup(config.CredentialID),
		client:     defaultClient(15 * time.Second),
		source:     "pulse",
	}

	if config.Provider == ProviderGeneric {
		if strings.TrimSpace(config.BodyTemplate) == "" {
			return nil, fmt.Errorf(
				"action %q uses the generic provider, which requires observability.bodyTemplate", name)
		}
		compiled, err := template.New("observability").Parse(config.BodyTemplate)
		if err != nil {
			return nil, fmt.Errorf("parsing bodyTemplate for action %q: %w", name, err)
		}
		action.template = compiled
	}

	return action, nil
}

// Name implements Action.
func (a *ObservabilityAction) Name() string { return a.name }

// Type implements Action.
func (a *ObservabilityAction) Type() string { return TypeObservability }

// Fire implements Action.
func (a *ObservabilityAction) Fire(ctx context.Context, current *incident.Incident) (string, error) {
	url, headers, payload, err := a.shape(current)
	if err != nil {
		return "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("building %s request: %w", a.config.Provider, err)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	response, err := a.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("calling %s: %w", a.config.Provider, err)
	}
	defer func() { _ = response.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("%s returned %d: %s",
			a.config.Provider, response.StatusCode, clip(string(body), 200))
	}

	return "", nil
}

// record is the provider-neutral view of an incident, used to build every
// payload shape below.
type record struct {
	Message   string            `json:"message"`
	Incident  string            `json:"incident"`
	Trigger   string            `json:"trigger"`
	RootCause string            `json:"rootCause"`
	Members   []string          `json:"members"`
	Novel     bool              `json:"novel"`
	Score     float64           `json:"score,omitempty"`
	Policy    string            `json:"policy,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
}

func (a *ObservabilityAction) record(current *incident.Incident) record {
	signal := current.RootCauseSignal()
	score := signal.DriftScore
	if score == 0 {
		score = signal.LatencyZScore
	}

	message := fmt.Sprintf("Pulse %s incident: root cause %s", current.Trigger, current.RootCause)
	if len(current.Members) > 1 {
		message = fmt.Sprintf("%s (%d checks affected)", message, len(current.Members))
	}

	timestamp := current.UpdatedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	return record{
		Message:   message,
		Incident:  current.ID,
		Trigger:   current.Trigger,
		RootCause: current.RootCause,
		Members:   current.ProbeNames(),
		Novel:     current.Novel,
		Score:     score,
		Policy:    current.Policy,
		Tags:      a.config.Tags,
		Timestamp: timestamp,
	}
}

// shape produces the URL, headers, and body for the configured provider.
func (a *ObservabilityAction) shape(
	current *incident.Incident,
) (string, map[string]string, []byte, error) {
	entry := a.record(current)
	endpoint := strings.TrimRight(a.config.Endpoint, "/")
	headers := map[string]string{"Content-Type": "application/json"}

	switch a.config.Provider {
	case ProviderDatadog:
		headers["DD-API-KEY"] = a.credential
		payload, err := json.Marshal([]map[string]any{{
			"ddsource": a.source,
			"service":  "pulse",
			"ddtags":   datadogTags(a.config.Tags, current),
			"message":  entry.Message,
			"status":   datadogStatus(current),
			"pulse":    entry,
		}})
		return endpoint + "/api/v2/logs", headers, payload, err

	case ProviderLoki:
		if a.config.Index != "" {
			// Loki calls this the tenant, supplied as an org header.
			headers["X-Scope-OrgID"] = a.config.Index
		}
		if a.credential != "" {
			headers["Authorization"] = basicAuth(a.config.Username, a.credential)
		}
		line, err := json.Marshal(entry)
		if err != nil {
			return "", nil, nil, err
		}
		payload, err := json.Marshal(map[string]any{
			"streams": []map[string]any{{
				"stream": lokiLabels(a.config.Tags, current),
				"values": [][2]string{{
					fmt.Sprintf("%d", entry.Timestamp.UnixNano()),
					string(line),
				}},
			}},
		})
		return endpoint + "/loki/api/v1/push", headers, payload, err

	case ProviderElasticsearch:
		if a.credential != "" {
			if a.config.Username != "" {
				headers["Authorization"] = basicAuth(a.config.Username, a.credential)
			} else {
				headers["Authorization"] = "ApiKey " + a.credential
			}
		}
		index := a.config.Index
		if index == "" {
			index = "pulse-incidents"
		}
		document := map[string]any{"@timestamp": entry.Timestamp.UTC().Format(time.RFC3339Nano)}
		if err := mergeRecord(document, entry); err != nil {
			return "", nil, nil, err
		}
		payload, err := json.Marshal(document)
		return fmt.Sprintf("%s/%s/_doc", endpoint, index), headers, payload, err

	case ProviderSplunk:
		headers["Authorization"] = "Splunk " + a.credential
		payload, err := json.Marshal(map[string]any{
			"time":       entry.Timestamp.Unix(),
			"host":       a.source,
			"source":     a.source,
			"sourcetype": "pulse:incident",
			"index":      a.config.Index,
			"event":      entry,
		})
		return endpoint + "/services/collector/event", headers, payload, err

	case ProviderOTLP:
		if a.credential != "" {
			headers["Authorization"] = "Bearer " + a.credential
		}
		payload, err := json.Marshal(otlpLogs(entry, a.config.Tags))
		return endpoint + "/v1/logs", headers, payload, err

	case ProviderGeneric:
		maps.Copy(headers, a.config.Headers)
		if a.credential != "" {
			headers["Authorization"] = "Bearer " + a.credential
		}
		var rendered bytes.Buffer
		if err := a.template.Execute(&rendered, current); err != nil {
			return "", nil, nil, fmt.Errorf("rendering bodyTemplate: %w", err)
		}
		return a.config.Endpoint, headers, rendered.Bytes(), nil

	default:
		return "", nil, nil, fmt.Errorf("unsupported observability provider %q", a.config.Provider)
	}
}

// datadogStatus maps an incident onto Datadog's log status levels.
func datadogStatus(current *incident.Incident) string {
	switch current.Trigger {
	case incident.TriggerFailureCorrelation, incident.TriggerFailureNovelty:
		return "error"
	default:
		// Drift and latency fire while the check is still passing, so they are
		// a warning rather than an outage.
		return "warning"
	}
}

func datadogTags(tags map[string]string, current *incident.Incident) string {
	pairs := make([]string, 0, len(tags)+3)
	for key, value := range tags {
		pairs = append(pairs, key+":"+value)
	}
	sort.Strings(pairs)

	pairs = append(pairs,
		"trigger:"+current.Trigger,
		"root_cause:"+strings.ReplaceAll(current.RootCause, "/", "_"),
		"incident:"+current.ID,
	)

	return strings.Join(pairs, ",")
}

func lokiLabels(tags map[string]string, current *incident.Incident) map[string]string {
	labels := map[string]string{
		"job":     "pulse",
		"trigger": current.Trigger,
	}
	maps.Copy(labels, tags)
	// Deliberately NOT labelling by incident ID or probe name: Loki labels are
	// indexed, and a new value per incident would create unbounded streams.
	return labels
}

func otlpLogs(entry record, tags map[string]string) map[string]any {
	attributes := make([]map[string]any, 0, 3+len(tags))
	attributes = append(attributes,
		map[string]any{"key": "pulse.incident", "value": map[string]any{"stringValue": entry.Incident}},
		map[string]any{"key": "pulse.trigger", "value": map[string]any{"stringValue": entry.Trigger}},
		map[string]any{"key": "pulse.root_cause", "value": map[string]any{"stringValue": entry.RootCause}},
	)

	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		attributes = append(attributes, map[string]any{
			"key":   key,
			"value": map[string]any{"stringValue": tags[key]},
		})
	}

	return map[string]any{
		"resourceLogs": []map[string]any{{
			"resource": map[string]any{
				"attributes": []map[string]any{
					{"key": "service.name", "value": map[string]any{"stringValue": "pulse"}},
				},
			},
			"scopeLogs": []map[string]any{{
				"logRecords": []map[string]any{{
					"timeUnixNano":   fmt.Sprintf("%d", entry.Timestamp.UnixNano()),
					"severityText":   "ERROR",
					"severityNumber": 17,
					"body":           map[string]any{"stringValue": entry.Message},
					"attributes":     attributes,
				}},
			}},
		}},
	}
}

func basicAuth(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

// mergeRecord flattens the neutral record into an existing document.
func mergeRecord(document map[string]any, entry record) error {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return err
	}
	maps.Copy(document, fields)

	return nil
}
