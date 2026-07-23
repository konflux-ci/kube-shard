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

package aggregation

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
	"github.com/konflux-ci/kube-shard/operator/internal/resources"
)

// ReconcileAPIServices ensures APIService objects are registered on the primary cluster
// pointing to the secondary API server's service.
func ReconcileAPIServices(ctx context.Context, c client.Client, shard *kubeshardv1alpha1.APIShard, caBundle []byte) error {
	logger := log.FromContext(ctx)

	serviceName := resources.SecondaryServiceName(shard)
	serviceNamespace := shard.Spec.TargetNamespace

	for _, apiGroup := range shard.Spec.APIGroups {
		for _, version := range apiGroup.Versions {
			apiServiceName := apiServiceNameFor(version, apiGroup.Group)
			logger.Info("Reconciling APIService", "name", apiServiceName)

			desired := &apiregistrationv1.APIService{
				ObjectMeta: metav1.ObjectMeta{
					Name: apiServiceName,
					Labels: map[string]string{
						"app.kubernetes.io/managed-by": "kube-shard-operator",
						"app.kubernetes.io/instance":   shard.Name,
					},
				},
				Spec: apiregistrationv1.APIServiceSpec{
					Group:                apiGroup.Group,
					Version:              version,
					GroupPriorityMinimum: 1000,
					VersionPriority:      100,
					Service: &apiregistrationv1.ServiceReference{
						Name:      serviceName,
						Namespace: serviceNamespace,
					},
					CABundle:              caBundle,
					InsecureSkipTLSVerify: len(caBundle) == 0,
				},
			}

			existing := &apiregistrationv1.APIService{}
			err := c.Get(ctx, types.NamespacedName{Name: apiServiceName}, existing)
			if err != nil {
				if client.IgnoreNotFound(err) != nil {
					return fmt.Errorf("getting APIService %s: %w", apiServiceName, err)
				}
				if err := c.Create(ctx, desired); err != nil {
					return fmt.Errorf("creating APIService %s: %w", apiServiceName, err)
				}
				logger.Info("Created APIService", "name", apiServiceName)
				continue
			}

			// Update if needed
			if existing.Spec.Service == nil ||
				existing.Spec.Service.Name != serviceName ||
				existing.Spec.Service.Namespace != serviceNamespace {
				existing.Spec = desired.Spec
				existing.Labels = desired.Labels
				if err := c.Update(ctx, existing); err != nil {
					return fmt.Errorf("updating APIService %s: %w", apiServiceName, err)
				}
				logger.Info("Updated APIService", "name", apiServiceName)
			}
		}
	}

	return nil
}

// DeleteAPIServices removes all APIService objects managed by this shard.
func DeleteAPIServices(ctx context.Context, c client.Client, shard *kubeshardv1alpha1.APIShard) error {
	logger := log.FromContext(ctx)

	for _, apiGroup := range shard.Spec.APIGroups {
		for _, version := range apiGroup.Versions {
			apiServiceName := apiServiceNameFor(version, apiGroup.Group)
			existing := &apiregistrationv1.APIService{}
			err := c.Get(ctx, types.NamespacedName{Name: apiServiceName}, existing)
			if err != nil {
				if client.IgnoreNotFound(err) != nil {
					return err
				}
				continue
			}

			// Only delete if managed by us
			if existing.Labels["app.kubernetes.io/managed-by"] != "kube-shard-operator" {
				continue
			}

			if err := c.Delete(ctx, existing); err != nil {
				return fmt.Errorf("deleting APIService %s: %w", apiServiceName, err)
			}
			logger.Info("Deleted APIService", "name", apiServiceName)
		}
	}

	return nil
}

func apiServiceNameFor(version, group string) string {
	return fmt.Sprintf("%s.%s", version, group)
}
