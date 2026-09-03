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

package controller

import (
	"context"
	"fmt"
	"maps"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	canaryv1alpha1 "github.com/bryanbarton525/pulse/api/v1alpha1"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// intelligenceResolver flattens AnomalyPolicy CRs into the probe runner's wire
// format, resolving Secret references into credential IDs along the way.
//
// It memoizes per policy rather than per canary. With thousands of canaries
// sharing a handful of policies, resolving per canary would re-read the same
// Secrets thousands of times per reconcile and would write the same Slack
// webhook into the auth store once per canary instead of once per policy.
type intelligenceResolver struct {
	reconciler *CanaryReconciler
	policies   map[types.NamespacedName]canaryv1alpha1.AnomalyPolicy
	memo       map[types.NamespacedName]*resolvedPolicy
}

// resolvedPolicy is one policy flattened once, reused by every canary that
// references it.
type resolvedPolicy struct {
	intelligence *proberunner.ProbeIntelligence
	credentials  map[string]string
	err          error
}

func newIntelligenceResolver(
	reconciler *CanaryReconciler,
	policies []canaryv1alpha1.AnomalyPolicy,
) *intelligenceResolver {
	indexed := make(map[types.NamespacedName]canaryv1alpha1.AnomalyPolicy, len(policies))
	for _, policy := range policies {
		indexed[types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}] = policy
	}

	return &intelligenceResolver{
		reconciler: reconciler,
		policies:   indexed,
		memo:       map[types.NamespacedName]*resolvedPolicy{},
	}
}

// resolve returns the flattened intelligence for one canary, plus any
// credentials that must be written into the auth store.
//
// A nil result with a nil error means the canary did not opt in.
func (ir *intelligenceResolver) resolve(
	ctx context.Context,
	canaryNamespace string,
	intelligence *canaryv1alpha1.CanaryIntelligence,
) (*proberunner.ProbeIntelligence, map[string]string, error) {
	if intelligence == nil || !intelligence.Enabled {
		return nil, nil, nil
	}

	namespace := intelligence.PolicyRef.Namespace
	if namespace == "" {
		namespace = canaryNamespace
	}
	key := types.NamespacedName{Namespace: namespace, Name: intelligence.PolicyRef.Name}

	resolved := ir.resolvePolicy(ctx, key)
	if resolved.err != nil {
		return nil, nil, resolved.err
	}

	return applyOverrides(resolved.intelligence, intelligence.Overrides), resolved.credentials, nil
}

// resolvePolicy flattens one policy, memoized for the life of this reconcile.
func (ir *intelligenceResolver) resolvePolicy(
	ctx context.Context,
	key types.NamespacedName,
) *resolvedPolicy {
	if cached, found := ir.memo[key]; found {
		return cached
	}

	policy, found := ir.policies[key]
	if !found {
		result := &resolvedPolicy{err: fmt.Errorf("AnomalyPolicy %s not found", key)}
		ir.memo[key] = result
		return result
	}

	intelligence, credentials, err := ir.flatten(ctx, policy)
	result := &resolvedPolicy{intelligence: intelligence, credentials: credentials, err: err}
	ir.memo[key] = result
	return result
}

// flatten converts one AnomalyPolicy into the runner wire format. Decimal
// strings become float64 here so a malformed threshold fails at reconcile time
// with a visible ConfigError, rather than inside a probe goroutine at runtime.
func (ir *intelligenceResolver) flatten(
	ctx context.Context,
	policy canaryv1alpha1.AnomalyPolicy,
) (*proberunner.ProbeIntelligence, map[string]string, error) {
	credentials := map[string]string{}
	intelligence := &proberunner.ProbeIntelligence{
		Policy: fmt.Sprintf("%s/%s", policy.Namespace, policy.Name),
	}

	model, err := ir.flattenModel(ctx, policy, credentials)
	if err != nil {
		return nil, nil, err
	}
	intelligence.Model = model

	triggers, err := flattenTriggers(policy.Spec.Triggers)
	if err != nil {
		return nil, nil, err
	}
	intelligence.Triggers = triggers

	topology, err := flattenTopology(policy.Spec.Topology)
	if err != nil {
		return nil, nil, err
	}
	intelligence.Topology = topology

	if policy.Spec.Incidents != nil {
		intelligence.Incidents = proberunner.ProbeIncidents{
			NotifyOnDownstream: policy.Spec.Incidents.NotifyOnDownstream,
		}
	}

	actions, err := ir.flattenActions(ctx, policy, credentials)
	if err != nil {
		return nil, nil, err
	}
	intelligence.Actions = actions

	intelligence.Throttle = proberunner.ProbeThrottle{CooldownSeconds: 900, MaxPerHour: 4}
	if policy.Spec.Throttle != nil {
		intelligence.Throttle = proberunner.ProbeThrottle{
			CooldownSeconds: policy.Spec.Throttle.CooldownSeconds,
			MaxPerHour:      policy.Spec.Throttle.MaxPerHour,
		}
	}

	return intelligence, credentials, nil
}

func (ir *intelligenceResolver) flattenModel(
	ctx context.Context,
	policy canaryv1alpha1.AnomalyPolicy,
	credentials map[string]string,
) (proberunner.ProbeModelConfig, error) {
	// Defaults: the models baked into the Pulse images.
	model := proberunner.ProbeModelConfig{
		Hot: proberunner.ProbeHotModel{
			Backend:           canaryv1alpha1.EmbeddingBackendPotion,
			ModelPath:         proberunner.DefaultHotModelPath,
			VocabPath:         proberunner.DefaultHotVocabPath,
			MaxSequenceLength: 256,
		},
		Cold: proberunner.ProbeColdModel{
			Backend: canaryv1alpha1.EmbeddingBackendONNX,
			ONNX: proberunner.ProbeONNXModel{
				ModelPath:         proberunner.DefaultColdModelPath,
				VocabPath:         proberunner.DefaultColdVocabPath,
				MaxSequenceLength: 256,
			},
		},
	}

	if policy.Spec.Model == nil {
		return model, nil
	}

	if hot := policy.Spec.Model.Hot; hot != nil {
		if hot.Backend != "" {
			model.Hot.Backend = hot.Backend
		}
		if hot.ModelPath != "" {
			model.Hot.ModelPath = hot.ModelPath
		}
		if hot.VocabPath != "" {
			model.Hot.VocabPath = hot.VocabPath
		}
		if hot.MaxSequenceLength > 0 {
			model.Hot.MaxSequenceLength = hot.MaxSequenceLength
		}
	}

	cold := policy.Spec.Model.Cold
	if cold == nil {
		return model, nil
	}

	if cold.Backend != "" {
		model.Cold.Backend = cold.Backend
	}
	if cold.ONNX != nil {
		if cold.ONNX.ModelPath != "" {
			model.Cold.ONNX.ModelPath = cold.ONNX.ModelPath
		}
		if cold.ONNX.VocabPath != "" {
			model.Cold.ONNX.VocabPath = cold.ONNX.VocabPath
		}
		if cold.ONNX.MaxSequenceLength > 0 {
			model.Cold.ONNX.MaxSequenceLength = cold.ONNX.MaxSequenceLength
		}
	}

	if cold.HTTP != nil {
		model.Cold.HTTP = proberunner.ProbeHTTPEmbedModel{
			Endpoint: cold.HTTP.Endpoint,
			Model:    cold.HTTP.Model,
		}
		if cold.HTTP.APIKeySecretRef != nil {
			id, err := ir.storeCredential(ctx, policy, "model-cold-apikey", *cold.HTTP.APIKeySecretRef, credentials)
			if err != nil {
				return model, fmt.Errorf("resolving cold model API key: %w", err)
			}
			model.Cold.HTTP.APIKeyCredentialID = id
		}
	}

	if model.Cold.Backend == canaryv1alpha1.EmbeddingBackendHTTP && model.Cold.HTTP.Endpoint == "" {
		return model, fmt.Errorf("cold model backend %q requires model.cold.http.endpoint", model.Cold.Backend)
	}

	return model, nil
}

func flattenTriggers(triggers *canaryv1alpha1.AnomalyTriggers) (proberunner.ProbeTriggers, error) {
	var flattened proberunner.ProbeTriggers
	if triggers == nil {
		return flattened, nil
	}

	if drift := triggers.BodyDrift; drift != nil && drift.Enabled {
		threshold, err := parseDecimal(drift.Threshold, 0.15, "triggers.bodyDrift.threshold")
		if err != nil {
			return flattened, err
		}
		flattened.BodyDrift = &proberunner.ProbeBodyDriftTrigger{
			Threshold:           threshold,
			WarmupChecks:        defaultInt(drift.WarmupChecks, 20),
			ConsecutiveBreaches: defaultInt(drift.ConsecutiveBreaches, 2),
			MaxBodyBytes:        defaultInt(drift.MaxBodyBytes, 4096),
			SampleEvery:         defaultInt(drift.SampleEvery, 1),
			Redact:              append([]string(nil), drift.Redact...),
		}
	}

	if latency := triggers.LatencyShift; latency != nil && latency.Enabled {
		threshold, err := parseDecimal(latency.ZScoreThreshold, 3.0, "triggers.latencyShift.zScoreThreshold")
		if err != nil {
			return flattened, err
		}
		flattened.LatencyShift = &proberunner.ProbeLatencyShiftTrigger{
			ZScoreThreshold:     threshold,
			WarmupChecks:        defaultInt(latency.WarmupChecks, 30),
			ConsecutiveBreaches: defaultInt(latency.ConsecutiveBreaches, 3),
		}
	}

	if correlation := triggers.FailureCorrelation; correlation != nil && correlation.Enabled {
		similarity, err := parseDecimal(
			correlation.SimilarityThreshold, 0.85, "triggers.failureCorrelation.similarityThreshold")
		if err != nil {
			return flattened, err
		}

		selector := ""
		if correlation.CandidateSelector != nil {
			parsed, err := metav1.LabelSelectorAsSelector(correlation.CandidateSelector)
			if err != nil {
				return flattened, fmt.Errorf("parsing triggers.failureCorrelation.candidateSelector: %w", err)
			}
			selector = parsed.String()
		}

		flattened.FailureCorrelation = &proberunner.ProbeFailureCorrelationTrigger{
			WindowSeconds:       defaultInt(correlation.WindowSeconds, 120),
			SimilarityThreshold: similarity,
			CandidateSelector:   selector,
		}
	}

	if novelty := triggers.FailureNovelty; novelty != nil && novelty.Enabled {
		threshold, err := parseDecimal(novelty.ClusterThreshold, 0.80, "triggers.failureNovelty.clusterThreshold")
		if err != nil {
			return flattened, err
		}
		flattened.FailureNovelty = &proberunner.ProbeFailureNoveltyTrigger{
			ClusterThreshold:      threshold,
			SettlingPeriodSeconds: defaultInt(novelty.SettlingPeriodSeconds, 300),
		}
	}

	return flattened, nil
}

func flattenTopology(topology *canaryv1alpha1.AnomalyTopology) (proberunner.ProbeTopology, error) {
	var flattened proberunner.ProbeTopology
	if topology == nil {
		return flattened, nil
	}

	confidence, err := parseDecimal(topology.InferMinConfidence, 0.85, "topology.inferMinConfidence")
	if err != nil {
		return flattened, err
	}

	flattened.InferDependencies = topology.InferDependencies
	flattened.InferMinObservations = defaultInt(topology.InferMinObservations, 5)
	flattened.InferMinConfidence = confidence

	for _, dependency := range topology.DependsOn {
		flattened.DependsOn = append(flattened.DependsOn, proberunner.ProbeDependency{
			Canary:   dependency.Canary,
			Upstream: append([]string(nil), dependency.Upstream...),
		})
	}

	return flattened, nil
}

func (ir *intelligenceResolver) flattenActions(
	ctx context.Context,
	policy canaryv1alpha1.AnomalyPolicy,
	credentials map[string]string,
) ([]proberunner.ProbeAction, error) {
	actions := make([]proberunner.ProbeAction, 0, len(policy.Spec.Actions))

	for _, action := range policy.Spec.Actions {
		flattened := proberunner.ProbeAction{Name: action.Name, Type: action.Type}

		switch action.Type {
		case canaryv1alpha1.AnomalyActionMetric:
			// No configuration and no credentials.

		case canaryv1alpha1.AnomalyActionLLM:
			if action.LLM == nil {
				return nil, fmt.Errorf("action %q of type llm requires the llm block", action.Name)
			}
			llm := &proberunner.ProbeLLMAction{
				Endpoint:       action.LLM.Endpoint,
				Model:          action.LLM.Model,
				SystemPrompt:   action.LLM.SystemPrompt,
				ContextChecks:  defaultInt(action.LLM.ContextChecks, 20),
				MaxTokens:      defaultInt(action.LLM.MaxTokens, 1024),
				TimeoutSeconds: defaultInt(action.LLM.TimeoutSeconds, 60),
			}
			if action.LLM.APIKeySecretRef != nil {
				id, err := ir.storeCredential(
					ctx, policy, "action-"+action.Name+"-apikey", *action.LLM.APIKeySecretRef, credentials)
				if err != nil {
					return nil, fmt.Errorf("resolving action %q API key: %w", action.Name, err)
				}
				llm.APIKeyCredentialID = id
			}
			flattened.LLM = llm

		case canaryv1alpha1.AnomalyActionSlack:
			if action.Slack == nil {
				return nil, fmt.Errorf("action %q of type slack requires the slack block", action.Name)
			}
			slack := &proberunner.ProbeSlackAction{
				Channels:             append([]string(nil), action.Slack.Channels...),
				IncludeInvestigation: action.Slack.IncludeInvestigation,
				Template:             action.Slack.Template,
			}
			if action.Slack.WebhookSecretRef != nil {
				id, err := ir.storeCredential(
					ctx, policy, "action-"+action.Name+"-webhook", *action.Slack.WebhookSecretRef, credentials)
				if err != nil {
					return nil, fmt.Errorf("resolving action %q webhook: %w", action.Name, err)
				}
				slack.WebhookCredentialID = id
			}
			if action.Slack.BotTokenSecretRef != nil {
				id, err := ir.storeCredential(
					ctx, policy, "action-"+action.Name+"-bottoken", *action.Slack.BotTokenSecretRef, credentials)
				if err != nil {
					return nil, fmt.Errorf("resolving action %q bot token: %w", action.Name, err)
				}
				slack.BotTokenCredentialID = id
			}
			if slack.WebhookCredentialID == "" && slack.BotTokenCredentialID == "" {
				return nil, fmt.Errorf(
					"action %q requires either slack.webhookSecretRef or slack.botTokenSecretRef", action.Name)
			}
			if len(slack.Channels) > 0 && slack.BotTokenCredentialID == "" {
				return nil, fmt.Errorf(
					"action %q sets slack.channels, which needs slack.botTokenSecretRef", action.Name)
			}
			flattened.Slack = slack

		case canaryv1alpha1.AnomalyActionObservability:
			if action.Observability == nil {
				return nil, fmt.Errorf(
					"action %q of type observability requires the observability block", action.Name)
			}
			sink := &proberunner.ProbeObservabilityAction{
				Provider:     action.Observability.Provider,
				Endpoint:     action.Observability.Endpoint,
				Username:     action.Observability.Username,
				Index:        action.Observability.Index,
				Tags:         maps.Clone(action.Observability.Tags),
				Headers:      maps.Clone(action.Observability.Headers),
				BodyTemplate: action.Observability.BodyTemplate,
			}
			if action.Observability.CredentialSecretRef != nil {
				id, err := ir.storeCredential(
					ctx, policy, "action-"+action.Name+"-credential",
					*action.Observability.CredentialSecretRef, credentials)
				if err != nil {
					return nil, fmt.Errorf("resolving action %q credential: %w", action.Name, err)
				}
				sink.CredentialID = id
			}
			flattened.Observability = sink

		default:
			return nil, fmt.Errorf("action %q has unsupported type %q", action.Name, action.Type)
		}

		actions = append(actions, flattened)
	}

	return actions, nil
}

// storeCredential resolves one Secret reference and records it under an ID
// derived from the POLICY, not the canary. Every canary sharing a policy shares
// the same credential entry, so a Slack webhook is stored once regardless of
// how many thousand canaries reference it.
func (ir *intelligenceResolver) storeCredential(
	ctx context.Context,
	policy canaryv1alpha1.AnomalyPolicy,
	suffix string,
	selector corev1.SecretKeySelector,
	credentials map[string]string,
) (string, error) {
	value, err := ir.reconciler.resolveSecretValue(ctx, policy.Namespace, selector)
	if err != nil {
		return "", err
	}

	id := probeCredentialID(policy.Namespace, policy.Name, suffix)
	credentials[id] = value
	return id, nil
}

// applyOverrides returns a copy of the policy-level intelligence with a single
// canary's threshold overrides applied. The shared value is never mutated —
// it is reused by every other canary on the same policy.
func applyOverrides(
	shared *proberunner.ProbeIntelligence,
	overrides *canaryv1alpha1.TriggerTuning,
) *proberunner.ProbeIntelligence {
	if shared == nil {
		return nil
	}
	if overrides == nil {
		return shared
	}

	tuned := *shared

	if shared.Triggers.BodyDrift != nil {
		drift := *shared.Triggers.BodyDrift
		if value, err := strconv.ParseFloat(overrides.DriftThreshold, 64); err == nil {
			drift.Threshold = value
		}
		if overrides.WarmupChecks > 0 {
			drift.WarmupChecks = overrides.WarmupChecks
		}
		tuned.Triggers.BodyDrift = &drift
	}

	if shared.Triggers.LatencyShift != nil {
		latency := *shared.Triggers.LatencyShift
		if value, err := strconv.ParseFloat(overrides.LatencyZScoreThreshold, 64); err == nil {
			latency.ZScoreThreshold = value
		}
		if overrides.WarmupChecks > 0 {
			latency.WarmupChecks = overrides.WarmupChecks
		}
		tuned.Triggers.LatencyShift = &latency
	}

	return &tuned
}

// parseDecimal converts a CRD decimal string into a float64. An empty string
// means "use the default" — the CRD supplies defaults, but a policy created
// before a field existed will not have one.
func parseDecimal(raw string, fallback float64, field string) (float64, error) {
	if raw == "" {
		return fallback, nil
	}

	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing %s %q: %w", field, raw, err)
	}

	return value, nil
}

func defaultInt(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

// populateProbeIntelligence attaches flattened AnomalyPolicy config to every
// probe whose canary opted in.
//
// Mirrors populateProbeAuth: a canary whose policy is missing or whose Secrets
// cannot be resolved gets a ConfigError rather than failing the whole reconcile,
// so one broken policy cannot take down monitoring for the entire cluster.
func (r *CanaryReconciler) populateProbeIntelligence(
	ctx context.Context,
	httpCanaries []canaryv1alpha1.HttpCanary,
	grpcCanaries []canaryv1alpha1.GrpcCanary,
	policies []canaryv1alpha1.AnomalyPolicy,
	config *proberunner.ProbeConfig,
	authStore *proberunner.AuthStore,
) {
	resolver := newIntelligenceResolver(r, policies)

	if authStore.Values == nil {
		authStore.Values = map[string]string{}
	}

	// buildProbeConfig appends HTTP canaries first, then gRPC canaries, so the
	// probe at index i corresponds to the same-ordered canary in that sequence.
	for index, canary := range httpCanaries {
		r.attachIntelligence(
			ctx, resolver, config, authStore, index, canary.Namespace, canary.Spec.Intelligence)
	}

	offset := len(httpCanaries)
	for index, canary := range grpcCanaries {
		r.attachIntelligence(
			ctx, resolver, config, authStore, offset+index, canary.Namespace, canary.Spec.Intelligence)
	}
}

func (r *CanaryReconciler) attachIntelligence(
	ctx context.Context,
	resolver *intelligenceResolver,
	config *proberunner.ProbeConfig,
	authStore *proberunner.AuthStore,
	index int,
	namespace string,
	intelligence *canaryv1alpha1.CanaryIntelligence,
) {
	if index >= len(config.Probes) {
		return
	}

	resolved, credentials, err := resolver.resolve(ctx, namespace, intelligence)
	if err != nil {
		// Don't clobber an auth error that was already recorded — the first
		// problem is the one worth showing.
		if config.Probes[index].ConfigError == "" {
			config.Probes[index].ConfigError = fmt.Sprintf("Invalid intelligence config: %v", err)
		}
		return
	}

	config.Probes[index].Intelligence = resolved
	maps.Copy(authStore.Values, credentials)
}
