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

package webhooksync

import (
	"context"
	"fmt"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
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
	requeueInterval      = 60 * time.Second
	errorRequeueInterval = 30 * time.Second
)

// Reconciler reconciles a WebhookSync object.
type Reconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ClientProvider *secondary.ClientProvider
}

// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=webhooksyncs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=webhooksyncs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=webhooksyncs/finalizers,verbs=update
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=mutatingwebhookconfigurations,verbs=get;list;watch
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingwebhookconfigurations,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile synchronises webhook configurations from the primary cluster to the
// secondary API server. It filters webhooks whose rules match the configured API
// groups, transforms their ClientConfig from service references to URLs, and
// deletes stale webhooks that no longer match.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var whSync kubeshardv1alpha1.WebhookSync
	if err := r.Get(ctx, req.NamespacedName, &whSync); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	statusBefore := whSync.Status.DeepCopy()

	secondaryClient, err := r.buildSecondaryClient(ctx, &whSync)
	if err != nil {
		logger.Error(err, "Failed to build secondary client")
		meta.SetStatusCondition(&whSync.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "SecondaryUnavailable",
			Message:            fmt.Sprintf("Cannot connect to secondary: %v", err),
			ObservedGeneration: whSync.Generation,
		})
		whSync.Status.ObservedGeneration = whSync.Generation
		if !equality.Semantic.DeepEqual(statusBefore, &whSync.Status) {
			if updateErr := r.Status().Update(ctx, &whSync); updateErr != nil {
				logger.Error(updateErr, "Failed to update WebhookSync status")
			}
		}
		return ctrl.Result{RequeueAfter: errorRequeueInterval}, nil
	}

	targetGroups := sets.New[string](whSync.Spec.APIGroups...)

	var validatingCount, mutatingCount int32
	var syncErrors []error
	syncedValidating := sets.New[string]()
	syncedMutating := sets.New[string]()

	// Sync ValidatingWebhookConfigurations
	var vwhcList admissionregistrationv1.ValidatingWebhookConfigurationList
	if err := r.List(ctx, &vwhcList); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing ValidatingWebhookConfigurations: %w", err)
	}

	for i := range vwhcList.Items {
		vwhc := &vwhcList.Items[i]
		if !shouldSyncValidatingWebhook(vwhc.Webhooks, targetGroups) {
			continue
		}

		transformed := transformValidatingWebhook(vwhc)
		existing := &admissionregistrationv1.ValidatingWebhookConfiguration{}
		err := secondaryClient.Get(ctx, types.NamespacedName{Name: transformed.Name}, existing)
		if apierrors.IsNotFound(err) {
			err = secondaryClient.Create(ctx, transformed)
		} else if err == nil {
			existing.Webhooks = transformed.Webhooks
			existing.Labels = transformed.Labels
			existing.Annotations = transformed.Annotations
			err = secondaryClient.Update(ctx, existing)
		}

		if err != nil {
			syncErrors = append(syncErrors, fmt.Errorf("syncing ValidatingWebhookConfiguration %s: %w", vwhc.Name, err))
			logger.Error(err, "Failed to sync ValidatingWebhookConfiguration", "name", vwhc.Name)
			continue
		}

		syncedValidating.Insert(vwhc.Name)
		validatingCount++
	}

	// Sync MutatingWebhookConfigurations
	var mwhcList admissionregistrationv1.MutatingWebhookConfigurationList
	if err := r.List(ctx, &mwhcList); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing MutatingWebhookConfigurations: %w", err)
	}

	for i := range mwhcList.Items {
		mwhc := &mwhcList.Items[i]
		if !shouldSyncMutatingWebhook(mwhc.Webhooks, targetGroups) {
			continue
		}

		transformed := transformMutatingWebhook(mwhc)
		existing := &admissionregistrationv1.MutatingWebhookConfiguration{}
		err := secondaryClient.Get(ctx, types.NamespacedName{Name: transformed.Name}, existing)
		if apierrors.IsNotFound(err) {
			err = secondaryClient.Create(ctx, transformed)
		} else if err == nil {
			existing.Webhooks = transformed.Webhooks
			existing.Labels = transformed.Labels
			existing.Annotations = transformed.Annotations
			err = secondaryClient.Update(ctx, existing)
		}

		if err != nil {
			syncErrors = append(syncErrors, fmt.Errorf("syncing MutatingWebhookConfiguration %s: %w", mwhc.Name, err))
			logger.Error(err, "Failed to sync MutatingWebhookConfiguration", "name", mwhc.Name)
			continue
		}

		syncedMutating.Insert(mwhc.Name)
		mutatingCount++
	}

	// Delete stale webhooks on secondary that no longer match primary
	if err := r.deleteStaleValidating(ctx, secondaryClient, syncedValidating); err != nil {
		syncErrors = append(syncErrors, err)
	}
	if err := r.deleteStaleMutating(ctx, secondaryClient, syncedMutating); err != nil {
		syncErrors = append(syncErrors, err)
	}

	whSync.Status.SyncedWebhooks = kubeshardv1alpha1.SyncedWebhookCounts{
		Validating: validatingCount,
		Mutating:   mutatingCount,
	}
	whSync.Status.ObservedGeneration = whSync.Generation

	if len(syncErrors) > 0 {
		r.ClientProvider.Invalidate(fmt.Sprintf("%s/%s", whSync.Namespace, whSync.Name))
		meta.SetStatusCondition(&whSync.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "SyncErrors",
			Message:            fmt.Sprintf("%d webhook(s) failed to sync", len(syncErrors)),
			ObservedGeneration: whSync.Generation,
		})
	} else {
		meta.SetStatusCondition(&whSync.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "SyncComplete",
			Message:            fmt.Sprintf("Synced %d validating and %d mutating webhook(s)", validatingCount, mutatingCount),
			ObservedGeneration: whSync.Generation,
		})
	}

	if !equality.Semantic.DeepEqual(statusBefore, &whSync.Status) {
		now := metav1.Now()
		whSync.Status.LastSyncTime = &now
		if err := r.Status().Update(ctx, &whSync); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// buildSecondaryClient returns a cached controller-runtime client for the
// secondary API server described by the WebhookSync's connection spec.
func (r *Reconciler) buildSecondaryClient(ctx context.Context, whSync *kubeshardv1alpha1.WebhookSync) (client.Client, error) {
	cacheKey := fmt.Sprintf("%s/%s", whSync.Namespace, whSync.Name)
	return r.ClientProvider.BuildClient(ctx, r.Client, whSync.Spec.SecondaryConnection, whSync.Namespace, cacheKey)
}

// deleteStaleValidating removes ValidatingWebhookConfigurations from the
// secondary that are not in the currentNames set (i.e. no longer match the
// primary).
func (r *Reconciler) deleteStaleValidating(ctx context.Context, secondaryClient client.Client, currentNames sets.Set[string]) error {
	logger := log.FromContext(ctx)

	var secondaryList admissionregistrationv1.ValidatingWebhookConfigurationList
	if err := secondaryClient.List(ctx, &secondaryList); err != nil {
		return fmt.Errorf("listing ValidatingWebhookConfigurations on secondary: %w", err)
	}

	for i := range secondaryList.Items {
		item := &secondaryList.Items[i]
		if !currentNames.Has(item.Name) {
			if err := secondaryClient.Delete(ctx, item); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "Failed to delete stale ValidatingWebhookConfiguration from secondary", "name", item.Name)
				return fmt.Errorf("deleting stale ValidatingWebhookConfiguration %s: %w", item.Name, err)
			}
			logger.Info("Deleted stale ValidatingWebhookConfiguration from secondary", "name", item.Name)
		}
	}
	return nil
}

// deleteStaleMutating removes MutatingWebhookConfigurations from the secondary
// that are not in the currentNames set (i.e. no longer match the primary).
func (r *Reconciler) deleteStaleMutating(ctx context.Context, secondaryClient client.Client, currentNames sets.Set[string]) error {
	logger := log.FromContext(ctx)

	var secondaryList admissionregistrationv1.MutatingWebhookConfigurationList
	if err := secondaryClient.List(ctx, &secondaryList); err != nil {
		return fmt.Errorf("listing MutatingWebhookConfigurations on secondary: %w", err)
	}

	for i := range secondaryList.Items {
		item := &secondaryList.Items[i]
		if !currentNames.Has(item.Name) {
			if err := secondaryClient.Delete(ctx, item); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "Failed to delete stale MutatingWebhookConfiguration from secondary", "name", item.Name)
				return fmt.Errorf("deleting stale MutatingWebhookConfiguration %s: %w", item.Name, err)
			}
			logger.Info("Deleted stale MutatingWebhookConfiguration from secondary", "name", item.Name)
		}
	}
	return nil
}

// shouldSyncValidatingWebhook returns true if any rule in the webhook list
// targets one of the targetGroups or the wildcard group "*".
func shouldSyncValidatingWebhook(webhooks []admissionregistrationv1.ValidatingWebhook, targetGroups sets.Set[string]) bool {
	for i := range webhooks {
		for j := range webhooks[i].Rules {
			for _, group := range webhooks[i].Rules[j].APIGroups {
				if targetGroups.Has(group) || group == "*" {
					return true
				}
			}
		}
	}
	return false
}

// shouldSyncMutatingWebhook returns true if any rule in the webhook list
// targets one of the targetGroups or the wildcard group "*".
func shouldSyncMutatingWebhook(webhooks []admissionregistrationv1.MutatingWebhook, targetGroups sets.Set[string]) bool {
	for i := range webhooks {
		for j := range webhooks[i].Rules {
			for _, group := range webhooks[i].Rules[j].APIGroups {
				if targetGroups.Has(group) || group == "*" {
					return true
				}
			}
		}
	}
	return false
}

// transformMutatingWebhook returns a deep copy of src suitable for the
// secondary: cluster metadata is stripped, and service-based ClientConfigs are
// converted to URL-based ones so the secondary can reach webhook servers
// directly via in-cluster DNS.
func transformMutatingWebhook(src *admissionregistrationv1.MutatingWebhookConfiguration) *admissionregistrationv1.MutatingWebhookConfiguration {
	result := src.DeepCopy()
	result.ResourceVersion = ""
	result.UID = ""
	result.OwnerReferences = nil
	result.ManagedFields = nil
	result.Generation = 0
	result.Finalizers = nil

	for i := range result.Webhooks {
		wh := &result.Webhooks[i]
		if wh.ClientConfig.Service != nil {
			svcRef := wh.ClientConfig.Service
			url := fmt.Sprintf("https://%s.%s.svc:%d%s",
				svcRef.Name,
				svcRef.Namespace,
				servicePort(svcRef.Port),
				servicePath(svcRef.Path),
			)
			wh.ClientConfig.URL = &url
			wh.ClientConfig.Service = nil
		}
	}

	return result
}

// transformValidatingWebhook returns a deep copy of src suitable for the
// secondary: cluster metadata is stripped, and service-based ClientConfigs are
// converted to URL-based ones so the secondary can reach webhook servers
// directly via in-cluster DNS.
func transformValidatingWebhook(src *admissionregistrationv1.ValidatingWebhookConfiguration) *admissionregistrationv1.ValidatingWebhookConfiguration {
	result := src.DeepCopy()
	result.ResourceVersion = ""
	result.UID = ""
	result.OwnerReferences = nil
	result.ManagedFields = nil
	result.Generation = 0
	result.Finalizers = nil

	for i := range result.Webhooks {
		wh := &result.Webhooks[i]
		if wh.ClientConfig.Service != nil {
			svcRef := wh.ClientConfig.Service
			url := fmt.Sprintf("https://%s.%s.svc:%d%s",
				svcRef.Name,
				svcRef.Namespace,
				servicePort(svcRef.Port),
				servicePath(svcRef.Path),
			)
			wh.ClientConfig.URL = &url
			wh.ClientConfig.Service = nil
		}
	}

	return result
}

func servicePort(port *int32) int32 {
	if port == nil {
		return 443
	}
	return *port
}

func servicePath(path *string) string {
	if path == nil {
		return ""
	}
	return *path
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeshardv1alpha1.WebhookSync{}).
		Watches(&admissionregistrationv1.MutatingWebhookConfiguration{}, &webhookEventHandler{client: r.Client}).
		Watches(&admissionregistrationv1.ValidatingWebhookConfiguration{}, &webhookEventHandler{client: r.Client}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(
			secretToWebhookSyncMapper(mgr.GetClient()),
		)).
		Named("webhooksync").
		Complete(r)
}

// secretToWebhookSyncMapper returns a MapFunc that maps Secret events to
// WebhookSync reconcile requests for any WebhookSync whose connection spec
// references the changed Secret.
func secretToWebhookSyncMapper(c client.Client) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list kubeshardv1alpha1.WebhookSyncList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for i := range list.Items {
			wh := &list.Items[i]
			if wh.Spec.SecondaryConnection.AuthSecretRef.Name == obj.GetName() ||
				wh.Spec.SecondaryConnection.CASecretRef.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      wh.Name,
						Namespace: wh.Namespace,
					},
				})
			}
		}
		return requests
	}
}
