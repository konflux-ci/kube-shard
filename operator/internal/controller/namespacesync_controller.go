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

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
	"github.com/konflux-ci/kube-shard/operator/internal/secondary"
)

const namespaceSyncRequeue = 60 * time.Second

// NamespaceSyncReconciler reconciles a NamespaceSync object.
type NamespaceSyncReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ClientProvider *secondary.ClientProvider
}

// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=namespacesyncs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=namespacesyncs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=namespacesyncs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

func (r *NamespaceSyncReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var nsSync kubeshardv1alpha1.NamespaceSync
	if err := r.Get(ctx, req.NamespacedName, &nsSync); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Verify parent APIShard exists and is ready
	var shard kubeshardv1alpha1.APIShard
	if err := r.Get(ctx, types.NamespacedName{Name: nsSync.Spec.ShardRef}, &shard); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Parent APIShard not found", "shardRef", nsSync.Spec.ShardRef)
			meta.SetStatusCondition(&nsSync.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "ShardNotFound",
				Message: fmt.Sprintf("APIShard %s not found", nsSync.Spec.ShardRef),
			})
			nsSync.Status.Phase = "Waiting"
			_ = r.Status().Update(ctx, &nsSync)
			return ctrl.Result{RequeueAfter: namespaceSyncRequeue}, nil
		}
		return ctrl.Result{}, err
	}

	if shard.Status.Phase != kubeshardv1alpha1.PhaseReady {
		logger.Info("APIShard not ready yet, requeuing", "shard", shard.Name, "phase", shard.Status.Phase)
		nsSync.Status.Phase = "Waiting"
		meta.SetStatusCondition(&nsSync.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "ShardNotReady",
			Message: fmt.Sprintf("APIShard %s is in phase %s", shard.Name, shard.Status.Phase),
		})
		_ = r.Status().Update(ctx, &nsSync)
		return ctrl.Result{RequeueAfter: namespaceSyncRequeue}, nil
	}

	// Get secondary client
	secondaryClient, _, err := r.getSecondaryClient(&shard)
	if err != nil {
		logger.Error(err, "Failed to get secondary client")
		return ctrl.Result{RequeueAfter: namespaceSyncRequeue}, nil
	}

	// List namespaces on primary matching the label selector
	selector, err := metav1.LabelSelectorAsSelector(&nsSync.Spec.LabelSelector)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("invalid label selector: %w", err)
	}

	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList, &client.ListOptions{
		LabelSelector: selector,
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing namespaces: %w", err)
	}

	excluded := make(map[string]bool, len(nsSync.Spec.ExcludeNamespaces))
	for _, ns := range nsSync.Spec.ExcludeNamespaces {
		excluded[ns] = true
	}

	var synced []kubeshardv1alpha1.SyncedNamespace
	var syncErrors []error

	for i := range nsList.Items {
		ns := &nsList.Items[i]
		if excluded[ns.Name] {
			continue
		}

		if err := r.ensureNamespaceOnSecondary(ctx, secondaryClient, ns); err != nil {
			syncErrors = append(syncErrors, fmt.Errorf("namespace %s: %w", ns.Name, err))
			continue
		}

		synced = append(synced, kubeshardv1alpha1.SyncedNamespace{
			Name:     ns.Name,
			SyncedAt: metav1.Now(),
		})
	}

	nsSync.Status.SyncedNamespaces = synced
	nsSync.Status.ObservedGeneration = nsSync.Generation

	if len(syncErrors) > 0 {
		nsSync.Status.Phase = kubeshardv1alpha1.PhaseDegraded
		meta.SetStatusCondition(&nsSync.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "SyncErrors",
			Message: fmt.Sprintf("%d namespace(s) failed to sync", len(syncErrors)),
		})
	} else {
		nsSync.Status.Phase = kubeshardv1alpha1.PhaseReady
		meta.SetStatusCondition(&nsSync.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionTrue,
			Reason:  "SyncComplete",
			Message: fmt.Sprintf("%d namespace(s) synced", len(synced)),
		})
	}

	if err := r.Status().Update(ctx, &nsSync); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: namespaceSyncRequeue}, nil
}

func (r *NamespaceSyncReconciler) getSecondaryClient(shard *kubeshardv1alpha1.APIShard) (kubernetes.Interface, *secondary.ClientConfig, error) {
	cfg := secondary.ClientConfig{
		Host: shard.Status.SecondaryEndpoint,
	}
	client, _, err := r.ClientProvider.GetOrCreate(shard.Name, cfg)
	if err != nil {
		return nil, nil, err
	}
	return client, &cfg, nil
}

func (r *NamespaceSyncReconciler) ensureNamespaceOnSecondary(ctx context.Context, secondaryClient kubernetes.Interface, ns *corev1.Namespace) error {
	_, err := secondaryClient.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	newNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ns.Name,
			Labels: filterLabels(ns.Labels),
		},
	}
	_, err = secondaryClient.CoreV1().Namespaces().Create(ctx, newNS, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

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
func (r *NamespaceSyncReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Suppress unused import
	_ = labels.Everything()

	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeshardv1alpha1.NamespaceSync{}).
		Watches(&corev1.Namespace{}, &namespaceEventHandler{client: r.Client}).
		Named("namespacesync").
		Complete(r)
}
