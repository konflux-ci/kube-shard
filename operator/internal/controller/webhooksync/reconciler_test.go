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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
	"github.com/konflux-ci/kube-shard/operator/internal/secondary"
)

var _ = Describe("WebhookSync Controller", func() {
	Context("When the referenced APIShard does not exist", func() {
		It("should set phase to Waiting", func() {
			whSync := &kubeshardv1alpha1.WebhookSync{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-wh-sync",
				},
				Spec: kubeshardv1alpha1.WebhookSyncSpec{
					ShardRef: "nonexistent-shard",
					SourceLabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "tekton"},
					},
					SyncMutating:   true,
					SyncValidating: true,
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
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: whSync.Name},
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &kubeshardv1alpha1.WebhookSync{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: whSync.Name}, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(kubeshardv1alpha1.PhaseWaiting))
		})
	})
})
