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

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

// webhookEventHandler enqueues all WebhookSync objects when a webhook configuration changes.
type webhookEventHandler struct {
	client client.Client
}

func (h *webhookEventHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueueAll(ctx, q)
}

func (h *webhookEventHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueueAll(ctx, q)
}

func (h *webhookEventHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueueAll(ctx, q)
}

func (h *webhookEventHandler) Generic(ctx context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueueAll(ctx, q)
}

func (h *webhookEventHandler) enqueueAll(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	var list kubeshardv1alpha1.WebhookSyncList
	if err := h.client.List(ctx, &list); err != nil {
		return
	}
	for i := range list.Items {
		q.Add(reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name: list.Items[i].Name,
			},
		})
	}
}
