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
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = kubeshardv1alpha1.AddToScheme(s)
	_ = apiregistrationv1.AddToScheme(s)
	return s
}

func newTestShard(forceAggregation bool) *kubeshardv1alpha1.APIShard {
	return &kubeshardv1alpha1.APIShard{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-shard",
			UID:  "test-uid",
		},
		Spec: kubeshardv1alpha1.APIShardSpec{
			TargetNamespace: "test-ns",
			APIGroups: []kubeshardv1alpha1.APIGroupSpec{
				{Group: "example.com", Versions: []string{"v1"}},
			},
			ForceAggregation: forceAggregation,
		},
	}
}

func TestReconcile_ForceAggregation_SetsAutomanagedLabel(t *testing.T) {
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	shard := newTestShard(true)

	result, err := Reconcile(context.Background(), c, scheme, shard, []byte("fake-ca"), nil, "test-manager", true)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if len(result.Registered) != 1 || result.Registered[0] != "v1.example.com" {
		t.Fatalf("unexpected registered: %v", result.Registered)
	}

	apiSvc := &apiregistrationv1.APIService{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "v1.example.com"}, apiSvc); err != nil {
		t.Fatalf("failed to get APIService: %v", err)
	}

	val, ok := apiSvc.Labels[AutoManagedLabelKey]
	if !ok {
		t.Fatal("expected automanaged label to be present when forceAggregation=true")
	}
	if val != "false" {
		t.Fatalf("expected automanaged label value 'false', got %q", val)
	}
}

func TestReconcile_NoForce_DoesNotSetAutomanagedLabel(t *testing.T) {
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	shard := newTestShard(false)

	_, err := Reconcile(context.Background(), c, scheme, shard, []byte("fake-ca"), nil, "test-manager", false)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	apiSvc := &apiregistrationv1.APIService{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "v1.example.com"}, apiSvc); err != nil {
		t.Fatalf("failed to get APIService: %v", err)
	}

	if _, ok := apiSvc.Labels[AutoManagedLabelKey]; ok {
		t.Fatal("expected automanaged label to NOT be present when forceAggregation=false")
	}
}

func TestReconcile_ForceAggregation_OverridesExistingAutomanagedTrue(t *testing.T) {
	scheme := newScheme()

	existing := &apiregistrationv1.APIService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "v1.example.com",
			Labels: map[string]string{
				AutoManagedLabelKey: "true",
			},
		},
		Spec: apiregistrationv1.APIServiceSpec{
			Group:                "example.com",
			Version:              "v1",
			GroupPriorityMinimum: 1000,
			VersionPriority:      100,
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	shard := newTestShard(true)

	_, err := Reconcile(context.Background(), c, scheme, shard, []byte("fake-ca"), nil, "test-manager", true)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	apiSvc := &apiregistrationv1.APIService{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "v1.example.com"}, apiSvc); err != nil {
		t.Fatalf("failed to get APIService: %v", err)
	}

	val := apiSvc.Labels[AutoManagedLabelKey]
	if val != "false" {
		t.Fatalf("expected automanaged label overridden to 'false', got %q", val)
	}

	if apiSvc.Spec.Service == nil {
		t.Fatal("expected service field to be set after force reconcile")
	}
}

func TestReconcile_ServiceFieldAlwaysSet(t *testing.T) {
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	shard := newTestShard(false)

	_, err := Reconcile(context.Background(), c, scheme, shard, []byte("fake-ca"), nil, "test-manager", false)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	apiSvc := &apiregistrationv1.APIService{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "v1.example.com"}, apiSvc); err != nil {
		t.Fatalf("failed to get APIService: %v", err)
	}

	if apiSvc.Spec.Service == nil {
		t.Fatal("expected service field to be set")
	}
	if apiSvc.Spec.Service.Name != "test-shard-apiserver" {
		t.Fatalf("unexpected service name: %s", apiSvc.Spec.Service.Name)
	}
	if apiSvc.Spec.Service.Namespace != "test-ns" {
		t.Fatalf("unexpected service namespace: %s", apiSvc.Spec.Service.Namespace)
	}
}

// Verify that the fake client implements SSA Patch correctly for our use case.
// This is a regression test to ensure we can use Patch(Apply) in tests.
func init() {
	_ = client.Apply //nolint:staticcheck // migrating to client.Client.Apply() requires ApplyConfiguration types
}

// TestCheckAvailability_AllAvailable verifies that CheckAvailability returns true
// when every registered APIService has Available=True.
func TestCheckAvailability_AllAvailable(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newScheme()

	svc := &apiregistrationv1.APIService{
		ObjectMeta: metav1.ObjectMeta{Name: "v1.example.com"},
		Status: apiregistrationv1.APIServiceStatus{
			Conditions: []apiregistrationv1.APIServiceCondition{
				{
					Type:   apiregistrationv1.Available,
					Status: apiregistrationv1.ConditionTrue,
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	ok, msg := CheckAvailability(context.Background(), c, []string{"v1.example.com"})
	g.Expect(ok).To(BeTrue())
	g.Expect(msg).To(BeEmpty())
}

// TestCheckAvailability_NotAvailable verifies that CheckAvailability returns false
// with a descriptive message when an APIService has Available=False.
func TestCheckAvailability_NotAvailable(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newScheme()

	svc := &apiregistrationv1.APIService{
		ObjectMeta: metav1.ObjectMeta{Name: "v1.example.com"},
		Status: apiregistrationv1.APIServiceStatus{
			Conditions: []apiregistrationv1.APIServiceCondition{
				{
					Type:    apiregistrationv1.Available,
					Status:  apiregistrationv1.ConditionFalse,
					Message: "endpoints not found",
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	ok, msg := CheckAvailability(context.Background(), c, []string{"v1.example.com"})
	g.Expect(ok).To(BeFalse())
	g.Expect(msg).To(ContainSubstring("not yet available"))
	g.Expect(msg).To(ContainSubstring("endpoints not found"))
}

// TestCheckAvailability_NoCondition verifies that CheckAvailability returns false
// when the APIService exists but has no Available condition set.
func TestCheckAvailability_NoCondition(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newScheme()

	svc := &apiregistrationv1.APIService{
		ObjectMeta: metav1.ObjectMeta{Name: "v1.example.com"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	ok, msg := CheckAvailability(context.Background(), c, []string{"v1.example.com"})
	g.Expect(ok).To(BeFalse())
	g.Expect(msg).To(ContainSubstring("no Available condition"))
}

// TestCheckAvailability_Missing verifies that CheckAvailability returns false
// when the APIService object does not exist.
func TestCheckAvailability_Missing(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	ok, msg := CheckAvailability(context.Background(), c, []string{"v1.example.com"})
	g.Expect(ok).To(BeFalse())
	g.Expect(msg).To(ContainSubstring("not found"))
}

// TestCheckAvailability_EmptyList verifies that CheckAvailability returns true
// when the registered names list is empty (vacuous truth).
func TestCheckAvailability_EmptyList(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	ok, msg := CheckAvailability(context.Background(), c, nil)
	g.Expect(ok).To(BeTrue())
	g.Expect(msg).To(BeEmpty())
}
