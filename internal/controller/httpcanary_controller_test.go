package controller

import (
	"context"
	"testing"

	canaryv1alpha1 "github.com/bryanbarton525/pulse/api/v1alpha1"
	"github.com/bryanbarton525/pulse/internal/proberunner"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBuildProbeConfigIncludesJourneyFields(t *testing.T) {
	t.Parallel()

	config := buildProbeConfig([]canaryv1alpha1.HttpCanary{
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "sample-ui-login",
			},
			Spec: canaryv1alpha1.HttpCanarySpec{
				URL:     "https://example.com/login",
				Method:  "POST",
				Headers: map[string]string{"Content-Type": "application/json"},
				MCP: &canaryv1alpha1.HttpCanaryMCP{
					ProtocolVersion:        "2025-11-25",
					ClientName:             "pulse",
					ClientVersion:          "0.1.0",
					RequireToolsCapability: true,
					MinToolCount:           1,
					RequiredTools:          []string{"search.docs"},
				},
				Body:           `{"username":"demo"}`,
				Interval:       30,
				ExpectedStatus: 200,
				ContainsText:   "dashboard",
				Outputs: []canaryv1alpha1.HttpCanaryOutput{
					{Type: canaryv1alpha1.HttpCanaryOutputPrometheus},
					{Type: canaryv1alpha1.HttpCanaryOutputStdout},
				},
				Journey: []canaryv1alpha1.HttpCanaryStep{
					{
						Name:           "open-login",
						URL:            "https://example.com/login",
						Method:         "GET",
						ExpectedStatus: 200,
						ContainsText:   "Sign in",
					},
					{
						Name:           "submit-login",
						URL:            "https://example.com/session",
						Method:         "POST",
						Headers:        map[string]string{"Content-Type": "application/json"},
						Body:           `{"username":"demo","password":"secret"}`,
						ExpectedStatus: 200,
						ContainsText:   "Welcome",
					},
				},
			},
		},
	})

	if len(config.Probes) != 1 {
		t.Fatalf("buildProbeConfig() produced %d probes, want 1", len(config.Probes))
	}

	probe := config.Probes[0]
	if probe.Name != "default/sample-ui-login" {
		t.Fatalf("probe name = %q, want %q", probe.Name, "default/sample-ui-login")
	}
	if probe.Method != "POST" {
		t.Fatalf("probe method = %q, want %q", probe.Method, "POST")
	}
	if probe.Body != `{"username":"demo"}` {
		t.Fatalf("probe body = %q", probe.Body)
	}
	if probe.ContainsText != "dashboard" {
		t.Fatalf("probe containsText = %q, want %q", probe.ContainsText, "dashboard")
	}
	if probe.MCP == nil {
		t.Fatal("probe MCP config is nil")
	}
	if probe.MCP.MinToolCount != 1 {
		t.Fatalf("probe MCP minToolCount = %d, want 1", probe.MCP.MinToolCount)
	}
	if len(probe.MCP.RequiredTools) != 1 || probe.MCP.RequiredTools[0] != "search.docs" {
		t.Fatalf("probe MCP requiredTools = %#v", probe.MCP.RequiredTools)
	}
	if got := probe.Headers["Content-Type"]; got != "application/json" {
		t.Fatalf("probe content-type = %q, want %q", got, "application/json")
	}
	if len(probe.Journey) != 2 {
		t.Fatalf("probe journey length = %d, want 2", len(probe.Journey))
	}
	if len(probe.Outputs) != 2 {
		t.Fatalf("probe outputs length = %d, want 2", len(probe.Outputs))
	}
	if probe.Outputs[0].Type != canaryv1alpha1.HttpCanaryOutputPrometheus {
		t.Fatalf("first output type = %q, want %q", probe.Outputs[0].Type, canaryv1alpha1.HttpCanaryOutputPrometheus)
	}
	if probe.Outputs[1].Type != canaryv1alpha1.HttpCanaryOutputStdout {
		t.Fatalf("second output type = %q, want %q", probe.Outputs[1].Type, canaryv1alpha1.HttpCanaryOutputStdout)
	}
	if probe.Journey[1].Method != "POST" {
		t.Fatalf("second journey method = %q, want %q", probe.Journey[1].Method, "POST")
	}
	if probe.Journey[1].ContainsText != "Welcome" {
		t.Fatalf("second journey containsText = %q, want %q", probe.Journey[1].ContainsText, "Welcome")
	}
}

func TestPopulateProbeAuthResolvesBearerCredentials(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1.AddToScheme() error = %v", err)
	}

	reconciler := HttpCanaryReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "mcp-auth", Namespace: "default"},
			Data:       map[string][]byte{"token": []byte("demo-token")},
		}).Build(),
	}

	canary := canaryv1alpha1.HttpCanary{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "sample-mcp"},
		Spec: canaryv1alpha1.HttpCanarySpec{
			URL:      "https://mcp.example.com/mcp",
			Interval: 30,
			Auth: &canaryv1alpha1.HttpCanaryAuth{
				Type: canaryv1alpha1.HttpCanaryAuthTypeBearer,
				Bearer: &canaryv1alpha1.HttpCanaryBearerAuth{
					TokenSecretRef: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "mcp-auth"},
						Key:                  "token",
					},
				},
			},
		},
	}

	config := buildProbeConfig([]canaryv1alpha1.HttpCanary{canary})
	authStore := proberunner.AuthStore{Values: map[string]string{}}
	reconciler.populateProbeAuth(context.Background(), []canaryv1alpha1.HttpCanary{canary}, &config, &authStore)

	if config.Probes[0].Auth == nil {
		t.Fatal("probe auth is nil")
	}
	if config.Probes[0].Auth.TokenCredentialID == "" {
		t.Fatal("probe token credential ID is empty")
	}
	if got := authStore.Values[config.Probes[0].Auth.TokenCredentialID]; got != "demo-token" {
		t.Fatalf("stored token = %q, want %q", got, "demo-token")
	}
	if config.Probes[0].ConfigError != "" {
		t.Fatalf("probe configError = %q, want empty", config.Probes[0].ConfigError)
	}
}

func TestPopulateProbeAuthSetsConfigErrorWhenSecretIsMissing(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1.AddToScheme() error = %v", err)
	}

	reconciler := HttpCanaryReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	canary := canaryv1alpha1.HttpCanary{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "sample-mcp"},
		Spec: canaryv1alpha1.HttpCanarySpec{
			URL:      "https://mcp.example.com/mcp",
			Interval: 30,
			Auth: &canaryv1alpha1.HttpCanaryAuth{
				Type: canaryv1alpha1.HttpCanaryAuthTypeBearer,
				Bearer: &canaryv1alpha1.HttpCanaryBearerAuth{
					TokenSecretRef: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "missing-auth"},
						Key:                  "token",
					},
				},
			},
		},
	}

	config := buildProbeConfig([]canaryv1alpha1.HttpCanary{canary})
	authStore := proberunner.AuthStore{Values: map[string]string{}}
	reconciler.populateProbeAuth(context.Background(), []canaryv1alpha1.HttpCanary{canary}, &config, &authStore)

	if config.Probes[0].ConfigError == "" {
		t.Fatal("probe configError is empty")
	}
	if config.Probes[0].Auth != nil {
		t.Fatal("probe auth is not nil")
	}
}
