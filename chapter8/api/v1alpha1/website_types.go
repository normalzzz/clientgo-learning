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
	"k8s.io/apimachinery/pkg/runtime"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

const (
	WebsitePhasePending   = "Pending"
	WebsitePhaseAvailable = "Available"
	WebsitePhaseDegraded  = "Degraded"
)

// WebsiteSpec describes the desired state of a Website.
type WebsiteSpec struct {
	// Image is the container image used by the website workload.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// Replicas is the desired number of website pods.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Port is the container port exposed by the website.
	// +kubebuilder:default=80
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int32 `json:"port,omitempty"`
}

// WebsiteStatus describes the observed state of a Website.
type WebsiteStatus struct {
	// ReadyReplicas is the number of pods currently ready to serve traffic.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// Phase summarizes the current lifecycle state.
	// +kubebuilder:validation:Enum=Pending;Available;Degraded
	// +optional
	Phase string `json:"phase,omitempty"`
}

// Website is a small custom resource used to demonstrate controller-gen and
// client-go code generation.
//
// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=web
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
type Website struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WebsiteSpec   `json:"spec,omitempty"`
	Status WebsiteStatus `json:"status,omitempty"`
}

// WebsiteList contains a list of Website.
//
// +kubebuilder:object:root=true
type WebsiteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Website `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Website{}, &WebsiteList{})
		return nil
	})
}
