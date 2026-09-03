package controller

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	canaryv1alpha1 "github.com/bryanbarton525/pulse/api/v1alpha1"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// testPolicy builds a policy exercising every action type that carries a secret.
func testPolicy() canaryv1alpha1.AnomalyPolicy {
	return canaryv1alpha1.AnomalyPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "pulse-system", Name: "prod-triage"},
		Spec: canaryv1alpha1.AnomalyPolicySpec{
			Triggers: &canaryv1alpha1.AnomalyTriggers{
				BodyDrift: &canaryv1alpha1.BodyDriftTrigger{
					Enabled: true, Threshold: "0.4", WarmupChecks: 15,
				},
				FailureCorrelation: &canaryv1alpha1.FailureCorrelationTrigger{
					Enabled: true, WindowSeconds: 90, SimilarityThreshold: "0.9",
				},
			},
			Actions: []canaryv1alpha1.AnomalyAction{
				{Name: "local", Type: canaryv1alpha1.AnomalyActionMetric},
				{
					Name: "investigate",
					Type: canaryv1alpha1.AnomalyActionLLM,
					LLM: &canaryv1alpha1.LLMAction{
						Endpoint: "http://sglang.ai.svc:8000/v1/chat/completions",
						Model:    "gemma-3-27b-it",
						APIKeySecretRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "sglang"},
							Key:                  "token",
						},
					},
				},
				{
					Name: "notify",
					Type: canaryv1alpha1.AnomalyActionSlack,
					Slack: &canaryv1alpha1.SlackAction{
						WebhookSecretRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "slack"},
							Key:                  "webhook-url",
						},
						IncludeInvestigation: true,
					},
				},
			},
		},
	}
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1.AddToScheme() error = %v", err)
	}
	// The reconciler builds a StatefulSet and Deployments, so the workload
	// types have to be registered for any test that exercises it.
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("appsv1.AddToScheme() error = %v", err)
	}
	if err := canaryv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("canaryv1alpha1.AddToScheme() error = %v", err)
	}
	return scheme
}

func intelligenceReconciler(t *testing.T) CanaryReconciler {
	t.Helper()

	return CanaryReconciler{
		Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "sglang", Namespace: "pulse-system"},
				Data:       map[string][]byte{"token": []byte("sglang-secret-token")},
			},
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "slack", Namespace: "pulse-system"},
				Data:       map[string][]byte{"webhook-url": []byte("https://hooks.slack.test/T/B/XYZ")},
			},
		).Build(),
	}
}

func canaryWithPolicy(name string) canaryv1alpha1.HttpCanary {
	return canaryv1alpha1.HttpCanary{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name, Labels: map[string]string{"tier": "prod"}},
		Spec: canaryv1alpha1.HttpCanarySpec{
			URL:      "https://example.com/health",
			Interval: 30,
			Intelligence: &canaryv1alpha1.CanaryIntelligence{
				Enabled: true,
				PolicyRef: canaryv1alpha1.PolicyReference{
					Name: "prod-triage", Namespace: "pulse-system",
				},
			},
		},
	}
}

func TestPopulateProbeIntelligenceFlattensPolicy(t *testing.T) {
	t.Parallel()

	reconciler := intelligenceReconciler(t)
	canary := canaryWithPolicy("api-health")
	canaries := []canaryv1alpha1.HttpCanary{canary}

	config := buildProbeConfig(canaries, nil)
	authStore := proberunner.AuthStore{Values: map[string]string{}}
	reconciler.populateProbeIntelligence(
		context.Background(), canaries, nil,
		[]canaryv1alpha1.AnomalyPolicy{testPolicy()}, &config, &authStore)

	probe := config.Probes[0]
	if probe.ConfigError != "" {
		t.Fatalf("probe configError = %q, want empty", probe.ConfigError)
	}
	if probe.Intelligence == nil {
		t.Fatal("probe intelligence is nil")
	}
	if got, want := probe.Intelligence.Policy, "pulse-system/prod-triage"; got != want {
		t.Fatalf("policy = %q, want %q", got, want)
	}

	// Decimal strings must be parsed at reconcile time, not at runtime.
	if probe.Intelligence.Triggers.BodyDrift == nil {
		t.Fatal("bodyDrift trigger is nil")
	}
	if got := probe.Intelligence.Triggers.BodyDrift.Threshold; got != 0.4 {
		t.Fatalf("bodyDrift threshold = %v, want 0.4", got)
	}
	if got := probe.Intelligence.Triggers.BodyDrift.WarmupChecks; got != 15 {
		t.Fatalf("bodyDrift warmupChecks = %d, want 15", got)
	}
	// Unset sub-fields fall back to defaults rather than zero.
	if got := probe.Intelligence.Triggers.BodyDrift.MaxBodyBytes; got != 4096 {
		t.Fatalf("bodyDrift maxBodyBytes = %d, want default 4096", got)
	}
	if probe.Intelligence.Triggers.FailureCorrelation.SimilarityThreshold != 0.9 {
		t.Fatalf("similarityThreshold = %v, want 0.9",
			probe.Intelligence.Triggers.FailureCorrelation.SimilarityThreshold)
	}
	// A trigger that was never configured stays disabled.
	if probe.Intelligence.Triggers.FailureNovelty != nil {
		t.Fatal("failureNovelty should be nil when not configured")
	}

	// Baked-in model defaults apply when spec.model is omitted.
	if got := probe.Intelligence.Model.Hot.ModelPath; got != proberunner.DefaultHotModelPath {
		t.Fatalf("hot modelPath = %q, want %q", got, proberunner.DefaultHotModelPath)
	}

	// Labels ride along so the engine can evaluate a candidateSelector.
	if got := probe.Labels["tier"]; got != "prod" {
		t.Fatalf("probe labels[tier] = %q, want %q", got, "prod")
	}
}

// Credentials must reach the runner as IDs pointing into the Secret-backed auth
// store, and the resolved values must never appear in the ConfigMap.
func TestPopulateProbeIntelligenceKeepsSecretsOutOfConfigMap(t *testing.T) {
	t.Parallel()

	reconciler := intelligenceReconciler(t)
	canaries := []canaryv1alpha1.HttpCanary{canaryWithPolicy("api-health")}

	config := buildProbeConfig(canaries, nil)
	authStore := proberunner.AuthStore{Values: map[string]string{}}
	reconciler.populateProbeIntelligence(
		context.Background(), canaries, nil,
		[]canaryv1alpha1.AnomalyPolicy{testPolicy()}, &config, &authStore)

	actions := config.Probes[0].Intelligence.Actions
	if len(actions) != 3 {
		t.Fatalf("action count = %d, want 3", len(actions))
	}

	llm := actions[1].LLM
	if llm == nil || llm.APIKeyCredentialID == "" {
		t.Fatal("llm action has no API key credential ID")
	}
	if got := authStore.Values[llm.APIKeyCredentialID]; got != "sglang-secret-token" {
		t.Fatalf("stored llm token = %q, want %q", got, "sglang-secret-token")
	}

	slack := actions[2].Slack
	if slack == nil || slack.WebhookCredentialID == "" {
		t.Fatal("slack action has no webhook credential ID")
	}

	// The rendered ConfigMap is the real leak surface — assert on its bytes.
	rendered, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}
	for _, secret := range []string{"sglang-secret-token", "https://hooks.slack.test/T/B/XYZ"} {
		if strings.Contains(string(rendered), secret) {
			t.Fatalf("probe config leaked secret value %q", secret)
		}
	}
}

// Thousands of canaries share a handful of policies. A per-canary credential ID
// would store the same webhook once per canary; keying on the policy stores it
// once total.
func TestPopulateProbeIntelligenceSharesCredentialsAcrossCanaries(t *testing.T) {
	t.Parallel()

	reconciler := intelligenceReconciler(t)
	canaries := []canaryv1alpha1.HttpCanary{
		canaryWithPolicy("api-health"),
		canaryWithPolicy("web-health"),
		canaryWithPolicy("worker-health"),
	}

	config := buildProbeConfig(canaries, nil)
	authStore := proberunner.AuthStore{Values: map[string]string{}}
	reconciler.populateProbeIntelligence(
		context.Background(), canaries, nil,
		[]canaryv1alpha1.AnomalyPolicy{testPolicy()}, &config, &authStore)

	// Two secrets in the policy, three canaries referencing it: two entries.
	if got := len(authStore.Values); got != 2 {
		t.Fatalf("auth store entry count = %d, want 2 (one per policy secret)", got)
	}

	first := config.Probes[0].Intelligence.Actions[2].Slack.WebhookCredentialID
	for index := 1; index < len(config.Probes); index++ {
		got := config.Probes[index].Intelligence.Actions[2].Slack.WebhookCredentialID
		if got != first {
			t.Fatalf("probe %d webhook credential ID = %q, want shared %q", index, got, first)
		}
	}
}

func TestPopulateProbeIntelligenceSetsConfigErrorWhenPolicyIsMissing(t *testing.T) {
	t.Parallel()

	reconciler := intelligenceReconciler(t)
	canaries := []canaryv1alpha1.HttpCanary{canaryWithPolicy("api-health")}

	config := buildProbeConfig(canaries, nil)
	authStore := proberunner.AuthStore{Values: map[string]string{}}
	// No policies supplied.
	reconciler.populateProbeIntelligence(context.Background(), canaries, nil, nil, &config, &authStore)

	if config.Probes[0].Intelligence != nil {
		t.Fatal("probe intelligence should be nil when the policy is missing")
	}
	if !strings.Contains(config.Probes[0].ConfigError, "not found") {
		t.Fatalf("configError = %q, want it to mention the missing policy", config.Probes[0].ConfigError)
	}
}

// A canary that never opted in must be completely untouched — this is the
// backward-compatibility guarantee for every existing canary in a cluster.
func TestPopulateProbeIntelligenceSkipsCanariesWithoutOptIn(t *testing.T) {
	t.Parallel()

	reconciler := intelligenceReconciler(t)
	canaries := []canaryv1alpha1.HttpCanary{{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "plain"},
		Spec:       canaryv1alpha1.HttpCanarySpec{URL: "https://example.com", Interval: 30},
	}}

	config := buildProbeConfig(canaries, nil)
	authStore := proberunner.AuthStore{Values: map[string]string{}}
	reconciler.populateProbeIntelligence(
		context.Background(), canaries, nil,
		[]canaryv1alpha1.AnomalyPolicy{testPolicy()}, &config, &authStore)

	if config.Probes[0].Intelligence != nil {
		t.Fatal("probe intelligence should be nil without spec.intelligence")
	}
	if config.Probes[0].ConfigError != "" {
		t.Fatalf("configError = %q, want empty", config.Probes[0].ConfigError)
	}
	if len(authStore.Values) != 0 {
		t.Fatalf("auth store entries = %d, want 0", len(authStore.Values))
	}
}

// gRPC canaries are appended after HTTP canaries; the index offset must line up
// or intelligence would land on the wrong probe.
func TestPopulateProbeIntelligenceMapsGrpcCanariesToTheRightProbe(t *testing.T) {
	t.Parallel()

	reconciler := intelligenceReconciler(t)
	httpCanaries := []canaryv1alpha1.HttpCanary{
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "plain-http"},
			Spec:       canaryv1alpha1.HttpCanarySpec{URL: "https://example.com", Interval: 30},
		},
	}
	grpcCanaries := []canaryv1alpha1.GrpcCanary{{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "grpc-health"},
		Spec: canaryv1alpha1.GrpcCanarySpec{
			URL:      "grpc.example.com:50051",
			Interval: 30,
			Intelligence: &canaryv1alpha1.CanaryIntelligence{
				Enabled:   true,
				PolicyRef: canaryv1alpha1.PolicyReference{Name: "prod-triage", Namespace: "pulse-system"},
			},
		},
	}}

	config := buildProbeConfig(httpCanaries, grpcCanaries)
	authStore := proberunner.AuthStore{Values: map[string]string{}}
	reconciler.populateProbeIntelligence(
		context.Background(), httpCanaries, grpcCanaries,
		[]canaryv1alpha1.AnomalyPolicy{testPolicy()}, &config, &authStore)

	if config.Probes[0].Intelligence != nil {
		t.Fatal("HTTP probe without opt-in should have nil intelligence")
	}
	if config.Probes[1].Intelligence == nil {
		t.Fatal("gRPC probe should have intelligence attached")
	}
	if got := config.Probes[1].Name; got != "default/grpc-health" {
		t.Fatalf("probe[1] name = %q, want default/grpc-health", got)
	}
}

// A per-canary override must not mutate the policy value shared by its siblings.
func TestApplyOverridesDoesNotMutateSharedPolicy(t *testing.T) {
	t.Parallel()

	reconciler := intelligenceReconciler(t)
	tuned := canaryWithPolicy("sensitive")
	tuned.Spec.Intelligence.Overrides = &canaryv1alpha1.TriggerTuning{DriftThreshold: "0.9"}

	canaries := []canaryv1alpha1.HttpCanary{canaryWithPolicy("normal"), tuned}
	config := buildProbeConfig(canaries, nil)
	authStore := proberunner.AuthStore{Values: map[string]string{}}
	reconciler.populateProbeIntelligence(
		context.Background(), canaries, nil,
		[]canaryv1alpha1.AnomalyPolicy{testPolicy()}, &config, &authStore)

	if got := config.Probes[1].Intelligence.Triggers.BodyDrift.Threshold; got != 0.9 {
		t.Fatalf("overridden threshold = %v, want 0.9", got)
	}
	if got := config.Probes[0].Intelligence.Triggers.BodyDrift.Threshold; got != 0.4 {
		t.Fatalf("sibling threshold = %v, want the policy value 0.4", got)
	}
}

func TestFlattenActionsRejectsInvalidConfigurations(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		action canaryv1alpha1.AnomalyAction
		want   string
	}{
		{
			name:   "llm without block",
			action: canaryv1alpha1.AnomalyAction{Name: "x", Type: canaryv1alpha1.AnomalyActionLLM},
			want:   "requires the llm block",
		},
		{
			name: "slack without any credential",
			action: canaryv1alpha1.AnomalyAction{
				Name: "x", Type: canaryv1alpha1.AnomalyActionSlack,
				Slack: &canaryv1alpha1.SlackAction{},
			},
			want: "webhookSecretRef or slack.botTokenSecretRef",
		},
		{
			name: "channels without bot token",
			action: canaryv1alpha1.AnomalyAction{
				Name: "x", Type: canaryv1alpha1.AnomalyActionSlack,
				Slack: &canaryv1alpha1.SlackAction{
					Channels: []string{"#sre"},
					WebhookSecretRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "slack"},
						Key:                  "webhook-url",
					},
				},
			},
			want: "needs slack.botTokenSecretRef",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			reconciler := intelligenceReconciler(t)
			policy := testPolicy()
			policy.Spec.Actions = []canaryv1alpha1.AnomalyAction{testCase.action}

			resolver := newIntelligenceResolver(&reconciler, []canaryv1alpha1.AnomalyPolicy{policy})
			_, _, err := resolver.flatten(context.Background(), policy)
			if err == nil {
				t.Fatal("flatten() error = nil, want a validation error")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("flatten() error = %q, want it to contain %q", err, testCase.want)
			}
		})
	}
}

func TestParseDecimalRejectsGarbage(t *testing.T) {
	t.Parallel()

	if _, err := parseDecimal("not-a-number", 0.35, "field"); err == nil {
		t.Fatal("parseDecimal() error = nil, want a parse error")
	}
	got, err := parseDecimal("", 0.35, "field")
	if err != nil {
		t.Fatalf("parseDecimal() error = %v", err)
	}
	if got != 0.35 {
		t.Fatalf("parseDecimal(\"\") = %v, want the fallback 0.35", got)
	}
}
