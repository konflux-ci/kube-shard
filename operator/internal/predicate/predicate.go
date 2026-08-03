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

package predicate

import (
	"maps"

	appsv1 "k8s.io/api/apps/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// generationOrMetadataChanged returns true if the update is NOT a status-only
// change. It detects spec changes (generation bump), ownerReference changes,
// and label/annotation changes — none of which are reflected in the generation
// field alone.
func generationOrMetadataChanged(oldObj, newObj client.Object) bool {
	if oldObj.GetGeneration() != newObj.GetGeneration() {
		return true
	}
	if !apiequality.Semantic.DeepEqual(oldObj.GetOwnerReferences(), newObj.GetOwnerReferences()) {
		return true
	}
	if !maps.Equal(oldObj.GetLabels(), newObj.GetLabels()) {
		return true
	}
	return !maps.Equal(oldObj.GetAnnotations(), newObj.GetAnnotations())
}

// IgnoreStatusUpdatesPredicate filters out status-only updates.
// Reconciliation triggers on spec changes (generation bump), ownerReference
// changes, and label/annotation changes.
//
// Safe to use on resources with a status subresource AND proper generation
// tracking: Deployments, CRDs, APIServices, and all custom resources with
// /status. Do NOT use on generation-exempt resources (Services, Nodes, PVs)
// or resources without a status subresource (Secrets, ConfigMaps, RBAC).
var IgnoreStatusUpdatesPredicate = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		if e.ObjectOld == nil || e.ObjectNew == nil {
			return true
		}
		return generationOrMetadataChanged(e.ObjectOld, e.ObjectNew)
	},
	CreateFunc:  func(e event.CreateEvent) bool { return true },
	DeleteFunc:  func(e event.DeleteEvent) bool { return true },
	GenericFunc: func(e event.GenericEvent) bool { return true },
}

type replicaCounts struct {
	Replicas, Ready, Available, Updated int32
}

func extractReplicaCounts(obj client.Object) (replicaCounts, bool) {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		return replicaCounts{o.Status.Replicas, o.Status.ReadyReplicas, o.Status.AvailableReplicas, o.Status.UpdatedReplicas}, true
	case *appsv1.StatefulSet:
		return replicaCounts{o.Status.Replicas, o.Status.ReadyReplicas, o.Status.AvailableReplicas, o.Status.UpdatedReplicas}, true
	default:
		return replicaCounts{}, false
	}
}

// DeploymentReadinessPredicate extends IgnoreStatusUpdatesPredicate by also
// triggering on deployment readiness changes (ReadyReplicas, AvailableReplicas,
// etc.) so the controller can react to health changes without polling.
var DeploymentReadinessPredicate = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		if e.ObjectOld == nil || e.ObjectNew == nil {
			return true
		}
		if generationOrMetadataChanged(e.ObjectOld, e.ObjectNew) {
			return true
		}
		oldCounts, ok := extractReplicaCounts(e.ObjectOld)
		if !ok {
			return true
		}
		newCounts, _ := extractReplicaCounts(e.ObjectNew)
		return oldCounts != newCounts
	},
	CreateFunc:  func(e event.CreateEvent) bool { return true },
	DeleteFunc:  func(e event.DeleteEvent) bool { return true },
	GenericFunc: func(e event.GenericEvent) bool { return true },
}

// StatefulSetReadinessPredicate extends IgnoreStatusUpdatesPredicate by also
// triggering on StatefulSet readiness changes so the controller can react to
// health changes without polling.
var StatefulSetReadinessPredicate = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		if e.ObjectOld == nil || e.ObjectNew == nil {
			return true
		}
		if generationOrMetadataChanged(e.ObjectOld, e.ObjectNew) {
			return true
		}
		oldCounts, ok := extractReplicaCounts(e.ObjectOld)
		if !ok {
			return true
		}
		newCounts, _ := extractReplicaCounts(e.ObjectNew)
		return oldCounts != newCounts
	},
	CreateFunc:  func(e event.CreateEvent) bool { return true },
	DeleteFunc:  func(e event.DeleteEvent) bool { return true },
	GenericFunc: func(e event.GenericEvent) bool { return true },
}
