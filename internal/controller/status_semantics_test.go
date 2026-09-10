package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	canaryv1alpha1 "github.com/bryanbarton525/pulse/api/v1alpha1"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

func TestIntelligenceTimestampCadence(t *testing.T) {
	t.Parallel()
	base := metav1.NewTime(time.Unix(1_000, 0))
	status := func(at time.Time) *canaryv1alpha1.CanaryIntelligenceStatus {
		value := metav1.NewTime(at)
		return &canaryv1alpha1.CanaryIntelligenceStatus{IncidentID: "incident", Trigger: "bodyDrift", LastSignalTime: &value}
	}
	for _, testCase := range []struct {
		name string
		next *canaryv1alpha1.CanaryIntelligenceStatus
		want bool
	}{
		{name: "below boundary", next: status(base.Add(59 * time.Second)), want: false},
		{name: "at boundary", next: status(base.Add(time.Minute)), want: true},
		{name: "above boundary", next: status(base.Add(61 * time.Second)), want: true},
		{name: "material change", next: &canaryv1alpha1.CanaryIntelligenceStatus{IncidentID: "incident", Trigger: "latencyShift", LastSignalTime: &base}, want: true},
		{name: "closure", next: nil, want: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := intelligenceStatusNeedsUpdate(status(base.Time), testCase.next); got != testCase.want {
				t.Fatalf("intelligenceStatusNeedsUpdate() = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestUnreferencedPolicyClearsInferredDependencies(t *testing.T) {
	t.Parallel()
	policy := &canaryv1alpha1.AnomalyPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "pulse-system", Name: "unused"},
		Status:     canaryv1alpha1.AnomalyPolicyStatus{InferredDependencies: []canaryv1alpha1.InferredDependency{{From: "a", To: "b"}}},
	}
	scheme := testScheme(t)
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(policy).WithObjects(policy).Build()
	reconciler := CanaryReconciler{Client: client}
	reconciler.syncPolicyModelStatus(context.Background(), []canaryv1alpha1.AnomalyPolicy{*policy}, nil)
	var current canaryv1alpha1.AnomalyPolicy
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}, &current); err != nil {
		t.Fatal(err)
	}
	if len(current.Status.InferredDependencies) != 0 {
		t.Fatalf("inferred dependencies = %v, want cleared", current.Status.InferredDependencies)
	}
}

func TestEmptyEngineResultsFallBackToHealthyRunner(t *testing.T) {
	t.Parallel()
	token := "secret"
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("engine authorization = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode([]proberunner.ProbeResult{})
	}))
	defer engine.Close()
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("runner authorization = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode([]proberunner.ProbeResult{{Name: "default/healthy", Healthy: true}})
	}))
	defer runner.Close()

	syncer := StatusSyncer{IncidentEngineURL: engine.URL, ProbeRunnerURL: runner.URL}
	results, err := syncer.fetchResultsWithToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Healthy {
		t.Fatalf("results = %#v, want healthy runner fallback", results)
	}
}
