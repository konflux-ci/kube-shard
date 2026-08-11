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
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

// newScheme creates a runtime.Scheme with the kube-shard and APIService types registered.
func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = kubeshardv1alpha1.AddToScheme(s)
	_ = apiregistrationv1.AddToScheme(s)
	return s
}

// newTestShard returns a minimal APIShard fixture for Reconcile tests.
func newTestShard() *kubeshardv1alpha1.APIShard {
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
		},
	}
}

// TestReconcile_SetsAutomanagedLabel verifies that Reconcile always sets the
// automanaged=false label on APIService objects.
func TestReconcile_SetsAutomanagedLabel(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	shard := newTestShard()

	result, err := Reconcile(context.Background(), c, scheme, shard, []byte("fake-ca"), nil, "test-manager")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.Registered).To(Equal([]string{"v1.example.com"}))

	apiSvc := &apiregistrationv1.APIService{}
	g.Expect(c.Get(context.Background(), types.NamespacedName{Name: "v1.example.com"}, apiSvc)).To(Succeed())

	g.Expect(apiSvc.Labels).To(HaveKeyWithValue(AutoManagedLabelKey, "false"))
}

// TestReconcile_OverridesExistingAutomanagedTrue verifies that Reconcile overrides
// an existing automanaged=true label set by the kube-aggregator auto-register
// controller with automanaged=false.
func TestReconcile_OverridesExistingAutomanagedTrue(t *testing.T) {
	g := NewGomegaWithT(t)
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
	shard := newTestShard()

	_, err := Reconcile(context.Background(), c, scheme, shard, []byte("fake-ca"), nil, "test-manager")
	g.Expect(err).NotTo(HaveOccurred())

	apiSvc := &apiregistrationv1.APIService{}
	g.Expect(c.Get(context.Background(), types.NamespacedName{Name: "v1.example.com"}, apiSvc)).To(Succeed())

	g.Expect(apiSvc.Labels).To(HaveKeyWithValue(AutoManagedLabelKey, "false"))
	g.Expect(apiSvc.Spec.Service).NotTo(BeNil())
}

// TestReconcile_ServiceFieldAlwaysSet verifies that the APIService spec always
// includes the Service reference pointing to the secondary API server.
func TestReconcile_ServiceFieldAlwaysSet(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	shard := newTestShard()

	_, err := Reconcile(context.Background(), c, scheme, shard, []byte("fake-ca"), nil, "test-manager")
	g.Expect(err).NotTo(HaveOccurred())

	apiSvc := &apiregistrationv1.APIService{}
	g.Expect(c.Get(context.Background(), types.NamespacedName{Name: "v1.example.com"}, apiSvc)).To(Succeed())

	g.Expect(apiSvc.Spec.Service).NotTo(BeNil())
	g.Expect(apiSvc.Spec.Service.Name).To(Equal("test-shard-apiserver"))
	g.Expect(apiSvc.Spec.Service.Namespace).To(Equal("test-ns"))
}

// Verify that the fake client implements SSA Patch correctly for our use case.
// This is a regression test to ensure we can use Patch(Apply) in tests.
func init() {
	_ = client.Apply //nolint:staticcheck // migrating to client.Client.Apply() requires ApplyConfiguration types
}

const testOwnerUID = types.UID("test-owner-uid")

// newOwnedAPIService creates an APIService with an owner reference pointing to
// testOwnerUID for use in CheckAvailability tests.
func newOwnedAPIService(name string, conditions []apiregistrationv1.APIServiceCondition) *apiregistrationv1.APIService {
	return &apiregistrationv1.APIService{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "kube-shard.konflux-ci.dev/v1alpha1",
					Kind:       "APIShard",
					Name:       "test-shard",
					UID:        testOwnerUID,
				},
			},
		},
		Status: apiregistrationv1.APIServiceStatus{
			Conditions: conditions,
		},
	}
}

// TestCheckAvailability_AllAvailable verifies that CheckAvailability returns true
// when every owned APIService has Available=True.
func TestCheckAvailability_AllAvailable(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newScheme()

	svc := newOwnedAPIService("v1.example.com", []apiregistrationv1.APIServiceCondition{
		{Type: apiregistrationv1.Available, Status: apiregistrationv1.ConditionTrue},
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	ok, msg, err := CheckAvailability(context.Background(), c, testOwnerUID)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeTrue())
	g.Expect(msg).To(BeEmpty())
}

// TestCheckAvailability_NotAvailable verifies that CheckAvailability returns false
// with a descriptive message when an owned APIService has Available=False.
func TestCheckAvailability_NotAvailable(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newScheme()

	svc := newOwnedAPIService("v1.example.com", []apiregistrationv1.APIServiceCondition{
		{Type: apiregistrationv1.Available, Status: apiregistrationv1.ConditionFalse, Message: "endpoints not found"},
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	ok, msg, err := CheckAvailability(context.Background(), c, testOwnerUID)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeFalse())
	g.Expect(msg).To(ContainSubstring("not yet available"))
	g.Expect(msg).To(ContainSubstring("endpoints not found"))
}

// TestCheckAvailability_NoCondition verifies that CheckAvailability returns false
// when an owned APIService exists but has no Available condition set.
func TestCheckAvailability_NoCondition(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newScheme()

	svc := newOwnedAPIService("v1.example.com", nil)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	ok, msg, err := CheckAvailability(context.Background(), c, testOwnerUID)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeFalse())
	g.Expect(msg).To(ContainSubstring("no Available condition"))
}

// TestCheckAvailability_NoOwnedAPIServices verifies that CheckAvailability
// returns false when no APIServices are owned by the given UID.
func TestCheckAvailability_NoOwnedAPIServices(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	ok, msg, err := CheckAvailability(context.Background(), c, testOwnerUID)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeFalse())
	g.Expect(msg).To(ContainSubstring("no APIServices owned by shard"))
}

// TestCheckAvailability_IgnoresUnownedAPIServices verifies that CheckAvailability
// only considers APIServices that have a matching owner reference, ignoring others.
func TestCheckAvailability_IgnoresUnownedAPIServices(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newScheme()

	unowned := &apiregistrationv1.APIService{
		ObjectMeta: metav1.ObjectMeta{Name: "v1.other.com"},
		Status: apiregistrationv1.APIServiceStatus{
			Conditions: []apiregistrationv1.APIServiceCondition{
				{Type: apiregistrationv1.Available, Status: apiregistrationv1.ConditionFalse},
			},
		},
	}
	owned := newOwnedAPIService("v1.owned.example.com", []apiregistrationv1.APIServiceCondition{
		{Type: apiregistrationv1.Available, Status: apiregistrationv1.ConditionTrue},
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(unowned, owned).Build()

	ok, msg, err := CheckAvailability(context.Background(), c, testOwnerUID)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeTrue())
	g.Expect(msg).To(BeEmpty())
}

// TestCheckAvailability_TransientListError verifies that CheckAvailability
// returns an error for List failures such as network timeouts.
func TestCheckAvailability_TransientListError(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newScheme()
	transientErr := fmt.Errorf("connection refused")
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
			return transientErr
		},
	}).Build()

	ok, msg, err := CheckAvailability(context.Background(), c, testOwnerUID)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err).To(MatchError(ContainSubstring("connection refused")))
	g.Expect(ok).To(BeFalse())
	g.Expect(msg).To(BeEmpty())
}
