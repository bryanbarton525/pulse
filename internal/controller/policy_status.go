package controller

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/log"

	canaryv1alpha1 "github.com/bryanbarton525/pulse/api/v1alpha1"
	"github.com/bryanbarton525/pulse/internal/proberunner"
)

const (
	conditionHotModelResolved  = "HotModelResolved"
	conditionColdModelResolved = "ColdModelResolved"
)

type effectiveModels struct {
	hot  proberunner.ProbeHotModel
	cold proberunner.ProbeColdModel
}

func (r *CanaryReconciler) syncPolicyModelStatus(
	ctx context.Context,
	policies []canaryv1alpha1.AnomalyPolicy,
	probes []proberunner.Probe,
) {
	logger := log.FromContext(ctx)
	models := map[string]effectiveModels{}
	references := map[string]int{}
	for _, probe := range probes {
		if probe.Intelligence == nil {
			continue
		}
		policy := probe.Intelligence.Policy
		references[policy]++
		if _, found := models[policy]; !found {
			models[policy] = effectiveModels{
				hot:  probe.Intelligence.Model.Hot,
				cold: probe.Intelligence.Model.Cold,
			}
		}
	}

	keys := make([]string, 0, len(models))
	for key := range models {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var selected effectiveModels
	if len(keys) > 0 {
		selected = models[keys[0]]
	}

	for index := range policies {
		policy := &policies[index]
		key := fmt.Sprintf("%s/%s", policy.Namespace, policy.Name)
		referenceCount := references[key]
		before := policy.Status.DeepCopy()
		policy.Status.ReferencedBy = referenceCount
		if referenceCount == 0 {
			policy.Status.InferredDependencies = nil
		}
		if len(keys) == 0 {
			policy.Status.ResolvedHotModel = ""
			policy.Status.ResolvedColdModel = ""
		} else {
			policy.Status.ResolvedHotModel = hotModelLabel(selected.hot)
			policy.Status.ResolvedColdModel = coldModelLabel(selected.cold)
		}

		current, referenced := models[key]
		setModelCondition(policy, conditionHotModelResolved, referenced,
			reflect.DeepEqual(current.hot, selected.hot), policy.Status.ResolvedHotModel)
		setModelCondition(policy, conditionColdModelResolved, referenced,
			reflect.DeepEqual(current.cold, selected.cold), policy.Status.ResolvedColdModel)

		if reflect.DeepEqual(before, &policy.Status) {
			continue
		}
		desired := policy.Status.DeepCopy()
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var current canaryv1alpha1.AnomalyPolicy
			if err := r.Get(ctx, types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}, &current); err != nil {
				return err
			}
			// The model reconciler owns model fields and reference count. It only
			// owns inferred dependencies when no canary references the policy.
			inferred := current.Status.InferredDependencies
			current.Status = *desired.DeepCopy()
			if referenceCount > 0 {
				current.Status.InferredDependencies = inferred
			}
			return r.Status().Update(ctx, &current)
		}); err != nil && !apierrors.IsNotFound(err) {
			logger.Error(err, "Failed to update AnomalyPolicy model status", "policy", key)
		}
	}
}

func setModelCondition(
	policy *canaryv1alpha1.AnomalyPolicy,
	conditionType string,
	referenced, matches bool,
	selected string,
) {
	condition := metav1.Condition{
		Type:               conditionType,
		ObservedGeneration: policy.Generation,
		Status:             metav1.ConditionTrue,
		Reason:             "Resolved",
		Message:            "Policy uses the cluster-wide resolved model " + selected,
	}
	if !referenced {
		condition.Status = metav1.ConditionUnknown
		condition.Reason = "Unreferenced"
		condition.Message = "No canary currently references this policy"
	} else if !matches {
		condition.Status = metav1.ConditionFalse
		condition.Reason = "ModelConflict"
		condition.Message = "Policy model was ignored; the cluster resolved " + selected
	}
	apiMeta.SetStatusCondition(&policy.Status.Conditions, condition)
}

func hotModelLabel(model proberunner.ProbeHotModel) string {
	return fmt.Sprintf("%s:%s", model.Backend, model.ModelPath)
}

func coldModelLabel(model proberunner.ProbeColdModel) string {
	if model.Backend == canaryv1alpha1.EmbeddingBackendHTTP {
		return fmt.Sprintf("http:%s:%s", model.HTTP.Endpoint, model.HTTP.Model)
	}
	return fmt.Sprintf("%s:%s", model.Backend, model.ONNX.ModelPath)
}
