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

// SecretKeyReference identifies a specific key within a Secret.
// The Secret must reside in the APIShard's targetNamespace (same namespace as Kine).
type SecretKeyReference struct {
	// Name of the Secret.
	Name string `json:"name"`
	// Key within the Secret that contains the Kine-compatible connection string.
	Key string `json:"key"`
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
	// NodeSelector constrains the secondary apiserver pods to nodes
	// matching the specified labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Tolerations allows the secondary apiserver pods to schedule onto
	// nodes with matching taints.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// TopologySpreadConstraints controls how secondary apiserver pods
	// are distributed across topology domains (zones, racks, etc.).
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
}

type KineSpec struct {
	// +kubebuilder:default=1
	Replicas  int32                       `json:"replicas,omitempty"`
	Image     string                      `json:"image,omitempty"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// NodeSelector constrains Kine pods to nodes matching the specified labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Tolerations allows Kine pods to schedule onto nodes with matching taints.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// TopologySpreadConstraints controls how Kine pods are distributed
	// across topology domains (zones, racks, etc.).
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
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

	// ColocateComponents enables co-location of apiserver and Kine pods
	// on the same nodes, combined with topology-aware routing
	// (trafficDistribution: PreferSameNode) on the Kine Service.
	// This minimizes gRPC latency between the apiserver and Kine.
	// +optional
	// +kubebuilder:default=true
	ColocateComponents *bool `json:"colocateComponents,omitempty"`
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
