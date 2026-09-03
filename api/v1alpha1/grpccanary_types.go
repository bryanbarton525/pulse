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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// GrpcCanarySpec defines the desired state of GrpcCanary
type GrpcCanarySpec struct {
	// URL is the gRPC endpoint to check (e.g., localhost:50051)
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// Service is the gRPC service name to check using the standard grpc.health.v1.Health protocol.
	// If omitted, the server's overall health is checked.
	// +optional
	Service string `json:"service,omitempty"`

	// Interval is the frequency in seconds to run the check.
	// +kubebuilder:validation:Minimum=5
	// +kubebuilder:default=30
	// +optional
	Interval int `json:"interval,omitempty"`

	// Outputs configure where Pulse emits probe telemetry for this canary.
	// +optional
	// +kubebuilder:default={{type: prometheus}}
	Outputs []HttpCanaryOutput `json:"outputs,omitempty"`

	// Intelligence opts this canary into model-driven evaluation. gRPC probes
	// have no response body, so bodyDrift does not apply — but latency shift,
	// failure correlation, and novelty routing all do.
	// +optional
	Intelligence *CanaryIntelligence `json:"intelligence,omitempty"`
}

// GrpcCanaryStatus defines the observed state of GrpcCanary.
type GrpcCanaryStatus struct {
	// Phase represents the current health state (e.g. Healthy, Unhealthy, Unknown).
	// +optional
	Phase string `json:"phase,omitempty"`

	// LastCheckTime is the timestamp of the most recent probe execution.
	// +optional
	LastCheckTime *metav1.Time `json:"lastCheckTime,omitempty"`

	// LastStatus is the status code from the most recent probe (using gRPC status codes).
	// +optional
	LastStatus int `json:"lastStatus,omitempty"`

	// Message contains human-readable details about the most recent probe execution.
	// +optional
	Message string `json:"message,omitempty"`

	// conditions represent the current state of the GrpcCanary resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Intelligence reports what the model concluded about this canary.
	// Only populated when spec.intelligence is set.
	// +optional
	Intelligence *CanaryIntelligenceStatus `json:"intelligence,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// GrpcCanary is the Schema for the grpccanaries API
type GrpcCanary struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of GrpcCanary
	// +required
	Spec GrpcCanarySpec `json:"spec"`

	// status defines the observed state of GrpcCanary
	// +optional
	Status GrpcCanaryStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// GrpcCanaryList contains a list of GrpcCanary
type GrpcCanaryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []GrpcCanary `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GrpcCanary{}, &GrpcCanaryList{})
}
