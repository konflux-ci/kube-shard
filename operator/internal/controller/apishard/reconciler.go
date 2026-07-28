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
	"encoding/base64"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/konflux-ci/konflux-ci/operator/pkg/tracking"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
	"github.com/konflux-ci/kube-shard/operator/internal/aggregation"
	"github.com/konflux-ci/kube-shard/operator/internal/certs"
	shardpredicate "github.com/konflux-ci/kube-shard/operator/internal/predicate"
	"github.com/konflux-ci/kube-shard/operator/internal/resources"
	"github.com/konflux-ci/kube-shard/operator/internal/secondary"
)

const (
	fieldManager = "kube-shard-operator"

	finalizerName     = "kube-shard.konflux-ci.dev/apiservice-cleanup"
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
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRoleBinding"},
	{Group: "cert-manager.io", Version: "v1", Kind: "Issuer"},
	{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
}

// Reconciler reconciles an APIShard object.
type Reconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ClientProvider *secondary.ClientProvider
}

// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=apishards,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=apishards/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cert-manager.io,resources=issuers;certificates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apiregistration.k8s.io,resources=apiservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=webhooksyncs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives the desired state for a single APIShard resource. It provisions
// the target namespace, storage backend, TLS certificates, auth configuration, and
// the secondary API server, then checks deployment health and updates status.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var shard kubeshardv1alpha1.APIShard
	if err := r.Get(ctx, req.NamespacedName, &shard); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion: remove APIServices before allowing the APIShard to be
	// deleted. A finalizer is used instead of ownerReferences because deletion
	// order matters: if the namespace-scoped resources (secondary Deployment,
	// Kine) were garbage-collected concurrently with the APIServices, there
	// would be a window where the APIService still routes traffic to a dead
	// backend, causing 503s for clients of the aggregated API groups. The
	// finalizer ensures APIServices are deregistered first, stopping traffic
	// routing, before the rest of the stack is torn down.
	if !shard.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&shard, finalizerName) {
			for _, name := range shard.Status.RegisteredAPIServices {
				apiSvc := &apiregistrationv1.APIService{}
				if err := r.Get(ctx, types.NamespacedName{Name: name}, apiSvc); err != nil {
					if apierrors.IsNotFound(err) {
						continue
					}
					return ctrl.Result{}, err
				}
				if err := r.Delete(ctx, apiSvc); err != nil && !apierrors.IsNotFound(err) {
					return ctrl.Result{}, err
				}
			}
			controllerutil.RemoveFinalizer(&shard, finalizerName)
			if err := r.Update(ctx, &shard); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure the finalizer is present before any resource creation
	if !controllerutil.ContainsFinalizer(&shard, finalizerName) {
		base := shard.DeepCopy()
		controllerutil.AddFinalizer(&shard, finalizerName)
		if err := r.Patch(ctx, &shard, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
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

	// Bind the secondary's ServiceAccount to system:auth-delegator
	if err := r.reconcileAuthDelegator(ctx, tc, &shard); err != nil {
		logger.Error(err, "Failed to reconcile auth-delegator binding")
		return r.setErrorAndRequeue(ctx, &shard, err)
	}

	// Copy front-proxy CA from primary for request-header authentication
	if err := r.reconcileRequestHeaderCA(ctx, tc, &shard); err != nil {
		logger.Error(err, "Failed to reconcile requestheader CA")
		return r.setErrorAndRequeue(ctx, &shard, err)
	}

	// Create admin kubeconfig Secret (depends on PKI secret, not the secondary)
	if err := r.reconcileAdminKubeconfig(ctx, tc, &shard); err != nil {
		logger.Error(err, "Failed to reconcile admin kubeconfig")
		return r.setErrorAndRequeue(ctx, &shard, err)
	}

	// Reconcile Secondary API server
	if err := r.reconcileSecondary(ctx, tc, &shard); err != nil {
		logger.Error(err, "Failed to reconcile secondary API server")
		return r.setErrorAndRequeue(ctx, &shard, err)
	}

	// Register APIService objects on the primary cluster
	if err := r.reconcileAPIServices(ctx, &shard); err != nil {
		logger.Error(err, "Failed to reconcile APIServices")
		return r.setErrorAndRequeue(ctx, &shard, err)
	}

	// Detect CRD conflicts and sync conflicting CRDs to the secondary.
	// When CRDs exist on the primary for aggregated groups, they shadow
	// the APIService registration. The operator syncs them to the secondary
	// so they can be served there, and reports the conflict so the user
	// can delete the CRDs from the primary (unless forceAggregation is set).
	conflictResult, err := aggregation.DetectCRDConflicts(ctx, r.Client, &shard)
	if err != nil {
		logger.Error(err, "Failed to detect CRD conflicts")
	} else if conflictResult != nil && conflictResult.HasConflict {
		if syncErr := r.syncCRDsToSecondary(ctx, &shard, conflictResult.ConflictingCRDs); syncErr != nil {
			logger.Error(syncErr, "Failed to sync conflicting CRDs to secondary")
		}
		if shard.Spec.ForceAggregation {
			meta.SetStatusCondition(&shard.Status.Conditions, metav1.Condition{
				Type:               kubeshardv1alpha1.ConditionCRDConflictDetected,
				Status:             metav1.ConditionTrue,
				Reason:             "ForcedAggregation",
				Message:            conflictResult.Message + "; aggregation forced via spec.forceAggregation",
				ObservedGeneration: shard.Generation,
			})
		} else {
			meta.SetStatusCondition(&shard.Status.Conditions, metav1.Condition{
				Type:               kubeshardv1alpha1.ConditionCRDConflictDetected,
				Status:             metav1.ConditionTrue,
				Reason:             "CRDsExistOnPrimary",
				Message:            conflictResult.Message,
				ObservedGeneration: shard.Generation,
			})
			shard.Status.Phase = kubeshardv1alpha1.PhaseBlocked
		}
	} else {
		meta.SetStatusCondition(&shard.Status.Conditions, metav1.Condition{
			Type:               kubeshardv1alpha1.ConditionCRDConflictDetected,
			Status:             metav1.ConditionFalse,
			Reason:             "NoConflicts",
			Message:            "No conflicting CRDs detected on primary",
			ObservedGeneration: shard.Generation,
		})
		if shard.Status.Phase == kubeshardv1alpha1.PhaseBlocked {
			shard.Status.Phase = ""
		}
	}

	// Check health of deployments
	healthy, err := r.checkDeploymentHealth(ctx, &shard)
	if err != nil {
		return ctrl.Result{}, err
	}

	if healthy {
		if shard.Status.Phase != kubeshardv1alpha1.PhaseBlocked {
			shard.Status.Phase = kubeshardv1alpha1.PhaseReady
		}
		shard.Status.SecondaryEndpoint = resources.SecondaryEndpoint(&shard)
		meta.SetStatusCondition(&shard.Status.Conditions, metav1.Condition{
			Type:               kubeshardv1alpha1.ConditionSecondaryHealthy,
			Status:             metav1.ConditionTrue,
			Reason:             "DeploymentAvailable",
			Message:            "Secondary API server is healthy",
			ObservedGeneration: shard.Generation,
		})

		if r.verifySecondaryAuth(ctx, &shard) {
			if err := r.reconcileNamespaceSync(ctx, tc, &shard); err != nil {
				logger.Error(err, "Failed to reconcile NamespaceSync")
			}
			if err := r.reconcileWebhookSync(ctx, tc, &shard); err != nil {
				logger.Error(err, "Failed to reconcile WebhookSync")
			}
			r.aggregateSubCRStatus(ctx, &shard)
		} else {
			logger.V(1).Info("Secondary auth not ready yet, deferring sub-CR creation")
		}
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

	// Cleanup orphaned resources only after all apply steps have completed,
	// so the tracked set is complete and nothing gets incorrectly deleted.
	if err := tc.CleanupOrphans(ctx, ownerLabelKey, shard.Name, managedGVKs); err != nil {
		logger.Error(err, "Failed to cleanup orphans")
	}

	shard.Status.Message = ""
	shard.Status.ObservedGeneration = shard.Generation
	meta.SetStatusCondition(&shard.Status.Conditions, metav1.Condition{
		Type:               kubeshardv1alpha1.ConditionReconciled,
		Status:             metav1.ConditionTrue,
		Reason:             "ReconcileSucceeded",
		Message:            "All resources reconciled successfully",
		ObservedGeneration: shard.Generation,
	})
	if err := r.Status().Update(ctx, &shard); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// ensureNamespace creates the target namespace if it doesn't already exist.
// This deliberately does NOT use the tracking client because ApplyOwned sets an
// owner reference — if the APIShard were deleted, garbage collection would
// cascade-delete the namespace and all workloads inside it.
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

// reconcileKine ensures the Kine deployment and service exist in the target namespace.
// Kine translates etcd gRPC calls into SQL queries against the configured storage backend.
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

// reconcileSecondary ensures the secondary kube-apiserver deployment and its
// ClusterIP service exist. The secondary API server serves the aggregated API
// groups backed by Kine.
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

// reconcileInClusterPostgreSQL deploys a PostgreSQL instance inside the target
// namespace along with a credentials Secret. Only called when the storage type
// is InClusterPostgreSQL; external PostgreSQL expects user-provided credentials.
//
// If the credentials Secret already exists, the existing password is reused to
// avoid breaking the running PostgreSQL instance. A new random password is
// generated only on first creation.
func (r *Reconciler) reconcileInClusterPostgreSQL(ctx context.Context, tc *tracking.Client, shard *kubeshardv1alpha1.APIShard) error {
	password, err := r.getOrGeneratePostgresPassword(ctx, shard)
	if err != nil {
		return fmt.Errorf("postgres password: %w", err)
	}

	secret := resources.BuildPostgreSQLSecret(shard, password)
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

// getOrGeneratePostgresPassword returns the existing PostgreSQL password from
// the credentials Secret if it exists, or generates a new cryptographically
// random one. This ensures password stability across reconciliation loops.
func (r *Reconciler) getOrGeneratePostgresPassword(ctx context.Context, shard *kubeshardv1alpha1.APIShard) (string, error) {
	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      resources.PostgreSQLSecretName(shard),
		Namespace: shard.Spec.TargetNamespace,
	}, existing)
	if err == nil {
		if pw, ok := existing.Data["POSTGRES_PASSWORD"]; ok && len(pw) > 0 {
			return string(pw), nil
		}
	}

	return resources.GeneratePassword()
}

// reconcileCertManager creates the cert-manager Issuer and Certificate resources
// that provision TLS for the secondary API server. The chain is:
// self-signed Issuer -> CA Certificate -> CA-backed Issuer -> serving Certificate.
func (r *Reconciler) reconcileCertManager(ctx context.Context, tc *tracking.Client, shard *kubeshardv1alpha1.APIShard) error {
	certResources := []*unstructured.Unstructured{
		certs.BuildSelfSignedIssuer(shard),
		certs.BuildCACertificate(shard),
		certs.BuildCAIssuer(shard),
		certs.BuildServingCertificate(shard),
		certs.BuildAdminClientCertificate(shard),
	}

	for _, res := range certResources {
		if err := tc.ApplyOwned(ctx, res); err != nil {
			return fmt.Errorf("applying %s %s: %w", res.GetKind(), res.GetName(), err)
		}
	}

	return nil
}

// reconcileAuthConfig creates a ConfigMap containing the kubeconfig used by the
// secondary API server's --authorization-webhook-config-file flag. This allows
// the secondary to delegate SubjectAccessReview calls to the primary cluster.
func (r *Reconciler) reconcileAuthConfig(ctx context.Context, tc *tracking.Client, shard *kubeshardv1alpha1.APIShard) error {
	cmName := fmt.Sprintf("%s-authz-config", shard.Name)
	webhookConfig := `apiVersion: v1
kind: Config
clusters:
- name: primary
  cluster:
    server: https://kubernetes.default.svc/apis/authorization.k8s.io/v1/subjectaccessreviews
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
`

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

// reconcileAuthDelegator creates a ClusterRoleBinding that grants the secondary
// API server's ServiceAccount the system:auth-delegator role. This allows the
// secondary to delegate SubjectAccessReview calls to the primary cluster when
// using webhook authorization.
func (r *Reconciler) reconcileAuthDelegator(ctx context.Context, tc *tracking.Client, shard *kubeshardv1alpha1.APIShard) error {
	bindingName := fmt.Sprintf("%s-auth-delegator", shard.Name)

	crb := &rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: bindingName,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "system:auth-delegator",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "default",
				Namespace: shard.Spec.TargetNamespace,
			},
		},
	}

	if err := controllerutil.SetControllerReference(shard, crb, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on auth-delegator CRB: %w", err)
	}

	if err := tc.ApplyOwned(ctx, crb); err != nil {
		return fmt.Errorf("auth-delegator ClusterRoleBinding: %w", err)
	}

	return nil
}

// reconcileRequestHeaderCA copies the front-proxy CA from the primary cluster's
// extension-apiserver-authentication ConfigMap in kube-system into a ConfigMap
// in the target namespace. The secondary API server uses this CA to verify
// request-header identity forwarding from the primary's aggregation proxy.
func (r *Reconciler) reconcileRequestHeaderCA(ctx context.Context, tc *tracking.Client, shard *kubeshardv1alpha1.APIShard) error {
	sourceCM := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      "extension-apiserver-authentication",
		Namespace: "kube-system",
	}, sourceCM); err != nil {
		return fmt.Errorf("reading extension-apiserver-authentication from kube-system: %w", err)
	}

	caData := sourceCM.Data["requestheader-client-ca-file"]
	if caData == "" {
		return fmt.Errorf("requestheader-client-ca-file not found in extension-apiserver-authentication ConfigMap")
	}

	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-requestheader-ca", shard.Name),
			Namespace: shard.Spec.TargetNamespace,
		},
		Data: map[string]string{
			"requestheader-client-ca-file": caData,
		},
	}

	if err := tc.ApplyOwned(ctx, cm); err != nil {
		return fmt.Errorf("requestheader CA configmap: %w", err)
	}

	return nil
}

// reconcileAPIServices registers APIService objects on the primary cluster and
// cleans up stale ones when API groups are removed from the spec. It reads the
// CA bundle from the cert-manager Secret — if the Secret isn't ready yet, it
// returns an error to trigger a requeue.
//
// Orphan detection uses the status-tracked list of previously registered names
// rather than labels, so external actors cannot trick the operator into deleting
// APIServices it does not own.
func (r *Reconciler) reconcileAPIServices(ctx context.Context, shard *kubeshardv1alpha1.APIShard) error {
	secretName := certs.PKISecretName(shard)
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: shard.Spec.TargetNamespace,
	}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("TLS secret %s not yet available (cert-manager pending)", secretName)
		}
		return fmt.Errorf("reading TLS secret %s: %w", secretName, err)
	}

	caBundle := secret.Data["ca.crt"]

	result, err := aggregation.Reconcile(ctx, r.Client, r.Scheme, shard, caBundle, shard.Status.RegisteredAPIServices, fieldManager, shard.Spec.ForceAggregation)
	if err != nil {
		return err
	}

	shard.Status.RegisteredAPIServices = result.Registered
	return nil
}

// reconcileAdminKubeconfig creates a Secret containing a kubeconfig and client
// certificate credentials for connecting to the secondary API server. Sub-CRs
// (NamespaceSync, WebhookSync) reference this secret to authenticate. The
// secondary kAS has --client-ca-file set to the PKI CA, so client certificates
// signed by that CA are accepted.
func (r *Reconciler) reconcileAdminKubeconfig(ctx context.Context, tc *tracking.Client, shard *kubeshardv1alpha1.APIShard) error {
	pkiSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      certs.PKISecretName(shard),
		Namespace: shard.Spec.TargetNamespace,
	}, pkiSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("reading PKI secret: %w", err)
	}
	caData := pkiSecret.Data["ca.crt"]
	if len(caData) == 0 {
		return nil
	}

	adminSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      certs.AdminClientSecretName(shard),
		Namespace: shard.Spec.TargetNamespace,
	}, adminSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("reading admin client cert secret: %w", err)
	}
	clientCert := adminSecret.Data["tls.crt"]
	clientKey := adminSecret.Data["tls.key"]
	if len(clientCert) == 0 || len(clientKey) == 0 {
		return nil
	}

	serverURL := fmt.Sprintf("https://%s-apiserver.%s.svc", shard.Name, shard.Spec.TargetNamespace)
	caBase64 := base64.StdEncoding.EncodeToString(caData)
	certBase64 := base64.StdEncoding.EncodeToString(clientCert)
	keyBase64 := base64.StdEncoding.EncodeToString(clientKey)

	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: secondary
  cluster:
    server: %s
    certificate-authority-data: %s
users:
- name: admin
  user:
    client-certificate-data: %s
    client-key-data: %s
contexts:
- name: default
  context:
    cluster: secondary
    user: admin
current-context: default
`, serverURL, caBase64, certBase64, keyBase64)

	secretName := fmt.Sprintf("%s-admin-kubeconfig", shard.Name)
	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: shard.Spec.TargetNamespace,
		},
		Data: map[string][]byte{
			"kubeconfig": []byte(kubeconfig),
			"tls.crt":    clientCert,
			"tls.key":    clientKey,
		},
	}

	if err := tc.ApplyOwned(ctx, secret); err != nil {
		return fmt.Errorf("admin kubeconfig secret: %w", err)
	}

	shard.Status.ConnectionSecret = &kubeshardv1alpha1.ConnectionSecretReference{
		Name:      secretName,
		Namespace: shard.Spec.TargetNamespace,
	}

	return nil
}

// reconcileNamespaceSync creates or updates a NamespaceSync CR that mirrors
// namespaces from the primary cluster to the secondary API server.
func (r *Reconciler) reconcileNamespaceSync(ctx context.Context, tc *tracking.Client, shard *kubeshardv1alpha1.APIShard) error {
	nsSync := &kubeshardv1alpha1.NamespaceSync{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-ns-sync", shard.Name),
			Namespace: shard.Spec.TargetNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/instance":   shard.Name,
				"app.kubernetes.io/managed-by": "kube-shard-operator",
			},
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, nsSync, func() error {
		if err := tc.SetOwnership(nsSync); err != nil {
			return err
		}

		nsSync.Spec = kubeshardv1alpha1.NamespaceSyncSpec{
			SecondaryConnection: kubeshardv1alpha1.SecondaryConnectionSpec{
				ServiceRef: kubeshardv1alpha1.ServiceReference{
					Name:      fmt.Sprintf("%s-apiserver", shard.Name),
					Namespace: shard.Spec.TargetNamespace,
					Port:      443,
				},
				AuthSecretRef: kubeshardv1alpha1.LocalSecretReference{
					Name: certs.AdminClientSecretName(shard),
				},
				CASecretRef: kubeshardv1alpha1.LocalSecretReference{
					Name: certs.PKISecretName(shard),
				},
			},
			LabelSelector: shard.Spec.NamespaceSync.LabelSelector,
		}

		return nil
	})

	return err
}

// reconcileWebhookSync creates or updates a WebhookSync CR that mirrors
// webhook configurations from the primary to the secondary API server.
func (r *Reconciler) reconcileWebhookSync(ctx context.Context, tc *tracking.Client, shard *kubeshardv1alpha1.APIShard) error {
	apiGroups := make([]string, 0, len(shard.Spec.APIGroups))
	for _, ag := range shard.Spec.APIGroups {
		apiGroups = append(apiGroups, ag.Group)
	}

	whSync := &kubeshardv1alpha1.WebhookSync{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-webhook-sync", shard.Name),
			Namespace: shard.Spec.TargetNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/instance":   shard.Name,
				"app.kubernetes.io/managed-by": "kube-shard-operator",
			},
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, whSync, func() error {
		if err := tc.SetOwnership(whSync); err != nil {
			return err
		}

		whSync.Spec = kubeshardv1alpha1.WebhookSyncSpec{
			SecondaryConnection: kubeshardv1alpha1.SecondaryConnectionSpec{
				ServiceRef: kubeshardv1alpha1.ServiceReference{
					Name:      fmt.Sprintf("%s-apiserver", shard.Name),
					Namespace: shard.Spec.TargetNamespace,
					Port:      443,
				},
				AuthSecretRef: kubeshardv1alpha1.LocalSecretReference{
					Name: certs.AdminClientSecretName(shard),
				},
				CASecretRef: kubeshardv1alpha1.LocalSecretReference{
					Name: certs.PKISecretName(shard),
				},
			},
			APIGroups: apiGroups,
		}

		return nil
	})

	return err
}

// aggregateSubCRStatus reads the status of NamespaceSync and WebhookSync sub-CRs
// and propagates their Ready condition onto the APIShard.
func (r *Reconciler) aggregateSubCRStatus(ctx context.Context, shard *kubeshardv1alpha1.APIShard) {
	nsSync := &kubeshardv1alpha1.NamespaceSync{}
	nsSyncName := fmt.Sprintf("%s-ns-sync", shard.Name)
	if err := r.Get(ctx, types.NamespacedName{Name: nsSyncName, Namespace: shard.Spec.TargetNamespace}, nsSync); err != nil {
		return
	}
	readyCond := meta.FindStatusCondition(nsSync.Status.Conditions, "Ready")
	if readyCond != nil {
		meta.SetStatusCondition(&shard.Status.Conditions, metav1.Condition{
			Type:               kubeshardv1alpha1.ConditionNamespaceSyncReady,
			Status:             readyCond.Status,
			Reason:             readyCond.Reason,
			Message:            readyCond.Message,
			ObservedGeneration: shard.Generation,
		})
	}

	whSync := &kubeshardv1alpha1.WebhookSync{}
	whSyncName := fmt.Sprintf("%s-webhook-sync", shard.Name)
	if err := r.Get(ctx, types.NamespacedName{Name: whSyncName, Namespace: shard.Spec.TargetNamespace}, whSync); err != nil {
		return
	}
	readyCond = meta.FindStatusCondition(whSync.Status.Conditions, "Ready")
	if readyCond != nil {
		meta.SetStatusCondition(&shard.Status.Conditions, metav1.Condition{
			Type:               kubeshardv1alpha1.ConditionWebhookSyncReady,
			Status:             readyCond.Status,
			Reason:             readyCond.Reason,
			Message:            readyCond.Message,
			ObservedGeneration: shard.Generation,
		})
	}
}

// syncCRDsToSecondary copies CRDs from the primary to the secondary API server.
// This allows the secondary to serve these API groups once the CRDs are removed
// from the primary (resolving the aggregation conflict).
func (r *Reconciler) syncCRDsToSecondary(ctx context.Context, shard *kubeshardv1alpha1.APIShard, crdNames []string) error {
	logger := log.FromContext(ctx)

	if r.ClientProvider == nil {
		return fmt.Errorf("ClientProvider not configured")
	}

	endpoint := resources.SecondaryEndpoint(shard)
	if endpoint == "" {
		return fmt.Errorf("secondary endpoint not available yet")
	}

	pkiSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      certs.PKISecretName(shard),
		Namespace: shard.Spec.TargetNamespace,
	}, pkiSecret); err != nil {
		return fmt.Errorf("reading PKI secret for CRD sync: %w", err)
	}

	adminSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      certs.AdminClientSecretName(shard),
		Namespace: shard.Spec.TargetNamespace,
	}, adminSecret); err != nil {
		return fmt.Errorf("reading admin client cert for CRD sync: %w", err)
	}

	secondaryClient, err := r.ClientProvider.GetOrCreate(shard.Name, secondary.ClientConfig{
		Host:       endpoint,
		CACert:     pkiSecret.Data["ca.crt"],
		ClientCert: adminSecret.Data["tls.crt"],
		ClientKey:  adminSecret.Data["tls.key"],
	})
	if err != nil {
		return fmt.Errorf("creating secondary client: %w", err)
	}

	for _, name := range crdNames {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := r.Get(ctx, types.NamespacedName{Name: name}, crd); err != nil {
			logger.Error(err, "Failed to get CRD from primary", "crd", name)
			continue
		}

		secondaryCRD := crd.DeepCopy()
		secondaryCRD.ResourceVersion = ""
		secondaryCRD.UID = ""
		secondaryCRD.OwnerReferences = nil
		secondaryCRD.ManagedFields = nil
		secondaryCRD.Generation = 0
		secondaryCRD.Finalizers = nil

		existing := &apiextensionsv1.CustomResourceDefinition{}
		err := secondaryClient.Get(ctx, types.NamespacedName{Name: name}, existing)
		if apierrors.IsNotFound(err) {
			if createErr := secondaryClient.Create(ctx, secondaryCRD); createErr != nil {
				logger.Error(createErr, "Failed to create CRD on secondary", "crd", name)
				continue
			}
			logger.Info("Synced CRD to secondary", "crd", name)
		} else if err != nil {
			logger.Error(err, "Failed to check CRD on secondary", "crd", name)
		}
	}

	return nil
}

// verifySecondaryAuth builds a client for the secondary API server using the
// admin credentials and performs a lightweight API discovery call. This gate
// prevents sub-CR creation until the secondary actually accepts authenticated
// requests, avoiding a flood of 401 errors when the kube-apiserver hasn't
// finished loading its client CA file.
func (r *Reconciler) verifySecondaryAuth(ctx context.Context, shard *kubeshardv1alpha1.APIShard) bool {
	logger := log.FromContext(ctx)

	pkiSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      certs.PKISecretName(shard),
		Namespace: shard.Spec.TargetNamespace,
	}, pkiSecret); err != nil {
		return false
	}

	adminSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      certs.AdminClientSecretName(shard),
		Namespace: shard.Spec.TargetNamespace,
	}, adminSecret); err != nil {
		return false
	}

	cfg := secondary.ClientConfig{
		Host:       resources.SecondaryEndpoint(shard),
		CACert:     pkiSecret.Data["ca.crt"],
		ClientCert: adminSecret.Data["tls.crt"],
		ClientKey:  adminSecret.Data["tls.key"],
	}

	if len(cfg.CACert) == 0 || len(cfg.ClientCert) == 0 || len(cfg.ClientKey) == 0 {
		return false
	}

	c, err := r.ClientProvider.GetOrCreate(shard.Name+"-verify", cfg)
	if err != nil {
		logger.V(1).Info("Failed to create verification client", "error", err)
		return false
	}

	var nsList corev1.NamespaceList
	if err := c.List(ctx, &nsList); err != nil {
		logger.V(1).Info("Secondary auth verification failed", "error", err)
		r.ClientProvider.Invalidate(shard.Name + "-verify")
		return false
	}

	return true
}

// checkDeploymentHealth returns true when both the Kine and secondary API server
// deployments have all replicas ready.
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

// setErrorAndRequeue sets the APIShard phase to Error, surfaces the error
// message in status.message, and returns a requeue result along with the
// original error for logging by the controller runtime.
func (r *Reconciler) setErrorAndRequeue(ctx context.Context, shard *kubeshardv1alpha1.APIShard, reconcileErr error) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	shard.Status.Phase = kubeshardv1alpha1.PhaseError
	shard.Status.Message = reconcileErr.Error()
	shard.Status.ObservedGeneration = shard.Generation
	meta.SetStatusCondition(&shard.Status.Conditions, metav1.Condition{
		Type:               kubeshardv1alpha1.ConditionReconciled,
		Status:             metav1.ConditionFalse,
		Reason:             "ReconcileError",
		Message:            reconcileErr.Error(),
		ObservedGeneration: shard.Generation,
	})
	if err := r.Status().Update(ctx, shard); err != nil {
		logger.Error(err, "Failed to update APIShard status after error")
	}
	return ctrl.Result{}, reconcileErr
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeshardv1alpha1.APIShard{}).
		Owns(&appsv1.Deployment{}, builder.WithPredicates(shardpredicate.DeploymentReadinessPredicate)).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&kubeshardv1alpha1.NamespaceSync{}).
		Owns(&kubeshardv1alpha1.WebhookSync{}).
		Owns(&apiregistrationv1.APIService{}, builder.WithPredicates(shardpredicate.IgnoreStatusUpdatesPredicate)).
		Owns(&rbacv1.ClusterRoleBinding{}).
		Watches(&apiextensionsv1.CustomResourceDefinition{}, &crdEventHandler{client: r.Client}).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(
			requestHeaderCAMapper(mgr.GetClient()),
		)).
		Named("apishard").
		Complete(r)
}

// requestHeaderCAMapper returns a MapFunc that triggers reconciliation of all
// APIShards when the extension-apiserver-authentication ConfigMap in kube-system
// changes, ensuring the secondary picks up front-proxy CA rotations.
func requestHeaderCAMapper(c client.Client) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		if obj.GetName() != "extension-apiserver-authentication" ||
			obj.GetNamespace() != "kube-system" {
			return nil
		}
		var shards kubeshardv1alpha1.APIShardList
		if err := c.List(ctx, &shards); err != nil {
			return nil
		}
		requests := make([]reconcile.Request, 0, len(shards.Items))
		for i := range shards.Items {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: shards.Items[i].Name},
			})
		}
		return requests
	}
}

// crdEventHandler maps CRD events to APIShard reconcile requests for any shard
// whose apiGroups overlap with the CRD's group.
type crdEventHandler struct {
	client client.Client
}

func (h *crdEventHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[ctrl.Request]) {
	h.enqueueForCRD(ctx, e.Object, q)
}

func (h *crdEventHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[ctrl.Request]) {
	h.enqueueForCRD(ctx, e.ObjectNew, q)
}

func (h *crdEventHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[ctrl.Request]) {
	h.enqueueForCRD(ctx, e.Object, q)
}

func (h *crdEventHandler) Generic(ctx context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[ctrl.Request]) {
	h.enqueueForCRD(ctx, e.Object, q)
}

// enqueueForCRD enqueues reconcile requests for every APIShard whose apiGroups
// overlap with the CRD's group, so conflict detection runs on CRD changes.
func (h *crdEventHandler) enqueueForCRD(ctx context.Context, obj client.Object, q workqueue.TypedRateLimitingInterface[ctrl.Request]) {
	crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return
	}

	var shards kubeshardv1alpha1.APIShardList
	if err := h.client.List(ctx, &shards); err != nil {
		return
	}

	for i := range shards.Items {
		shard := &shards.Items[i]
		for _, ag := range shard.Spec.APIGroups {
			if ag.Group == crd.Spec.Group {
				q.Add(ctrl.Request{NamespacedName: types.NamespacedName{Name: shard.Name}})
				break
			}
		}
	}
}
