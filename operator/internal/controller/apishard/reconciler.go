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
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/konflux-ci/konflux-ci/operator/pkg/tracking"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
	"github.com/konflux-ci/kube-shard/operator/internal/certs"
	"github.com/konflux-ci/kube-shard/operator/internal/resources"
)

const (
	requeueDelay  = 30 * time.Second
	fieldManager  = "kube-shard-operator"

	ownerLabelKey     = "kube-shard.konflux-ci.dev/owner"
	componentLabelKey = "kube-shard.konflux-ci.dev/component"
)

// managedGVKs lists all GVKs that the Reconciler may create,
// used by the tracking client for orphan cleanup.
var managedGVKs = []schema.GroupVersionKind{
	{Group: "apps", Version: "v1", Kind: "Deployment"},
	{Group: "", Version: "v1", Kind: "Service"},
	{Group: "", Version: "v1", Kind: "Secret"},
	{Group: "", Version: "v1", Kind: "ConfigMap"},
	{Group: "cert-manager.io", Version: "v1", Kind: "Issuer"},
	{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
}

// Reconciler reconciles an APIShard object.
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=apishards,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=apishards/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cert-manager.io,resources=issuers;certificates,verbs=get;list;watch;create;update;patch;delete

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var shard kubeshardv1alpha1.APIShard
	if err := r.Get(ctx, req.NamespacedName, &shard); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !shard.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// Ensure target namespace exists
	if err := r.ensureNamespace(ctx, &shard); err != nil {
		logger.Error(err, "Failed to ensure target namespace")
		return r.setErrorAndRequeue(ctx, &shard, err)
	}

	if shard.Status.Phase == "" {
		shard.Status.Phase = kubeshardv1alpha1.PhaseProvisioning
		if err := r.Status().Update(ctx, &shard); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Create a tracking client with ownership for SSA + orphan cleanup
	tc := tracking.NewClientWithOwnership(r.Client, tracking.OwnershipConfig{
		Owner:             &shard,
		OwnerLabelKey:     ownerLabelKey,
		ComponentLabelKey: componentLabelKey,
		Component:         "apishard",
		FieldManager:      fieldManager,
	})

	// Reconcile in-cluster PostgreSQL if configured
	if shard.Spec.Storage.Type == kubeshardv1alpha1.StorageTypeInClusterPostgreSQL {
		if err := r.reconcileInClusterPostgreSQL(ctx, tc, &shard); err != nil {
			logger.Error(err, "Failed to reconcile in-cluster PostgreSQL")
			return r.setErrorAndRequeue(ctx, &shard, err)
		}
	}

	// Reconcile Kine
	if err := r.reconcileKine(ctx, tc, &shard); err != nil {
		logger.Error(err, "Failed to reconcile Kine")
		return r.setErrorAndRequeue(ctx, &shard, err)
	}

	// Reconcile cert-manager resources for TLS
	if err := r.reconcileCertManager(ctx, tc, &shard); err != nil {
		logger.Error(err, "Failed to reconcile cert-manager resources")
		return r.setErrorAndRequeue(ctx, &shard, err)
	}

	// Reconcile auth-config ConfigMap for webhook authorization
	if err := r.reconcileAuthConfig(ctx, tc, &shard); err != nil {
		logger.Error(err, "Failed to reconcile auth config")
		return r.setErrorAndRequeue(ctx, &shard, err)
	}

	// Reconcile Secondary API server
	if err := r.reconcileSecondary(ctx, tc, &shard); err != nil {
		logger.Error(err, "Failed to reconcile secondary API server")
		return r.setErrorAndRequeue(ctx, &shard, err)
	}

	// Cleanup orphaned resources that were not applied this reconcile
	if err := tc.CleanupOrphans(ctx, ownerLabelKey, shard.Name, managedGVKs); err != nil {
		logger.Error(err, "Failed to cleanup orphans")
	}

	// Check health of deployments
	healthy, err := r.checkDeploymentHealth(ctx, &shard)
	if err != nil {
		return ctrl.Result{}, err
	}

	if healthy {
		shard.Status.Phase = kubeshardv1alpha1.PhaseReady
		shard.Status.SecondaryEndpoint = resources.SecondaryEndpoint(&shard)
		meta.SetStatusCondition(&shard.Status.Conditions, metav1.Condition{
			Type:               kubeshardv1alpha1.ConditionSecondaryHealthy,
			Status:             metav1.ConditionTrue,
			Reason:             "DeploymentAvailable",
			Message:            "Secondary API server is healthy",
			ObservedGeneration: shard.Generation,
		})
	} else {
		shard.Status.Phase = kubeshardv1alpha1.PhaseProvisioning
		meta.SetStatusCondition(&shard.Status.Conditions, metav1.Condition{
			Type:               kubeshardv1alpha1.ConditionSecondaryHealthy,
			Status:             metav1.ConditionFalse,
			Reason:             "DeploymentNotReady",
			Message:            "Secondary API server is not yet ready",
			ObservedGeneration: shard.Generation,
		})
	}

	shard.Status.ObservedGeneration = shard.Generation
	if err := r.Status().Update(ctx, &shard); err != nil {
		return ctrl.Result{}, err
	}

	if !healthy {
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

func (r *Reconciler) ensureNamespace(ctx context.Context, shard *kubeshardv1alpha1.APIShard) error {
	ns := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: shard.Spec.TargetNamespace}, ns)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	ns = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: shard.Spec.TargetNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kube-shard-operator",
				"app.kubernetes.io/instance":   shard.Name,
			},
		},
	}
	return r.Create(ctx, ns)
}

func (r *Reconciler) reconcileKine(ctx context.Context, tc *tracking.Client, shard *kubeshardv1alpha1.APIShard) error {
	deployment := resources.BuildKineDeployment(shard)
	if err := tc.ApplyOwned(ctx, deployment); err != nil {
		return fmt.Errorf("kine deployment: %w", err)
	}

	svc := resources.BuildKineService(shard)
	if err := tc.ApplyOwned(ctx, svc); err != nil {
		return fmt.Errorf("kine service: %w", err)
	}

	return nil
}

func (r *Reconciler) reconcileSecondary(ctx context.Context, tc *tracking.Client, shard *kubeshardv1alpha1.APIShard) error {
	deployment := resources.BuildSecondaryDeployment(shard)
	if err := tc.ApplyOwned(ctx, deployment); err != nil {
		return fmt.Errorf("secondary deployment: %w", err)
	}

	svc := resources.BuildSecondaryService(shard)
	if err := tc.ApplyOwned(ctx, svc); err != nil {
		return fmt.Errorf("secondary service: %w", err)
	}

	return nil
}

func (r *Reconciler) reconcileInClusterPostgreSQL(ctx context.Context, tc *tracking.Client, shard *kubeshardv1alpha1.APIShard) error {
	secret := resources.BuildPostgreSQLSecret(shard)
	if err := tc.ApplyOwned(ctx, secret); err != nil {
		return fmt.Errorf("postgresql secret: %w", err)
	}

	deployment := resources.BuildPostgreSQLDeployment(shard)
	if err := tc.ApplyOwned(ctx, deployment); err != nil {
		return fmt.Errorf("postgresql deployment: %w", err)
	}

	svc := resources.BuildPostgreSQLService(shard)
	if err := tc.ApplyOwned(ctx, svc); err != nil {
		return fmt.Errorf("postgresql service: %w", err)
	}

	return nil
}

func (r *Reconciler) reconcileCertManager(ctx context.Context, tc *tracking.Client, shard *kubeshardv1alpha1.APIShard) error {
	certResources := []*unstructured.Unstructured{
		certs.BuildSelfSignedIssuer(shard),
		certs.BuildCACertificate(shard),
		certs.BuildCAIssuer(shard),
		certs.BuildServingCertificate(shard),
	}

	for _, res := range certResources {
		if err := tc.ApplyOwned(ctx, res); err != nil {
			return fmt.Errorf("applying %s %s: %w", res.GetKind(), res.GetName(), err)
		}
	}

	return nil
}

func (r *Reconciler) reconcileAuthConfig(ctx context.Context, tc *tracking.Client, shard *kubeshardv1alpha1.APIShard) error {
	cmName := fmt.Sprintf("%s-auth-config", shard.Name)
	webhookConfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: primary
  cluster:
    server: https://kubernetes.default.svc
    certificate-authority: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
users:
- name: webhook
  user:
    tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
current-context: webhook
contexts:
- context:
    cluster: primary
    user: webhook
  name: webhook
`)

	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: shard.Spec.TargetNamespace,
		},
		Data: map[string]string{
			"webhook-config.yaml": webhookConfig,
		},
	}

	if err := tc.ApplyOwned(ctx, cm); err != nil {
		return fmt.Errorf("auth config configmap: %w", err)
	}

	return nil
}

func (r *Reconciler) checkDeploymentHealth(ctx context.Context, shard *kubeshardv1alpha1.APIShard) (bool, error) {
	kineDeploy := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      resources.KineDeploymentName(shard),
		Namespace: shard.Spec.TargetNamespace,
	}, kineDeploy); err != nil {
		return false, err
	}

	secondaryDeploy := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      resources.SecondaryDeploymentName(shard),
		Namespace: shard.Spec.TargetNamespace,
	}, secondaryDeploy); err != nil {
		return false, err
	}

	kineReady := kineDeploy.Status.ReadyReplicas > 0 &&
		kineDeploy.Status.ReadyReplicas == kineDeploy.Status.Replicas
	secondaryReady := secondaryDeploy.Status.ReadyReplicas > 0 &&
		secondaryDeploy.Status.ReadyReplicas == secondaryDeploy.Status.Replicas

	return kineReady && secondaryReady, nil
}

func (r *Reconciler) setErrorAndRequeue(ctx context.Context, shard *kubeshardv1alpha1.APIShard, reconcileErr error) (ctrl.Result, error) {
	shard.Status.Phase = kubeshardv1alpha1.PhaseError
	shard.Status.ObservedGeneration = shard.Generation
	if err := r.Status().Update(ctx, shard); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueDelay}, reconcileErr
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeshardv1alpha1.APIShard{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Named("apishard").
		Complete(r)
}
