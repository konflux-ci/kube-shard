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
	"fmt"
	"math/rand"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/konflux-ci/konflux-ci/operator/pkg/tracking"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
	"github.com/konflux-ci/kube-shard/operator/internal/certs"
	"github.com/konflux-ci/kube-shard/operator/internal/resources"
	"github.com/konflux-ci/kube-shard/operator/internal/secondary"
)

// randString generates a random lowercase alphanumeric string of length n,
// used to generate unique names in tests to avoid collisions.
func randString(n int) string { //nolint:unparam
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

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
			Name: extensionAPIServerAuthCM, Namespace: kubeSystemNamespace,
		}, cm)
		if err == nil {
			cm.Data = data
			Expect(k8sClient.Update(ctx, cm)).To(Succeed())
		} else {
			cm = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      extensionAPIServerAuthCM,
					Namespace: kubeSystemNamespace,
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
		Expect(allowedNames).To(Equal([]string{defaultFrontProxyClient}))
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
		Expect(allowedNames).To(Equal([]string{defaultFrontProxyClient}))
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

var _ = Describe("reconcileInClusterPostgreSQL security context overrides", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	createPGShard := func(suffix string) *kubeshardv1alpha1.APIShard {
		nsName := "test-pg-sc-" + suffix
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		_ = k8sClient.Create(ctx, ns)

		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-pg-sc-" + suffix,
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: nsName,
				APIGroups: []kubeshardv1alpha1.APIGroupSpec{
					{Group: "tekton.dev", Versions: []string{"v1"}},
				},
				Storage: kubeshardv1alpha1.StorageSpec{
					Type:      kubeshardv1alpha1.StorageTypeInClusterPostgreSQL,
					InCluster: &kubeshardv1alpha1.InClusterStorage{},
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

	It("should set RunAsUser and FSGroup when SCCAvailable is false", func() {
		reconciler.SCCAvailable = false
		shard := createPGShard("no-scc")
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		tc := newTrackingClient(shard)
		err := reconciler.reconcileInClusterPostgreSQL(ctx, tc, shard)
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      resources.PostgreSQLStatefulSetName(shard),
			Namespace: shard.Spec.TargetNamespace,
		}, sts)).To(Succeed())

		podSC := sts.Spec.Template.Spec.SecurityContext
		Expect(podSC.RunAsUser).NotTo(BeNil())
		Expect(*podSC.RunAsUser).To(Equal(int64(999)))
		Expect(podSC.FSGroup).NotTo(BeNil())
		Expect(*podSC.FSGroup).To(Equal(int64(999)))
	})

	It("should not set RunAsUser or FSGroup when SCCAvailable is true", func() {
		reconciler.SCCAvailable = true
		shard := createPGShard("with-scc")
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		tc := newTrackingClient(shard)
		err := reconciler.reconcileInClusterPostgreSQL(ctx, tc, shard)
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      resources.PostgreSQLStatefulSetName(shard),
			Namespace: shard.Spec.TargetNamespace,
		}, sts)).To(Succeed())

		podSC := sts.Spec.Template.Spec.SecurityContext
		Expect(podSC.RunAsUser).To(BeNil(),
			"RunAsUser must not be set on OpenShift so the SCC assigns from the namespace range")
		Expect(podSC.FSGroup).To(BeNil(),
			"FSGroup must not be set on OpenShift; the SCC assigns from the namespace range")
	})
})

var _ = Describe("buildCertResources", func() {
	newShard := func(storageType kubeshardv1alpha1.StorageType) *kubeshardv1alpha1.APIShard {
		return &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{Name: "cert-res-test"},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: "cert-res-ns",
				APIGroups: []kubeshardv1alpha1.APIGroupSpec{
					{Group: "tekton.dev", Versions: []string{"v1"}},
				},
				Storage: kubeshardv1alpha1.StorageSpec{
					Type: storageType,
				},
				NamespaceSync: kubeshardv1alpha1.NamespaceSyncConfig{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"type": "tenant"},
					},
				},
			},
		}
	}

	hasResource := func(resources []*unstructured.Unstructured, name string) bool {
		for _, r := range resources {
			if r.GetName() == name {
				return true
			}
		}
		return false
	}

	It("should include PostgreSQL cert resources for InClusterPostgreSQL storage", func() {
		shard := newShard(kubeshardv1alpha1.StorageTypeInClusterPostgreSQL)
		resources := buildCertResources(shard)

		Expect(hasResource(resources, certs.PostgreSQLCAIssuerName(shard))).To(BeTrue(),
			"PostgreSQL CA Issuer should be present")
		Expect(hasResource(resources, certs.PostgreSQLCACertificateName(shard))).To(BeTrue(),
			"PostgreSQL CA Certificate should be present")
		Expect(hasResource(resources, certs.PostgreSQLServingCertificateName(shard))).To(BeTrue(),
			"PostgreSQL Serving Certificate should be present")
	})

	It("should not include PostgreSQL cert resources for SQLite storage", func() {
		shard := newShard(kubeshardv1alpha1.StorageTypeSQLite)
		resources := buildCertResources(shard)

		Expect(hasResource(resources, certs.PostgreSQLCAIssuerName(shard))).To(BeFalse(),
			"PostgreSQL CA Issuer should not be present for SQLite")
		Expect(hasResource(resources, certs.PostgreSQLCACertificateName(shard))).To(BeFalse(),
			"PostgreSQL CA Certificate should not be present for SQLite")
		Expect(hasResource(resources, certs.PostgreSQLServingCertificateName(shard))).To(BeFalse(),
			"PostgreSQL Serving Certificate should not be present for SQLite")
	})

	It("should not include PostgreSQL cert resources for external PostgreSQL storage", func() {
		shard := newShard(kubeshardv1alpha1.StorageTypePostgreSQL)
		resources := buildCertResources(shard)

		Expect(hasResource(resources, certs.PostgreSQLCAIssuerName(shard))).To(BeFalse(),
			"PostgreSQL CA Issuer should not be present for external PostgreSQL")
		Expect(hasResource(resources, certs.PostgreSQLCACertificateName(shard))).To(BeFalse(),
			"PostgreSQL CA Certificate should not be present for external PostgreSQL")
		Expect(hasResource(resources, certs.PostgreSQLServingCertificateName(shard))).To(BeFalse(),
			"PostgreSQL Serving Certificate should not be present for external PostgreSQL")
	})

	It("should always include the base shard and Kine cert resources", func() {
		for _, storageType := range []kubeshardv1alpha1.StorageType{
			kubeshardv1alpha1.StorageTypeSQLite,
			kubeshardv1alpha1.StorageTypeInClusterPostgreSQL,
			kubeshardv1alpha1.StorageTypePostgreSQL,
		} {
			shard := newShard(storageType)
			resources := buildCertResources(shard)

			Expect(len(resources)).To(BeNumerically(">=", 10),
				"base cert resources should always be present for storage type %s", storageType)
			Expect(hasResource(resources, certs.IssuerName(shard))).To(BeTrue(),
				"shard CA Issuer should be present for storage type %s", storageType)
			Expect(hasResource(resources, certs.KineCAIssuerName(shard))).To(BeTrue(),
				"Kine CA Issuer should be present for storage type %s", storageType)
		}
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

		Expect(cm.Data).To(HaveKey("authn-webhook-config.yaml"))
		Expect(cm.Data["authn-webhook-config.yaml"]).To(ContainSubstring("tokenreviews"))
		Expect(cm.Data["authn-webhook-config.yaml"]).To(ContainSubstring("tokenFile"))
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
		Expect(crb.Subjects[0].Name).To(Equal(resources.SecondaryServiceAccountName(shard)))
		Expect(crb.Subjects[0].Namespace).To(Equal(nsName))
	})
})

var _ = Describe("reconcileAPIServerSCC", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	It("is a no-op when SCCAvailable is false", func() {
		reconciler.SCCAvailable = false

		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-scc-noop",
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: "default",
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
		err := reconciler.reconcileAPIServerSCC(ctx, tc, shard)
		Expect(err).NotTo(HaveOccurred())

		cr := &rbacv1.ClusterRole{}
		err = k8sClient.Get(ctx, types.NamespacedName{
			Name: resources.APIServerSCCClusterRoleName(shard),
		}, cr)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "ClusterRole should not be created when SCC is unavailable")
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
		err := reconciler.reconcileSecondary(ctx, tc, shard, []string{defaultFrontProxyClient})
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

var _ = Describe("reconcileAdminKubeconfig", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	createShardWithNS := func(suffix string) *kubeshardv1alpha1.APIShard {
		nsName := "test-kubeconfig-" + suffix
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		_ = k8sClient.Create(ctx, ns)

		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-kubeconfig-" + suffix,
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

	It("should be a no-op when PKI secret does not exist", func() {
		shard := createShardWithNS("no-pki")
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		tc := newTrackingClient(shard)
		err := reconciler.reconcileAdminKubeconfig(ctx, tc, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(shard.Status.ConnectionSecret).To(BeNil())
	})

	It("should be a no-op when PKI secret has empty ca.crt", func() {
		shard := createShardWithNS("empty-ca")
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		pkiSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      certs.PKISecretName(shard),
				Namespace: shard.Spec.TargetNamespace,
			},
			Data: map[string][]byte{
				"ca.crt":  {},
				"tls.crt": []byte("cert"),
				"tls.key": []byte("key"),
			},
		}
		Expect(k8sClient.Create(ctx, pkiSecret)).To(Succeed())

		tc := newTrackingClient(shard)
		err := reconciler.reconcileAdminKubeconfig(ctx, tc, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(shard.Status.ConnectionSecret).To(BeNil())
	})

	It("should be a no-op when admin client cert secret does not exist", func() {
		shard := createShardWithNS("no-admin")
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		pkiSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      certs.PKISecretName(shard),
				Namespace: shard.Spec.TargetNamespace,
			},
			Data: map[string][]byte{
				"ca.crt": []byte("fake-ca"),
			},
		}
		Expect(k8sClient.Create(ctx, pkiSecret)).To(Succeed())

		tc := newTrackingClient(shard)
		err := reconciler.reconcileAdminKubeconfig(ctx, tc, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(shard.Status.ConnectionSecret).To(BeNil())
	})

	It("should create kubeconfig secret when PKI and admin certs exist", func() {
		shard := createShardWithNS("full")
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		pkiSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      certs.PKISecretName(shard),
				Namespace: shard.Spec.TargetNamespace,
			},
			Data: map[string][]byte{
				"ca.crt": []byte("fake-ca-cert"),
			},
		}
		Expect(k8sClient.Create(ctx, pkiSecret)).To(Succeed())

		adminSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      certs.AdminClientSecretName(shard),
				Namespace: shard.Spec.TargetNamespace,
			},
			Data: map[string][]byte{
				"tls.crt": []byte("fake-client-cert"),
				"tls.key": []byte("fake-client-key"),
			},
		}
		Expect(k8sClient.Create(ctx, adminSecret)).To(Succeed())

		tc := newTrackingClient(shard)
		err := reconciler.reconcileAdminKubeconfig(ctx, tc, shard)
		Expect(err).NotTo(HaveOccurred())

		Expect(shard.Status.ConnectionSecret).NotTo(BeNil())
		Expect(shard.Status.ConnectionSecret.Name).To(Equal(shard.Name + "-admin-kubeconfig"))
		Expect(shard.Status.ConnectionSecret.Namespace).To(Equal(shard.Spec.TargetNamespace))

		kubeconfigSecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      shard.Name + "-admin-kubeconfig",
			Namespace: shard.Spec.TargetNamespace,
		}, kubeconfigSecret)).To(Succeed())

		Expect(kubeconfigSecret.Data).To(HaveKey("kubeconfig"))
		Expect(kubeconfigSecret.Data).To(HaveKey("tls.crt"))
		Expect(kubeconfigSecret.Data).To(HaveKey("tls.key"))
		Expect(string(kubeconfigSecret.Data["kubeconfig"])).To(ContainSubstring(shard.Name + "-apiserver"))
	})
})

var _ = Describe("APIShard Deletion", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	It("should remove the finalizer and complete deletion when no APIServices registered", func() {
		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-del-noapisvc",
				Finalizers: []string{finalizerName},
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: "test-del-noapisvc-ns",
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

		Expect(k8sClient.Delete(ctx, shard)).To(Succeed())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, shard)).To(Succeed())
		Expect(shard.DeletionTimestamp.IsZero()).To(BeFalse())

		result, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: shard.Name},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))

		err = k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, shard)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("should return no error when APIShard is not found", func() {
		result, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "nonexistent-shard"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))
	})
})

var _ = Describe("reconcileCRDConflicts", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	createConflictShard := func(suffix, group string) *kubeshardv1alpha1.APIShard {
		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-conflict-" + suffix,
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: "test-conflict-" + suffix + "-ns",
				APIGroups: []kubeshardv1alpha1.APIGroupSpec{
					{Group: group, Versions: []string{"v1"}},
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

	It("should set CRDConflict=False when no conflicting CRDs exist", func() {
		shard := createConflictShard("none", "noconflict.example.com")
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		reconciler.reconcileCRDConflicts(ctx, shard)

		cond := meta.FindStatusCondition(shard.Status.Conditions, kubeshardv1alpha1.ConditionCRDConflictDetected)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("NoConflicts"))
	})

	It("should detect conflict and set CRDSyncFailed when secondary client is unavailable", func() {
		shard := createConflictShard("synced", "synced.example.com")
		defer func() {
			crd := &apiextensionsv1.CustomResourceDefinition{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gadgets.synced.example.com"}, crd); err == nil {
				_ = k8sClient.Delete(ctx, crd)
			}
			_ = k8sClient.Delete(ctx, shard)
		}()

		crd := &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: "gadgets.synced.example.com",
			},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "synced.example.com",
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   "gadgets",
					Singular: "gadget",
					Kind:     "Gadget",
				},
				Scope: apiextensionsv1.NamespaceScoped,
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{
						Name:    "v1",
						Served:  true,
						Storage: true,
						Schema: &apiextensionsv1.CustomResourceValidation{
							OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
								Type: "object",
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, crd)).To(Succeed())

		reconciler.reconcileCRDConflicts(ctx, shard)

		cond := meta.FindStatusCondition(shard.Status.Conditions, kubeshardv1alpha1.ConditionCRDConflictDetected)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal("CRDSyncFailed"))
		Expect(cond.Message).To(ContainSubstring("sync failed"))
	})
})

var _ = Describe("checkAPIServiceAvailability", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	// newAvailabilityShard creates a minimal APIShard with a stable UID and an
	// optional CRDConflict condition (status + reason) for availability gate
	// tests. CheckAvailability discovers APIServices via owner references
	// matching the shard's UID.
	newAvailabilityShard := func(
		suffix string,
		conflictStatus metav1.ConditionStatus,
		conflictReason string,
	) *kubeshardv1alpha1.APIShard {
		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-avail-" + suffix,
				UID:        types.UID("test-avail-" + suffix + "-uid"),
				Generation: 1,
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: "test-avail-" + suffix + "-ns",
				APIGroups: []kubeshardv1alpha1.APIGroupSpec{
					{Group: "example.com", Versions: []string{"v1"}},
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
		if conflictStatus != "" {
			meta.SetStatusCondition(&shard.Status.Conditions, metav1.Condition{
				Type:   kubeshardv1alpha1.ConditionCRDConflictDetected,
				Status: conflictStatus,
				Reason: conflictReason,
			})
		}
		return shard
	}

	It("should skip the check and remove stale condition when no CRDConflict", func() {
		shard := newAvailabilityShard("no-conflict", "", "")
		meta.SetStatusCondition(&shard.Status.Conditions, metav1.Condition{
			Type:   kubeshardv1alpha1.ConditionAPIServicesRegistered,
			Status: metav1.ConditionFalse,
			Reason: "SecondaryUnhealthy",
		})

		result, err := reconciler.checkAPIServiceAvailability(ctx, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeZero())

		cond := meta.FindStatusCondition(shard.Status.Conditions, kubeshardv1alpha1.ConditionAPIServicesRegistered)
		Expect(cond).To(BeNil(), "stale condition should be removed on skip path")
	})

	It("should skip the check when CRDConflictDetected is False", func() {
		shard := newAvailabilityShard("conflict-false", metav1.ConditionFalse, "NoConflicts")

		result, err := reconciler.checkAPIServiceAvailability(ctx, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeZero())
	})

	It("should keep Provisioning and requeue when CRD sync failed", func() {
		shard := newAvailabilityShard("sync-failed", metav1.ConditionTrue, "CRDSyncFailed")

		result, err := reconciler.checkAPIServiceAvailability(ctx, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(10 * time.Second))
		Expect(shard.Status.Phase).To(Equal(kubeshardv1alpha1.PhaseProvisioning))
	})

	It("should set Provisioning and requeue when no APIServices are owned", func() {
		shard := newAvailabilityShard("unavail", metav1.ConditionTrue, "CRDsSyncedToSecondary")

		result, err := reconciler.checkAPIServiceAvailability(ctx, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(10 * time.Second))
		Expect(shard.Status.Phase).To(Equal(kubeshardv1alpha1.PhaseProvisioning))

		cond := meta.FindStatusCondition(shard.Status.Conditions, kubeshardv1alpha1.ConditionAPIServicesRegistered)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("APIServicesNotAvailable"))
	})

	It("should set AllAvailable and not requeue when owned APIServices are available", func() {
		shard := newAvailabilityShard("avail", metav1.ConditionTrue, "CRDsSyncedToSecondary")

		group := "avail" + randString(4) + ".example.com"
		svcName := "v1." + group
		svc := &apiregistrationv1.APIService{
			ObjectMeta: metav1.ObjectMeta{
				Name: svcName,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: kubeshardv1alpha1.GroupVersion.String(),
						Kind:       "APIShard",
						Name:       shard.Name,
						UID:        shard.UID,
					},
				},
			},
			Spec: apiregistrationv1.APIServiceSpec{
				Group:                group,
				Version:              "v1",
				GroupPriorityMinimum: 100,
				VersionPriority:      10,
			},
		}
		Expect(k8sClient.Create(ctx, svc)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, svc) }()

		svc.Status = apiregistrationv1.APIServiceStatus{
			Conditions: []apiregistrationv1.APIServiceCondition{
				{
					Type:   apiregistrationv1.Available,
					Status: apiregistrationv1.ConditionTrue,
				},
			},
		}
		Expect(k8sClient.Status().Update(ctx, svc)).To(Succeed())

		result, err := reconciler.checkAPIServiceAvailability(ctx, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeZero())

		cond := meta.FindStatusCondition(shard.Status.Conditions, kubeshardv1alpha1.ConditionAPIServicesRegistered)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal("AllAvailable"))
	})
})

var _ = Describe("checkDeploymentHealth", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	createHealthShard := func(suffix string, storageType kubeshardv1alpha1.StorageType) *kubeshardv1alpha1.APIShard {
		nsName := "test-health-" + suffix
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		_ = k8sClient.Create(ctx, ns)

		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-health-" + suffix,
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: nsName,
				APIGroups: []kubeshardv1alpha1.APIGroupSpec{
					{Group: "tekton.dev", Versions: []string{"v1"}},
				},
				Storage: kubeshardv1alpha1.StorageSpec{
					Type: storageType,
				},
				NamespaceSync: kubeshardv1alpha1.NamespaceSyncConfig{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"type": "tenant"},
					},
				},
				Secondary: kubeshardv1alpha1.SecondarySpec{Replicas: 1},
				Kine:      kubeshardv1alpha1.KineSpec{Replicas: 1},
			},
		}
		Expect(k8sClient.Create(ctx, shard)).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, shard)).To(Succeed())
		return shard
	}

	createDeployment := func(name, namespace string, readyReplicas int32) {
		var one int32 = 1
		deploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &one,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": name},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, deploy)).To(Succeed())
		deploy.Status.Replicas = 1
		deploy.Status.ReadyReplicas = readyReplicas
		Expect(k8sClient.Status().Update(ctx, deploy)).To(Succeed())
	}

	It("should return true when Kine and Secondary are both ready (SQLite)", func() {
		shard := createHealthShard("sqlite-ok", kubeshardv1alpha1.StorageTypeSQLite)
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		createDeployment(resources.KineDeploymentName(shard), shard.Spec.TargetNamespace, 1)
		createDeployment(resources.SecondaryDeploymentName(shard), shard.Spec.TargetNamespace, 1)

		healthy, err := reconciler.checkDeploymentHealth(ctx, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(healthy).To(BeTrue())
	})

	It("should return false when Kine is not ready", func() {
		shard := createHealthShard("kine-bad", kubeshardv1alpha1.StorageTypeSQLite)
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		createDeployment(resources.KineDeploymentName(shard), shard.Spec.TargetNamespace, 0)
		createDeployment(resources.SecondaryDeploymentName(shard), shard.Spec.TargetNamespace, 1)

		healthy, err := reconciler.checkDeploymentHealth(ctx, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(healthy).To(BeFalse())
	})

	It("should check PostgreSQL StatefulSet when storage type is InClusterPostgreSQL", func() {
		shard := createHealthShard("pg-ok", kubeshardv1alpha1.StorageTypeInClusterPostgreSQL)
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		createDeployment(resources.KineDeploymentName(shard), shard.Spec.TargetNamespace, 1)
		createDeployment(resources.SecondaryDeploymentName(shard), shard.Spec.TargetNamespace, 1)

		var one int32 = 1
		pgSts := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      resources.PostgreSQLStatefulSetName(shard),
				Namespace: shard.Spec.TargetNamespace,
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: &one,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "pg"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "pg"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "pg", Image: "postgres"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, pgSts)).To(Succeed())
		pgSts.Status.Replicas = 1
		pgSts.Status.ReadyReplicas = 1
		Expect(k8sClient.Status().Update(ctx, pgSts)).To(Succeed())

		healthy, err := reconciler.checkDeploymentHealth(ctx, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(healthy).To(BeTrue())
	})

	It("should return false when PG StatefulSet is not ready", func() {
		shard := createHealthShard("pg-bad", kubeshardv1alpha1.StorageTypeInClusterPostgreSQL)
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		createDeployment(resources.KineDeploymentName(shard), shard.Spec.TargetNamespace, 1)
		createDeployment(resources.SecondaryDeploymentName(shard), shard.Spec.TargetNamespace, 1)

		var one int32 = 1
		pgSts := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      resources.PostgreSQLStatefulSetName(shard),
				Namespace: shard.Spec.TargetNamespace,
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: &one,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "pg"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "pg"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "pg", Image: "postgres"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, pgSts)).To(Succeed())
		pgSts.Status.Replicas = 1
		pgSts.Status.ReadyReplicas = 0
		Expect(k8sClient.Status().Update(ctx, pgSts)).To(Succeed())

		healthy, err := reconciler.checkDeploymentHealth(ctx, shard)
		Expect(err).NotTo(HaveOccurred())
		Expect(healthy).To(BeFalse())
	})
})

var _ = Describe("setErrorAndRequeue", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	It("should set Phase to Error and populate Message and Reconciled condition", func() {
		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-err-requeue",
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: "test-err-requeue-ns",
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
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		testErr := fmt.Errorf("something went wrong")
		result, err := reconciler.setErrorAndRequeue(ctx, shard, testErr)
		Expect(err).To(MatchError("something went wrong"))
		Expect(result).To(Equal(ctrl.Result{}))

		updated := &kubeshardv1alpha1.APIShard{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, updated)).To(Succeed())
		Expect(updated.Status.Phase).To(Equal(kubeshardv1alpha1.PhaseError))
		Expect(updated.Status.Message).To(Equal("something went wrong"))

		cond := meta.FindStatusCondition(updated.Status.Conditions, kubeshardv1alpha1.ConditionReconciled)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("ReconcileError"))
	})
})

var _ = Describe("requestHeaderCAMapper", func() {
	It("should return reconcile requests for all shards when ConfigMap matches", func() {
		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-rhca-mapper",
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: "test-rhca-mapper-ns",
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
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		mapper := requestHeaderCAMapper(k8sClient)
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      extensionAPIServerAuthCM,
				Namespace: kubeSystemNamespace,
			},
		}
		requests := mapper(ctx, cm)
		Expect(requests).NotTo(BeEmpty())

		found := false
		for _, req := range requests {
			if req.Name == "test-rhca-mapper" {
				found = true
				break
			}
		}
		Expect(found).To(BeTrue(), "expected reconcile request for test-rhca-mapper shard")
	})

	It("should return nil for non-matching ConfigMap", func() {
		mapper := requestHeaderCAMapper(k8sClient)
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "some-other-configmap",
				Namespace: kubeSystemNamespace,
			},
		}
		requests := mapper(ctx, cm)
		Expect(requests).To(BeNil())
	})

	It("should return nil for wrong namespace", func() {
		mapper := requestHeaderCAMapper(k8sClient)
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      extensionAPIServerAuthCM,
				Namespace: "default",
			},
		}
		requests := mapper(ctx, cm)
		Expect(requests).To(BeNil())
	})
})

var _ = Describe("connectionSecretMapper", func() {
	It("should return reconcile request for PostgreSQL shard with matching secret", func() {
		nsName := "test-connmap-pg"
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		_ = k8sClient.Create(ctx, ns)

		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-connmap-pg",
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: nsName,
				APIGroups: []kubeshardv1alpha1.APIGroupSpec{
					{Group: "tekton.dev", Versions: []string{"v1"}},
				},
				Storage: kubeshardv1alpha1.StorageSpec{
					Type: kubeshardv1alpha1.StorageTypePostgreSQL,
					ConnectionSecretRef: &kubeshardv1alpha1.SecretKeyReference{
						Name: "my-pg-secret",
						Key:  "dsn",
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
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		mapper := connectionSecretMapper(k8sClient)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-pg-secret",
				Namespace: nsName,
			},
		}
		requests := mapper(ctx, secret)
		Expect(requests).To(HaveLen(1))
		Expect(requests[0].Name).To(Equal("test-connmap-pg"))
	})

	It("should return no requests for non-matching secret", func() {
		mapper := connectionSecretMapper(k8sClient)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "unrelated-secret",
				Namespace: "default",
			},
		}
		requests := mapper(ctx, secret)
		Expect(requests).To(BeEmpty())
	})

	It("should return no requests for SQLite shard", func() {
		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-connmap-sqlite",
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: "test-connmap-sqlite-ns",
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
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		mapper := connectionSecretMapper(k8sClient)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "some-secret",
				Namespace: "test-connmap-sqlite-ns",
			},
		}
		requests := mapper(ctx, secret)

		for _, req := range requests {
			Expect(req.Name).NotTo(Equal("test-connmap-sqlite"))
		}
	})
})

var _ = Describe("reconcileMetrics", func() {
	var (
		shard      *kubeshardv1alpha1.APIShard
		tc         *tracking.Client
		reconciler *Reconciler
		ns         *corev1.Namespace
	)

	BeforeEach(func() {
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "metrics-test-" + randString(6)}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		shard = &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{Name: "metrics-shard-" + randString(6)},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: ns.Name,
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
				Kine:      kubeshardv1alpha1.KineSpec{Replicas: 1},
				Secondary: kubeshardv1alpha1.SecondarySpec{Replicas: 1},
			},
		}
		Expect(k8sClient.Create(ctx, shard)).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, shard)).To(Succeed())

		reconciler = &Reconciler{
			Client:                  k8sClient,
			Scheme:                  k8sClient.Scheme(),
			ServiceMonitorAvailable: true,
		}
		tc = newTrackingClient(shard)
	})

	AfterEach(func() {
		crb := &rbacv1.ClusterRoleBinding{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-metrics-reader", shard.Name)}, crb); err == nil {
			_ = k8sClient.Delete(ctx, crb)
		}
		_ = k8sClient.Delete(ctx, shard)
	})

	It("creates ServiceMonitors and RBAC when ServiceMonitor CRD is available", func() {
		err := reconciler.reconcileMetrics(ctx, tc, shard)
		Expect(err).NotTo(HaveOccurred())

		sa := &corev1.ServiceAccount{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      resources.MetricsReaderServiceAccountName(shard),
			Namespace: ns.Name,
		}, sa)).To(Succeed())

		cr := &rbacv1.ClusterRole{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: resources.MetricsReaderClusterRoleName,
		}, cr)).To(Succeed())
		Expect(cr.Rules[0].NonResourceURLs).To(ContainElement("/metrics"))

		crb := &rbacv1.ClusterRoleBinding{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: fmt.Sprintf("%s-metrics-reader", shard.Name),
		}, crb)).To(Succeed())

		role := &rbacv1.Role{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      fmt.Sprintf("%s-prometheus-discovery", shard.Name),
			Namespace: ns.Name,
		}, role)).To(Succeed())
		Expect(role.Rules).To(HaveLen(2))
		Expect(role.Rules[0].Resources).To(ConsistOf("services", "endpoints", "pods"))
		Expect(role.Rules[1].Resources).To(ConsistOf("servicemonitors"))

		rb := &rbacv1.RoleBinding{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      fmt.Sprintf("%s-prometheus-discovery", shard.Name),
			Namespace: ns.Name,
		}, rb)).To(Succeed())
		Expect(rb.Subjects[0].Name).To(Equal("prometheus-k8s"))
		Expect(rb.Subjects[0].Namespace).To(Equal("openshift-monitoring"))

		kineSM := &monitoringv1.ServiceMonitor{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      fmt.Sprintf("%s-kine-metrics", shard.Name),
			Namespace: ns.Name,
		}, kineSM)).To(Succeed())
		Expect(kineSM.Spec.Endpoints[0].Port).To(Equal("metrics"))
		Expect(*kineSM.Spec.Endpoints[0].Scheme).To(Equal(monitoringv1.SchemeHTTPS))

		apiserverSM := &monitoringv1.ServiceMonitor{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      fmt.Sprintf("%s-apiserver-metrics", shard.Name),
			Namespace: ns.Name,
		}, apiserverSM)).To(Succeed())
		Expect(apiserverSM.Spec.Endpoints[0].Scheme).NotTo(BeNil())
		Expect(*apiserverSM.Spec.Endpoints[0].Scheme).To(Equal(monitoringv1.SchemeHTTPS))
	})

	It("skips all metrics resources when CRD is unavailable", func() {
		reconciler.ServiceMonitorAvailable = false

		err := reconciler.reconcileMetrics(ctx, tc, shard)
		Expect(err).NotTo(HaveOccurred())

		sa := &corev1.ServiceAccount{}
		err = k8sClient.Get(ctx, types.NamespacedName{
			Name:      resources.MetricsReaderServiceAccountName(shard),
			Namespace: ns.Name,
		}, sa)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		kineSM := &monitoringv1.ServiceMonitor{}
		err = k8sClient.Get(ctx, types.NamespacedName{
			Name:      fmt.Sprintf("%s-kine-metrics", shard.Name),
			Namespace: ns.Name,
		}, kineSM)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		apiserverSM := &monitoringv1.ServiceMonitor{}
		err = k8sClient.Get(ctx, types.NamespacedName{
			Name:      fmt.Sprintf("%s-apiserver-metrics", shard.Name),
			Namespace: ns.Name,
		}, apiserverSM)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
})

var _ = Describe("ensureNamespace", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	It("should be idempotent when namespace already exists", func() {
		nsName := "test-ns-exists"
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-ns-exists",
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
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		err := reconciler.ensureNamespace(ctx, shard)
		Expect(err).NotTo(HaveOccurred())
	})

	It("should create namespace with correct labels", func() {
		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-ns-labels",
			},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: "test-ns-labels-ns",
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
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		err := reconciler.ensureNamespace(ctx, shard)
		Expect(err).NotTo(HaveOccurred())

		ns := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-ns-labels-ns"}, ns)).To(Succeed())
		Expect(ns.Labels).To(HaveKeyWithValue(resources.LabelManagedBy, resources.ManagedByValue))
		Expect(ns.Labels).To(HaveKeyWithValue(resources.LabelInstance, "test-ns-labels"))
	})

	It("adds OpenShift cluster-monitoring label to new namespace", func() {
		nsName := "ns-label-new-" + randString(6)
		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{Name: "ns-label-shard-" + randString(6)},
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
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		err := reconciler.ensureNamespace(ctx, shard)
		Expect(err).NotTo(HaveOccurred())

		ns := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, ns)).To(Succeed())
		Expect(ns.Labels).To(HaveKeyWithValue("openshift.io/cluster-monitoring", "true"))
	})

	It("adds OpenShift cluster-monitoring label to existing namespace", func() {
		nsName := "ns-existing-" + randString(6)
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		shard := &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{Name: "ns-label-exist-" + randString(6)},
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
		defer func() { _ = k8sClient.Delete(ctx, shard) }()

		err := reconciler.ensureNamespace(ctx, shard)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, ns)).To(Succeed())
		Expect(ns.Labels).To(HaveKeyWithValue("openshift.io/cluster-monitoring", "true"))
	})
})

var _ = Describe("PDB auto-creation", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	Context("reconcileKine", func() {
		It("should create a PDB when Kine replicas >= 2", func() {
			nsName := "test-kine-pdb-yes"
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
			_ = k8sClient.Create(ctx, ns)

			shard := &kubeshardv1alpha1.APIShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-kine-pdb-yes",
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
					Kine: kubeshardv1alpha1.KineSpec{
						Replicas: 2,
					},
				},
			}
			Expect(k8sClient.Create(ctx, shard)).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, shard)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, shard) }()

			tc := newTrackingClient(shard)
			err := reconciler.reconcileKine(ctx, tc, shard)
			Expect(err).NotTo(HaveOccurred())

			pdb := &policyv1.PodDisruptionBudget{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      resources.KineDeploymentName(shard),
				Namespace: nsName,
			}, pdb)).To(Succeed())
			Expect(pdb.Spec.MaxUnavailable).NotTo(BeNil())
			Expect(pdb.Spec.MaxUnavailable.IntValue()).To(Equal(1))
		})

		It("should not create a PDB when Kine replicas < 2", func() {
			nsName := "test-kine-pdb-no"
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
			_ = k8sClient.Create(ctx, ns)

			shard := &kubeshardv1alpha1.APIShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-kine-pdb-no",
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
					Kine: kubeshardv1alpha1.KineSpec{
						Replicas: 1,
					},
				},
			}
			Expect(k8sClient.Create(ctx, shard)).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, shard)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, shard) }()

			tc := newTrackingClient(shard)
			err := reconciler.reconcileKine(ctx, tc, shard)
			Expect(err).NotTo(HaveOccurred())

			pdb := &policyv1.PodDisruptionBudget{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resources.KineDeploymentName(shard),
				Namespace: nsName,
			}, pdb)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("reconcileSecondary", func() {
		It("should create a PDB when Secondary replicas >= 2", func() {
			nsName := "test-sec-pdb-yes"
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
			_ = k8sClient.Create(ctx, ns)

			shard := &kubeshardv1alpha1.APIShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-sec-pdb-yes",
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
						Replicas: 3,
					},
				},
			}
			Expect(k8sClient.Create(ctx, shard)).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shard.Name}, shard)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, shard) }()

			tc := newTrackingClient(shard)
			err := reconciler.reconcileSecondary(ctx, tc, shard, []string{defaultFrontProxyClient})
			Expect(err).NotTo(HaveOccurred())

			pdb := &policyv1.PodDisruptionBudget{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      resources.SecondaryDeploymentName(shard),
				Namespace: nsName,
			}, pdb)).To(Succeed())
			Expect(pdb.Spec.MaxUnavailable).NotTo(BeNil())
			Expect(pdb.Spec.MaxUnavailable.IntValue()).To(Equal(1))
		})

		It("should not create a PDB when Secondary replicas < 2", func() {
			nsName := "test-sec-pdb-no"
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
			_ = k8sClient.Create(ctx, ns)

			shard := &kubeshardv1alpha1.APIShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-sec-pdb-no",
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
			defer func() { _ = k8sClient.Delete(ctx, shard) }()

			tc := newTrackingClient(shard)
			err := reconciler.reconcileSecondary(ctx, tc, shard, []string{defaultFrontProxyClient})
			Expect(err).NotTo(HaveOccurred())

			pdb := &policyv1.PodDisruptionBudget{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resources.SecondaryDeploymentName(shard),
				Namespace: nsName,
			}, pdb)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})
})

var _ = Describe("transformCRDConversion", func() {
	It("should convert service reference to URL", func() {
		port := int32(443)
		path := "/convert"
		crd := &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Conversion: &apiextensionsv1.CustomResourceConversion{
					Strategy: apiextensionsv1.WebhookConverter,
					Webhook: &apiextensionsv1.WebhookConversion{
						ConversionReviewVersions: []string{"v1"},
						ClientConfig: &apiextensionsv1.WebhookClientConfig{
							Service: &apiextensionsv1.ServiceReference{
								Namespace: "openshift-pipelines",
								Name:      "tekton-pipelines-webhook",
								Port:      &port,
								Path:      &path,
							},
							CABundle: []byte("fake-ca-bundle"),
						},
					},
				},
			},
		}

		transformCRDConversion(crd)

		Expect(crd.Spec.Conversion.Strategy).To(Equal(apiextensionsv1.WebhookConverter))
		Expect(crd.Spec.Conversion.Webhook.ClientConfig.Service).To(BeNil())
		Expect(crd.Spec.Conversion.Webhook.ClientConfig.URL).NotTo(BeNil())
		Expect(*crd.Spec.Conversion.Webhook.ClientConfig.URL).To(Equal(
			"https://tekton-pipelines-webhook.openshift-pipelines.svc:443/convert",
		))
		Expect(crd.Spec.Conversion.Webhook.ClientConfig.CABundle).To(Equal([]byte("fake-ca-bundle")))
	})

	It("should use default port 443 when port is nil", func() {
		crd := &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Conversion: &apiextensionsv1.CustomResourceConversion{
					Strategy: apiextensionsv1.WebhookConverter,
					Webhook: &apiextensionsv1.WebhookConversion{
						ConversionReviewVersions: []string{"v1"},
						ClientConfig: &apiextensionsv1.WebhookClientConfig{
							Service: &apiextensionsv1.ServiceReference{
								Namespace: "openshift-pipelines",
								Name:      "tekton-pipelines-webhook",
							},
						},
					},
				},
			},
		}

		transformCRDConversion(crd)

		Expect(*crd.Spec.Conversion.Webhook.ClientConfig.URL).To(Equal(
			"https://tekton-pipelines-webhook.openshift-pipelines.svc:443",
		))
	})

	It("should not modify CRD with strategy None", func() {
		crd := &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Conversion: &apiextensionsv1.CustomResourceConversion{
					Strategy: apiextensionsv1.NoneConverter,
				},
			},
		}

		transformCRDConversion(crd)

		Expect(crd.Spec.Conversion.Strategy).To(Equal(apiextensionsv1.NoneConverter))
		Expect(crd.Spec.Conversion.Webhook).To(BeNil())
	})

	It("should not modify CRD with no conversion", func() {
		crd := &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{},
		}

		transformCRDConversion(crd)

		Expect(crd.Spec.Conversion).To(BeNil())
	})

	It("should not modify CRD with URL already set", func() {
		existingURL := "https://already-set.example.com:8443/convert"
		crd := &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Conversion: &apiextensionsv1.CustomResourceConversion{
					Strategy: apiextensionsv1.WebhookConverter,
					Webhook: &apiextensionsv1.WebhookConversion{
						ConversionReviewVersions: []string{"v1"},
						ClientConfig: &apiextensionsv1.WebhookClientConfig{
							URL: &existingURL,
						},
					},
				},
			},
		}

		transformCRDConversion(crd)

		Expect(crd.Spec.Conversion.Webhook.ClientConfig.Service).To(BeNil())
		Expect(*crd.Spec.Conversion.Webhook.ClientConfig.URL).To(Equal(existingURL))
	})
})

var _ = Describe("syncCRDsToSecondary", func() {
	const (
		shardName = "sync-test"
		shardNS   = "sync-test-ns"
		crdGroup  = "synctest.example.com"
		crdPlural = "synctests"
		crdName   = "synctests.synctest.example.com"
	)

	var (
		reconciler *Reconciler
		shard      *kubeshardv1alpha1.APIShard
		provider   *secondary.ClientProvider
	)

	BeforeEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: shardNS}}
		_ = k8sClient.Create(ctx, ns)

		provider = secondary.NewClientProvider(k8sClient.Scheme())
		provider.InjectClientForTest(shardName, k8sClient)

		reconciler = &Reconciler{
			Client:         k8sClient,
			Scheme:         k8sClient.Scheme(),
			ClientProvider: provider,
		}

		shard = &kubeshardv1alpha1.APIShard{
			ObjectMeta: metav1.ObjectMeta{Name: shardName},
			Spec: kubeshardv1alpha1.APIShardSpec{
				TargetNamespace: shardNS,
				APIGroups: []kubeshardv1alpha1.APIGroupSpec{
					{Group: crdGroup, Versions: []string{"v1", "v1beta1"}},
				},
				Storage: kubeshardv1alpha1.StorageSpec{Type: kubeshardv1alpha1.StorageTypeSQLite},
				NamespaceSync: kubeshardv1alpha1.NamespaceSyncConfig{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"type": "tenant"},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, shard)).To(Succeed())

		pkiSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      certs.PKISecretName(shard),
				Namespace: shardNS,
			},
			Data: map[string][]byte{
				"ca.crt":  []byte("fake-ca-cert"),
				"tls.crt": []byte("fake-tls-cert"),
				"tls.key": []byte("fake-tls-key"),
			},
		}
		Expect(k8sClient.Create(ctx, pkiSecret)).To(Succeed())

		adminSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      certs.AdminClientSecretName(shard),
				Namespace: shardNS,
			},
			Data: map[string][]byte{
				"tls.crt": []byte("fake-client-cert"),
				"tls.key": []byte("fake-client-key"),
			},
		}
		Expect(k8sClient.Create(ctx, adminSecret)).To(Succeed())
	})

	AfterEach(func() {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: crdName}, crd); err == nil {
			_ = k8sClient.Delete(ctx, crd)
		}
		_ = k8sClient.Delete(ctx, shard)
		_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: certs.PKISecretName(shard), Namespace: shardNS}})
		_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: certs.AdminClientSecretName(shard), Namespace: shardNS}})
	})

	It("should apply a CRD with URL-based conversion via SSA preserving caBundle", func() {
		url := "https://my-webhook.my-ns.svc:9443/convert"
		caBundle := []byte("test-ca-bundle-data")
		crd := &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: crdName},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: crdGroup,
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   crdPlural,
					Singular: "synctest",
					Kind:     "SyncTest",
					ListKind: "SyncTestList",
				},
				Scope: apiextensionsv1.NamespaceScoped,
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{
						Name: "v1", Served: true, Storage: true,
						Schema: &apiextensionsv1.CustomResourceValidation{
							OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object"},
						},
					},
					{
						Name: "v1beta1", Served: true, Storage: false,
						Schema: &apiextensionsv1.CustomResourceValidation{
							OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object"},
						},
					},
				},
				Conversion: &apiextensionsv1.CustomResourceConversion{
					Strategy: apiextensionsv1.WebhookConverter,
					Webhook: &apiextensionsv1.WebhookConversion{
						ConversionReviewVersions: []string{"v1"},
						ClientConfig: &apiextensionsv1.WebhookClientConfig{
							URL:      &url,
							CABundle: caBundle,
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, crd)).To(Succeed())

		err := reconciler.syncCRDsToSecondary(ctx, shard, []string{crdName})
		Expect(err).NotTo(HaveOccurred())

		synced := &apiextensionsv1.CustomResourceDefinition{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: crdName}, synced)).To(Succeed())

		Expect(synced.Spec.Conversion).NotTo(BeNil())
		Expect(synced.Spec.Conversion.Strategy).To(Equal(apiextensionsv1.WebhookConverter))
		Expect(synced.Spec.Conversion.Webhook.ClientConfig.URL).NotTo(BeNil())
		Expect(*synced.Spec.Conversion.Webhook.ClientConfig.URL).To(Equal(url))
		Expect(synced.Spec.Conversion.Webhook.ClientConfig.CABundle).To(Equal(caBundle),
			"caBundle should be preserved through the SSA apply")
	})

	It("should apply a CRD without conversion webhook via SSA", func() {
		crd := &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: crdName},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: crdGroup,
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   crdPlural,
					Singular: "synctest",
					Kind:     "SyncTest",
					ListKind: "SyncTestList",
				},
				Scope: apiextensionsv1.NamespaceScoped,
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{
						Name: "v1", Served: true, Storage: true,
						Schema: &apiextensionsv1.CustomResourceValidation{
							OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object"},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, crd)).To(Succeed())

		err := reconciler.syncCRDsToSecondary(ctx, shard, []string{crdName})
		Expect(err).NotTo(HaveOccurred())

		synced := &apiextensionsv1.CustomResourceDefinition{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: crdName}, synced)).To(Succeed())
		Expect(synced.Spec.Group).To(Equal(crdGroup))
		Expect(synced.Spec.Versions).To(HaveLen(1))
	})

	It("should return an error when a CRD does not exist on the primary", func() {
		err := reconciler.syncCRDsToSecondary(ctx, shard, []string{"nonexistent.synctest.example.com"})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("get CRD nonexistent.synctest.example.com from primary"))
	})
})
