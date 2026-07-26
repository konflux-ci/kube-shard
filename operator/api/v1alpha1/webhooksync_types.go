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

type SyncedWebhookCounts struct {
	Validating int32 `json:"validating,omitempty"`
	Mutating   int32 `json:"mutating,omitempty"`
}

type WebhookSyncSpec struct {
	// SecondaryConnection specifies how to connect to the secondary API server.
	SecondaryConnection SecondaryConnectionSpec `json:"secondaryConnection"`

	// APIGroups is the list of API groups whose webhooks should be synced.
	APIGroups []string `json:"apiGroups"`
}

type WebhookSyncStatus struct {
	Conditions         []metav1.Condition  `json:"conditions,omitempty"`
	SyncedWebhooks     SyncedWebhookCounts `json:"syncedWebhooks,omitempty"`
	LastSyncTime       *metav1.Time        `json:"lastSyncTime,omitempty"`
	ObservedGeneration int64               `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type WebhookSync struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WebhookSyncSpec   `json:"spec,omitempty"`
	Status WebhookSyncStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type WebhookSyncList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WebhookSync `json:"items"`
}
