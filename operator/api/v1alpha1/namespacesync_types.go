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

// SecondaryConnectionSpec defines how to connect to a secondary API server.
type SecondaryConnectionSpec struct {
	ServiceRef    ServiceReference     `json:"serviceRef"`
	AuthSecretRef LocalSecretReference `json:"authSecretRef"`
	CASecretRef   LocalSecretReference `json:"caSecretRef"`
}

type ServiceReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Port      int32  `json:"port"`
}

type LocalSecretReference struct {
	Name string `json:"name"`
}

type NamespaceSyncSpec struct {
	SecondaryConnection SecondaryConnectionSpec `json:"secondaryConnection"`
	LabelSelector       metav1.LabelSelector    `json:"labelSelector"`
	ExcludeNamespaces   []string                `json:"excludeNamespaces,omitempty"`
}

type NamespaceSyncStatus struct {
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	SyncedNamespaces   int32              `json:"syncedNamespaces,omitempty"`
	LastSyncTime       *metav1.Time       `json:"lastSyncTime,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Synced",type=integer,JSONPath=`.status.syncedNamespaces`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
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
