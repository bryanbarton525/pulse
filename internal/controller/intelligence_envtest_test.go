package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/yaml"

	canaryv1alpha1 "github.com/bryanbarton525/pulse/api/v1alpha1"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

// These specs exercise the reconciler against a real API server. Before this
// file the suite booted envtest and asserted nothing, so nothing verified that
// the generated CRDs actually accept the objects the controller expects.
var _ = Describe("Canary reconciler with intelligence", Ordered, func() {
	const (
		namespace = "pulse-system"
		timeout   = 20 * time.Second
		interval  = 250 * time.Millisecond
	)

	var reconciler *CanaryReconciler

	reconcileOnce := func() {
		GinkgoHelper()
		_, err := reconciler.Reconcile(ctx, reconcileRequest(namespace))
		Expect(err).NotTo(HaveOccurred())
	}

	probeConfig := func() proberunner.ProbeConfig {
		GinkgoHelper()

		var configMap corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Namespace: namespace, Name: ProbeConfigName,
		}, &configMap)).To(Succeed())

		var config proberunner.ProbeConfig
		Expect(yaml.Unmarshal([]byte(configMap.Data[ProbeConfigFile]), &config)).To(Succeed())
		return config
	}

	BeforeAll(func() {
		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: namespace},
		})).To(Or(Succeed(), MatchError(ContainSubstring("already exists"))))

		reconciler = &CanaryReconciler{
			Client:              k8sClient,
			Scheme:              k8sClient.Scheme(),
			Namespace:           namespace,
			ProbeRunnerImage:    "pulse-probe-runner:test",
			IncidentEngineImage: "pulse-incident-engine:test",
		}
	})

	Context("with no canary opted in", func() {
		It("creates the probe runner but no incident engine", func() {
			Expect(k8sClient.Create(ctx, &canaryv1alpha1.HttpCanary{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "plain-canary"},
				Spec: canaryv1alpha1.HttpCanarySpec{
					URL: "https://example.com/health", Interval: 30, ExpectedStatus: 200,
				},
			})).To(Succeed())

			reconcileOnce()

			var statefulSet appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: ProbeRunnerName,
			}, &statefulSet)).To(Succeed())

			// A cluster not using the feature must see no extra pods at all.
			var engine appsv1.Deployment
			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: IncidentEngineName,
			}, &engine)
			Expect(apierrors.IsNotFound(err)).To(BeTrue(),
				"the incident engine should not exist until a canary opts in")

			config := probeConfig()
			Expect(config.Probes).To(HaveLen(1))
			Expect(config.Probes[0].Intelligence).To(BeNil())
		})
	})

	Context("with an AnomalyPolicy and an opted-in canary", func() {
		BeforeAll(func() {
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "slack-auth"},
				Data:       map[string][]byte{"webhook-url": []byte("https://hooks.slack.test/T/B/XYZ")},
			})).To(Succeed())

			Expect(k8sClient.Create(ctx, &canaryv1alpha1.AnomalyPolicy{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "prod-triage"},
				Spec: canaryv1alpha1.AnomalyPolicySpec{
					Triggers: &canaryv1alpha1.AnomalyTriggers{
						BodyDrift: &canaryv1alpha1.BodyDriftTrigger{
							Enabled: true, Threshold: "0.4", WarmupChecks: 15,
						},
						FailureCorrelation: &canaryv1alpha1.FailureCorrelationTrigger{
							Enabled: true, WindowSeconds: 90, SimilarityThreshold: "0.9",
						},
					},
					Topology: &canaryv1alpha1.AnomalyTopology{
						DependsOn: []canaryv1alpha1.CanaryDependency{{
							Canary:   "default/smart-canary",
							Upstream: []string{"data/postgres-health"},
						}},
					},
					Actions: []canaryv1alpha1.AnomalyAction{
						{Name: "local", Type: canaryv1alpha1.AnomalyActionMetric},
						{
							Name: "notify",
							Type: canaryv1alpha1.AnomalyActionSlack,
							Slack: &canaryv1alpha1.SlackAction{
								WebhookSecretRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: "slack-auth"},
									Key:                  "webhook-url",
								},
							},
						},
					},
				},
			})).To(Succeed())

			Expect(k8sClient.Create(ctx, &canaryv1alpha1.HttpCanary{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default", Name: "smart-canary",
					Labels: map[string]string{"tier": "prod"},
				},
				Spec: canaryv1alpha1.HttpCanarySpec{
					URL: "https://example.com/orders", Interval: 30, ExpectedStatus: 200,
					Intelligence: &canaryv1alpha1.CanaryIntelligence{
						Enabled: true,
						PolicyRef: canaryv1alpha1.PolicyReference{
							Name: "prod-triage", Namespace: namespace,
						},
					},
				},
			})).To(Succeed())

			reconcileOnce()
		})

		It("flattens the policy into the probe ConfigMap", func() {
			config := probeConfig()

			var probe *proberunner.Probe
			for index := range config.Probes {
				if config.Probes[index].Name == "default/smart-canary" {
					probe = &config.Probes[index]
				}
			}
			Expect(probe).NotTo(BeNil())
			Expect(probe.ConfigError).To(BeEmpty())
			Expect(probe.Intelligence).NotTo(BeNil())
			Expect(probe.Intelligence.Policy).To(Equal("pulse-system/prod-triage"))

			// Decimal strings from the CRD become float64 at reconcile time.
			Expect(probe.Intelligence.Triggers.BodyDrift).NotTo(BeNil())
			Expect(probe.Intelligence.Triggers.BodyDrift.Threshold).To(Equal(0.4))
			Expect(probe.Intelligence.Triggers.FailureCorrelation.SimilarityThreshold).To(Equal(0.9))

			// Labels ride along so the engine can evaluate a candidateSelector.
			Expect(probe.Labels).To(HaveKeyWithValue("tier", "prod"))

			// Declared topology reaches the engine through the same file.
			Expect(probe.Intelligence.Topology.DependsOn).To(HaveLen(1))
			Expect(probe.Intelligence.Topology.DependsOn[0].Upstream).
				To(ConsistOf("data/postgres-health"))
		})

		It("keeps resolved credentials out of the ConfigMap", func() {
			var configMap corev1.ConfigMap
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: ProbeConfigName,
			}, &configMap)).To(Succeed())

			Expect(configMap.Data[ProbeConfigFile]).
				NotTo(ContainSubstring("https://hooks.slack.test"))

			// The value lives only in the Secret, reachable by credential ID.
			var secret corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: ProbeAuthName,
			}, &secret)).To(Succeed())

			var store proberunner.AuthStore
			Expect(yaml.Unmarshal(secret.Data[ProbeAuthFile], &store)).To(Succeed())

			found := false
			for _, value := range store.Values {
				if value == "https://hooks.slack.test/T/B/XYZ" {
					found = true
				}
			}
			Expect(found).To(BeTrue(), "the webhook should be in the auth Secret")
		})

		It("creates the incident engine and points runners at it", func() {
			Eventually(func() error {
				var engine appsv1.Deployment
				return k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace, Name: IncidentEngineName,
				}, &engine)
			}, timeout, interval).Should(Succeed())

			var engine appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: IncidentEngineName,
			}, &engine)).To(Succeed())
			Expect(*engine.Spec.Replicas).To(BeEquivalentTo(1),
				"correlation state is in memory, so a second replica would split the picture")

			var statefulSet appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: ProbeRunnerName,
			}, &statefulSet)).To(Succeed())

			args := statefulSet.Spec.Template.Spec.Containers[0].Args
			Expect(args).To(ContainElement(ContainSubstring("--incident-engine=")))

			// The pod needs its own name to derive its shard.
			var hasPodName bool
			for _, env := range statefulSet.Spec.Template.Spec.Containers[0].Env {
				if env.Name == "POD_NAME" && env.ValueFrom != nil {
					hasPodName = true
				}
			}
			Expect(hasPodName).To(BeTrue(), "POD_NAME is how a replica learns its shard")
		})

		It("removes the incident engine when the last canary opts out", func() {
			var canary canaryv1alpha1.HttpCanary
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: "default", Name: "smart-canary",
			}, &canary)).To(Succeed())

			canary.Spec.Intelligence = nil
			Expect(k8sClient.Update(ctx, &canary)).To(Succeed())

			reconcileOnce()

			Eventually(func() bool {
				var engine appsv1.Deployment
				err := k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace, Name: IncidentEngineName,
				}, &engine)
				return apierrors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("with a canary referencing a policy that does not exist", func() {
		It("records a config error without failing the whole reconcile", func() {
			Expect(k8sClient.Create(ctx, &canaryv1alpha1.HttpCanary{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "orphan-canary"},
				Spec: canaryv1alpha1.HttpCanarySpec{
					URL: "https://example.com/orphan", Interval: 30, ExpectedStatus: 200,
					Intelligence: &canaryv1alpha1.CanaryIntelligence{
						Enabled:   true,
						PolicyRef: canaryv1alpha1.PolicyReference{Name: "nonexistent"},
					},
				},
			})).To(Succeed())

			reconcileOnce()

			config := probeConfig()

			var orphan, healthy *proberunner.Probe
			for index := range config.Probes {
				switch config.Probes[index].Name {
				case "default/orphan-canary":
					orphan = &config.Probes[index]
				case "default/plain-canary":
					healthy = &config.Probes[index]
				}
			}

			Expect(orphan).NotTo(BeNil())
			Expect(orphan.ConfigError).To(ContainSubstring("not found"))

			// One broken canary must not disturb any other.
			Expect(healthy).NotTo(BeNil())
			Expect(healthy.ConfigError).To(BeEmpty())
		})
	})
})

// reconcileRequest builds the fixed trigger key the reconciler expects. The
// key is a deduplication token, not a real object — Reconcile ignores it and
// lists every CR instead.
func reconcileRequest(namespace string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{
		Namespace: namespace,
		Name:      "pulse-config-trigger",
	}}
}
