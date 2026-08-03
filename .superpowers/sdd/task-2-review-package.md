diff --git a/operator/internal/controller/apishard/reconciler.go b/operator/internal/controller/apishard/reconciler.go
index 12950eb..ff788e7 100644
--- a/operator/internal/controller/apishard/reconciler.go
+++ b/operator/internal/controller/apishard/reconciler.go
@@ -67,20 +67,21 @@ const (
 // used by the tracking client for orphan cleanup.
 var managedGVKs = []schema.GroupVersionKind{
 	{Group: "apps", Version: "v1", Kind: "Deployment"},
 	{Group: "apps", Version: "v1", Kind: "StatefulSet"},
 	{Group: "", Version: "v1", Kind: "Service"},
 	{Group: "", Version: "v1", Kind: "Secret"},
 	{Group: "", Version: "v1", Kind: "ConfigMap"},
 	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRoleBinding"},
 	{Group: "cert-manager.io", Version: "v1", Kind: "Issuer"},
 	{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
+	{Group: "policy", Version: "v1", Kind: "PodDisruptionBudget"},
 }
 
 // Reconciler reconciles an APIShard object.
 type Reconciler struct {
 	client.Client
 	Scheme         *runtime.Scheme
 	ClientProvider *secondary.ClientProvider
 }
 
 // +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=apishards,verbs=get;list;watch;create;update;patch;delete
@@ -90,20 +91,21 @@ type Reconciler struct {
 // +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
 // +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
 // +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch
 // +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
 // +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
 // +kubebuilder:rbac:groups=cert-manager.io,resources=issuers;certificates,verbs=get;list;watch;create;update;patch;delete
 // +kubebuilder:rbac:groups=apiregistration.k8s.io,resources=apiservices,verbs=get;list;watch;create;update;patch;delete
 // +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update
 // +kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=webhooksyncs,verbs=get;list;watch;create;update;patch;delete
 // +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
+// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
 
 // Reconcile drives the desired state for a single APIShard resource. It provisions
 // the target namespace, storage backend, TLS certificates, auth configuration, and
 // the secondary API server, then checks deployment health and updates status.
 func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
 	logger := log.FromContext(ctx)
 
 	var shard kubeshardv1alpha1.APIShard
 	if err := r.Get(ctx, req.NamespacedName, &shard); err != nil {
 		if apierrors.IsNotFound(err) {
@@ -296,20 +298,31 @@ func (r *Reconciler) reconcileStorage(ctx context.Context, tc *tracking.Client,
 // If the cluster does not support trafficDistribution on Services (Kubernetes < 1.33),
 // the field is cleared and the apply is retried so reconciliation is not blocked.
 func (r *Reconciler) reconcileKine(ctx context.Context, tc *tracking.Client, shard *kubeshardv1alpha1.APIShard) error {
 	logger := log.FromContext(ctx)
 
 	deployment := resources.BuildKineDeployment(shard)
 	if err := tc.ApplyOwned(ctx, deployment); err != nil {
 		return fmt.Errorf("kine deployment: %w", err)
 	}
 
+	if pdb := resources.BuildPDB(
+		resources.KineDeploymentName(shard),
+		shard.Spec.TargetNamespace,
+		shard.Spec.Kine.Replicas,
+		deployment.Spec.Selector.MatchLabels,
+	); pdb != nil {
+		if err := tc.ApplyOwned(ctx, pdb); err != nil {
+			return fmt.Errorf("kine pdb: %w", err)
+		}
+	}
+
 	svc := resources.BuildKineService(shard)
 	if err := tc.ApplyOwned(ctx, svc); err != nil {
 		if svc.Spec.TrafficDistribution != nil && isTrafficDistributionUnsupported(err) {
 			logger.Info("trafficDistribution not supported on this cluster, falling back without it")
 			svc.Spec.TrafficDistribution = nil
 			if retryErr := tc.ApplyOwned(ctx, svc); retryErr != nil {
 				return fmt.Errorf("kine service (fallback without trafficDistribution): %w", retryErr)
 			}
 			return nil
 		}
@@ -331,20 +344,31 @@ func isTrafficDistributionUnsupported(err error) bool {
 
 // reconcileSecondary ensures the secondary kube-apiserver deployment and its
 // ClusterIP service exist. The secondary API server serves the aggregated API
 // groups backed by Kine.
 func (r *Reconciler) reconcileSecondary(ctx context.Context, tc *tracking.Client, shard *kubeshardv1alpha1.APIShard, requestHeaderAllowedNames []string) error {
 	deployment := resources.BuildSecondaryDeployment(shard, requestHeaderAllowedNames)
 	if err := tc.ApplyOwned(ctx, deployment); err != nil {
 		return fmt.Errorf("secondary deployment: %w", err)
 	}
 
+	if pdb := resources.BuildPDB(
+		resources.SecondaryDeploymentName(shard),
+		shard.Spec.TargetNamespace,
+		shard.Spec.Secondary.Replicas,
+		deployment.Spec.Selector.MatchLabels,
+	); pdb != nil {
+		if err := tc.ApplyOwned(ctx, pdb); err != nil {
+			return fmt.Errorf("secondary pdb: %w", err)
+		}
+	}
+
 	svc := resources.BuildSecondaryService(shard)
 	if err := tc.ApplyOwned(ctx, svc); err != nil {
 		return fmt.Errorf("secondary service: %w", err)
 	}
 
 	return nil
 }
 
 // reconcileInClusterPostgreSQL deploys a PostgreSQL instance inside the target
 // namespace along with a credentials Secret. Only called when the storage type
