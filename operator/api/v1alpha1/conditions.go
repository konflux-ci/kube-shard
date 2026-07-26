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

const (
	// ConditionSecondaryHealthy indicates that the secondary API server is healthy.
	ConditionSecondaryHealthy = "SecondaryHealthy"

	// ConditionCRDsInstalled indicates that all target CRDs are installed on the secondary.
	ConditionCRDsInstalled = "CRDsInstalled"

	// ConditionAPIServicesRegistered indicates that APIService objects are registered on the primary.
	ConditionAPIServicesRegistered = "APIServicesRegistered"

	// ConditionCRDConflictDetected indicates that conflicting CRDs exist on the primary for aggregated API groups.
	ConditionCRDConflictDetected = "CRDConflictDetected"

	// ConditionNamespaceSyncReady indicates the namespace sync controller is active.
	ConditionNamespaceSyncReady = "NamespaceSyncReady"

	// ConditionWebhookSyncReady indicates the webhook sync controller is active.
	ConditionWebhookSyncReady = "WebhookSyncReady"

	// ConditionReconciled indicates whether the last reconcile loop completed successfully.
	ConditionReconciled = "Reconciled"
)

const (
	PhaseProvisioning = "Provisioning"
	PhaseBlocked      = "Blocked"
	PhaseReady        = "Ready"
	PhaseDegraded     = "Degraded"
	PhaseError        = "Error"
	PhaseWaiting      = "Waiting"
)
