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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
	"github.com/konflux-ci/kube-shard/operator/internal/resources"
)

var _ = Describe("APIShard Controller", func() {
	const (
		timeout  = 10 * time.Second
		interval = 250 * time.Millisecond
	)

	Context("When creating an APIShard with SQLite storage", func() {
		var shard *kubeshardv1alpha1.APIShard

		BeforeEach(func() {
			shard = &kubeshardv1alpha1.APIShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-shard",
				},
				Spec: kubeshardv1alpha1.APIShardSpec{
					TargetNamespace: "test-shard-ns",
					APIGroups: []kubeshardv1alpha1.APIGroupSpec{
						{
							Group:    "tekton.dev",
							Versions: []string{"v1"},
						},
					},
					Storage: kubeshardv1alpha1.StorageSpec{
						Type: kubeshardv1alpha1.StorageTypeSQLite,
					},
					NamespaceSync: kubeshardv1alpha1.NamespaceSyncConfig{
						LabelSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{"type": "tenant"},
						},
					},
					Secondary: kubeshardv1alpha1.SecondarySpec{
						Replicas: 1,
					},
					Kine: kubeshardv1alpha1.KineSpec{
						Replicas: 1,
					},
				},
			}
			Expect(k8sClient.Create(ctx, shard)).To(Succeed())
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, shard)).To(Succeed())
		})

		It("should create the target namespace", func() {
			reconciler := &APIShardReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: shard.Name},
			})
			Expect(err).NotTo(HaveOccurred())

			ns := &corev1.Namespace{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-shard-ns"}, ns)
			}, timeout, interval).Should(Succeed())
		})

		It("should create Kine deployment and service", func() {
			reconciler := &APIShardReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: shard.Name},
			})
			Expect(err).NotTo(HaveOccurred())

			kineDeploy := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      resources.KineDeploymentName(shard),
					Namespace: "test-shard-ns",
				}, kineDeploy)
			}, timeout, interval).Should(Succeed())

			Expect(kineDeploy.Spec.Template.Spec.Containers[0].Image).To(Equal(resources.DefaultKineImage))

			kineSvc := &corev1.Service{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      resources.KineServiceName(shard),
					Namespace: "test-shard-ns",
				}, kineSvc)
			}, timeout, interval).Should(Succeed())
		})

		It("should create secondary API server deployment and service", func() {
			reconciler := &APIShardReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: shard.Name},
			})
			Expect(err).NotTo(HaveOccurred())

			secondaryDeploy := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      resources.SecondaryDeploymentName(shard),
					Namespace: "test-shard-ns",
				}, secondaryDeploy)
			}, timeout, interval).Should(Succeed())

			Expect(secondaryDeploy.Spec.Template.Spec.Containers[0].Image).To(Equal(resources.DefaultSecondaryImage))

			secondarySvc := &corev1.Service{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      resources.SecondaryServiceName(shard),
					Namespace: "test-shard-ns",
				}, secondarySvc)
			}, timeout, interval).Should(Succeed())
		})

		It("should add a finalizer", func() {
			reconciler := &APIShardReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: shard.Name},
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &kubeshardv1alpha1.APIShard{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(finalizerName))
		})
	})

	Context("When creating an APIShard with InClusterPostgreSQL storage", func() {
		var shard *kubeshardv1alpha1.APIShard

		BeforeEach(func() {
			shard = &kubeshardv1alpha1.APIShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pg-shard",
				},
				Spec: kubeshardv1alpha1.APIShardSpec{
					TargetNamespace: "test-pg-ns",
					APIGroups: []kubeshardv1alpha1.APIGroupSpec{
						{
							Group:    "tekton.dev",
							Versions: []string{"v1"},
						},
					},
					Storage: kubeshardv1alpha1.StorageSpec{
						Type: kubeshardv1alpha1.StorageTypeInClusterPostgreSQL,
						InCluster: &kubeshardv1alpha1.InClusterStorage{
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
						},
					},
					NamespaceSync: kubeshardv1alpha1.NamespaceSyncConfig{
						LabelSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{"type": "tenant"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, shard)).To(Succeed())
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, shard)).To(Succeed())
		})

		It("should create PostgreSQL deployment, service, and secret", func() {
			reconciler := &APIShardReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: shard.Name},
			})
			Expect(err).NotTo(HaveOccurred())

			pgDeploy := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      resources.PostgreSQLDeploymentName(shard),
					Namespace: "test-pg-ns",
				}, pgDeploy)
			}, timeout, interval).Should(Succeed())

			pgSvc := &corev1.Service{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      resources.PostgreSQLServiceName(shard),
					Namespace: "test-pg-ns",
				}, pgSvc)
			}, timeout, interval).Should(Succeed())

			pgSecret := &corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      resources.PostgreSQLSecretName(shard),
					Namespace: "test-pg-ns",
				}, pgSecret)
			}, timeout, interval).Should(Succeed())
		})
	})
})
