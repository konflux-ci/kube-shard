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
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
	"github.com/konflux-ci/kube-shard/operator/internal/resources"
)

const (
	finalizerName = "kube-shard.konflux-ci.dev/finalizer"
	requeueDelay  = 30 * time.Second
)

// APIShardReconciler reconciles a APIShard object
type APIShardReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=apishards,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=apishards/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=apishards/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

func (r *APIShardReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var shard kubeshardv1alpha1.APIShard
	if err := r.Get(ctx, req.NamespacedName, &shard); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !shard.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &shard)
	}

	if !controllerutil.ContainsFinalizer(&shard, finalizerName) {
		controllerutil.AddFinalizer(&shard, finalizerName)
		if err := r.Update(ctx, &shard); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Ensure target namespace exists
	if err := r.ensureNamespace(ctx, &shard); err != nil {
		logger.Error(err, "Failed to ensure target namespace")
		return r.setPhaseAndRequeue(ctx, &shard, kubeshardv1alpha1.PhaseError, err)
	}

	// Set phase to Provisioning if not already set
	if shard.Status.Phase == "" {
		shard.Status.Phase = kubeshardv1alpha1.PhaseProvisioning
		if err := r.Status().Update(ctx, &shard); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Reconcile in-cluster PostgreSQL if configured
	if shard.Spec.Storage.Type == kubeshardv1alpha1.StorageTypeInClusterPostgreSQL {
		if err := r.reconcileInClusterPostgreSQL(ctx, &shard); err != nil {
			logger.Error(err, "Failed to reconcile in-cluster PostgreSQL")
			return r.setPhaseAndRequeue(ctx, &shard, kubeshardv1alpha1.PhaseError, err)
		}
	}

	// Reconcile Kine
	if err := r.reconcileKine(ctx, &shard); err != nil {
		logger.Error(err, "Failed to reconcile Kine")
		return r.setPhaseAndRequeue(ctx, &shard, kubeshardv1alpha1.PhaseError, err)
	}

	// Reconcile Secondary API server
	if err := r.reconcileSecondary(ctx, &shard); err != nil {
		logger.Error(err, "Failed to reconcile secondary API server")
		return r.setPhaseAndRequeue(ctx, &shard, kubeshardv1alpha1.PhaseError, err)
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

func (r *APIShardReconciler) reconcileDelete(ctx context.Context, shard *kubeshardv1alpha1.APIShard) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling APIShard deletion")

	controllerutil.RemoveFinalizer(shard, finalizerName)
	if err := r.Update(ctx, shard); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *APIShardReconciler) ensureNamespace(ctx context.Context, shard *kubeshardv1alpha1.APIShard) error {
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

func (r *APIShardReconciler) reconcileKine(ctx context.Context, shard *kubeshardv1alpha1.APIShard) error {
	// Reconcile Kine Deployment
	desired := resources.BuildKineDeployment(shard)
	if err := r.reconcileDeployment(ctx, shard, desired); err != nil {
		return fmt.Errorf("kine deployment: %w", err)
	}

	// Reconcile Kine Service
	desiredSvc := resources.BuildKineService(shard)
	if err := r.reconcileService(ctx, shard, desiredSvc); err != nil {
		return fmt.Errorf("kine service: %w", err)
	}

	return nil
}

func (r *APIShardReconciler) reconcileSecondary(ctx context.Context, shard *kubeshardv1alpha1.APIShard) error {
	// Reconcile Secondary Deployment
	desired := resources.BuildSecondaryDeployment(shard)
	if err := r.reconcileDeployment(ctx, shard, desired); err != nil {
		return fmt.Errorf("secondary deployment: %w", err)
	}

	// Reconcile Secondary Service
	desiredSvc := resources.BuildSecondaryService(shard)
	if err := r.reconcileService(ctx, shard, desiredSvc); err != nil {
		return fmt.Errorf("secondary service: %w", err)
	}

	return nil
}

func (r *APIShardReconciler) reconcileDeployment(ctx context.Context, shard *kubeshardv1alpha1.APIShard, desired *appsv1.Deployment) error {
	existing := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		if err := ctrl.SetControllerReference(shard, desired, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Update if spec changed
	if !equality.Semantic.DeepEqual(existing.Spec.Template.Spec, desired.Spec.Template.Spec) ||
		!equality.Semantic.DeepEqual(existing.Spec.Replicas, desired.Spec.Replicas) {
		existing.Spec.Template = desired.Spec.Template
		existing.Spec.Replicas = desired.Spec.Replicas
		return r.Update(ctx, existing)
	}

	return nil
}

func (r *APIShardReconciler) reconcileService(ctx context.Context, shard *kubeshardv1alpha1.APIShard, desired *corev1.Service) error {
	existing := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		if err := ctrl.SetControllerReference(shard, desired, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Update if spec changed (preserve ClusterIP)
	if !equality.Semantic.DeepEqual(existing.Spec.Ports, desired.Spec.Ports) ||
		!equality.Semantic.DeepEqual(existing.Spec.Selector, desired.Spec.Selector) {
		existing.Spec.Ports = desired.Spec.Ports
		existing.Spec.Selector = desired.Spec.Selector
		return r.Update(ctx, existing)
	}

	return nil
}

func (r *APIShardReconciler) checkDeploymentHealth(ctx context.Context, shard *kubeshardv1alpha1.APIShard) (bool, error) {
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

func (r *APIShardReconciler) setPhaseAndRequeue(ctx context.Context, shard *kubeshardv1alpha1.APIShard, phase string, reconcileErr error) (ctrl.Result, error) {
	shard.Status.Phase = phase
	shard.Status.ObservedGeneration = shard.Generation
	if err := r.Status().Update(ctx, shard); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueDelay}, reconcileErr
}

func (r *APIShardReconciler) reconcileInClusterPostgreSQL(ctx context.Context, shard *kubeshardv1alpha1.APIShard) error {
	// Secret
	desiredSecret := resources.BuildPostgreSQLSecret(shard)
	existingSecret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: desiredSecret.Name, Namespace: desiredSecret.Namespace}, existingSecret)
	if apierrors.IsNotFound(err) {
		if err := ctrl.SetControllerReference(shard, desiredSecret, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, desiredSecret); err != nil {
			return fmt.Errorf("creating postgresql secret: %w", err)
		}
	} else if err != nil {
		return err
	}

	// Deployment
	desiredDeploy := resources.BuildPostgreSQLDeployment(shard)
	if err := r.reconcileDeployment(ctx, shard, desiredDeploy); err != nil {
		return fmt.Errorf("postgresql deployment: %w", err)
	}

	// Service
	desiredSvc := resources.BuildPostgreSQLService(shard)
	if err := r.reconcileService(ctx, shard, desiredSvc); err != nil {
		return fmt.Errorf("postgresql service: %w", err)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *APIShardReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeshardv1alpha1.APIShard{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Named("apishard").
		Complete(r)
}
