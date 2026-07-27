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

package namespacesync

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
	"github.com/konflux-ci/kube-shard/operator/internal/secondary"
)

var _ = Describe("NamespaceSync Controller", func() {
	const testNamespace = "test-ns-sync-ns"

	BeforeEach(func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
		}
		_ = k8sClient.Create(ctx, ns)
	})

	Context("When the auth secret does not exist", func() {
		It("should set Ready=False with reason SecondaryUnavailable", func() {
			nsSync := &kubeshardv1alpha1.NamespaceSync{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ns-sync",
					Namespace: testNamespace,
				},
				Spec: kubeshardv1alpha1.NamespaceSyncSpec{
					SecondaryConnection: kubeshardv1alpha1.SecondaryConnectionSpec{
						ServiceRef: kubeshardv1alpha1.ServiceReference{
							Name:      "secondary-apiserver",
							Namespace: "kube-shard-system",
							Port:      6443,
						},
						AuthSecretRef: kubeshardv1alpha1.LocalSecretReference{
							Name: "nonexistent-auth-secret",
						},
						CASecretRef: kubeshardv1alpha1.LocalSecretReference{
							Name: "nonexistent-ca-secret",
						},
					},
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"type": "tenant"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, nsSync)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, nsSync)
			}()

			reconciler := &Reconciler{
				Client:         k8sClient,
				Scheme:         k8sClient.Scheme(),
				ClientProvider: secondary.NewClientProvider(k8sClient.Scheme()),
			}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nsSync.Name,
					Namespace: nsSync.Namespace,
				},
			})
			Expect(err).To(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			updated := &kubeshardv1alpha1.NamespaceSync{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      nsSync.Name,
				Namespace: nsSync.Namespace,
			}, updated)).To(Succeed())

			Expect(updated.Status.Conditions).NotTo(BeEmpty())
			readyCond := findCondition(updated.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("SecondaryUnavailable"))
		})
	})
})

func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}
