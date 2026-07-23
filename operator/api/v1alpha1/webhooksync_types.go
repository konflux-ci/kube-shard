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

type WebhookSyncSpec struct {
	// ShardRef references the parent APIShard.
	ShardRef string `json:"shardRef"`

	// SourceLabelSelector selects webhook configurations on the primary to sync.
	SourceLabelSelector metav1.LabelSelector `json:"sourceLabelSelector"`

	// SourceNames explicitly names webhook configurations to sync (alternative to selector).
	SourceNames []string `json:"sourceNames,omitempty"`

	// SyncMutating controls whether MutatingWebhookConfigurations are synced.
	// +kubebuilder:default=true
	SyncMutating bool `json:"syncMutating,omitempty"`

	// SyncValidating controls whether ValidatingWebhookConfigurations are synced.
	// +kubebuilder:default=true
	SyncValidating bool `json:"syncValidating,omitempty"`
}

type SyncedWebhook struct {
	Name     string      `json:"name"`
	Kind     string      `json:"kind"`
	SyncedAt metav1.Time `json:"syncedAt"`
}

type WebhookSyncStatus struct {
	Phase              string             `json:"phase,omitempty"`
	SyncedWebhooks     []SyncedWebhook    `json:"syncedWebhooks,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Shard",type=string,JSONPath=`.spec.shardRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
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

func init() {
	SchemeBuilder.Register(&WebhookSync{}, &WebhookSyncList{})
}
