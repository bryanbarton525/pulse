package controller

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	canaryv1alpha1 "github.com/bryanbarton525/pulse/api/v1alpha1"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// Resource names used by the controller for the infrastructure it manages.
const (
	ProbeRunnerName = "pulse-probe-runner"
	ProbeConfigName = "pulse-probe-config"
	ProbeConfigFile = "probes.yaml"
	ProbeRunnerPort = 9090
)

// Labels applied to all resources the controller manages.
var managedLabels = map[string]string{
	"app.kubernetes.io/name":       ProbeRunnerName,
	"app.kubernetes.io/managed-by": "pulse-controller",
}

// HttpCanaryReconciler reconciles HttpCanary objects.
//
// This controller is an ORCHESTRATOR, not a worker:
//   - Lists all HttpCanary CRs across all namespaces
//   - Builds a probe config and writes it to a ConfigMap
//   - Ensures a probe runner Deployment + Service exist
//
// Status updates are handled separately by the StatusSyncer (status_syncer.go).
//
// SCALING DESIGN: All HttpCanary events are mapped to a single reconcile key
// (see SetupWithManager). This means even if 1,000 CRs change at once, the
// work queue deduplicates them into ONE reconcile call, not 1,000.
type HttpCanaryReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Namespace is where the controller creates infrastructure resources
	// (ConfigMap, Deployment, Service). This is the operator's own namespace,
	// typically "pulse-system". Set from the POD_NAMESPACE env var.
	Namespace string

	// ProbeRunnerImage is the container image for the probe runner Deployment.
	ProbeRunnerImage string
}

// RBAC markers — controller-gen reads these to generate config/rbac/role.yaml.
//
// +kubebuilder:rbac:groups=canary.iambarton.com,resources=httpcanaries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=canary.iambarton.com,resources=httpcanaries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=canary.iambarton.com,resources=httpcanaries/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch

// Reconcile is called whenever any HttpCanary CR changes.
//
// Because all events are mapped to a single key (see SetupWithManager), this
// function runs AT MOST ONCE per batch of changes — not once per CR.
// It always lists all CRs and rebuilds the full infrastructure state.
func (r *HttpCanaryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// ──────────────────────────────────────────────────────
	// STEP 1: List ALL HttpCanary CRs across all namespaces.
	//
	// We ignore req.NamespacedName entirely. It's always the
	// same fixed "trigger" key (see SetupWithManager). The
	// real source of truth is the full list of CRs.
	// ──────────────────────────────────────────────────────
	var canaryList canaryv1alpha1.HttpCanaryList
	if err := r.List(ctx, &canaryList); err != nil {
		logger.Error(err, "Failed to list HttpCanary resources")
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling probe infrastructure", "httpCanaryCount", len(canaryList.Items))

	// ──────────────────────────────────────────────────────
	// STEP 2: Build the probe config from all CRs.
	// ──────────────────────────────────────────────────────
	config := buildProbeConfig(canaryList.Items)

	configYAML, err := yaml.Marshal(config)
	if err != nil {
		logger.Error(err, "Failed to marshal probe config")
		return ctrl.Result{}, err
	}

	// ──────────────────────────────────────────────────────
	// STEP 3: Ensure ConfigMap with the probe config.
	// ──────────────────────────────────────────────────────
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ProbeConfigName,
			Namespace: r.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, configMap, func() error {
		configMap.Labels = managedLabels
		configMap.Data = map[string]string{
			ProbeConfigFile: string(configYAML),
		}
		return nil
	})
	if err != nil {
		logger.Error(err, "Failed to ensure ConfigMap")
		return ctrl.Result{}, err
	}
	logger.Info("ConfigMap reconciled", "result", result)

	// ──────────────────────────────────────────────────────
	// STEP 4: Ensure the probe runner Deployment.
	// ──────────────────────────────────────────────────────
	if err := r.ensureDeployment(ctx); err != nil {
		logger.Error(err, "Failed to ensure Deployment")
		return ctrl.Result{}, err
	}

	// ──────────────────────────────────────────────────────
	// STEP 5: Ensure the probe runner Service.
	// ──────────────────────────────────────────────────────
	if err := r.ensureService(ctx); err != nil {
		logger.Error(err, "Failed to ensure Service")
		return ctrl.Result{}, err
	}

	// No RequeueAfter — this controller only runs on CR changes.
	// Status polling is handled by the StatusSyncer (status_syncer.go).
	return ctrl.Result{}, nil
}

// buildProbeConfig converts a list of HttpCanary CRs into the probe runner's
// config format. The "namespace/name" key lets the StatusSyncer map results
// back to specific CRs.
func buildProbeConfig(canaries []canaryv1alpha1.HttpCanary) proberunner.ProbeConfig {
	config := proberunner.ProbeConfig{
		Probes: make([]proberunner.Probe, 0, len(canaries)),
	}

	for _, c := range canaries {
		config.Probes = append(config.Probes, proberunner.Probe{
			Name:           fmt.Sprintf("%s/%s", c.Namespace, c.Name),
			URL:            c.Spec.URL,
			Interval:       c.Spec.Interval,
			ExpectedStatus: c.Spec.ExpectedStatus,
		})
	}

	return config
}

// ensureDeployment creates or updates the probe runner Deployment.
func (r *HttpCanaryReconciler) ensureDeployment(ctx context.Context) error {
	logger := log.FromContext(ctx)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ProbeRunnerName,
			Namespace: r.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		deploy.Labels = managedLabels

		deploy.Spec.Replicas = ptr.To(int32(1))
		deploy.Spec.Selector = &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"app.kubernetes.io/name": ProbeRunnerName,
			},
		}

		deploy.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					"app.kubernetes.io/name": ProbeRunnerName,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "probe-runner",
						Image: r.ProbeRunnerImage,
						Args: []string{
							fmt.Sprintf("--config=/etc/pulse/%s", ProbeConfigFile),
							fmt.Sprintf("--listen=:%d", ProbeRunnerPort),
						},
						Ports: []corev1.ContainerPort{
							{
								Name:          "http",
								ContainerPort: ProbeRunnerPort,
								Protocol:      corev1.ProtocolTCP,
							},
						},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/healthz",
									Port: intstr.FromString("http"),
								},
							},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    *parseQuantity("100m"),
								corev1.ResourceMemory: *parseQuantity("64Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    *parseQuantity("200m"),
								corev1.ResourceMemory: *parseQuantity("128Mi"),
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "probe-config",
								MountPath: "/etc/pulse",
								ReadOnly:  true,
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "probe-config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: ProbeConfigName,
								},
							},
						},
					},
				},
			},
		}

		return nil
	})
	if err != nil {
		return err
	}

	logger.Info("Deployment reconciled", "result", result)
	return nil
}

// ensureService creates or updates the probe runner Service.
func (r *HttpCanaryReconciler) ensureService(ctx context.Context) error {
	logger := log.FromContext(ctx)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ProbeRunnerName,
			Namespace: r.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = managedLabels

		svc.Spec.Selector = map[string]string{
			"app.kubernetes.io/name": ProbeRunnerName,
		}

		if svc.Spec.ClusterIP == "" {
			svc.Spec.Type = corev1.ServiceTypeClusterIP
		}

		svc.Spec.Ports = []corev1.ServicePort{
			{
				Name:       "http",
				Port:       ProbeRunnerPort,
				TargetPort: intstr.FromString("http"),
				Protocol:   corev1.ProtocolTCP,
			},
		}

		return nil
	})
	if err != nil {
		return err
	}

	logger.Info("Service reconciled", "result", result)
	return nil
}

func parseQuantity(s string) *resource.Quantity {
	q := resource.MustParse(s)
	return &q
}

// SetupWithManager registers this controller with the manager.
//
// SCALING: Instead of using For() (which creates one work queue entry per CR),
// we use Watches() with EnqueueRequestsFromMapFunc to map ALL HttpCanary events
// to a SINGLE fixed reconcile key.
//
// Why this matters with thousands of canaries:
//
//	For() approach (what we had before):
//	  1,000 CRs change → 1,000 work queue entries → 1,000 Reconcile() calls
//	  Each one lists all CRs, rebuilds ConfigMap, ensures Deployment...
//	  = 1,000x redundant work
//
//	Watches() + single key approach:
//	  1,000 CRs change → 1,000 events → all map to same key → deduplicated to 1
//	  ONE Reconcile() call lists all CRs, rebuilds ConfigMap, ensures Deployment
//	  = 1x work
//
// The work queue deduplicates entries with the same key. By mapping every
// HttpCanary event to the same NamespacedName, we guarantee at most one
// active reconcile at a time, no matter how many CRs change.
func (r *HttpCanaryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// This fixed key is what appears in the work queue. It doesn't correspond
	// to a real Kubernetes object — it's just a deduplication token. The
	// Reconcile function ignores it and lists all CRs instead.
	triggerKey := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Namespace: r.Namespace,
			Name:      "pulse-config-trigger",
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("httpcanary").
		// Watches() + MapFunc replaces For(). Every HttpCanary event
		// (create, update, delete) gets mapped to the same trigger key.
		Watches(&canaryv1alpha1.HttpCanary{},
			handler.EnqueueRequestsFromMapFunc(
				func(_ context.Context, _ client.Object) []ctrl.Request {
					return []ctrl.Request{triggerKey}
				},
			),
		).
		Complete(r)
}
