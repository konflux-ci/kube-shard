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

package apishard

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/konflux-ci/konflux-ci/operator/pkg/tracking"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
	"github.com/konflux-ci/kube-shard/operator/internal/resources"
)

// newTrackingClient creates a tracking.Client configured with ownership for the given APIShard.
func newTrackingClient(shard *kubeshardv1alpha1.APIShard) *tracking.Client {
	return tracking.NewClientWithOwnership(k8sClient, tracking.OwnershipConfig{
		Owner:             shard,
		OwnerLabelKey:     ownerLabelKey,
		ComponentLabelKey: componentLabelKey,
		Component:         "apishard",
		FieldManager:      fieldManager,
	})
}

var _ = Describe("reconcileRequestHeaderCA", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	createShard := func(suffix string) *kubeshardv1alpha1.APIShard {
		nsName := "test-rh-" + suffix
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		_ = k8sClient.Create(ctx, ns)

		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-rh-" + suffix,
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: nsName,
				APIGroups: []kubeshardv1alpha1.APIGroupSpec{
					{Group: "tekton.dev", Versions: []string{"v1"}},
				},
				Storage: kubeshardv1alpha1.StorageSpec{
					Type: kubeshardv1alpha1.StorageTypeSQLite,
				},
				NamespaceSync: kubeshardv1alpha1.NamespaceSyncConfig{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"type": "tenant"},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, shard)).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, shard)).To(Succeed())
		return shard
	}

	setAuthConfigMap := func(data map[string]string) {
		cm := &corev1.ConfigMap{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name: "extension-apiserver-authentication", Namespace: "kube-system",
		}, cm)
		if err == nil {
			cm.Data = data
			Expect(k8sClient.Update(ctx, cm)).To(Succeed())
		} else {
			cm = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "extension-apiserver-authentication",
					Namespace: "kube-system",
				},
				Data: data,
			}
			Expect(k8sClient.Create(ctx, cm)).To(Succeed())
		}
	}

	It("should return allowed names from the ConfigMap (OpenShift style)", func() {
		shard := createShard("openshift")
		setAuthConfigMap(map[string]string{
			"requestheader-client-ca-file": "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n",
			"requestheader-allowed-names":  `["kube-apiserver-proxy","system:kube-apiserver-proxy","system:openshift-aggregator"]`,
		})

		tc := newTrackingClient(shard)
		allowedNames, err := reconciler.reconcileRequestHeaderCA(ctx, tc, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(allowedNames).To(Equal([]string{
			"kube-apiserver-proxy",
			"system:kube-apiserver-proxy",
			"system:openshift-aggregator",
		}))
	})

	It("should fall back to front-proxy-client when allowed names are absent", func() {
		shard := createShard("fallback")
		setAuthConfigMap(map[string]string{
			"requestheader-client-ca-file": "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n",
		})

		tc := newTrackingClient(shard)
		allowedNames, err := reconciler.reconcileRequestHeaderCA(ctx, tc, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(allowedNames).To(Equal([]string{"front-proxy-client"}))
	})

	It("should fall back to front-proxy-client when allowed names is an empty array", func() {
		shard := createShard("empty")
		setAuthConfigMap(map[string]string{
			"requestheader-client-ca-file": "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n",
			"requestheader-allowed-names":  `[]`,
		})

		tc := newTrackingClient(shard)
		allowedNames, err := reconciler.reconcileRequestHeaderCA(ctx, tc, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(allowedNames).To(Equal([]string{"front-proxy-client"}))
	})

	It("should return error when requestheader-client-ca-file is missing", func() {
		shard := createShard("noca")
		setAuthConfigMap(map[string]string{
			"requestheader-allowed-names": `["front-proxy-client"]`,
		})

		tc := newTrackingClient(shard)
		_, err := reconciler.reconcileRequestHeaderCA(ctx, tc, shard)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("requestheader-client-ca-file"))
	})
})

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
			current := &kubeshardv1alpha1.APIShard{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, current); err == nil {
				if controllerutil.ContainsFinalizer(current, finalizerName) {
					controllerutil.RemoveFinalizer(current, finalizerName)
					Expect(k8sClient.Update(ctx, current)).To(Succeed())
				}
				Expect(k8sClient.Delete(ctx, current)).To(Succeed())
			}
		})

		It("should create the target namespace", func() {
			reconciler := &Reconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			// Reconcile returns an error because cert-manager CRDs are not
			// installed in envtest, but resources created before that step
			// (namespace, Kine) are still applied.
			_, _ = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: shard.Name},
			})

			ns := &corev1.Namespace{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-shard-ns"}, ns)
			}, timeout, interval).Should(Succeed())
		})

		It("should create Kine deployment and service", func() {
			reconciler := &Reconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, _ = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: shard.Name},
			})

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

		It("should add the cleanup finalizer", func() {
			reconciler := &Reconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, _ = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: shard.Name},
			})

			updated := &kubeshardv1alpha1.APIShard{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, updated)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(updated, finalizerName)).To(BeTrue())
		})

		It("should set status phase to Error when cert-manager is unavailable", func() {
			reconciler := &Reconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: shard.Name},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Issuer"))

			updated := &kubeshardv1alpha1.APIShard{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(kubeshardv1alpha1.PhaseError))
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
			current := &kubeshardv1alpha1.APIShard{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, current); err == nil {
				if controllerutil.ContainsFinalizer(current, finalizerName) {
					controllerutil.RemoveFinalizer(current, finalizerName)
					Expect(k8sClient.Update(ctx, current)).To(Succeed())
				}
				Expect(k8sClient.Delete(ctx, current)).To(Succeed())
			}
		})

		It("should create PostgreSQL deployment, service, and secret", func() {
			reconciler := &Reconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, _ = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: shard.Name},
			})

			pgSts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      resources.PostgreSQLStatefulSetName(shard),
					Namespace: "test-pg-ns",
				}, pgSts)
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

var _ = Describe("validateExternalPostgreSQLSecret", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	createPGShard := func(suffix string, ref *kubeshardv1alpha1.SecretKeyReference) *kubeshardv1alpha1.APIShard {
		nsName := "test-ext-pg-" + suffix
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		_ = k8sClient.Create(ctx, ns)

		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-ext-pg-" + suffix,
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: nsName,
				APIGroups: []kubeshardv1alpha1.APIGroupSpec{
					{Group: "tekton.dev", Versions: []string{"v1"}},
				},
				Storage: kubeshardv1alpha1.StorageSpec{
					Type:                kubeshardv1alpha1.StorageTypePostgreSQL,
					ConnectionSecretRef: ref,
				},
				NamespaceSync: kubeshardv1alpha1.NamespaceSyncConfig{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"type": "tenant"},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, shard)).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, shard)).To(Succeed())
		return shard
	}

	It("should set StorageReady=False when connectionSecretRef is nil", func() {
		shard := createPGShard("no-ref", nil)
		defer func() {
			_ = k8sClient.Delete(ctx, shard)
		}()

		err := reconciler.validateExternalPostgreSQLSecret(ctx, shard)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("connectionSecretRef is not set"))

		cond := meta.FindStatusCondition(shard.Status.Conditions, kubeshardv1alpha1.ConditionStorageReady)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("MissingConnectionSecretRef"))
	})

	It("should set StorageReady=False when Secret is not found", func() {
		shard := createPGShard("not-found", &kubeshardv1alpha1.SecretKeyReference{
			Name: "nonexistent-secret",
			Key:  "dsn",
		})
		defer func() {
			_ = k8sClient.Delete(ctx, shard)
		}()

		err := reconciler.validateExternalPostgreSQLSecret(ctx, shard)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("not found"))

		cond := meta.FindStatusCondition(shard.Status.Conditions, kubeshardv1alpha1.ConditionStorageReady)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("SecretNotFound"))
	})

	It("should set StorageReady=False when key is missing from Secret", func() {
		shard := createPGShard("no-key", &kubeshardv1alpha1.SecretKeyReference{
			Name: "pg-secret-no-key",
			Key:  "dsn",
		})
		defer func() {
			_ = k8sClient.Delete(ctx, shard)
		}()

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pg-secret-no-key",
				Namespace: shard.Spec.TargetNamespace,
			},
			Data: map[string][]byte{
				"wrong-key": []byte("postgres://..."),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		err := reconciler.validateExternalPostgreSQLSecret(ctx, shard)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("not found in secret"))

		cond := meta.FindStatusCondition(shard.Status.Conditions, kubeshardv1alpha1.ConditionStorageReady)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("KeyNotFound"))
	})

	It("should set StorageReady=True when Secret and key are valid", func() {
		shard := createPGShard("valid", &kubeshardv1alpha1.SecretKeyReference{
			Name: "pg-secret-valid",
			Key:  "dsn",
		})
		defer func() {
			_ = k8sClient.Delete(ctx, shard)
		}()

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pg-secret-valid",
				Namespace: shard.Spec.TargetNamespace,
			},
			Data: map[string][]byte{
				"dsn": []byte("postgres://user:pass@host:5432/db"),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		err := reconciler.validateExternalPostgreSQLSecret(ctx, shard)
		Expect(err).NotTo(HaveOccurred())

		cond := meta.FindStatusCondition(shard.Status.Conditions, kubeshardv1alpha1.ConditionStorageReady)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal("SecretValid"))
	})
})

var _ = Describe("getOrGeneratePostgresPassword", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	It("should reuse password from existing secret", func() {
		nsName := "test-pw-reuse"
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		_ = k8sClient.Create(ctx, ns)

		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-pw-reuse",
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: nsName,
				APIGroups: []kubeshardv1alpha1.APIGroupSpec{
					{Group: "tekton.dev", Versions: []string{"v1"}},
				},
				Storage: kubeshardv1alpha1.StorageSpec{
					Type: kubeshardv1alpha1.StorageTypeInClusterPostgreSQL,
				},
				NamespaceSync: kubeshardv1alpha1.NamespaceSyncConfig{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"type": "tenant"},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, shard)).To(Succeed())
		defer func() {
			_ = k8sClient.Delete(ctx, shard)
		}()

		existingSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      resources.PostgreSQLSecretName(shard),
				Namespace: nsName,
			},
			Data: map[string][]byte{
				"POSTGRES_PASSWORD": []byte("my-existing-password"),
			},
		}
		Expect(k8sClient.Create(ctx, existingSecret)).To(Succeed())

		password, err := reconciler.getOrGeneratePostgresPassword(ctx, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(password).To(Equal("my-existing-password"))
	})

	It("should generate a new password when no secret exists", func() {
		nsName := "test-pw-gen"
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		_ = k8sClient.Create(ctx, ns)

		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-pw-gen",
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: nsName,
				APIGroups: []kubeshardv1alpha1.APIGroupSpec{
					{Group: "tekton.dev", Versions: []string{"v1"}},
				},
				Storage: kubeshardv1alpha1.StorageSpec{
					Type: kubeshardv1alpha1.StorageTypeInClusterPostgreSQL,
				},
				NamespaceSync: kubeshardv1alpha1.NamespaceSyncConfig{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"type": "tenant"},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, shard)).To(Succeed())
		defer func() {
			_ = k8sClient.Delete(ctx, shard)
		}()

		password, err := reconciler.getOrGeneratePostgresPassword(ctx, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(password).NotTo(BeEmpty())
		Expect(len(password)).To(BeNumerically(">=", 16))
	})
})

var _ = Describe("reconcileAuthConfig", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	It("should create the authz-config ConfigMap with webhook config", func() {
		nsName := "test-authcfg"
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		_ = k8sClient.Create(ctx, ns)

		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-authcfg",
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: nsName,
				APIGroups: []kubeshardv1alpha1.APIGroupSpec{
					{Group: "tekton.dev", Versions: []string{"v1"}},
				},
				Storage: kubeshardv1alpha1.StorageSpec{
					Type: kubeshardv1alpha1.StorageTypeSQLite,
				},
				NamespaceSync: kubeshardv1alpha1.NamespaceSyncConfig{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"type": "tenant"},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, shard)).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, shard)).To(Succeed())
		defer func() {
			_ = k8sClient.Delete(ctx, shard)
		}()

		tc := newTrackingClient(shard)
		err := reconciler.reconcileAuthConfig(ctx, tc, shard)
		Expect(err).NotTo(HaveOccurred())

		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      shard.Name + "-authz-config",
			Namespace: nsName,
		}, cm)).To(Succeed())

		Expect(cm.Data).To(HaveKey("webhook-config.yaml"))
		Expect(cm.Data["webhook-config.yaml"]).To(ContainSubstring("subjectaccessreviews"))
		Expect(cm.Data["webhook-config.yaml"]).To(ContainSubstring("tokenFile"))
	})
})

var _ = Describe("reconcileAuthDelegator", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	It("should create a ClusterRoleBinding for auth-delegator", func() {
		nsName := "test-auth-del"
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		_ = k8sClient.Create(ctx, ns)

		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-auth-del",
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: nsName,
				APIGroups: []kubeshardv1alpha1.APIGroupSpec{
					{Group: "tekton.dev", Versions: []string{"v1"}},
				},
				Storage: kubeshardv1alpha1.StorageSpec{
					Type: kubeshardv1alpha1.StorageTypeSQLite,
				},
				NamespaceSync: kubeshardv1alpha1.NamespaceSyncConfig{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"type": "tenant"},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, shard)).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, shard)).To(Succeed())
		defer func() {
			crb := &rbacv1.ClusterRoleBinding{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name + "-auth-delegator"}, crb); err == nil {
				_ = k8sClient.Delete(ctx, crb)
			}
			_ = k8sClient.Delete(ctx, shard)
		}()

		tc := newTrackingClient(shard)
		err := reconciler.reconcileAuthDelegator(ctx, tc, shard)
		Expect(err).NotTo(HaveOccurred())

		crb := &rbacv1.ClusterRoleBinding{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: shard.Name + "-auth-delegator",
		}, crb)).To(Succeed())

		Expect(crb.RoleRef.Name).To(Equal("system:auth-delegator"))
		Expect(crb.RoleRef.Kind).To(Equal("ClusterRole"))
		Expect(crb.Subjects).To(HaveLen(1))
		Expect(crb.Subjects[0].Kind).To(Equal("ServiceAccount"))
		Expect(crb.Subjects[0].Name).To(Equal("default"))
		Expect(crb.Subjects[0].Namespace).To(Equal(nsName))
	})
})

var _ = Describe("reconcileSecondary", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	It("should create secondary deployment and service", func() {
		nsName := "test-secondary"
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		_ = k8sClient.Create(ctx, ns)

		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-secondary",
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: nsName,
				APIGroups: []kubeshardv1alpha1.APIGroupSpec{
					{Group: "tekton.dev", Versions: []string{"v1"}},
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
			},
		}
		Expect(k8sClient.Create(ctx, shard)).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, shard)).To(Succeed())
		defer func() {
			_ = k8sClient.Delete(ctx, shard)
		}()

		tc := newTrackingClient(shard)
		err := reconciler.reconcileSecondary(ctx, tc, shard, []string{"front-proxy-client"})
		Expect(err).NotTo(HaveOccurred())

		deploy := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      resources.SecondaryDeploymentName(shard),
			Namespace: nsName,
		}, deploy)).To(Succeed())

		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      resources.SecondaryServiceName(shard),
			Namespace: nsName,
		}, svc)).To(Succeed())
	})
})
