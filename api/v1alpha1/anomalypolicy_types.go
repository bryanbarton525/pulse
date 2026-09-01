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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Embedding backends.
//
// Pulse uses two tiers. The "hot" tier scores every passing check for body
// drift, so it must be nearly free — it uses static Model2Vec embeddings, which
// are a token lookup plus a mean rather than a transformer forward pass. The
// "cold" tier only runs on failures (three orders of magnitude rarer), so it can
// afford a real transformer where semantic precision actually matters.
const (
	EmbeddingBackendPotion = "potion"
	EmbeddingBackendONNX   = "onnx"
	EmbeddingBackendHTTP   = "http"
)

// Embedding spaces. Vectors from different spaces are NEVER comparable —
// a potion vector and a MiniLM vector have no meaningful cosine distance.
// Every vector carries its space so mixing them fails loudly.
const (
	EmbeddingSpacePotion = "potion"
	EmbeddingSpaceMiniLM = "minilm"
)

// Trigger kinds. None of these claim a deterministic pass/fail is "anomalous" —
// a canary's assertions are hardcoded, so its result cannot be surprising.
// These ask questions the assertions cannot answer.
const (
	// TriggerBodyDrift fires when a check PASSES but its response body's meaning
	// moved away from the learned baseline — an empty result set, a stack trace
	// inside a 200, a maintenance page.
	TriggerBodyDrift = "bodyDrift"

	// TriggerLatencyShift fires when a passing check's duration distribution
	// shifts. Pure statistics; no model involved.
	TriggerLatencyShift = "latencyShift"

	// TriggerFailureCorrelation merges concurrent failures across canaries into a
	// single incident when there is evidence they are related.
	TriggerFailureCorrelation = "failureCorrelation"

	// TriggerFailureNovelty routes never-before-seen failure shapes to expensive
	// investigation and keeps known ones quiet.
	TriggerFailureNovelty = "failureNovelty"
)

// Action types.
const (
	AnomalyActionMetric        = "metric"
	AnomalyActionLLM           = "llm"
	AnomalyActionSlack         = "slack"
	AnomalyActionObservability = "observability"
)

// Observability sink providers.
const (
	ObservabilityProviderDatadog       = "datadog"
	ObservabilityProviderLoki          = "loki"
	ObservabilityProviderElasticsearch = "elasticsearch"
	ObservabilityProviderSplunk        = "splunk"
	ObservabilityProviderOTLP          = "otlp"
	ObservabilityProviderGeneric       = "generic"
)

// Roles a canary can hold within an incident.
const (
	IncidentRoleRootCause  = "rootCause"
	IncidentRoleDownstream = "downstream"
)

// ── Model configuration ───────────────────────────────────────────────────

// AnomalyModelConfig selects the embedding models for both tiers.
// Omit it entirely to use the models baked into the Pulse images.
type AnomalyModelConfig struct {
	// Hot configures the embedder used on the body-drift path, which runs on
	// every passing check.
	// +optional
	Hot *HotModelConfig `json:"hot,omitempty"`

	// Cold configures the embedder used for failure correlation and novelty,
	// which only runs on failures.
	//
	// This setting is CLUSTER-WIDE, not per-policy. Correlation merges failures
	// across canaries governed by different policies -- a service and its
	// database rarely share one -- by comparing their failure vectors, and
	// vectors from different models are not comparable. If policies disagree,
	// the incident engine resolves to one deterministically (first by policy
	// name) and logs the ones it ignored.
	// +optional
	Cold *ColdModelConfig `json:"cold,omitempty"`
}

// HotModelConfig configures the high-throughput static embedder.
type HotModelConfig struct {
	// Backend selects the hot-path implementation.
	// +kubebuilder:validation:Enum=potion
	// +kubebuilder:default=potion
	// +optional
	Backend string `json:"backend,omitempty"`

	// ModelPath is the path INSIDE the probe runner image to the converted
	// static embedding matrix. Override it only when you have baked or mounted
	// your own model.
	// +optional
	ModelPath string `json:"modelPath,omitempty"`

	// VocabPath is the path inside the probe runner image to the tokenizer vocab.
	// +optional
	VocabPath string `json:"vocabPath,omitempty"`

	// MaxSequenceLength caps tokens per document.
	// +kubebuilder:validation:Minimum=16
	// +kubebuilder:default=256
	// +optional
	MaxSequenceLength int `json:"maxSequenceLength,omitempty"`
}

// ColdModelConfig configures the precision embedder used on the failure path.
type ColdModelConfig struct {
	// Backend selects between the in-process ONNX runtime and an external
	// OpenAI-compatible embeddings service.
	// +kubebuilder:validation:Enum=onnx;http
	// +kubebuilder:default=onnx
	// +optional
	Backend string `json:"backend,omitempty"`

	// ONNX configures the in-process transformer. Used when backend is "onnx".
	// +optional
	ONNX *ONNXModelConfig `json:"onnx,omitempty"`

	// HTTP configures an external embeddings endpoint. Used when backend is "http".
	//
	// NOTE: this must be a server running an EMBEDDING model. A generation
	// server (for example SGLang serving a chat model) rejects /v1/embeddings
	// unless it was launched with --is-embedding.
	// +optional
	HTTP *HTTPEmbeddingConfig `json:"http,omitempty"`
}

// ONNXModelConfig points at an ONNX sentence-transformer inside the image.
type ONNXModelConfig struct {
	// +optional
	ModelPath string `json:"modelPath,omitempty"`

	// +optional
	VocabPath string `json:"vocabPath,omitempty"`

	// +kubebuilder:validation:Minimum=16
	// +kubebuilder:default=256
	// +optional
	MaxSequenceLength int `json:"maxSequenceLength,omitempty"`
}

// HTTPEmbeddingConfig points at an OpenAI-compatible /v1/embeddings service.
type HTTPEmbeddingConfig struct {
	// Endpoint is the full embeddings URL.
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`

	// Model is the model name passed in the request body.
	// +optional
	Model string `json:"model,omitempty"`

	// APIKeySecretRef supplies a bearer token for the endpoint.
	// +optional
	APIKeySecretRef *corev1.SecretKeySelector `json:"apiKeySecretRef,omitempty"`
}

// ── Triggers ──────────────────────────────────────────────────────────────

// AnomalyTriggers selects which evaluations run. Omitting a block disables
// that trigger; including an empty block enables it with defaults.
type AnomalyTriggers struct {
	// +optional
	BodyDrift *BodyDriftTrigger `json:"bodyDrift,omitempty"`

	// +optional
	LatencyShift *LatencyShiftTrigger `json:"latencyShift,omitempty"`

	// +optional
	FailureCorrelation *FailureCorrelationTrigger `json:"failureCorrelation,omitempty"`

	// +optional
	FailureNovelty *FailureNoveltyTrigger `json:"failureNovelty,omitempty"`
}

// BodyDriftTrigger detects a passing check whose response body changed meaning.
//
// This is the only trigger that needs a learned per-probe baseline, and
// therefore the only one with a warmup period. Baselines live in memory and are
// never persisted — nothing derived from a response body leaves the probe
// runner pod.
type BodyDriftTrigger struct {
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Threshold is the cosine distance from the baseline above which a body is
	// considered drifted. Expressed as a decimal string to keep floats out of
	// the CRD schema.
	//
	// The default is derived from measurements against the bundled model, on a
	// JSON payload whose item count, field values, and timestamps all vary
	// between checks:
	//
	//	healthy variation   median 0.015, p95 0.052, max 0.063
	//	empty result set    0.200
	//	truncated JSON      0.204
	//	null collection     0.286
	//	stack trace in 200  0.473
	//	maintenance page    0.591
	//
	// 0.15 sits roughly 2.4x above the healthy maximum and below the weakest
	// real failure. Raise it if your payloads vary more than the above; lower
	// it only with ConsecutiveBreaches raised to match.
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +kubebuilder:default="0.15"
	// +optional
	Threshold string `json:"threshold,omitempty"`

	// WarmupChecks is how many samples must be observed before scoring is
	// trusted. The runner collects these in a fast burst on startup rather than
	// waiting on the probe interval.
	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:default=20
	// +optional
	WarmupChecks int `json:"warmupChecks,omitempty"`

	// ConsecutiveBreaches debounces the signal — how many drifted checks in a
	// row before a signal is raised.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=2
	// +optional
	ConsecutiveBreaches int `json:"consecutiveBreaches,omitempty"`

	// MaxBodyBytes truncates the response body before normalization.
	// +kubebuilder:validation:Minimum=256
	// +kubebuilder:default=4096
	// +optional
	MaxBodyBytes int `json:"maxBodyBytes,omitempty"`

	// SampleEvery embeds only every Nth passing check. Raise it if the hot path
	// ever becomes CPU-bound; drift moves over hours, so coarse sampling is
	// usually harmless.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +optional
	SampleEvery int `json:"sampleEvery,omitempty"`

	// Redact is a list of regular expressions stripped from the body BEFORE it
	// is embedded or logged. Use it for anything the model should never see.
	// +optional
	Redact []string `json:"redact,omitempty"`
}

// LatencyShiftTrigger detects a passing check that is getting slower.
// No model is involved — this is an EWMA mean and variance over check duration.
type LatencyShiftTrigger struct {
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// ZScoreThreshold is how many standard deviations above the rolling mean
	// counts as a shift.
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +kubebuilder:default="3.0"
	// +optional
	ZScoreThreshold string `json:"zScoreThreshold,omitempty"`

	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:default=30
	// +optional
	WarmupChecks int `json:"warmupChecks,omitempty"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=3
	// +optional
	ConsecutiveBreaches int `json:"consecutiveBreaches,omitempty"`
}

// FailureCorrelationTrigger merges concurrent failures into a single incident.
//
// The candidate set is every concurrent failure in the cluster, regardless of
// which policy or namespace each canary belongs to — a service and its database
// will rarely share a policy, and they are exactly the pair worth correlating.
// A merge requires EVIDENCE: a declared dependency edge, a promoted inferred
// edge, or failure text that is semantically near-identical. Two unrelated
// things failing at the same moment stay two incidents.
type FailureCorrelationTrigger struct {
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// WindowSeconds is how far apart two failures can be and still correlate.
	// +kubebuilder:validation:Minimum=5
	// +kubebuilder:default=120
	// +optional
	WindowSeconds int `json:"windowSeconds,omitempty"`

	// SimilarityThreshold is the cosine similarity between two failure
	// observations above which they count as evidence of a shared cause.
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +kubebuilder:default="0.85"
	// +optional
	SimilarityThreshold string `json:"similarityThreshold,omitempty"`

	// CandidateSelector optionally narrows which canaries may correlate with
	// each other. This is a guardrail for hard boundaries (never mix dev with
	// prod), not the primary mechanism — evidence gating is.
	// +optional
	CandidateSelector *metav1.LabelSelector `json:"candidateSelector,omitempty"`
}

// FailureNoveltyTrigger decides whether a failure shape has been seen before.
// This is a routing function, not detection: new shapes earn an investigation,
// the four hundredth repeat of a known one just increments a counter.
type FailureNoveltyTrigger struct {
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// ClusterThreshold is the cosine similarity above which a failure joins an
	// existing cluster instead of forming a new one.
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +kubebuilder:default="0.80"
	// +optional
	ClusterThreshold string `json:"clusterThreshold,omitempty"`

	// SettlingPeriodSeconds suppresses novelty escalation after the incident
	// engine starts. The cluster set is in-memory, so immediately after a
	// restart every failure looks new.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=300
	// +optional
	SettlingPeriodSeconds int `json:"settlingPeriodSeconds,omitempty"`
}

// ── Topology ──────────────────────────────────────────────────────────────

// AnomalyTopology describes how canaries depend on each other, which is what
// lets correlation name a root cause rather than just a list of victims.
type AnomalyTopology struct {
	// DependsOn declares upstream edges. Entries may reference canaries governed
	// by a different AnomalyPolicy — topology cuts across ownership.
	// +optional
	DependsOn []CanaryDependency `json:"dependsOn,omitempty"`

	// InferDependencies enables co-occurrence learning over failure onsets.
	// Learned edges are PROPOSED in status only; they never affect correlation
	// until a human promotes one into DependsOn.
	// +optional
	InferDependencies bool `json:"inferDependencies,omitempty"`

	// InferMinObservations is how many times a co-occurrence must be seen
	// before it is proposed.
	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:default=5
	// +optional
	InferMinObservations int `json:"inferMinObservations,omitempty"`

	// InferMinConfidence is the minimum confidence for a proposed edge.
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +kubebuilder:default="0.85"
	// +optional
	InferMinConfidence string `json:"inferMinConfidence,omitempty"`
}

// CanaryDependency declares that one canary depends on others.
// Names are "namespace/name", matching the probe key format.
type CanaryDependency struct {
	// +kubebuilder:validation:MinLength=1
	Canary string `json:"canary"`

	// +optional
	Upstream []string `json:"upstream,omitempty"`
}

// AnomalyIncidents controls how incident membership affects notification.
type AnomalyIncidents struct {
	// NotifyOnDownstream sends a short "suppressed, part of incident X" note
	// when a canary under this policy is a downstream victim rather than the
	// root cause. Off by default — collapsing the storm is the whole point.
	// +optional
	NotifyOnDownstream bool `json:"notifyOnDownstream,omitempty"`
}

// ── Actions ───────────────────────────────────────────────────────────────

// AnomalyAction is one thing to do when an incident opens.
// Actions fire in declared order, so placing an llm action before a slack
// action lets the notification carry the investigation.
type AnomalyAction struct {
	// Name identifies the action in metrics and logs.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// +kubebuilder:validation:Enum=metric;llm;slack;observability
	Type string `json:"type"`

	// +optional
	LLM *LLMAction `json:"llm,omitempty"`

	// +optional
	Slack *SlackAction `json:"slack,omitempty"`

	// +optional
	Observability *ObservabilityAction `json:"observability,omitempty"`
}

// LLMAction escalates an incident to a language model for root-cause analysis.
// One call per incident, not per canary.
type LLMAction struct {
	// Endpoint is an OpenAI-compatible chat completions URL.
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`

	// +optional
	Model string `json:"model,omitempty"`

	// +optional
	APIKeySecretRef *corev1.SecretKeySelector `json:"apiKeySecretRef,omitempty"`

	// SystemPrompt overrides the built-in SRE prompt.
	// +optional
	SystemPrompt string `json:"systemPrompt,omitempty"`

	// ContextChecks is how many prior results per member to include.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=20
	// +optional
	ContextChecks int `json:"contextChecks,omitempty"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1024
	// +optional
	MaxTokens int `json:"maxTokens,omitempty"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=60
	// +optional
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
}

// SlackAction posts an incident to Slack, via incoming webhook or bot token.
type SlackAction struct {
	// WebhookSecretRef holds an incoming-webhook URL.
	// +optional
	WebhookSecretRef *corev1.SecretKeySelector `json:"webhookSecretRef,omitempty"`

	// BotTokenSecretRef holds a bot token for chat.postMessage. Required when
	// Channels is set.
	// +optional
	BotTokenSecretRef *corev1.SecretKeySelector `json:"botTokenSecretRef,omitempty"`

	// Channels to post to via chat.postMessage.
	// +optional
	Channels []string `json:"channels,omitempty"`

	// IncludeInvestigation appends the LLM root-cause analysis, when an llm
	// action ran earlier in the list.
	// +optional
	IncludeInvestigation bool `json:"includeInvestigation,omitempty"`

	// Template is an optional Go template over the incident. Empty uses the
	// built-in Block Kit message.
	// +optional
	Template string `json:"template,omitempty"`
}

// ObservabilityAction ships incident data to a logging or metrics backend.
// One HTTP poster, shaped per provider.
type ObservabilityAction struct {
	// +kubebuilder:validation:Enum=datadog;loki;elasticsearch;splunk;otlp;generic
	Provider string `json:"provider"`

	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`

	// CredentialSecretRef holds the API key, token, or password for the
	// provider. Interpretation depends on Provider.
	// +optional
	CredentialSecretRef *corev1.SecretKeySelector `json:"credentialSecretRef,omitempty"`

	// Username is used by providers that authenticate with basic auth
	// (Elasticsearch, Loki behind a gateway).
	// +optional
	Username string `json:"username,omitempty"`

	// Index is the Elasticsearch index or Loki tenant, depending on provider.
	// +optional
	Index string `json:"index,omitempty"`

	// Tags are attached to every emitted record.
	// +optional
	Tags map[string]string `json:"tags,omitempty"`

	// Headers are extra HTTP headers. Only honored for the generic provider.
	// +optional
	Headers map[string]string `json:"headers,omitempty"`

	// BodyTemplate is a Go template over the incident. Only honored for the
	// generic provider.
	// +optional
	BodyTemplate string `json:"bodyTemplate,omitempty"`
}

// AnomalyThrottle bounds how often a given incident can fire a given action,
// so a flapping canary cannot spam a channel or burn GPU time.
type AnomalyThrottle struct {
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=900
	// +optional
	CooldownSeconds int `json:"cooldownSeconds,omitempty"`

	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=4
	// +optional
	MaxPerHour int `json:"maxPerHour,omitempty"`
}

// ── Policy ────────────────────────────────────────────────────────────────

// AnomalyPolicySpec defines the desired state of AnomalyPolicy.
//
// Every block is optional. The smallest useful policy enables one trigger and
// one action; everything else falls back to defaults that work out of the box.
type AnomalyPolicySpec struct {
	// Model selects the embedding models. Omit for the baked-in defaults.
	// +optional
	Model *AnomalyModelConfig `json:"model,omitempty"`

	// Triggers selects which evaluations run.
	// +optional
	Triggers *AnomalyTriggers `json:"triggers,omitempty"`

	// Topology describes dependencies between canaries.
	// +optional
	Topology *AnomalyTopology `json:"topology,omitempty"`

	// Incidents controls notification behavior for incident members.
	// +optional
	Incidents *AnomalyIncidents `json:"incidents,omitempty"`

	// Actions are performed, in order, when an incident opens.
	// +optional
	Actions []AnomalyAction `json:"actions,omitempty"`

	// Throttle bounds action frequency.
	// +optional
	Throttle *AnomalyThrottle `json:"throttle,omitempty"`
}

// InferredDependency is a co-occurrence edge the engine learned but has NOT
// acted on. Promote it by copying it into spec.topology.dependsOn.
type InferredDependency struct {
	// From is the proposed upstream canary ("namespace/name").
	From string `json:"from"`

	// To is the proposed downstream canary ("namespace/name").
	To string `json:"to"`

	// Confidence is the fraction of To's failures preceded by a From failure.
	// +optional
	Confidence string `json:"confidence,omitempty"`

	// Observations is how many times the co-occurrence was seen.
	// +optional
	Observations int `json:"observations,omitempty"`

	// +optional
	LastObserved *metav1.Time `json:"lastObserved,omitempty"`
}

// AnomalyPolicyStatus defines the observed state of AnomalyPolicy.
type AnomalyPolicyStatus struct {
	// conditions represent the current state of the AnomalyPolicy resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ReferencedBy counts canaries currently using this policy.
	// +optional
	ReferencedBy int `json:"referencedBy,omitempty"`

	// InferredDependencies are proposed edges awaiting human review. They do not
	// affect correlation until promoted into spec.topology.dependsOn.
	// +optional
	InferredDependencies []InferredDependency `json:"inferredDependencies,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Referenced",type=integer,JSONPath=`.status.referencedBy`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AnomalyPolicy is the Schema for the anomalypolicies API.
//
// It bundles the model, triggers, topology, and actions that a set of canaries
// share. Canaries opt in with spec.intelligence.policyRef; canaries without one
// behave exactly as they did before this feature existed.
//
// NOTE ON CROSS-POLICY INCIDENTS: an incident can span canaries governed by
// different policies, because topology cuts across ownership. When that
// happens, the effective correlation settings come from the policy of the probe
// whose failure OPENED the incident, and the root cause's policy owns action
// dispatch.
type AnomalyPolicy struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of AnomalyPolicy
	// +required
	Spec AnomalyPolicySpec `json:"spec"`

	// status defines the observed state of AnomalyPolicy
	// +optional
	Status AnomalyPolicyStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AnomalyPolicyList contains a list of AnomalyPolicy
type AnomalyPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []AnomalyPolicy `json:"items"`
}

// ── Canary opt-in ─────────────────────────────────────────────────────────

// PolicyReference points at an AnomalyPolicy.
type PolicyReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace defaults to the canary's own namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// TriggerTuning overrides a few policy thresholds for a single canary, so a
// noisy or unusually sensitive endpoint does not require its own policy.
type TriggerTuning struct {
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +optional
	DriftThreshold string `json:"driftThreshold,omitempty"`

	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +optional
	LatencyZScoreThreshold string `json:"latencyZScoreThreshold,omitempty"`

	// +kubebuilder:validation:Minimum=2
	// +optional
	WarmupChecks int `json:"warmupChecks,omitempty"`
}

// CanaryIntelligence opts a canary into model-driven evaluation.
// Shared by HttpCanary and GrpcCanary.
type CanaryIntelligence struct {
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// PolicyRef selects the AnomalyPolicy governing this canary.
	// +kubebuilder:validation:Required
	PolicyRef PolicyReference `json:"policyRef"`

	// Overrides tunes a few thresholds for this canary only.
	// +optional
	Overrides *TriggerTuning `json:"overrides,omitempty"`
}

// CanaryIntelligenceStatus reports what the model concluded about this canary.
type CanaryIntelligenceStatus struct {
	// Policy is the AnomalyPolicy that evaluated this canary ("namespace/name").
	// +optional
	Policy string `json:"policy,omitempty"`

	// IncidentID groups canaries that failed together for the same reason.
	// +optional
	IncidentID string `json:"incidentID,omitempty"`

	// Role is rootCause or downstream.
	// +kubebuilder:validation:Enum=rootCause;downstream
	// +optional
	Role string `json:"role,omitempty"`

	// Trigger names which evaluation produced the signal.
	// +kubebuilder:validation:Enum=bodyDrift;latencyShift;failureCorrelation;failureNovelty
	// +optional
	Trigger string `json:"trigger,omitempty"`

	// Score is the trigger's score, formatted as a decimal string.
	// +optional
	Score string `json:"score,omitempty"`

	// +optional
	LastSignalTime *metav1.Time `json:"lastSignalTime,omitempty"`

	// Investigation is the language model's root-cause analysis, in Markdown.
	// +kubebuilder:validation:MaxLength=8192
	// +optional
	Investigation string `json:"investigation,omitempty"`
}

func init() {
	SchemeBuilder.Register(&AnomalyPolicy{}, &AnomalyPolicyList{})
}
