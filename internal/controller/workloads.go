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
	"os"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// Names for the incident engine workload.
const (
	IncidentEngineName = "pulse-incident-engine"
	IncidentEnginePort = 9090

	// ProbeRunnerHeadlessName is the headless Service a StatefulSet requires
	// for stable pod DNS.
	ProbeRunnerHeadlessName = "pulse-probe-runner-headless"
)

// resourceDefaults describes one workload's default requests and limits.
type resourceDefaults struct {
	cpuRequest, memoryRequest string
	cpuLimit, memoryLimit     string
}

// probeRunnerResources are modest by design. The hot path is static embeddings
// — a token lookup and a mean — so a runner costs barely more with the feature
// enabled than without it.
var probeRunnerResources = resourceDefaults{
	cpuRequest: "100m", memoryRequest: "128Mi",
	cpuLimit: "500m", memoryLimit: "512Mi",
}

// incidentEngineResources are larger because this is the one pod that loads a
// transformer. It is a single replica regardless of cluster size.
var incidentEngineResources = resourceDefaults{
	cpuRequest: "200m", memoryRequest: "512Mi",
	cpuLimit: "1", memoryLimit: "1Gi",
}

// resolveResources applies environment overrides over a set of defaults, so
// operators can size these workloads without rebuilding the manager.
func resolveResources(prefix string, defaults resourceDefaults) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    *quantityOrDefault(prefix+"_CPU_REQUEST", defaults.cpuRequest),
			corev1.ResourceMemory: *quantityOrDefault(prefix+"_MEMORY_REQUEST", defaults.memoryRequest),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    *quantityOrDefault(prefix+"_CPU_LIMIT", defaults.cpuLimit),
			corev1.ResourceMemory: *quantityOrDefault(prefix+"_MEMORY_LIMIT", defaults.memoryLimit),
		},
	}
}

func quantityOrDefault(variable, fallback string) *resource.Quantity {
	if raw := os.Getenv(variable); raw != "" {
		if parsed, err := resource.ParseQuantity(raw); err == nil {
			return &parsed
		}
	}

	return parseQuantity(fallback)
}

// probeRunnerShards reads the desired replica count.
//
// The default of one reproduces the original single-pod behavior exactly,
// including its sharding: one shard owns every probe.
func probeRunnerShards() int32 {
	// Shares the runner's parser so the replica count the controller sets and
	// the shard maths each pod performs can never disagree. The result is
	// bounded by MaxShards, which is what makes narrowing to int32 safe.
	return int32(proberunner.ParseShardCount(os.Getenv("PULSE_PROBE_RUNNER_SHARDS")))
}

// ensureProbeRunner creates or updates the probe runner StatefulSet.
//
// A StatefulSet rather than a Deployment because shards need STABLE ORDINALS.
// Each pod derives which probes it owns from the numeric suffix of its own
// name, so the split needs no leader election, no lease, and no shared state —
// but it does need pod names that mean something, which only a StatefulSet
// provides.
func (r *CanaryReconciler) ensureProbeRunner(ctx context.Context, engineURL string) error {
	logger := log.FromContext(ctx)

	r.removeLegacyProbeRunnerDeployment(ctx)

	shards := probeRunnerShards()

	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: ProbeRunnerName, Namespace: r.Namespace},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, statefulSet, func() error {
		statefulSet.Labels = managedLabels

		replicas := shards
		statefulSet.Spec.Replicas = &replicas
		statefulSet.Spec.ServiceName = ProbeRunnerHeadlessName
		statefulSet.Spec.Selector = &metav1.LabelSelector{
			MatchLabels: map[string]string{"app.kubernetes.io/name": ProbeRunnerName},
		}
		// Probes are independent, so there is no reason to roll pods one at a
		// time and leave part of the cluster unmonitored for longer than needed.
		statefulSet.Spec.PodManagementPolicy = appsv1.ParallelPodManagement

		args := []string{
			fmt.Sprintf("--config=/etc/pulse/%s", ProbeConfigFile),
			fmt.Sprintf("--auth-file=/etc/pulse-auth/%s", ProbeAuthFile),
			fmt.Sprintf("--listen=:%d", ProbeRunnerPort),
		}
		if engineURL != "" {
			args = append(args, "--incident-engine="+engineURL)
		}

		statefulSet.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{"app.kubernetes.io/name": ProbeRunnerName},
			},
			Spec: corev1.PodSpec{
				ImagePullSecrets: r.ProbeRunnerImagePullSecrets,
				Containers: []corev1.Container{{
					Name:  "probe-runner",
					Image: r.ProbeRunnerImage,
					Args:  args,
					Env: []corev1.EnvVar{
						{
							// The pod's own name is how it learns its shard.
							Name: "POD_NAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
							},
						},
						{Name: "PULSE_PROBE_RUNNER_SHARDS", Value: strconv.Itoa(int(shards))},
					},
					Ports: []corev1.ContainerPort{{
						Name:          "http",
						ContainerPort: ProbeRunnerPort,
						Protocol:      corev1.ProtocolTCP,
					}},
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/healthz",
								Port: intstr.FromString("http"),
							},
						},
					},
					Resources: resolveResources("PULSE_PROBE_RUNNER", probeRunnerResources),
					VolumeMounts: []corev1.VolumeMount{
						{Name: "probe-config", MountPath: "/etc/pulse", ReadOnly: true},
						{Name: "probe-auth", MountPath: "/etc/pulse-auth", ReadOnly: true},
					},
				}},
				Volumes: probeVolumes(),
			},
		}

		return nil
	})
	if err != nil {
		return err
	}

	logger.Info("Probe runner StatefulSet reconciled", "result", result, "shards", shards)
	return nil
}

// removeLegacyProbeRunnerDeployment deletes the Deployment that earlier releases
// used for the probe runner.
//
// The probe runner became a StatefulSet so that shards could derive their
// identity from a stable pod ordinal, but it kept the same name and the same
// pod selector. Without this, an upgraded cluster runs both: the StatefulSet
// pods each own their hash slice, while the orphaned Deployment pod — having no
// POD_NAME or PULSE_PROBE_RUNNER_SHARDS env — falls back to shard 0 of 1 and
// runs EVERY probe. The result is every check executed twice, doubled counters,
// and a Service load-balancing across both.
//
// Only an object carrying Pulse's own managed labels is removed, so a Deployment
// that happens to share the name is left alone.
func (r *CanaryReconciler) removeLegacyProbeRunnerDeployment(ctx context.Context) {
	logger := log.FromContext(ctx)

	var legacy appsv1.Deployment
	err := r.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: ProbeRunnerName}, &legacy)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "Could not check for a legacy probe runner Deployment")
		}
		return
	}

	if !hasManagedLabels(legacy.Labels) {
		logger.Info("A Deployment shares the probe runner name but is not managed by Pulse; leaving it alone",
			"name", ProbeRunnerName)
		return
	}

	logger.Info("Removing the legacy probe runner Deployment, superseded by the StatefulSet",
		"name", ProbeRunnerName)
	r.deleteIfPresent(ctx, &legacy, "legacy probe runner Deployment")
}

// hasManagedLabels reports whether an object carries the labels Pulse stamps on
// everything it owns.
func hasManagedLabels(labels map[string]string) bool {
	for key, value := range managedLabels {
		if labels[key] != value {
			return false
		}
	}
	return len(managedLabels) > 0
}

// ensureIncidentEngine creates or updates the correlation and action tier.
//
// It is created ONLY when at least one canary opted into intelligence, so a
// cluster that does not use the feature sees no extra pods at all.
func (r *CanaryReconciler) ensureIncidentEngine(ctx context.Context, wanted bool) error {
	logger := log.FromContext(ctx)

	if !wanted {
		r.removeIncidentEngine(ctx)
		return nil
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: IncidentEngineName, Namespace: r.Namespace},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		deployment.Labels = managedLabels

		// Exactly one replica. All correlation state is in memory, and two
		// replicas would each see half the failures and reach different
		// conclusions about what is related to what.
		replicas := int32(1)
		deployment.Spec.Replicas = &replicas
		deployment.Spec.Selector = &metav1.LabelSelector{
			MatchLabels: map[string]string{"app.kubernetes.io/name": IncidentEngineName},
		}
		deployment.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}

		deployment.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{"app.kubernetes.io/name": IncidentEngineName},
			},
			Spec: corev1.PodSpec{
				ImagePullSecrets: r.ProbeRunnerImagePullSecrets,
				Containers: []corev1.Container{{
					Name:  "incident-engine",
					Image: r.IncidentEngineImage,
					Args: []string{
						fmt.Sprintf("--config=/etc/pulse/%s", ProbeConfigFile),
						fmt.Sprintf("--auth-file=/etc/pulse-auth/%s", ProbeAuthFile),
						fmt.Sprintf("--listen=:%d", IncidentEnginePort),
					},
					Ports: []corev1.ContainerPort{{
						Name:          "http",
						ContainerPort: IncidentEnginePort,
						Protocol:      corev1.ProtocolTCP,
					}},
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/healthz",
								Port: intstr.FromString("http"),
							},
						},
					},
					Resources: resolveResources("PULSE_INCIDENT_ENGINE", incidentEngineResources),
					VolumeMounts: []corev1.VolumeMount{
						{Name: "probe-config", MountPath: "/etc/pulse", ReadOnly: true},
						{Name: "probe-auth", MountPath: "/etc/pulse-auth", ReadOnly: true},
					},
				}},
				Volumes: probeVolumes(),
			},
		}

		return nil
	})
	if err != nil {
		return err
	}
	logger.Info("Incident engine Deployment reconciled", "result", result)

	return r.ensureNamedService(ctx, IncidentEngineName, IncidentEngineName, IncidentEnginePort, false)
}

// removeIncidentEngine deletes the engine once nothing references a policy.
//
// This NEVER returns an error, and that is deliberate. Removing the engine is
// cleanup, not a precondition for monitoring: it runs before the probe runner
// is reconciled, so returning an error here would abort the reconcile and leave
// a cluster with no probe runner at all. That is exactly what happened when the
// manager lacked delete permission — every cluster not using intelligence, the
// default path, failed to deploy anything.
//
// Failures are logged and reconciliation continues. The worst case is a
// lingering engine Deployment, which costs one idle pod.
func (r *CanaryReconciler) removeIncidentEngine(ctx context.Context) {
	logger := log.FromContext(ctx)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: IncidentEngineName, Namespace: r.Namespace},
	}
	r.deleteIfPresent(ctx, deployment, "incident engine Deployment")

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: IncidentEngineName, Namespace: r.Namespace},
	}
	r.deleteIfPresent(ctx, service, "incident engine Service")

	logger.V(1).Info("No canary uses intelligence; the incident engine is not deployed")
}

// deleteIfPresent removes an object, treating "already gone" as success and
// "not permitted" as a warning rather than a failure.
//
// Forbidden is tolerated so that an operator upgrading from a release whose
// ClusterRole lacked delete degrades to a lingering object instead of a broken
// reconcile loop. The RBAC in this repo grants delete; a stale bound role in a
// live cluster might not, and that must not take monitoring down.
func (r *CanaryReconciler) deleteIfPresent(ctx context.Context, object client.Object, description string) {
	logger := log.FromContext(ctx)

	err := r.Delete(ctx, object)
	switch {
	case err == nil:
		logger.Info("Deleted "+description, "name", object.GetName())
	case apierrors.IsNotFound(err):
		// Already gone, which is the steady state.
	case apierrors.IsForbidden(err):
		logger.Info("Not permitted to delete "+description+"; leaving it in place. "+
			"Grant delete on this resource to the manager ClusterRole.",
			"name", object.GetName())
	default:
		logger.Error(err, "Failed to delete "+description, "name", object.GetName())
	}
}

// ensureNamedService creates or updates one ClusterIP or headless Service.
func (r *CanaryReconciler) ensureNamedService(
	ctx context.Context,
	name, selector string,
	port int32,
	headless bool,
) error {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: r.Namespace},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, service, func() error {
		service.Labels = managedLabels
		service.Spec.Selector = map[string]string{"app.kubernetes.io/name": selector}

		if headless {
			service.Spec.ClusterIP = corev1.ClusterIPNone
		} else if service.Spec.ClusterIP == "" {
			service.Spec.Type = corev1.ServiceTypeClusterIP
		}

		service.Spec.Ports = []corev1.ServicePort{{
			Name:       "http",
			Port:       port,
			TargetPort: intstr.FromString("http"),
			Protocol:   corev1.ProtocolTCP,
		}}

		return nil
	})

	return err
}

func probeVolumes() []corev1.Volume {
	return []corev1.Volume{
		{
			Name: "probe-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: ProbeConfigName},
				},
			},
		},
		{
			Name: "probe-auth",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: ProbeAuthName},
			},
		},
	}
}

// incidentEngineURL is the in-cluster address runners ship observations to.
func (r *CanaryReconciler) incidentEngineURL() string {
	return fmt.Sprintf("http://%s.%s.svc:%d", IncidentEngineName, r.Namespace, IncidentEnginePort)
}

// anyProbeUsesIntelligence reports whether the engine is needed at all.
func anyProbeUsesIntelligence(config proberunner.ProbeConfig) bool {
	for _, probe := range config.Probes {
		if probe.Intelligence != nil {
			return true
		}
	}
	return false
}
