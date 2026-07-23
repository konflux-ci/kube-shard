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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
	"github.com/konflux-ci/kube-shard/operator/internal/secondary"
)

var _ = Describe("NamespaceSync Controller", func() {
	Context("When the referenced APIShard does not exist", func() {
		It("should set phase to Waiting", func() {
			nsSync := &kubeshardv1alpha1.NamespaceSync{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns-sync",
				},
				Spec: kubeshardv1alpha1.NamespaceSyncSpec{
					ShardRef: "nonexistent-shard",
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"type": "tenant"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, nsSync)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, nsSync)
			}()

			reconciler := &NamespaceSyncReconciler{
				Client:         k8sClient,
				Scheme:         k8sClient.Scheme(),
				ClientProvider: secondary.NewClientProvider(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: nsSync.Name},
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &kubeshardv1alpha1.NamespaceSync{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nsSync.Name}, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal("Waiting"))
		})
	})
})
