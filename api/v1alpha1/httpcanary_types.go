package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// HttpCanaryStep defines one HTTP request in a scripted journey.
type HttpCanaryStep struct {
	// Name is a human-readable step label.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// URL is the HTTP endpoint to call for this step.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// Method is the HTTP method used for this step.
	// +kubebuilder:default=GET
	// +kubebuilder:validation:Enum=GET;POST;PUT;PATCH;DELETE;HEAD
	Method string `json:"method,omitempty"`

	// Headers are added to the request.
	Headers map[string]string `json:"headers,omitempty"`

	// Body is sent as the request body for methods that support it.
	Body string `json:"body,omitempty"`

	// ExpectedStatus is the HTTP status code that indicates success.
	// +kubebuilder:validation:Minimum=100
	// +kubebuilder:validation:Maximum=599
	// +kubebuilder:default=200
	ExpectedStatus int `json:"expectedStatus,omitempty"`

	// ContainsText requires the response body to contain this substring.
	ContainsText string `json:"containsText,omitempty"`
}

// HttpCanarySpec defines the desired state of HttpCanary.
// This is what the user fills in when they write their YAML.
type HttpCanarySpec struct {
	// URL is the HTTP endpoint to check.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// Method is the HTTP method used for a simple one-request check.
	// +kubebuilder:default=GET
	// +kubebuilder:validation:Enum=GET;POST;PUT;PATCH;DELETE;HEAD
	Method string `json:"method,omitempty"`

	// Headers are added to the request.
	Headers map[string]string `json:"headers,omitempty"`

	// Body is sent as the request body for methods that support it.
	Body string `json:"body,omitempty"`

	// Interval is how often (in seconds) to run the check.
	// +kubebuilder:validation:Minimum=5
	// +kubebuilder:default=30
	Interval int `json:"interval,omitempty"`

	// ExpectedStatus is the HTTP status code that indicates success.
	// +kubebuilder:validation:Minimum=100
	// +kubebuilder:validation:Maximum=599
	// +kubebuilder:default=200
	ExpectedStatus int `json:"expectedStatus,omitempty"`

	// ContainsText requires the response body to contain this substring.
	ContainsText string `json:"containsText,omitempty"`

	// Journey defines a scripted sequence of HTTP requests.
	Journey []HttpCanaryStep `json:"journey,omitempty"`
}

// HttpCanaryStatus defines the observed state of HttpCanary.
// The controller fills this in — the user never sets it directly.
type HttpCanaryStatus struct {
	// Phase is the current state: Healthy, Unhealthy, or Unknown.
	// +kubebuilder:validation:Enum=Healthy;Unhealthy;Unknown
	Phase string `json:"phase,omitempty"`

	// LastCheckTime is when the canary was last evaluated.
	LastCheckTime *metav1.Time `json:"lastCheckTime,omitempty"`

	// LastStatus is the HTTP status code from the most recent check.
	LastStatus int `json:"lastStatus,omitempty"`

	// Message provides human-readable detail about the current state.
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.url`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HttpCanary is the Schema for the httpcanaries API.
// It represents a single HTTP endpoint health check.
type HttpCanary struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HttpCanarySpec   `json:"spec,omitempty"`
	Status HttpCanaryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HttpCanaryList contains a list of HttpCanary.
type HttpCanaryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HttpCanary `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HttpCanary{}, &HttpCanaryList{})
}
