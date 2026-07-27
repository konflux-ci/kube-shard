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

package periodic

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// NewTickerSource returns a controller-runtime Source that enqueues reconcile
// requests at regular intervals, and a manager.Runnable that drives the ticker.
// The caller must register the Runnable with the manager via mgr.Add().
//
// listFunc is called on each tick to determine which objects to reconcile.
// The channel has a buffer of 1; if a tick arrives while the previous one is
// still being processed, it is dropped (the next tick will fire normally).
func NewTickerSource(
	interval time.Duration,
	listFunc func(ctx context.Context) []reconcile.Request,
) (source.Source, manager.Runnable) {
	ch := make(chan event.GenericEvent, 1)

	runnable := manager.RunnableFunc(func(ctx context.Context) error {
		logger := log.FromContext(ctx).WithName("periodic-ticker")
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				sentinel := event.GenericEvent{
					Object: &metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name: "periodic-sync",
						},
					},
				}
				select {
				case ch <- sentinel:
					logger.V(2).Info("Periodic tick sent")
				default:
					logger.V(2).Info("Periodic tick dropped (channel full)")
				}
			}
		}
	})

	src := source.Channel(ch, handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, _ client.Object) []reconcile.Request {
			return listFunc(ctx)
		},
	))

	return src, runnable
}
