package proberunner

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ProbeConfig is the top-level structure that gets serialized into a ConfigMap.
// The controller builds this from all HttpCanary CRs, writes it to a ConfigMap,
// and the probe runner reads it from the mounted file.
type ProbeConfig struct {
	Probes []Probe `yaml:"probes"`
}

// AuthStore is the Secret-backed runtime credential map consumed by the probe runner.
type AuthStore struct {
	Values map[string]string `yaml:"values"`
}

const (
	ProbeOutputPrometheus = "prometheus"
	ProbeOutputStdout     = "stdout"
)

// Probe types.
const (
	ProbeTypeHTTP = "http"
	ProbeTypeGRPC = "grpc"
)

// Default model locations inside the Pulse images. Users override these only
// when they have baked or mounted their own weights.
const (
	DefaultHotModelPath  = "/models/potion/model.bin"
	DefaultHotVocabPath  = "/models/potion/vocab.txt"
	DefaultColdModelPath = "/models/minilm/model.onnx"
	DefaultColdVocabPath = "/models/minilm/vocab.txt"
)

// ProbeOutput defines one destination for probe execution telemetry.
type ProbeOutput struct {
	Type string `yaml:"type"`
}

// Probe represents a single HTTP check to execute.
// Each field maps 1:1 to an HttpCanary CR's spec fields.
//
// Name uses the format "namespace/name" so the controller can map results
// back to specific CRs when it queries the /results endpoint.
type Probe struct {
	Name           string            `yaml:"name"`
	Type           string            `yaml:"type"` // "http" or "grpc"
	URL            string            `yaml:"url"`
	Method         string            `yaml:"method,omitempty"`
	Headers        map[string]string `yaml:"headers,omitempty"`
	Auth           *ProbeAuth        `yaml:"auth,omitempty"`
	Body           string            `yaml:"body,omitempty"`
	Interval       int               `yaml:"interval"`
	ExpectedStatus int               `yaml:"expectedStatus"`
	ContainsText   string            `yaml:"containsText,omitempty"`
	MCP            *ProbeMCP         `yaml:"mcp,omitempty"`
	Journey        []ProbeStep       `yaml:"journey,omitempty"`
	Outputs        []ProbeOutput     `yaml:"outputs,omitempty"`
	ConfigError    string            `yaml:"configError,omitempty"`
	GrpcService    string            `yaml:"grpcService,omitempty"`

	// Labels mirrors the canary's metadata labels. The incident engine needs
	// them to evaluate a policy's correlation candidateSelector, which is
	// expressed as a label selector over canaries.
	Labels map[string]string `yaml:"labels,omitempty"`

	// Intelligence is the flattened AnomalyPolicy governing this probe.
	// Nil means the canary did not opt in, and every evaluation is skipped.
	Intelligence *ProbeIntelligence `yaml:"intelligence,omitempty"`
}

// ProbeIntelligence is one AnomalyPolicy, resolved and flattened for a single
// probe.
//
// Two things are already done by the time this reaches the runner:
//
//   - Decimal-string thresholds from the CRD are parsed into float64, so a
//     malformed threshold surfaces as a reconcile-time ConfigError rather than
//     a runtime parse failure in a probe goroutine.
//   - Secret references are resolved to credential IDs pointing into the
//     mounted AuthStore. Credential values never appear in the ConfigMap.
type ProbeIntelligence struct {
	// Policy is the governing AnomalyPolicy as "namespace/name".
	Policy string `yaml:"policy"`

	Model     ProbeModelConfig `yaml:"model"`
	Triggers  ProbeTriggers    `yaml:"triggers"`
	Topology  ProbeTopology    `yaml:"topology,omitempty"`
	Incidents ProbeIncidents   `yaml:"incidents,omitempty"`
	Actions   []ProbeAction    `yaml:"actions,omitempty"`
	Throttle  ProbeThrottle    `yaml:"throttle,omitempty"`
}

// ProbeModelConfig carries both embedding tiers.
type ProbeModelConfig struct {
	Hot  ProbeHotModel  `yaml:"hot"`
	Cold ProbeColdModel `yaml:"cold"`
}

// ProbeHotModel configures the static embedder used on the body-drift path.
type ProbeHotModel struct {
	Backend           string `yaml:"backend"`
	ModelPath         string `yaml:"modelPath,omitempty"`
	VocabPath         string `yaml:"vocabPath,omitempty"`
	MaxSequenceLength int    `yaml:"maxSequenceLength,omitempty"`
}

// ProbeColdModel configures the precision embedder used on the failure path.
type ProbeColdModel struct {
	Backend string              `yaml:"backend"`
	ONNX    ProbeONNXModel      `yaml:"onnx,omitempty"`
	HTTP    ProbeHTTPEmbedModel `yaml:"http,omitempty"`
}

// ProbeONNXModel points at an ONNX sentence-transformer inside the image.
type ProbeONNXModel struct {
	ModelPath         string `yaml:"modelPath,omitempty"`
	VocabPath         string `yaml:"vocabPath,omitempty"`
	MaxSequenceLength int    `yaml:"maxSequenceLength,omitempty"`
}

// ProbeHTTPEmbedModel points at an external OpenAI-compatible embeddings API.
type ProbeHTTPEmbedModel struct {
	Endpoint           string `yaml:"endpoint,omitempty"`
	Model              string `yaml:"model,omitempty"`
	APIKeyCredentialID string `yaml:"apiKeyCredentialID,omitempty"`
}

// ProbeTriggers selects which evaluations run for this probe.
// A nil trigger is disabled.
type ProbeTriggers struct {
	BodyDrift          *ProbeBodyDriftTrigger          `yaml:"bodyDrift,omitempty"`
	LatencyShift       *ProbeLatencyShiftTrigger       `yaml:"latencyShift,omitempty"`
	FailureCorrelation *ProbeFailureCorrelationTrigger `yaml:"failureCorrelation,omitempty"`
	FailureNovelty     *ProbeFailureNoveltyTrigger     `yaml:"failureNovelty,omitempty"`
}

// ProbeBodyDriftTrigger scores the response body of PASSING checks.
type ProbeBodyDriftTrigger struct {
	Threshold           float64  `yaml:"threshold"`
	WarmupChecks        int      `yaml:"warmupChecks"`
	ConsecutiveBreaches int      `yaml:"consecutiveBreaches"`
	MaxBodyBytes        int      `yaml:"maxBodyBytes"`
	SampleEvery         int      `yaml:"sampleEvery"`
	Redact              []string `yaml:"redact,omitempty"`
}

// ProbeLatencyShiftTrigger watches check duration. No model involved.
type ProbeLatencyShiftTrigger struct {
	ZScoreThreshold     float64 `yaml:"zScoreThreshold"`
	WarmupChecks        int     `yaml:"warmupChecks"`
	ConsecutiveBreaches int     `yaml:"consecutiveBreaches"`
}

// ProbeFailureCorrelationTrigger governs how this probe's failures merge with
// other probes' failures into a single incident.
type ProbeFailureCorrelationTrigger struct {
	WindowSeconds       int     `yaml:"windowSeconds"`
	SimilarityThreshold float64 `yaml:"similarityThreshold"`

	// CandidateSelector is a serialized label selector (the string form parsed
	// by k8s.io/apimachinery/pkg/labels). Empty means every probe in the
	// cluster is a correlation candidate, which is the default: evidence
	// gating, not configuration, is what keeps unrelated failures apart.
	CandidateSelector string `yaml:"candidateSelector,omitempty"`
}

// ProbeFailureNoveltyTrigger governs new-failure-shape routing.
type ProbeFailureNoveltyTrigger struct {
	ClusterThreshold      float64 `yaml:"clusterThreshold"`
	SettlingPeriodSeconds int     `yaml:"settlingPeriodSeconds"`
}

// ProbeTopology carries the declared dependency edges from this probe's policy.
// The incident engine unions the topology of every policy into one graph,
// because a service and its database frequently live under different policies.
type ProbeTopology struct {
	DependsOn            []ProbeDependency `yaml:"dependsOn,omitempty"`
	InferDependencies    bool              `yaml:"inferDependencies,omitempty"`
	InferMinObservations int               `yaml:"inferMinObservations,omitempty"`
	InferMinConfidence   float64           `yaml:"inferMinConfidence,omitempty"`
}

// ProbeDependency is one declared edge: Canary depends on Upstream.
type ProbeDependency struct {
	Canary   string   `yaml:"canary"`
	Upstream []string `yaml:"upstream,omitempty"`
}

// ProbeIncidents controls notification for incident membership.
type ProbeIncidents struct {
	NotifyOnDownstream bool `yaml:"notifyOnDownstream,omitempty"`
}

// ProbeAction is one flattened action, with credentials replaced by IDs.
type ProbeAction struct {
	Name          string                    `yaml:"name"`
	Type          string                    `yaml:"type"`
	LLM           *ProbeLLMAction           `yaml:"llm,omitempty"`
	Slack         *ProbeSlackAction         `yaml:"slack,omitempty"`
	Observability *ProbeObservabilityAction `yaml:"observability,omitempty"`
}

// ProbeLLMAction escalates an incident to a language model.
type ProbeLLMAction struct {
	Endpoint           string `yaml:"endpoint"`
	Model              string `yaml:"model,omitempty"`
	APIKeyCredentialID string `yaml:"apiKeyCredentialID,omitempty"`
	SystemPrompt       string `yaml:"systemPrompt,omitempty"`
	ContextChecks      int    `yaml:"contextChecks,omitempty"`
	MaxTokens          int    `yaml:"maxTokens,omitempty"`
	TimeoutSeconds     int    `yaml:"timeoutSeconds,omitempty"`
}

// ProbeSlackAction posts an incident to Slack.
type ProbeSlackAction struct {
	WebhookCredentialID  string   `yaml:"webhookCredentialID,omitempty"`
	BotTokenCredentialID string   `yaml:"botTokenCredentialID,omitempty"`
	Channels             []string `yaml:"channels,omitempty"`
	IncludeInvestigation bool     `yaml:"includeInvestigation,omitempty"`
	Template             string   `yaml:"template,omitempty"`
}

// ProbeObservabilityAction ships incident data to a logging or metrics backend.
type ProbeObservabilityAction struct {
	Provider     string            `yaml:"provider"`
	Endpoint     string            `yaml:"endpoint"`
	CredentialID string            `yaml:"credentialID,omitempty"`
	Username     string            `yaml:"username,omitempty"`
	Index        string            `yaml:"index,omitempty"`
	Tags         map[string]string `yaml:"tags,omitempty"`
	Headers      map[string]string `yaml:"headers,omitempty"`
	BodyTemplate string            `yaml:"bodyTemplate,omitempty"`
}

// ProbeThrottle bounds action frequency per (incident signature, action).
type ProbeThrottle struct {
	CooldownSeconds int `yaml:"cooldownSeconds,omitempty"`
	MaxPerHour      int `yaml:"maxPerHour,omitempty"`
}

// ProbeAuth defines a runtime auth strategy that references mounted credentials.
type ProbeAuth struct {
	Type                 string `yaml:"type"`
	UsernameCredentialID string `yaml:"usernameCredentialID,omitempty"`
	PasswordCredentialID string `yaml:"passwordCredentialID,omitempty"`
	TokenCredentialID    string `yaml:"tokenCredentialID,omitempty"`
	HeaderName           string `yaml:"headerName,omitempty"`
	ValueCredentialID    string `yaml:"valueCredentialID,omitempty"`
}

// ProbeMCP defines a runtime MCP initialize + tools/list probe.
type ProbeMCP struct {
	ProtocolVersion        string   `yaml:"protocolVersion,omitempty"`
	ClientName             string   `yaml:"clientName,omitempty"`
	ClientVersion          string   `yaml:"clientVersion,omitempty"`
	RequireToolsCapability bool     `yaml:"requireToolsCapability,omitempty"`
	MinToolCount           int      `yaml:"minToolCount,omitempty"`
	RequiredTools          []string `yaml:"requiredTools,omitempty"`
}

// ProbeStep represents one HTTP request in a scripted synthetic journey.
type ProbeStep struct {
	Name           string            `yaml:"name"`
	URL            string            `yaml:"url"`
	Method         string            `yaml:"method,omitempty"`
	Headers        map[string]string `yaml:"headers,omitempty"`
	Body           string            `yaml:"body,omitempty"`
	ExpectedStatus int               `yaml:"expectedStatus"`
	ContainsText   string            `yaml:"containsText,omitempty"`
}

// LoadConfigFromFile reads a YAML config file from disk (the mounted ConfigMap)
// and parses it into a ProbeConfig struct.
//
// This is called:
//   - Once at startup
//   - Again whenever the file changes (ConfigMap update triggers a volume remount)
func LoadConfigFromFile(path string) (*ProbeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	var config ProbeConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}

	return &config, nil
}

// LoadAuthStoreFromFile reads the mounted Secret file that contains probe credentials.
func LoadAuthStoreFromFile(path string) (*AuthStore, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading auth file %s: %w", path, err)
	}

	var store AuthStore
	if err := yaml.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("parsing auth file %s: %w", path, err)
	}
	if store.Values == nil {
		store.Values = map[string]string{}
	}

	return &store, nil
}

// DependenciesFor returns the declared upstreams for one canary from this
// policy's topology.
func (p *ProbeIntelligence) DependenciesFor(canary string) []string {
	if p == nil {
		return nil
	}

	for _, dependency := range p.Topology.DependsOn {
		if dependency.Canary == canary {
			return dependency.Upstream
		}
	}
	return nil
}
