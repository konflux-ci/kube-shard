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

package namespacesync

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
	"github.com/konflux-ci/kube-shard/operator/internal/secondary"
)

const (
	namespaceSyncRequeue      = 60 * time.Second
	namespaceSyncErrorRequeue = 30 * time.Second
)

var systemNamespaces = sets.New[string](
	"kube-system",
	"kube-public",
	"kube-node-lease",
	"default",
)

// Reconciler reconciles a NamespaceSync object.
type Reconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ClientProvider *secondary.ClientProvider
}

// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=namespacesyncs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=namespacesyncs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=namespacesyncs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile synchronises namespaces from the primary cluster to the secondary
// API server. It computes the desired set of namespaces (matching the label
// selector minus exclusions), creates missing ones on the secondary, and
// deletes orphaned ones that are no longer desired.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var nsSync kubeshardv1alpha1.NamespaceSync
	if err := r.Get(ctx, req.NamespacedName, &nsSync); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	secondaryClient, err := r.buildSecondaryClient(ctx, &nsSync)
	if err != nil {
		logger.Error(err, "Failed to build secondary client")
		meta.SetStatusCondition(&nsSync.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "SecondaryUnavailable",
			Message:            err.Error(),
			ObservedGeneration: nsSync.Generation,
		})
		nsSync.Status.ObservedGeneration = nsSync.Generation
		if updateErr := r.Status().Update(ctx, &nsSync); updateErr != nil {
			logger.Error(updateErr, "Failed to update NamespaceSync status")
		}
		return ctrl.Result{RequeueAfter: namespaceSyncErrorRequeue}, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(&nsSync.Spec.LabelSelector)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("invalid label selector: %w", err)
	}

	var primaryNamespaces corev1.NamespaceList
	if err := r.List(ctx, &primaryNamespaces, &client.ListOptions{
		LabelSelector: selector,
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing primary namespaces: %w", err)
	}

	excluded := sets.New[string](nsSync.Spec.ExcludeNamespaces...)

	desiredNames := sets.New[string]()
	for i := range primaryNamespaces.Items {
		ns := &primaryNamespaces.Items[i]
		if excluded.Has(ns.Name) {
			continue
		}
		desiredNames.Insert(ns.Name)
	}

	var secondaryNamespaces corev1.NamespaceList
	if err := secondaryClient.List(ctx, &secondaryNamespaces); err != nil {
		logger.Error(err, "Failed to list secondary namespaces")
		meta.SetStatusCondition(&nsSync.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "SecondaryUnavailable",
			Message:            fmt.Sprintf("failed to list secondary namespaces: %v", err),
			ObservedGeneration: nsSync.Generation,
		})
		nsSync.Status.ObservedGeneration = nsSync.Generation
		if updateErr := r.Status().Update(ctx, &nsSync); updateErr != nil {
			logger.Error(updateErr, "Failed to update NamespaceSync status")
		}
		return ctrl.Result{RequeueAfter: namespaceSyncErrorRequeue}, nil
	}

	secondaryExisting := sets.New[string]()
	for i := range secondaryNamespaces.Items {
		secondaryExisting.Insert(secondaryNamespaces.Items[i].Name)
	}

	var syncErrors []error

	// Create missing namespaces on secondary
	for i := range primaryNamespaces.Items {
		ns := &primaryNamespaces.Items[i]
		if excluded.Has(ns.Name) || secondaryExisting.Has(ns.Name) {
			continue
		}
		if err := r.ensureNamespaceOnSecondary(ctx, secondaryClient, ns); err != nil {
			syncErrors = append(syncErrors, fmt.Errorf("namespace %s: %w", ns.Name, err))
		}
	}

	// Delete orphaned namespaces from secondary
	for i := range secondaryNamespaces.Items {
		name := secondaryNamespaces.Items[i].Name
		if desiredNames.Has(name) || systemNamespaces.Has(name) {
			continue
		}
		if err := r.deleteNamespaceOnSecondary(ctx, secondaryClient, name); err != nil {
			syncErrors = append(syncErrors, fmt.Errorf("deleting namespace %s: %w", name, err))
		}
	}

	now := metav1.Now()
	nsSync.Status.SyncedNamespaces = int32(desiredNames.Len())
	nsSync.Status.LastSyncTime = &now
	nsSync.Status.ObservedGeneration = nsSync.Generation

	if len(syncErrors) > 0 {
		meta.SetStatusCondition(&nsSync.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "SyncErrors",
			Message:            fmt.Sprintf("%d namespace(s) failed to sync", len(syncErrors)),
			ObservedGeneration: nsSync.Generation,
		})
	} else {
		meta.SetStatusCondition(&nsSync.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "SyncComplete",
			Message:            fmt.Sprintf("%d namespace(s) synced", desiredNames.Len()),
			ObservedGeneration: nsSync.Generation,
		})
	}

	if err := r.Status().Update(ctx, &nsSync); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: namespaceSyncRequeue}, nil
}

// buildSecondaryClient returns a cached controller-runtime client for the
// secondary API server described by the NamespaceSync's connection spec.
func (r *Reconciler) buildSecondaryClient(ctx context.Context, nsSync *kubeshardv1alpha1.NamespaceSync) (client.Client, error) {
	cacheKey := fmt.Sprintf("%s/%s", nsSync.Namespace, nsSync.Name)
	return r.ClientProvider.BuildClient(ctx, r.Client, nsSync.Spec.SecondaryConnection, nsSync.Namespace, cacheKey)
}

// ensureNamespaceOnSecondary creates the given namespace on the secondary API
// server if it does not already exist, copying filtered labels from the primary.
func (r *Reconciler) ensureNamespaceOnSecondary(ctx context.Context, secondaryClient client.Client, ns *corev1.Namespace) error {
	newNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ns.Name,
			Labels: filterLabels(ns.Labels),
		},
	}
	err := secondaryClient.Create(ctx, newNS)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// deleteNamespaceOnSecondary removes a namespace from the secondary API server.
// System namespaces are never deleted.
func (r *Reconciler) deleteNamespaceOnSecondary(ctx context.Context, secondaryClient client.Client, name string) error {
	if systemNamespaces.Has(name) {
		return nil
	}
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	err := secondaryClient.Delete(ctx, ns)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// filterLabels returns a copy of the label map with Kubernetes-internal labels
// (e.g. kubernetes.io/metadata.name) removed so they are not propagated to the
// secondary.
func filterLabels(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	filtered := make(map[string]string, len(src))
	for k, v := range src {
		if k == "kubernetes.io/metadata.name" {
			continue
		}
		filtered[k] = v
	}
	return filtered
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeshardv1alpha1.NamespaceSync{}).
		Watches(&corev1.Namespace{}, &namespaceEventHandler{client: r.Client}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(
			secretToNamespaceSyncMapper(mgr.GetClient()),
		)).
		Named("namespacesync").
		Complete(r)
}

// secretToNamespaceSyncMapper returns a MapFunc that maps Secret events to
// NamespaceSync reconcile requests for any NamespaceSync whose connection spec
// references the changed Secret.
func secretToNamespaceSyncMapper(c client.Client) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list kubeshardv1alpha1.NamespaceSyncList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for i := range list.Items {
			ns := &list.Items[i]
			if ns.Spec.SecondaryConnection.AuthSecretRef.Name == obj.GetName() ||
				ns.Spec.SecondaryConnection.CASecretRef.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      ns.Name,
						Namespace: ns.Namespace,
					},
				})
			}
		}
		return requests
	}
}
