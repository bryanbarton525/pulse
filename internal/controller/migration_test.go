package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// legacyProbeRunnerDeployment is what releases before the StatefulSet created.
// Same name, same selector -- which is precisely why it has to be removed.
func legacyProbeRunnerDeployment(namespace string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ProbeRunnerName,
			Namespace: namespace,
			Labels:    managedLabels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app.kubernetes.io/name": ProbeRunnerName},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app.kubernetes.io/name": ProbeRunnerName},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "probe-runner", Image: "old:v1"}},
				},
			},
		},
	}
}

func migrationReconciler(t *testing.T, objects ...client.Object) *CanaryReconciler {
	t.Helper()

	return &CanaryReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(testScheme(t)).
			WithObjects(objects...).
			Build(),
		Namespace:           "pulse-system",
		ProbeRunnerImage:    "pulse-probe-runner:test",
		IncidentEngineImage: "pulse-incident-engine:test",
	}
}

// Upgrading from a release that used a Deployment must not leave both running.
//
// The orphaned Deployment pod has no POD_NAME or PULSE_PROBE_RUNNER_SHARDS, so
// ShardFromEnvironment gives it shard 0 of 1 and it executes EVERY probe --
// duplicating every check the StatefulSet shards are already running, and
// double-counting their metrics.
func TestUpgradeRemovesTheLegacyProbeRunnerDeployment(t *testing.T) {
	t.Parallel()

	reconciler := migrationReconciler(t, legacyProbeRunnerDeployment("pulse-system"))

	if err := reconciler.ensureProbeRunner(context.Background(), ""); err != nil {
		t.Fatalf("ensureProbeRunner() error = %v", err)
	}

	var deployment appsv1.Deployment
	err := reconciler.Get(context.Background(), types.NamespacedName{
		Namespace: "pulse-system", Name: ProbeRunnerName,
	}, &deployment)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("legacy Deployment still exists (err = %v); both workloads would run", err)
	}

	var statefulSet appsv1.StatefulSet
	if err := reconciler.Get(context.Background(), types.NamespacedName{
		Namespace: "pulse-system", Name: ProbeRunnerName,
	}, &statefulSet); err != nil {
		t.Fatalf("StatefulSet was not created: %v", err)
	}
}

// A Deployment that merely shares the name but is not Pulse's must survive.
func TestMigrationLeavesForeignDeploymentsAlone(t *testing.T) {
	t.Parallel()

	foreign := legacyProbeRunnerDeployment("pulse-system")
	foreign.Labels = map[string]string{"app.kubernetes.io/name": "something-else"}

	reconciler := migrationReconciler(t, foreign)

	if err := reconciler.ensureProbeRunner(context.Background(), ""); err != nil {
		t.Fatalf("ensureProbeRunner() error = %v", err)
	}

	var deployment appsv1.Deployment
	if err := reconciler.Get(context.Background(), types.NamespacedName{
		Namespace: "pulse-system", Name: ProbeRunnerName,
	}, &deployment); err != nil {
		t.Fatalf("an unmanaged Deployment was deleted: %v", err)
	}
}

func TestMigrationIsANoOpWhenNoLegacyDeploymentExists(t *testing.T) {
	t.Parallel()

	reconciler := migrationReconciler(t)

	if err := reconciler.ensureProbeRunner(context.Background(), ""); err != nil {
		t.Fatalf("ensureProbeRunner() error = %v", err)
	}

	var statefulSet appsv1.StatefulSet
	if err := reconciler.Get(context.Background(), types.NamespacedName{
		Namespace: "pulse-system", Name: ProbeRunnerName,
	}, &statefulSet); err != nil {
		t.Fatalf("StatefulSet was not created: %v", err)
	}
}

// forbidDeletes simulates a manager bound to a stale ClusterRole without the
// delete verb -- the exact condition that took reconciliation down.
func forbidDeletes() interceptor.Funcs {
	return interceptor.Funcs{
		Delete: func(
			_ context.Context, _ client.WithWatch, object client.Object, _ ...client.DeleteOption,
		) error {
			return apierrors.NewForbidden(
				schema.GroupResource{Group: "apps", Resource: "deployments"},
				object.GetName(),
				errForbidden{},
			)
		},
	}
}

type errForbidden struct{}

func (errForbidden) Error() string { return "not permitted" }

// Cleanup must never be able to block the probe runner.
//
// removeIncidentEngine runs BEFORE the probe runner is reconciled. When it
// returned an error on Forbidden, Reconcile aborted and no StatefulSet was ever
// created -- on every cluster that had not enabled intelligence, which is the
// default path.
func TestForbiddenCleanupDoesNotBlockTheProbeRunner(t *testing.T) {
	t.Parallel()

	reconciler := &CanaryReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(testScheme(t)).
			WithObjects(legacyProbeRunnerDeployment("pulse-system")).
			WithInterceptorFuncs(forbidDeletes()).
			Build(),
		Namespace:           "pulse-system",
		ProbeRunnerImage:    "pulse-probe-runner:test",
		IncidentEngineImage: "pulse-incident-engine:test",
	}

	ctx := context.Background()

	// Neither of these may return an error just because a delete was refused.
	if err := reconciler.ensureIncidentEngine(ctx, false); err != nil {
		t.Fatalf("ensureIncidentEngine() error = %v; cleanup must not fail reconcile", err)
	}
	if err := reconciler.ensureProbeRunner(ctx, ""); err != nil {
		t.Fatalf("ensureProbeRunner() error = %v; a refused migration must not fail reconcile", err)
	}

	// The StatefulSet is the thing that actually matters, and it must exist.
	var statefulSet appsv1.StatefulSet
	if err := reconciler.Get(ctx, types.NamespacedName{
		Namespace: "pulse-system", Name: ProbeRunnerName,
	}, &statefulSet); err != nil {
		t.Fatalf("StatefulSet was not created despite a refused delete: %v", err)
	}
}
