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
}

type ConnectionPoolConfig struct {
	// MaxIdleConnections sets --datastore-max-idle-connections
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxIdleConnections *int `json:"maxIdleConnections,omitempty"`
	// MaxOpenConnections sets --datastore-max-open-connections (0 = unlimited)
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxOpenConnections *int `json:"maxOpenConnections,omitempty"`
	// MaxLifetime sets --datastore-connection-max-lifetime
	// +optional
	MaxLifetime *metav1.Duration `json:"maxLifetime,omitempty"`
}

type CompactionConfig struct {
	// Interval sets --compact-interval (0 = disabled)
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`
	// MinRetain sets --compact-min-retain
	// +optional
	// +kubebuilder:validation:Minimum=0
	MinRetain *int64 `json:"minRetain,omitempty"`
	// BatchSize sets --compact-batch-size
	// +optional
	// +kubebuilder:validation:Minimum=0
	BatchSize *int64 `json:"batchSize,omitempty"`
}

type KineSpec struct {
	// +kubebuilder:default=1
	Replicas  int32                       `json:"replicas,omitempty"`
	Image     string                      `json:"image,omitempty"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// +optional
	ConnectionPool *ConnectionPoolConfig `json:"connectionPool,omitempty"`
	// +optional
	Compaction *CompactionConfig `json:"compaction,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	PollBatchSize *int64 `json:"pollBatchSize,omitempty"`
	// +optional
	WatchProgressNotifyInterval *metav1.Duration `json:"watchProgressNotifyInterval,omitempty"`
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
