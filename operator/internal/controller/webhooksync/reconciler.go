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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
	"github.com/konflux-ci/kube-shard/operator/internal/secondary"
)

const webhookSyncRequeue = 60 * time.Second

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

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var whSync kubeshardv1alpha1.WebhookSync
	if err := r.Get(ctx, req.NamespacedName, &whSync); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Verify parent APIShard exists and is ready
	var shard kubeshardv1alpha1.APIShard
	if err := r.Get(ctx, types.NamespacedName{Name: whSync.Spec.ShardRef}, &shard); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Parent APIShard not found", "shardRef", whSync.Spec.ShardRef)
			whSync.Status.Phase = kubeshardv1alpha1.PhaseWaiting
			meta.SetStatusCondition(&whSync.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "ShardNotFound",
				Message: fmt.Sprintf("APIShard %s not found", whSync.Spec.ShardRef),
			})
			if updateErr := r.Status().Update(ctx, &whSync); updateErr != nil {
				logger.Error(updateErr, "Failed to update WebhookSync status")
			}
			return ctrl.Result{RequeueAfter: webhookSyncRequeue}, nil
		}
		return ctrl.Result{}, err
	}

	if shard.Status.Phase != kubeshardv1alpha1.PhaseReady {
		whSync.Status.Phase = kubeshardv1alpha1.PhaseWaiting
		meta.SetStatusCondition(&whSync.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "ShardNotReady",
			Message: fmt.Sprintf("APIShard %s is in phase %s", shard.Name, shard.Status.Phase),
		})
		if updateErr := r.Status().Update(ctx, &whSync); updateErr != nil {
			logger.Error(updateErr, "Failed to update WebhookSync status")
		}
		return ctrl.Result{RequeueAfter: webhookSyncRequeue}, nil
	}

	// Get secondary controller-runtime client
	secondaryClient, err := r.getSecondaryClient(&shard)
	if err != nil {
		logger.Error(err, "Failed to get secondary client")
		return ctrl.Result{RequeueAfter: webhookSyncRequeue}, nil
	}

	var synced []kubeshardv1alpha1.SyncedWebhook
	var syncErrors []error

	if whSync.Spec.SyncMutating {
		syncedMutating, errs := r.syncMutatingWebhooks(ctx, &whSync, secondaryClient)
		synced = append(synced, syncedMutating...)
		syncErrors = append(syncErrors, errs...)
	}

	if whSync.Spec.SyncValidating {
		syncedValidating, errs := r.syncValidatingWebhooks(ctx, &whSync, secondaryClient)
		synced = append(synced, syncedValidating...)
		syncErrors = append(syncErrors, errs...)
	}

	whSync.Status.SyncedWebhooks = synced
	whSync.Status.ObservedGeneration = whSync.Generation

	if len(syncErrors) > 0 {
		whSync.Status.Phase = kubeshardv1alpha1.PhaseDegraded
		meta.SetStatusCondition(&whSync.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "SyncErrors",
			Message: fmt.Sprintf("%d webhook(s) failed to sync", len(syncErrors)),
		})
	} else {
		whSync.Status.Phase = kubeshardv1alpha1.PhaseReady
		meta.SetStatusCondition(&whSync.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionTrue,
			Reason:  "SyncComplete",
			Message: fmt.Sprintf("%d webhook(s) synced", len(synced)),
		})
	}

	if err := r.Status().Update(ctx, &whSync); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: webhookSyncRequeue}, nil
}

func (r *Reconciler) getSecondaryClient(shard *kubeshardv1alpha1.APIShard) (client.Client, error) {
	cfg := secondary.ClientConfig{
		Host: shard.Status.SecondaryEndpoint,
	}
	return r.ClientProvider.GetOrCreate(shard.Name, cfg)
}

func (r *Reconciler) syncMutatingWebhooks(
	ctx context.Context,
	whSync *kubeshardv1alpha1.WebhookSync,
	secondaryClient client.Client,
) ([]kubeshardv1alpha1.SyncedWebhook, []error) {
	logger := log.FromContext(ctx)

	var mwhcList admissionregistrationv1.MutatingWebhookConfigurationList
	opts, err := r.listOptions(whSync)
	if err != nil {
		return nil, []error{err}
	}
	if err := r.List(ctx, &mwhcList, opts...); err != nil {
		return nil, []error{fmt.Errorf("listing MutatingWebhookConfigurations: %w", err)}
	}

	synced := make([]kubeshardv1alpha1.SyncedWebhook, 0, len(mwhcList.Items))
	var errs []error

	for i := range mwhcList.Items {
		mwhc := &mwhcList.Items[i]
		if !r.shouldSync(whSync, mwhc.Name) {
			continue
		}

		transformed := transformMutatingWebhook(mwhc)

		existing := &admissionregistrationv1.MutatingWebhookConfiguration{}
		err := secondaryClient.Get(ctx, types.NamespacedName{Name: transformed.Name}, existing)
		if apierrors.IsNotFound(err) {
			err = secondaryClient.Create(ctx, transformed)
		} else if err == nil {
			existing.Webhooks = transformed.Webhooks
			err = secondaryClient.Update(ctx, existing)
		}

		if err != nil {
			errs = append(errs, fmt.Errorf("syncing MutatingWebhookConfiguration %s: %w", mwhc.Name, err))
			logger.Error(err, "Failed to sync MutatingWebhookConfiguration", "name", mwhc.Name)
			continue
		}

		synced = append(synced, kubeshardv1alpha1.SyncedWebhook{
			Name:     mwhc.Name,
			Kind:     "MutatingWebhookConfiguration",
			SyncedAt: metav1.Now(),
		})
	}

	return synced, errs
}

func (r *Reconciler) syncValidatingWebhooks(
	ctx context.Context,
	whSync *kubeshardv1alpha1.WebhookSync,
	secondaryClient client.Client,
) ([]kubeshardv1alpha1.SyncedWebhook, []error) {
	logger := log.FromContext(ctx)

	var vwhcList admissionregistrationv1.ValidatingWebhookConfigurationList
	opts, err := r.listOptions(whSync)
	if err != nil {
		return nil, []error{err}
	}
	if err := r.List(ctx, &vwhcList, opts...); err != nil {
		return nil, []error{fmt.Errorf("listing ValidatingWebhookConfigurations: %w", err)}
	}

	synced := make([]kubeshardv1alpha1.SyncedWebhook, 0, len(vwhcList.Items))
	var errs []error

	for i := range vwhcList.Items {
		vwhc := &vwhcList.Items[i]
		if !r.shouldSync(whSync, vwhc.Name) {
			continue
		}

		transformed := transformValidatingWebhook(vwhc)

		existing := &admissionregistrationv1.ValidatingWebhookConfiguration{}
		err := secondaryClient.Get(ctx, types.NamespacedName{Name: transformed.Name}, existing)
		if apierrors.IsNotFound(err) {
			err = secondaryClient.Create(ctx, transformed)
		} else if err == nil {
			existing.Webhooks = transformed.Webhooks
			err = secondaryClient.Update(ctx, existing)
		}

		if err != nil {
			errs = append(errs, fmt.Errorf("syncing ValidatingWebhookConfiguration %s: %w", vwhc.Name, err))
			logger.Error(err, "Failed to sync ValidatingWebhookConfiguration", "name", vwhc.Name)
			continue
		}

		synced = append(synced, kubeshardv1alpha1.SyncedWebhook{
			Name:     vwhc.Name,
			Kind:     "ValidatingWebhookConfiguration",
			SyncedAt: metav1.Now(),
		})
	}

	return synced, errs
}

func (r *Reconciler) listOptions(whSync *kubeshardv1alpha1.WebhookSync) ([]client.ListOption, error) {
	selector, err := metav1.LabelSelectorAsSelector(&whSync.Spec.SourceLabelSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
	}
	if selector.Empty() {
		return nil, nil
	}
	return []client.ListOption{
		&client.ListOptions{LabelSelector: selector},
	}, nil
}

func (r *Reconciler) shouldSync(whSync *kubeshardv1alpha1.WebhookSync, name string) bool {
	if len(whSync.Spec.SourceNames) == 0 {
		return true
	}
	for _, n := range whSync.Spec.SourceNames {
		if n == name {
			return true
		}
	}
	return false
}

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
		Named("webhooksync").
		Complete(r)
}
