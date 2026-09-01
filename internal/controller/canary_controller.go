package controller

import (
	"context"
	"fmt"
	"maps"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
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
	ProbeAuthName   = "pulse-probe-auth"
	ProbeAuthFile   = "auth.yaml"
	ProbeRunnerPort = 9090
)

// Labels applied to all resources the controller manages.
var managedLabels = map[string]string{
	"app.kubernetes.io/name":       ProbeRunnerName,
	"app.kubernetes.io/managed-by": "pulse-controller",
}

// CanaryReconciler reconciles HttpCanary objects.
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
type CanaryReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Namespace is where the controller creates infrastructure resources
	// (ConfigMap, Deployment, Service). This is the operator's own namespace,
	// typically "pulse-system". Set from the POD_NAMESPACE env var.
	Namespace string

	// ProbeRunnerImage is the container image for the probe runner Deployment.
	ProbeRunnerImage string

	// ProbeRunnerImagePullSecrets are attached to the probe runner pod template
	// so the cluster can pull private images.
	ProbeRunnerImagePullSecrets []corev1.LocalObjectReference

	// IncidentEngineImage is the container image for the correlation and
	// action tier. It is a separate image from the probe runner because it is
	// the only one that carries ONNX Runtime and a transformer.
	IncidentEngineImage string
}

// RBAC markers — controller-gen reads these to generate config/rbac/role.yaml.
//
// +kubebuilder:rbac:groups=canary.iambarton.com,resources=httpcanaries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=canary.iambarton.com,resources=httpcanaries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=canary.iambarton.com,resources=httpcanaries/finalizers,verbs=update
// +kubebuilder:rbac:groups=canary.iambarton.com,resources=grpccanaries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=canary.iambarton.com,resources=grpccanaries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=canary.iambarton.com,resources=grpccanaries/finalizers,verbs=update
// +kubebuilder:rbac:groups=canary.iambarton.com,resources=anomalypolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=canary.iambarton.com,resources=anomalypolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is called whenever any HttpCanary CR changes.
//
// Because all events are mapped to a single key (see SetupWithManager), this
// function runs AT MOST ONCE per batch of changes — not once per CR.
// It always lists all CRs and rebuilds the full infrastructure state.
func (r *CanaryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// ──────────────────────────────────────────────────────
	// STEP 1: List ALL HttpCanary CRs across all namespaces.
	//
	// We ignore req.NamespacedName entirely. It's always the
	// same fixed "trigger" key (see SetupWithManager). The
	// real source of truth is the full list of CRs.
	// ──────────────────────────────────────────────────────
	var httpCanaryList canaryv1alpha1.HttpCanaryList
	if err := r.List(ctx, &httpCanaryList); err != nil {
		logger.Error(err, "Failed to list HttpCanary resources")
		return ctrl.Result{}, err
	}

	var grpcCanaryList canaryv1alpha1.GrpcCanaryList
	if err := r.List(ctx, &grpcCanaryList); err != nil {
		logger.Error(err, "Failed to list GrpcCanary resources")
		return ctrl.Result{}, err
	}

	var policyList canaryv1alpha1.AnomalyPolicyList
	if err := r.List(ctx, &policyList); err != nil {
		logger.Error(err, "Failed to list AnomalyPolicy resources")
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling probe infrastructure", "httpCanaryCount", len(httpCanaryList.Items), "grpcCanaryCount", len(grpcCanaryList.Items), "policyCount", len(policyList.Items))

	// ──────────────────────────────────────────────────────
	// STEP 2: Build the probe config from all CRs.
	// ──────────────────────────────────────────────────────
	config := buildProbeConfig(httpCanaryList.Items, grpcCanaryList.Items)
	authStore := proberunner.AuthStore{Values: map[string]string{}}
	r.populateProbeAuth(ctx, httpCanaryList.Items, &config, &authStore)
	r.populateProbeIntelligence(ctx, httpCanaryList.Items, grpcCanaryList.Items, policyList.Items, &config, &authStore)

	configYAML, err := yaml.Marshal(config)
	if err != nil {
		logger.Error(err, "Failed to marshal probe config")
		return ctrl.Result{}, err
	}

	authYAML, err := yaml.Marshal(authStore)
	if err != nil {
		logger.Error(err, "Failed to marshal probe auth store")
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
	// STEP 4: Ensure Secret with probe auth material.
	// ──────────────────────────────────────────────────────
	if err := r.ensureAuthSecret(ctx, authYAML); err != nil {
		logger.Error(err, "Failed to ensure auth Secret")
		return ctrl.Result{}, err
	}

	// ──────────────────────────────────────────────────────
	// STEP 5: Ensure the incident engine, but only when some
	// canary actually opted into intelligence. A cluster that
	// does not use the feature gets no extra pods.
	// ──────────────────────────────────────────────────────
	wantsEngine := anyProbeUsesIntelligence(config)
	if err := r.ensureIncidentEngine(ctx, wantsEngine); err != nil {
		logger.Error(err, "Failed to ensure the incident engine")
		return ctrl.Result{}, err
	}

	engineURL := ""
	if wantsEngine {
		engineURL = r.incidentEngineURL()
	}

	// ──────────────────────────────────────────────────────
	// STEP 6: Ensure the probe runner StatefulSet and its
	// Services.
	// ──────────────────────────────────────────────────────
	if err := r.ensureProbeRunner(ctx, engineURL); err != nil {
		logger.Error(err, "Failed to ensure the probe runner StatefulSet")
		return ctrl.Result{}, err
	}

	// The headless Service gives StatefulSet pods stable DNS; the ClusterIP
	// Service keeps the original /results endpoint working for single-shard
	// deployments and for out-of-cluster debugging.
	if err := r.ensureNamedService(
		ctx, ProbeRunnerHeadlessName, ProbeRunnerName, ProbeRunnerPort, true); err != nil {
		logger.Error(err, "Failed to ensure the headless Service")
		return ctrl.Result{}, err
	}
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
func buildProbeConfig(httpCanaries []canaryv1alpha1.HttpCanary, grpcCanaries []canaryv1alpha1.GrpcCanary) proberunner.ProbeConfig {
	config := proberunner.ProbeConfig{
		Probes: make([]proberunner.Probe, 0, len(httpCanaries)+len(grpcCanaries)),
	}

	for _, c := range httpCanaries {
		journey := make([]proberunner.ProbeStep, 0, len(c.Spec.Journey))
		for _, step := range c.Spec.Journey {
			journey = append(journey, proberunner.ProbeStep{
				Name:           step.Name,
				URL:            step.URL,
				Method:         step.Method,
				Headers:        step.Headers,
				Body:           step.Body,
				ExpectedStatus: step.ExpectedStatus,
				ContainsText:   step.ContainsText,
			})
		}

		config.Probes = append(config.Probes, proberunner.Probe{
			Name:           fmt.Sprintf("%s/%s", c.Namespace, c.Name),
			Type:           proberunner.ProbeTypeHTTP,
			URL:            c.Spec.URL,
			Method:         c.Spec.Method,
			Headers:        c.Spec.Headers,
			Body:           c.Spec.Body,
			Interval:       c.Spec.Interval,
			ExpectedStatus: c.Spec.ExpectedStatus,
			ContainsText:   c.Spec.ContainsText,
			MCP:            buildProbeMCP(c.Spec.MCP),
			Journey:        journey,
			Outputs:        buildProbeOutputs(c.Spec.Outputs),
			Labels:         maps.Clone(c.Labels),
		})
	}

	for _, c := range grpcCanaries {
		config.Probes = append(config.Probes, proberunner.Probe{
			Name:        fmt.Sprintf("%s/%s", c.Namespace, c.Name),
			Type:        proberunner.ProbeTypeGRPC,
			URL:         c.Spec.URL,
			Interval:    c.Spec.Interval,
			GrpcService: c.Spec.Service,
			Outputs:     buildProbeOutputs(c.Spec.Outputs),
			Labels:      maps.Clone(c.Labels),
		})
	}

	return config
}

func buildProbeOutputs(outputs []canaryv1alpha1.HttpCanaryOutput) []proberunner.ProbeOutput {
	if len(outputs) == 0 {
		return nil
	}

	probeOutputs := make([]proberunner.ProbeOutput, 0, len(outputs))
	for _, output := range outputs {
		probeOutputs = append(probeOutputs, proberunner.ProbeOutput{Type: output.Type})
	}

	return probeOutputs
}

func buildProbeMCP(mcp *canaryv1alpha1.HttpCanaryMCP) *proberunner.ProbeMCP {
	if mcp == nil {
		return nil
	}

	return &proberunner.ProbeMCP{
		ProtocolVersion:        mcp.ProtocolVersion,
		ClientName:             mcp.ClientName,
		ClientVersion:          mcp.ClientVersion,
		RequireToolsCapability: mcp.RequireToolsCapability,
		MinToolCount:           mcp.MinToolCount,
		RequiredTools:          append([]string(nil), mcp.RequiredTools...),
	}
}

func (r *CanaryReconciler) populateProbeAuth(
	ctx context.Context,
	canaries []canaryv1alpha1.HttpCanary,
	config *proberunner.ProbeConfig,
	authStore *proberunner.AuthStore,
) {
	if authStore.Values == nil {
		authStore.Values = map[string]string{}
	}

	for index, canary := range canaries {
		probeAuth, values, err := r.buildProbeAuth(ctx, canary)
		if err != nil {
			config.Probes[index].ConfigError = fmt.Sprintf("Invalid auth config: %v", err)
			continue
		}

		config.Probes[index].Auth = probeAuth
		maps.Copy(authStore.Values, values)
	}
}

func (r *CanaryReconciler) buildProbeAuth(
	ctx context.Context,
	canary canaryv1alpha1.HttpCanary,
) (*proberunner.ProbeAuth, map[string]string, error) {
	if canary.Spec.Auth == nil {
		return nil, nil, nil
	}

	auth := canary.Spec.Auth
	credentials := map[string]string{}

	switch auth.Type {
	case canaryv1alpha1.HttpCanaryAuthTypeBasic:
		if auth.Basic == nil {
			return nil, nil, fmt.Errorf("basic auth requires the basic block")
		}

		username, err := r.resolveSecretValue(ctx, canary.Namespace, auth.Basic.UsernameSecretRef)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving basic username: %w", err)
		}
		password, err := r.resolveSecretValue(ctx, canary.Namespace, auth.Basic.PasswordSecretRef)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving basic password: %w", err)
		}

		usernameID := probeCredentialID(canary.Namespace, canary.Name, "basic-username")
		passwordID := probeCredentialID(canary.Namespace, canary.Name, "basic-password")
		credentials[usernameID] = username
		credentials[passwordID] = password

		return &proberunner.ProbeAuth{
			Type:                 auth.Type,
			UsernameCredentialID: usernameID,
			PasswordCredentialID: passwordID,
		}, credentials, nil
	case canaryv1alpha1.HttpCanaryAuthTypeBearer:
		if auth.Bearer == nil {
			return nil, nil, fmt.Errorf("bearer auth requires the bearer block")
		}

		token, err := r.resolveSecretValue(ctx, canary.Namespace, auth.Bearer.TokenSecretRef)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving bearer token: %w", err)
		}

		tokenID := probeCredentialID(canary.Namespace, canary.Name, "bearer-token")
		credentials[tokenID] = token

		return &proberunner.ProbeAuth{Type: auth.Type, TokenCredentialID: tokenID}, credentials, nil
	case canaryv1alpha1.HttpCanaryAuthTypeAPIKey:
		if auth.APIKey == nil {
			return nil, nil, fmt.Errorf("apiKey auth requires the apiKey block")
		}
		if auth.APIKey.HeaderName == "" {
			return nil, nil, fmt.Errorf("apiKey auth requires headerName")
		}

		value, err := r.resolveSecretValue(ctx, canary.Namespace, auth.APIKey.ValueSecretRef)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving apiKey value: %w", err)
		}

		valueID := probeCredentialID(canary.Namespace, canary.Name, "api-key")
		credentials[valueID] = value

		return &proberunner.ProbeAuth{Type: auth.Type, HeaderName: auth.APIKey.HeaderName, ValueCredentialID: valueID}, credentials, nil
	default:
		return nil, nil, fmt.Errorf("unsupported auth type %q", auth.Type)
	}
}

func (r *CanaryReconciler) resolveSecretValue(
	ctx context.Context,
	namespace string,
	selector corev1.SecretKeySelector,
) (string, error) {
	if selector.Name == "" {
		return "", fmt.Errorf("secret name is required")
	}
	if selector.Key == "" {
		return "", fmt.Errorf("secret key is required")
	}

	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: selector.Name}, &secret); err != nil {
		return "", err
	}

	value, found := secret.Data[selector.Key]
	if !found {
		return "", fmt.Errorf("secret %s/%s is missing key %q", namespace, selector.Name, selector.Key)
	}

	return string(value), nil
}

func probeCredentialID(namespace, name, suffix string) string {
	return fmt.Sprintf("%s__%s__%s", namespace, name, suffix)
}

func (r *CanaryReconciler) ensureAuthSecret(ctx context.Context, authYAML []byte) error {
	logger := log.FromContext(ctx)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ProbeAuthName,
			Namespace: r.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Labels = managedLabels
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{
			ProbeAuthFile: authYAML,
		}
		return nil
	})
	if err != nil {
		return err
	}

	logger.Info("Auth Secret reconciled", "result", result)
	return nil
}

// ensureService creates or updates the probe runner Service.
func (r *CanaryReconciler) ensureService(ctx context.Context) error {
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
func (r *CanaryReconciler) SetupWithManager(mgr ctrl.Manager) error {
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
		Named("canary").
		// Watches() + MapFunc replaces For(). Every HttpCanary and GrpcCanary event
		// gets mapped to the same trigger key.
		Watches(&canaryv1alpha1.HttpCanary{},
			handler.EnqueueRequestsFromMapFunc(
				func(_ context.Context, _ client.Object) []ctrl.Request {
					return []ctrl.Request{triggerKey}
				},
			),
		).
		Watches(&canaryv1alpha1.GrpcCanary{},
			handler.EnqueueRequestsFromMapFunc(
				func(_ context.Context, _ client.Object) []ctrl.Request {
					return []ctrl.Request{triggerKey}
				},
			),
		).
		Watches(&canaryv1alpha1.AnomalyPolicy{},
			handler.EnqueueRequestsFromMapFunc(
				func(_ context.Context, _ client.Object) []ctrl.Request {
					return []ctrl.Request{triggerKey}
				},
			),
		).
		Watches(&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(
				func(_ context.Context, _ client.Object) []ctrl.Request {
					return []ctrl.Request{triggerKey}
				},
			),
		).
		Complete(r)
}
