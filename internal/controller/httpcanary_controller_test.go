package controller

import (
	"testing"

	canaryv1alpha1 "github.com/bryanbarton525/pulse/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
				URL:            "https://example.com/login",
				Method:         "POST",
				Headers:        map[string]string{"Content-Type": "application/json"},
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
