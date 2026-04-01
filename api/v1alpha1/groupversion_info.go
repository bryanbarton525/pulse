// Package v1alpha1 contains API Schema definitions for the canary v1alpha1 API group.
// +kubebuilder:object:generate=true
// +groupName=canary.iambarton.com
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the API group and version for this package.
	GroupVersion = schema.GroupVersion{Group: "canary.iambarton.com", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionResource scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	// This gets called in main.go to register our types with the manager.
	AddToScheme = SchemeBuilder.AddToScheme
)
