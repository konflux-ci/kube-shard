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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
	"github.com/konflux-ci/kube-shard/operator/internal/resources"
)

// ReconcileResult holds the outcome of an APIService reconciliation pass.
type ReconcileResult struct {
	// Registered is the complete set of APIService names now managed by the shard.
	Registered []string
}

// AutoManagedLabelKey is the label key used by the kube-aggregator auto-register
// controller to track APIServices it manages.
const AutoManagedLabelKey = "kube-aggregator.kubernetes.io/automanaged"

// Reconcile creates or updates APIService objects for the desired API groups and
// deletes any previously registered APIServices that are no longer desired.
//
// Each APIService is labelled automanaged=false via SSA with ForceOwnership so
// the kube-aggregator auto-register controller does not reclaim it when CRDs
// exist on the primary for the same API group.
//
// Orphan detection uses the previouslyRegistered list (from APIShard status) rather
// than labels or owner references — this prevents an attacker with APIService write
// access from tricking the operator into deleting resources it doesn't own.
func Reconcile(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	shard *kubeshardv1alpha1.APIShard,
	caBundle []byte,
	previouslyRegistered []string,
	fieldManager string,
) (*ReconcileResult, error) {
	logger := log.FromContext(ctx)

	serviceName := resources.SecondaryServiceName(shard)
	serviceNamespace := shard.Spec.TargetNamespace

	desired := desiredAPIServiceNames(shard)
	desiredSet := sets.New[string](desired...)

	// Apply desired APIServices using Server-Side Apply.
	// SSA only sends fields we manage, avoiding spurious updates from
	// server-defaulted fields (e.g. ServiceReference.Port) that would
	// trigger an infinite reconcile loop via Owns() watch events.
	for _, apiGroup := range shard.Spec.APIGroups {
		for _, version := range apiGroup.Versions {
			name := apiServiceName(version, apiGroup.Group)

			objMeta := metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					AutoManagedLabelKey: "false",
				},
			}

			apiSvc := &apiregistrationv1.APIService{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apiregistration.k8s.io/v1",
					Kind:       "APIService",
				},
				ObjectMeta: objMeta,
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

			if err := controllerutil.SetControllerReference(shard, apiSvc, scheme); err != nil {
				return nil, fmt.Errorf("setting owner reference on APIService %s: %w", name, err)
			}

			if err := c.Patch(ctx, apiSvc, client.Apply, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil { //nolint:staticcheck // migrating to client.Client.Apply() requires ApplyConfiguration types
				return nil, fmt.Errorf("applying APIService %s: %w", name, err)
			}
			logger.V(1).Info("Applied APIService", "name", name)
		}
	}

	// Delete orphaned APIServices: previously registered but no longer desired
	for _, name := range previouslyRegistered {
		if desiredSet.Has(name) {
			continue
		}

		existing := &apiregistrationv1.APIService{}
		err := c.Get(ctx, types.NamespacedName{Name: name}, existing)
		if err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, fmt.Errorf("getting orphaned APIService %s: %w", name, err)
			}
			continue
		}

		if err := c.Delete(ctx, existing); err != nil {
			return nil, fmt.Errorf("deleting orphaned APIService %s: %w", name, err)
		}
		logger.Info("Deleted orphaned APIService", "name", name)
	}

	return &ReconcileResult{Registered: desired}, nil
}

// CheckAvailability verifies that all registered APIService objects have the
// Available condition set to True by the kube-aggregator. Returns true when
// every APIService is available, or false with a message describing the first
// unavailable one.
func CheckAvailability(
	ctx context.Context,
	c client.Client,
	registeredNames []string,
) (bool, string) {
	for _, name := range registeredNames {
		apiSvc := &apiregistrationv1.APIService{}
		if err := c.Get(ctx, types.NamespacedName{Name: name}, apiSvc); err != nil {
			return false, fmt.Sprintf("APIService %s not found: %v", name, err)
		}
		available := false
		for _, cond := range apiSvc.Status.Conditions {
			if cond.Type == apiregistrationv1.Available {
				if cond.Status != apiregistrationv1.ConditionTrue {
					return false, fmt.Sprintf(
						"APIService %s not yet available: %s",
						name, cond.Message,
					)
				}
				available = true
				break
			}
		}
		if !available {
			return false, fmt.Sprintf(
				"APIService %s has no Available condition yet", name,
			)
		}
	}
	return true, ""
}

// DesiredAPIServiceNames returns the list of APIService names that should exist
// for the given shard based on its spec.
func DesiredAPIServiceNames(shard *kubeshardv1alpha1.APIShard) []string {
	return desiredAPIServiceNames(shard)
}

func desiredAPIServiceNames(shard *kubeshardv1alpha1.APIShard) []string {
	var names []string
	for _, apiGroup := range shard.Spec.APIGroups {
		for _, version := range apiGroup.Versions {
			names = append(names, apiServiceName(version, apiGroup.Group))
		}
	}
	return names
}

func apiServiceName(version, group string) string {
	return fmt.Sprintf("%s.%s", version, group)
}
