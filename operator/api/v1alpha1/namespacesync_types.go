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

type NamespaceSyncSpec struct {
	// ShardRef references the parent APIShard this sync belongs to.
	ShardRef string `json:"shardRef"`

	// LabelSelector selects which namespaces on the primary to mirror to the secondary.
	LabelSelector metav1.LabelSelector `json:"labelSelector"`

	// ExcludeNamespaces is a list of namespaces to never sync (e.g., kube-system).
	ExcludeNamespaces []string `json:"excludeNamespaces,omitempty"`
}

type SyncedNamespace struct {
	Name     string      `json:"name"`
	SyncedAt metav1.Time `json:"syncedAt"`
}

type NamespaceSyncStatus struct {
	Phase              string             `json:"phase,omitempty"`
	SyncedCount        int32              `json:"syncedCount,omitempty"`
	SyncedNamespaces   []SyncedNamespace  `json:"syncedNamespaces,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Shard",type=string,JSONPath=`.spec.shardRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Synced",type=integer,JSONPath=`.status.syncedCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NamespaceSync struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NamespaceSyncSpec   `json:"spec,omitempty"`
	Status NamespaceSyncStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NamespaceSyncList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NamespaceSync `json:"items"`
}
