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
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	canaryv1alpha1 "github.com/bryanbarton525/pulse/api/v1alpha1"
	"github.com/bryanbarton525/pulse/internal/incident"
)

// Actions describe what the controller did about the object, which the events
// API records alongside the reason.
const (
	EventActionCorrelate = "Correlate"
	EventActionSuppress  = "Suppress"
	EventActionScore     = "Score"
)

// Reasons used on the Kubernetes Events this syncer emits.
const (
	EventReasonIncidentOpened       = "IncidentOpened"
	EventReasonSuppressedByIncident = "SuppressedByIncident"
	EventReasonBodyDrift            = "BodyDrift"
	EventReasonLatencyShift         = "LatencyShift"
)

// intelligenceView is one probe's slice of an incident, ready to write to a
// canary's status.
type intelligenceView struct {
	status   canaryv1alpha1.CanaryIntelligenceStatus
	incident incident.Incident
}

// syncIncidents pulls open incidents from the engine and writes them onto the
// canaries involved.
//
// This runs in the controller rather than the engine because only the
// controller has a Kubernetes client. The engine deliberately has none: it
// mounts a ConfigMap and a Secret and talks HTTP, which keeps its RBAC surface
// empty even though it is the component holding a model and calling out to
// third-party APIs.
func (s *StatusSyncer) syncIncidents(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("status-syncer")

	incidents, err := s.fetchIncidents()
	if err != nil {
		// The engine is optional. A cluster with no AnomalyPolicy never
		// deploys one, so a failure here is expected and not worth an error.
		logger.V(1).Info("Could not fetch incidents", "error", err)
		return
	}

	views := buildIntelligenceViews(incidents)

	var httpCanaries canaryv1alpha1.HttpCanaryList
	if err := s.List(ctx, &httpCanaries); err != nil {
		logger.Error(err, "Failed to list HttpCanary resources for incident sync")
		return
	}
	var grpcCanaries canaryv1alpha1.GrpcCanaryList
	if err := s.List(ctx, &grpcCanaries); err != nil {
		logger.Error(err, "Failed to list GrpcCanary resources for incident sync")
		return
	}

	updated := 0
	for index := range httpCanaries.Items {
		canary := &httpCanaries.Items[index]
		key := fmt.Sprintf("%s/%s", canary.Namespace, canary.Name)

		view, involved := views[key]
		next := viewStatus(view, involved)

		if intelligenceStatusEqual(canary.Status.Intelligence, next) {
			continue
		}

		s.emitIncidentEvent(canary, canary.Status.Intelligence, next, view)
		canary.Status.Intelligence = next

		if err := s.Status().Update(ctx, canary); err != nil {
			if !errors.IsNotFound(err) {
				logger.Error(err, "Failed to update intelligence status", "canary", key)
			}
			continue
		}
		updated++
	}

	for index := range grpcCanaries.Items {
		canary := &grpcCanaries.Items[index]
		key := fmt.Sprintf("%s/%s", canary.Namespace, canary.Name)

		view, involved := views[key]
		next := viewStatus(view, involved)

		if intelligenceStatusEqual(canary.Status.Intelligence, next) {
			continue
		}

		s.emitIncidentEvent(canary, canary.Status.Intelligence, next, view)
		canary.Status.Intelligence = next

		if err := s.Status().Update(ctx, canary); err != nil {
			if !errors.IsNotFound(err) {
				logger.Error(err, "Failed to update intelligence status", "grpccanary", key)
			}
			continue
		}
		updated++
	}

	if updated > 0 {
		logger.Info("Incident status sync complete",
			"openIncidents", len(incidents), "statusesUpdated", updated)
	}

	s.syncProposals(ctx)
}

// buildIntelligenceViews indexes incident membership by probe.
func buildIntelligenceViews(incidents []incident.Incident) map[string]intelligenceView {
	views := make(map[string]intelligenceView)

	for _, current := range incidents {
		for _, member := range current.Members {
			signal := member.Signal

			score := signal.DriftScore
			if score == 0 {
				score = signal.LatencyZScore
			}

			status := canaryv1alpha1.CanaryIntelligenceStatus{
				Policy:     current.Policy,
				IncidentID: current.ID,
				Role:       member.Role,
				Trigger:    current.Trigger,
			}
			if score != 0 {
				status.Score = strconv.FormatFloat(score, 'f', 4, 64)
			}
			if !current.UpdatedAt.IsZero() {
				at := metav1.NewTime(current.UpdatedAt)
				status.LastSignalTime = &at
			}
			// Only the root cause carries the analysis. A victim's status
			// pointing at somebody else's root-cause narrative would be
			// actively misleading during an outage.
			if member.Role == canaryv1alpha1.IncidentRoleRootCause {
				status.Investigation = current.Investigation
			}

			views[member.Probe] = intelligenceView{status: status, incident: current}
		}
	}

	return views
}

func viewStatus(view intelligenceView, involved bool) *canaryv1alpha1.CanaryIntelligenceStatus {
	if !involved {
		return nil
	}
	status := view.status
	return &status
}

// emitIncidentEvent records a Kubernetes Event when a canary enters an incident.
//
// Events are the half of the `metric` action that needs an API client, which
// is why they live here and not in the engine's action dispatcher.
func (s *StatusSyncer) emitIncidentEvent(
	object runtime.Object,
	previous, next *canaryv1alpha1.CanaryIntelligenceStatus,
	view intelligenceView,
) {
	if s.Recorder == nil || next == nil {
		return
	}
	// Only announce a NEW incident, not every status refresh.
	if previous != nil && previous.IncidentID == next.IncidentID {
		return
	}

	// The new events API takes a `related` object and an `action` alongside the
	// reason. There is no secondary object here, so related is nil.
	switch {
	case next.Trigger == incident.TriggerBodyDrift:
		s.Recorder.Eventf(object, nil, "Warning", EventReasonBodyDrift, EventActionScore,
			"Response body drifted from its baseline (score %s) while the check was still passing",
			next.Score)

	case next.Trigger == incident.TriggerLatencyShift:
		s.Recorder.Eventf(object, nil, "Warning", EventReasonLatencyShift, EventActionScore,
			"Check is passing but %s standard deviations slower than its baseline", next.Score)

	case next.Role == canaryv1alpha1.IncidentRoleDownstream:
		s.Recorder.Eventf(object, nil, "Normal", EventReasonSuppressedByIncident, EventActionSuppress,
			"Failing as part of incident %s; root cause is %s",
			next.IncidentID, view.incident.RootCause)

	default:
		s.Recorder.Eventf(object, nil, "Warning", EventReasonIncidentOpened, EventActionCorrelate,
			"Incident %s opened with %d affected check(s); root cause is %s",
			next.IncidentID, len(view.incident.Members), view.incident.RootCause)
	}
}

// syncProposals writes learned dependency edges into the status of the
// policies whose canaries they concern.
//
// Each policy sees only the edges touching its own canaries, so a team
// reviewing proposals is not handed the whole cluster's topology.
func (s *StatusSyncer) syncProposals(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("status-syncer")

	proposals, err := s.fetchProposals()
	if err != nil || len(proposals) == 0 {
		return
	}

	var policies canaryv1alpha1.AnomalyPolicyList
	if err := s.List(ctx, &policies); err != nil {
		logger.Error(err, "Failed to list AnomalyPolicy resources")
		return
	}
	if len(policies.Items) == 0 {
		return
	}

	owners, err := s.policyOwners(ctx)
	if err != nil {
		logger.Error(err, "Failed to map canaries to policies")
		return
	}

	for index := range policies.Items {
		policy := &policies.Items[index]
		key := fmt.Sprintf("%s/%s", policy.Namespace, policy.Name)

		relevant := make([]canaryv1alpha1.InferredDependency, 0, len(proposals))
		for _, proposal := range proposals {
			if owners[proposal.From] != key && owners[proposal.To] != key {
				continue
			}
			at := metav1.NewTime(proposal.LastObserved)
			relevant = append(relevant, canaryv1alpha1.InferredDependency{
				From:         proposal.From,
				To:           proposal.To,
				Confidence:   strconv.FormatFloat(proposal.Confidence, 'f', 2, 64),
				Observations: proposal.Observations,
				LastObserved: &at,
			})
		}

		sort.Slice(relevant, func(i, j int) bool {
			if relevant[i].From != relevant[j].From {
				return relevant[i].From < relevant[j].From
			}
			return relevant[i].To < relevant[j].To
		})

		if inferredEqual(policy.Status.InferredDependencies, relevant) {
			continue
		}

		policy.Status.InferredDependencies = relevant
		if err := s.Status().Update(ctx, policy); err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "Failed to update AnomalyPolicy status", "policy", key)
		}
	}
}

// policyOwners maps each canary to the policy governing it.
func (s *StatusSyncer) policyOwners(ctx context.Context) (map[string]string, error) {
	owners := map[string]string{}

	var httpCanaries canaryv1alpha1.HttpCanaryList
	if err := s.List(ctx, &httpCanaries); err != nil {
		return nil, err
	}
	for _, canary := range httpCanaries.Items {
		if canary.Spec.Intelligence == nil {
			continue
		}
		owners[fmt.Sprintf("%s/%s", canary.Namespace, canary.Name)] =
			policyKey(canary.Namespace, canary.Spec.Intelligence.PolicyRef)
	}

	var grpcCanaries canaryv1alpha1.GrpcCanaryList
	if err := s.List(ctx, &grpcCanaries); err != nil {
		return nil, err
	}
	for _, canary := range grpcCanaries.Items {
		if canary.Spec.Intelligence == nil {
			continue
		}
		owners[fmt.Sprintf("%s/%s", canary.Namespace, canary.Name)] =
			policyKey(canary.Namespace, canary.Spec.Intelligence.PolicyRef)
	}

	return owners, nil
}

func policyKey(canaryNamespace string, reference canaryv1alpha1.PolicyReference) string {
	namespace := reference.Namespace
	if namespace == "" {
		namespace = canaryNamespace
	}
	return fmt.Sprintf("%s/%s", namespace, reference.Name)
}

func intelligenceStatusEqual(left, right *canaryv1alpha1.CanaryIntelligenceStatus) bool {
	if left == nil || right == nil {
		return left == right
	}

	return left.IncidentID == right.IncidentID &&
		left.Role == right.Role &&
		left.Trigger == right.Trigger &&
		left.Score == right.Score &&
		left.Policy == right.Policy &&
		left.Investigation == right.Investigation
}

func inferredEqual(left, right []canaryv1alpha1.InferredDependency) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].From != right[index].From ||
			left[index].To != right[index].To ||
			left[index].Confidence != right[index].Confidence ||
			left[index].Observations != right[index].Observations {
			return false
		}
	}
	return true
}

// fetchIncidents polls the incident engine's open incidents.
func (s *StatusSyncer) fetchIncidents() ([]incident.Incident, error) {
	var incidents []incident.Incident
	err := s.fetchJSON(s.incidentEngineURL()+"/incidents", &incidents)
	return incidents, err
}

// fetchProposals polls the engine's learned dependency edges.
func (s *StatusSyncer) fetchProposals() ([]incident.Proposal, error) {
	var topology struct {
		Proposals []incident.Proposal `json:"proposals"`
	}
	err := s.fetchJSON(s.incidentEngineURL()+"/topology", &topology)
	return topology.Proposals, err
}

func (s *StatusSyncer) fetchJSON(url string, target any) error {
	httpClient := &http.Client{Timeout: 5 * time.Second}

	response, err := httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s returned status %d", url, response.StatusCode)
	}

	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decoding %s: %w", url, err)
	}

	return nil
}

func (s *StatusSyncer) incidentEngineURL() string {
	if s.IncidentEngineURL != "" {
		return s.IncidentEngineURL
	}

	return fmt.Sprintf("http://%s.%s.svc:%d", IncidentEngineName, s.Namespace, IncidentEnginePort)
}
