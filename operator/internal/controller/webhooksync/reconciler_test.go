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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
	"github.com/konflux-ci/kube-shard/operator/internal/secondary"
)

var _ = Describe("WebhookSync Controller", func() {
	const testNamespace = "test-webhooksync-ns"

	BeforeEach(func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
		}
		_ = k8sClient.Create(ctx, ns)
	})

	Context("When the auth secret does not exist", func() {
		It("should set Ready=False with SecondaryUnavailable", func() {
			whSync := &kubeshardv1alpha1.WebhookSync{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-wh-sync",
					Namespace: testNamespace,
				},
				Spec: kubeshardv1alpha1.WebhookSyncSpec{
					SecondaryConnection: kubeshardv1alpha1.SecondaryConnectionSpec{
						ServiceRef: kubeshardv1alpha1.ServiceReference{
							Name:      "secondary-apiserver",
							Namespace: testNamespace,
							Port:      443,
						},
						AuthSecretRef: kubeshardv1alpha1.LocalSecretReference{
							Name: "nonexistent-auth-secret",
						},
						CASecretRef: kubeshardv1alpha1.LocalSecretReference{
							Name: "nonexistent-ca-secret",
						},
					},
					APIGroups: []string{"tekton.dev"},
				},
			}
			Expect(k8sClient.Create(ctx, whSync)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, whSync)
			}()

			reconciler := &Reconciler{
				Client:         k8sClient,
				Scheme:         k8sClient.Scheme(),
				ClientProvider: secondary.NewClientProvider(k8sClient.Scheme()),
			}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      whSync.Name,
					Namespace: whSync.Namespace,
				},
			})
			Expect(err).To(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			updated := &kubeshardv1alpha1.WebhookSync{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      whSync.Name,
				Namespace: whSync.Namespace,
			}, updated)).To(Succeed())

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
