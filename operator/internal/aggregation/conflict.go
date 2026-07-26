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
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

// ConflictResult contains information about CRD conflicts detected on the primary.
type ConflictResult struct {
	HasConflict     bool
	ConflictingCRDs []string
	Message         string
}

// DetectCRDConflicts checks whether CRDs exist on the primary cluster for the API groups
// that are supposed to be served by the secondary API server. When CRDs exist on the
// primary for an aggregated group, the kube-apiserver creates "Local APIServices" that
// shadow any registered APIService with a service reference, preventing aggregation.
func DetectCRDConflicts(ctx context.Context, c client.Client, shard *kubeshardv1alpha1.APIShard) (*ConflictResult, error) {
	logger := log.FromContext(ctx)

	groups := make([]string, 0, len(shard.Spec.APIGroups))
	for _, g := range shard.Spec.APIGroups {
		groups = append(groups, g.Group)
	}
	aggregatedGroups := sets.New[string](groups...)

	crdList := &apiextensionsv1.CustomResourceDefinitionList{}
	if err := c.List(ctx, crdList); err != nil {
		return nil, fmt.Errorf("listing CRDs: %w", err)
	}

	var conflicting []string
	for i := range crdList.Items {
		crd := &crdList.Items[i]
		if aggregatedGroups.Has(crd.Spec.Group) {
			conflicting = append(conflicting, crd.Name)
			logger.Info("Detected conflicting CRD on primary",
				"crd", crd.Name,
				"group", crd.Spec.Group,
				"shard", shard.Name,
			)
		}
	}

	if len(conflicting) == 0 {
		return &ConflictResult{HasConflict: false}, nil
	}

	return &ConflictResult{
		HasConflict:     true,
		ConflictingCRDs: conflicting,
		Message:         fmt.Sprintf("CRDs on primary conflict with aggregation: %s", strings.Join(conflicting, ", ")),
	}, nil
}
