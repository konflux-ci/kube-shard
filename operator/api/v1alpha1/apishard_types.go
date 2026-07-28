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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type StorageType string

const (
	StorageTypeSQLite              StorageType = "SQLite"
	StorageTypeInClusterPostgreSQL StorageType = "InClusterPostgreSQL"
	StorageTypePostgreSQL          StorageType = "PostgreSQL"
)

type APIGroupSpec struct {
	Group    string   `json:"group"`
	Versions []string `json:"versions"`
}

type StorageSpec struct {
	// +kubebuilder:validation:Enum=SQLite;InClusterPostgreSQL;PostgreSQL
	Type                StorageType         `json:"type"`
	ConnectionSecretRef *SecretKeyReference `json:"connectionSecretRef,omitempty"`
	InCluster           *InClusterStorage   `json:"inCluster,omitempty"`
}

type SecretKeyReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
}

type InClusterStorage struct {
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

type NamespaceSyncConfig struct {
	LabelSelector metav1.LabelSelector `json:"labelSelector"`
}

type SecondarySpec struct {
	// +kubebuilder:default=1
	Replicas  int32                       `json:"replicas,omitempty"`
	Image     string                      `json:"image,omitempty"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

type KineSpec struct {
	// +kubebuilder:default=1
	Replicas int32  `json:"replicas,omitempty"`
	Image    string `json:"image,omitempty"`
}

type APIShardSpec struct {
	TargetNamespace string              `json:"targetNamespace"`
	APIGroups       []APIGroupSpec      `json:"apiGroups"`
	Storage         StorageSpec         `json:"storage"`
	NamespaceSync   NamespaceSyncConfig `json:"namespaceSync"`
	Secondary       SecondarySpec       `json:"secondary,omitempty"`
	Kine            KineSpec            `json:"kine,omitempty"`

	// ForceAggregation, when true, causes the operator to override the
	// kube-aggregator auto-register controller by explicitly marking
	// APIService objects as not auto-managed. This allows aggregation
	// to work even when CRDs exist on the primary for the same API groups.
	// When false, the operator reports the conflict and sets phase to
	// Blocked, leaving remediation to the user.
	// +kubebuilder:default=true
	ForceAggregation bool `json:"forceAggregation,omitempty"`
}

type ConnectionSecretReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type APIShardStatus struct {
	Phase                 string                     `json:"phase,omitempty"`
	Message               string                     `json:"message,omitempty"`
	ConnectionSecret      *ConnectionSecretReference `json:"connectionSecret,omitempty"`
	SecondaryEndpoint     string                     `json:"secondaryEndpoint,omitempty"`
	RegisteredAPIServices []string                   `json:"registeredAPIServices,omitempty"`
	Conditions            []metav1.Condition         `json:"conditions,omitempty"`
	ObservedGeneration    int64                      `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.targetNamespace`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`,priority=1
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.secondaryEndpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type APIShard struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   APIShardSpec   `json:"spec,omitempty"`
	Status APIShardStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type APIShardList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []APIShard `json:"items"`
}
