# kube-shard Operator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Kubebuilder operator that declaratively manages secondary API servers for CRD API aggregation, replacing the manual `setup-phase*.sh` scripts.

**Architecture:** Three controllers (APIShard, NamespaceSync, WebhookSync) following top-down config flow. Parent `APIShard` reconciler manages infrastructure and creates sub-CRs. Sub-CR reconcilers handle continuous sync loops independently.

**Tech Stack:** Go 1.26+, Kubebuilder v4, controller-runtime, cert-manager, Ginkgo/Gomega, envtest

## Global Constraints

- Go module: `github.com/konflux-ci/kube-shard`
- API group: `kube-shard.konflux-ci.dev`
- API version: `v1alpha1`
- Operator namespace: `kube-shard-operator`
- All operator code lives under `operator/` subdirectory in this repository
- Reuse packages from `github.com/konflux-ci/konflux-ci/operator/pkg/` where applicable (tracking, hashedconfigmap, hashedsecret, clusterinfo, kubernetes)
- Follow Konflux coding conventions: Ginkgo for tests, Gomega for assertions, `set -euo pipefail` for shell
- cert-manager is a runtime dependency (must be installed on target cluster)
- Kubebuilder markers generate RBAC and CRD manifests — always run `make manifests generate` after API changes

---

## File Structure

```
operator/
├── api/v1alpha1/
│   ├── apishard_types.go          # APIShard CRD types (spec, status, conditions)
│   ├── namespacesync_types.go     # NamespaceSync CRD types
│   ├── webhooksync_types.go       # WebhookSync CRD types
│   ├── groupversion_info.go       # GVK registration
│   ├── conditions.go              # Condition type constants and helpers
│   └── zz_generated.deepcopy.go   # Generated
├── cmd/main.go                    # Operator entrypoint, manager setup
├── internal/
│   ├── controller/
│   │   ├── apishard_controller.go       # APIShard reconciler (lifecycle + status aggregation)
│   │   ├── apishard_controller_test.go
│   │   ├── namespacesync_controller.go  # NamespaceSync reconciler
│   │   ├── namespacesync_controller_test.go
│   │   ├── webhooksync_controller.go    # WebhookSync reconciler
│   │   ├── webhooksync_controller_test.go
│   │   └── suite_test.go               # envtest suite setup
│   └── secondary/
│       ├── client.go              # Build k8s client to secondary from connection spec
│       └── client_test.go
├── config/
│   ├── crd/bases/                 # Generated CRD YAML
│   ├── rbac/                      # Generated RBAC
│   ├── manager/                   # Manager deployment
│   ├── default/                   # Default kustomization
│   └── samples/
│       ├── tekton-shard-sqlite.yaml
│       └── tekton-shard-postgresql.yaml
├── test/e2e/
│   ├── e2e_suite_test.go
│   ├── apishard_test.go
│   ├── namespacesync_test.go
│   └── webhooksync_test.go
├── go.mod
├── go.sum
├── Makefile
└── Dockerfile
```

---

## Task 1: Scaffold operator and define APIShard CRD types

**Files:**
- Create: `operator/` (entire scaffold)
- Create: `operator/api/v1alpha1/apishard_types.go`
- Create: `operator/api/v1alpha1/conditions.go`
- Create: `operator/api/v1alpha1/groupversion_info.go`

**Interfaces:**
- Consumes: nothing (first task)
- Produces: `APIShardSpec`, `APIShardStatus`, `APIShard` types; condition constants (`ConditionSecondaryHealthy`, `ConditionCRDsInstalled`, `ConditionAPIServicesRegistered`, `ConditionCRDConflictDetected`, `ConditionNamespaceSyncReady`, `ConditionWebhookSyncReady`); phase constants (`PhaseProvisioning`, `PhaseBlocked`, `PhaseReady`, `PhaseDegraded`)

- [ ] **Step 1: Scaffold operator with Kubebuilder**

```bash
cd /home/gbenhaim/repos/github.com/konflux-ci/kube-kine
mkdir -p operator && cd operator
kubebuilder init --domain konflux-ci.dev --repo github.com/konflux-ci/kube-shard/operator
kubebuilder create api --group kube-shard --version v1alpha1 --kind APIShard --resource --controller
```

- [ ] **Step 2: Define APIShard spec types**

Replace the generated `operator/api/v1alpha1/apishard_types.go` with the full type definitions:

```go
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
)

type StorageType string

const (
	StorageTypeSQLite              StorageType = "SQLite"
	StorageTypeInClusterPostgreSQL StorageType = "InClusterPostgreSQL"
	StorageTypePostgreSQL          StorageType = "PostgreSQL"
)

type APIGroupSpec struct {
	Group    string   `json:"group"`
	Versions []string `json:"versions"`
}

type StorageSpec struct {
	Type                StorageType          `json:"type"`
	ConnectionSecretRef *SecretKeyReference  `json:"connectionSecretRef,omitempty"`
	InCluster           *InClusterStorage    `json:"inCluster,omitempty"`
}

type SecretKeyReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
}

type InClusterStorage struct {
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

type NamespaceSyncSpec struct {
	LabelSelector metav1.LabelSelector `json:"labelSelector"`
}

type SecondarySpec struct {
	Replicas  int32                       `json:"replicas,omitempty"`
	Image     string                      `json:"image,omitempty"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

type KineSpec struct {
	Replicas int32  `json:"replicas,omitempty"`
	Image    string `json:"image,omitempty"`
}

type APIShardSpec struct {
	TargetNamespace string           `json:"targetNamespace"`
	APIGroups       []APIGroupSpec   `json:"apiGroups"`
	Storage         StorageSpec      `json:"storage"`
	NamespaceSync   NamespaceSyncSpec `json:"namespaceSync"`
	Secondary       SecondarySpec    `json:"secondary,omitempty"`
	Kine            KineSpec         `json:"kine,omitempty"`
}

type ConnectionSecretReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type APIShardStatus struct {
	Phase              string                       `json:"phase,omitempty"`
	ConnectionSecret   *ConnectionSecretReference   `json:"connectionSecret,omitempty"`
	SecondaryEndpoint  string                       `json:"secondaryEndpoint,omitempty"`
	Conditions         []metav1.Condition           `json:"conditions,omitempty"`
	ObservedGeneration int64                        `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type APIShard struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   APIShardSpec   `json:"spec,omitempty"`
	Status APIShardStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type APIShardList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []APIShard `json:"items"`
}

func init() {
	SchemeBuilder.Register(&APIShard{}, &APIShardList{})
}
```

- [ ] **Step 3: Define condition constants**

Create `operator/api/v1alpha1/conditions.go`:

```go
package v1alpha1

const (
	PhaseProvisioning = "Provisioning"
	PhaseBlocked      = "Blocked"
	PhaseReady        = "Ready"
	PhaseDegraded     = "Degraded"

	ConditionSecondaryHealthy      = "SecondaryHealthy"
	ConditionCRDsInstalled         = "CRDsInstalled"
	ConditionAPIServicesRegistered = "APIServicesRegistered"
	ConditionCRDConflictDetected   = "CRDConflictDetected"
	ConditionNamespaceSyncReady    = "NamespaceSyncReady"
	ConditionWebhookSyncReady      = "WebhookSyncReady"
)
```

- [ ] **Step 4: Run make manifests generate**

```bash
cd operator
make manifests generate
```

Expected: CRD YAML generated in `config/crd/bases/`, deepcopy generated.

- [ ] **Step 5: Verify build compiles**

```bash
cd operator
make build
```

Expected: Binary builds without errors.

- [ ] **Step 6: Commit**

```bash
git add operator/
git commit -m "feat(operator): scaffold and define APIShard CRD types"
```

---

## Task 2: Implement APIShardReconciler — deploy Kine and secondary kube-apiserver

**Files:**
- Modify: `operator/internal/controller/apishard_controller.go`
- Create: `operator/internal/controller/apishard_controller_test.go`
- Modify: `operator/cmd/main.go` (if needed for RBAC)

**Interfaces:**
- Consumes: `APIShard` types from Task 1
- Produces: Reconciler that creates Kine Deployment+Service, secondary kube-apiserver Deployment+Service, admin kubeconfig Secret. Sets `SecondaryHealthy` condition and `status.phase`.

- [ ] **Step 1: Write envtest test for Kine deployment creation**

In `operator/internal/controller/apishard_controller_test.go`:

```go
var _ = Describe("APIShardReconciler", func() {
    Context("when an APIShard is created with SQLite storage", func() {
        It("should create a Kine Deployment", func() {
            shard := &v1alpha1.APIShard{
                ObjectMeta: metav1.ObjectMeta{Name: "test-shard"},
                Spec: v1alpha1.APIShardSpec{
                    TargetNamespace: "test-ns",
                    APIGroups: []v1alpha1.APIGroupSpec{
                        {Group: "tekton.dev", Versions: []string{"v1"}},
                    },
                    Storage: v1alpha1.StorageSpec{Type: v1alpha1.StorageTypeSQLite},
                    Kine:    v1alpha1.KineSpec{Image: "rancher/kine:v0.14.14", Replicas: 1},
                    Secondary: v1alpha1.SecondarySpec{
                        Image: "registry.k8s.io/kube-apiserver:v1.36.2",
                        Replicas: 1,
                    },
                },
            }
            Expect(k8sClient.Create(ctx, shard)).To(Succeed())

            // Verify Kine Deployment created
            kineDeployment := &appsv1.Deployment{}
            Eventually(func() error {
                return k8sClient.Get(ctx, types.NamespacedName{
                    Name: "test-shard-kine", Namespace: "test-ns",
                }, kineDeployment)
            }, timeout, interval).Should(Succeed())

            Expect(kineDeployment.Spec.Template.Spec.Containers[0].Image).To(Equal("rancher/kine:v0.14.14"))
        })
    })
})
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd operator
make test
```

Expected: FAIL — reconciler doesn't create the Deployment yet.

- [ ] **Step 3: Implement reconciler — ensure namespace, deploy Kine**

In `operator/internal/controller/apishard_controller.go`, implement the reconcile loop steps 1-4:
- Ensure target namespace exists
- Build Kine Deployment spec (image, args based on storage type, ports, health probes)
- Build Kine Service spec (port 2379)
- Create or update using `controllerutil.CreateOrUpdate`
- Set owner reference on all created resources

The Kine Deployment args for SQLite: `["--endpoint=sqlite:///data/kine.db", "--listen-address=tcp://0.0.0.0:2379"]`

- [ ] **Step 4: Implement reconciler — deploy secondary kube-apiserver**

Continue the reconciler with step 5:
- Build secondary kube-apiserver Deployment spec with all flags (etcd-servers pointing to Kine service, secure-port, TLS, requestheader, authorization-mode=Webhook, disable admission plugins)
- Build secondary Service (port 443 → 6443)
- For now, skip cert-manager (use a placeholder Secret name for certs); cert-manager integration follows in a later step
- Set owner references

- [ ] **Step 5: Implement reconciler — create admin kubeconfig Secret and update status**

Continue with step 6:
- Generate a static admin token (for PoC; production will use cert-manager certs)
- Create a kubeconfig Secret (`<shard>-admin-kubeconfig`) with server URL, CA, and token
- Update `status.connectionSecret` with the Secret reference
- Update `status.secondaryEndpoint`
- Set `status.phase = PhaseProvisioning`
- Set `SecondaryHealthy` condition to False initially

- [ ] **Step 6: Add RBAC markers**

Add kubebuilder RBAC markers to the reconciler:

```go
//+kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=apishards,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=apishards/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kube-shard.konflux-ci.dev,resources=apishards/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services;secrets;configmaps;namespaces,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apiregistration.k8s.io,resources=apiservices,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
//+kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingwebhookconfigurations;mutatingwebhookconfigurations,verbs=get;list;watch
```

- [ ] **Step 7: Run tests and verify pass**

```bash
cd operator
make manifests generate
make test
```

Expected: Tests pass — Kine Deployment and secondary Deployment are created.

- [ ] **Step 8: Commit**

```bash
git add operator/
git commit -m "feat(operator): implement APIShardReconciler — deploy Kine and secondary"
```

---

## Task 3: Implement secondary client and health checking

**Files:**
- Create: `operator/internal/secondary/client.go`
- Create: `operator/internal/secondary/client_test.go`
- Modify: `operator/internal/controller/apishard_controller.go`

**Interfaces:**
- Consumes: `APIShard` types, admin kubeconfig Secret
- Produces: `secondary.NewClient(kubeconfig []byte) (client.Client, error)`, `secondary.CheckHealth(endpoint string, token string) (bool, error)`. Reconciler uses these to set `SecondaryHealthy` condition and gate sub-CR creation.

- [ ] **Step 1: Write test for secondary client builder**

```go
// operator/internal/secondary/client_test.go
var _ = Describe("Secondary Client", func() {
    It("should build a client from a valid kubeconfig", func() {
        kubeconfig := buildTestKubeconfig("https://localhost:6443", "test-token", nil)
        client, err := NewClient(kubeconfig)
        Expect(err).NotTo(HaveOccurred())
        Expect(client).NotTo(BeNil())
    })

    It("should return error for invalid kubeconfig", func() {
        _, err := NewClient([]byte("invalid"))
        Expect(err).To(HaveOccurred())
    })
})
```

- [ ] **Step 2: Implement secondary client**

```go
// operator/internal/secondary/client.go
package secondary

import (
    "k8s.io/client-go/tools/clientcmd"
    "sigs.k8s.io/controller-runtime/pkg/client"
)

func NewClient(kubeconfig []byte) (client.Client, error) {
    restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
    if err != nil {
        return nil, err
    }
    restConfig.TLSClientConfig.Insecure = true // PoC: skip TLS verify for secondary
    return client.New(restConfig, client.Options{})
}
```

- [ ] **Step 3: Add health check to reconciler**

Update `apishard_controller.go` to check secondary health after deployment is ready:
- Check if Deployment has `ReadyReplicas >= 1`
- If yes: set `SecondaryHealthy=True`, `phase=Ready` (simplified for this phase)
- If no: set `SecondaryHealthy=False`, `phase=Provisioning`, requeue after 5s

- [ ] **Step 4: Run tests**

```bash
cd operator
make test
```

Expected: All tests pass.

- [ ] **Step 5: Commit**

```bash
git add operator/
git commit -m "feat(operator): add secondary client builder and health checking"
```

---

## Task 4: CRD aggregation and conflict detection

**Files:**
- Modify: `operator/internal/controller/apishard_controller.go`
- Modify: `operator/internal/controller/apishard_controller_test.go`

**Interfaces:**
- Consumes: `APIShard` types, secondary client from Task 3
- Produces: Reconciler steps 7-8: CRD extraction from primary, installation on secondary, APIService registration, CRD conflict detection. Sets `CRDsInstalled`, `APIServicesRegistered`, `CRDConflictDetected` conditions.

- [ ] **Step 1: Write test for CRD conflict detection**

```go
It("should set CRDConflictDetected=True when CRDs exist on primary", func() {
    // Create a CRD on the primary (envtest) for tekton.dev
    crd := &apiextensionsv1.CustomResourceDefinition{
        ObjectMeta: metav1.ObjectMeta{Name: "pipelineruns.tekton.dev"},
        Spec: apiextensionsv1.CustomResourceDefinitionSpec{
            Group: "tekton.dev",
            Names: apiextensionsv1.CustomResourceDefinitionNames{
                Plural: "pipelineruns", Singular: "pipelinerun", Kind: "PipelineRun",
            },
            Scope: apiextensionsv1.NamespaceScoped,
            Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
                {Name: "v1", Served: true, Storage: true,
                 Schema: &apiextensionsv1.CustomResourceValidation{
                     OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object"},
                 }},
            },
        },
    }
    Expect(k8sClient.Create(ctx, crd)).To(Succeed())

    // Verify condition
    Eventually(func() string {
        shard := &v1alpha1.APIShard{}
        _ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-shard"}, shard)
        return meta.FindStatusCondition(shard.Status.Conditions, v1alpha1.ConditionCRDConflictDetected).Status
    }, timeout, interval).Should(Equal(metav1.ConditionTrue))
})
```

- [ ] **Step 2: Implement CRD conflict detection**

Add to reconciler:
- List all CRDs on primary
- Filter for CRDs whose `spec.group` matches any `spec.apiGroups[].group`
- If matches found: set `CRDConflictDetected=True` with message listing conflicting CRDs
- If no matches: set `CRDConflictDetected=False`

- [ ] **Step 3: Implement APIService registration**

Add to reconciler:
- For each API group/version in `spec.apiGroups`, create an `APIService` object
- Set `spec.service` pointing to the secondary's Service
- Set `spec.caBundle` from the serving CA cert Secret
- Set `groupPriorityMinimum: 1000`, `versionPriority: 100` (50 for beta, 10 for alpha)
- Always register (even during conflict — harmless when shadowed)

- [ ] **Step 4: Implement CRD installation on secondary**

Add to reconciler:
- For each CRD on primary matching configured API groups: extract it
- Strip operator metadata (resourceVersion, uid, managedFields, operator annotations)
- If conversion strategy is Webhook: change to None
- Apply to secondary via the secondary client

- [ ] **Step 5: Add CRD watch to controller**

In the controller setup (`SetupWithManager`), add a watch on CRDs filtered by the configured API groups using `handler.EnqueueRequestsFromMapFunc`.

- [ ] **Step 6: Run tests**

```bash
cd operator
make manifests generate
make test
```

Expected: Conflict detection test passes.

- [ ] **Step 7: Commit**

```bash
git add operator/
git commit -m "feat(operator): implement CRD aggregation and conflict detection"
```

---

## Task 5: Define NamespaceSync CRD and implement reconciler

**Files:**
- Create: `operator/api/v1alpha1/namespacesync_types.go`
- Create: `operator/internal/controller/namespacesync_controller.go`
- Create: `operator/internal/controller/namespacesync_controller_test.go`
- Modify: `operator/internal/controller/apishard_controller.go` (gated sub-CR creation)
- Modify: `operator/cmd/main.go` (register controller)

**Interfaces:**
- Consumes: Secondary client from Task 3, `APIShard` status (SecondaryHealthy condition)
- Produces: `NamespaceSync` CRD, `NamespaceSyncReconciler` that mirrors namespaces. APIShard reconciler creates `NamespaceSync` sub-CR when secondary is healthy. Sets `NamespaceSyncReady` condition on parent.

- [ ] **Step 1: Define NamespaceSync types**

Create `operator/api/v1alpha1/namespacesync_types.go`:

```go
package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type SecondaryConnectionSpec struct {
    ServiceRef    ServiceReference       `json:"serviceRef"`
    AuthSecretRef corev1.LocalObjectReference `json:"authSecretRef"`
    CASecretRef   corev1.LocalObjectReference `json:"caSecretRef"`
}

type ServiceReference struct {
    Name      string `json:"name"`
    Namespace string `json:"namespace"`
    Port      int32  `json:"port"`
}

type NamespaceSyncSpec struct {
    SecondaryConnection SecondaryConnectionSpec `json:"secondaryConnection"`
    LabelSelector       metav1.LabelSelector   `json:"labelSelector"`
}

type NamespaceSyncStatus struct {
    Conditions       []metav1.Condition `json:"conditions,omitempty"`
    SyncedNamespaces int32              `json:"syncedNamespaces,omitempty"`
    LastSyncTime     *metav1.Time       `json:"lastSyncTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Synced",type=integer,JSONPath=`.status.syncedNamespaces`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type NamespaceSync struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec   NamespaceSyncSpec   `json:"spec,omitempty"`
    Status NamespaceSyncStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NamespaceSyncList struct {
    metav1.TypeMeta `json:",inline"`
    metav1.ListMeta `json:"metadata,omitempty"`
    Items           []NamespaceSync `json:"items"`
}

func init() {
    SchemeBuilder.Register(&NamespaceSync{}, &NamespaceSyncList{})
}
```

- [ ] **Step 2: Scaffold controller**

```bash
cd operator
kubebuilder create api --group kube-shard --version v1alpha1 --kind NamespaceSync --resource=false --controller
```

- [ ] **Step 3: Write test for namespace sync**

```go
var _ = Describe("NamespaceSyncReconciler", func() {
    It("should sync namespaces matching label selector to secondary", func() {
        // Create NamespaceSync CR
        nsSync := &v1alpha1.NamespaceSync{...}
        Expect(k8sClient.Create(ctx, nsSync)).To(Succeed())

        // Create a namespace matching the selector
        ns := &corev1.Namespace{
            ObjectMeta: metav1.ObjectMeta{
                Name:   "tenant-a",
                Labels: map[string]string{"konflux.dev/type": "tenant"},
            },
        }
        Expect(k8sClient.Create(ctx, ns)).To(Succeed())

        // Verify status updated
        Eventually(func() int32 {
            sync := &v1alpha1.NamespaceSync{}
            _ = k8sClient.Get(ctx, types.NamespacedName{...}, sync)
            return sync.Status.SyncedNamespaces
        }, timeout, interval).Should(BeNumerically(">=", 1))
    })
})
```

- [ ] **Step 4: Implement NamespaceSyncReconciler**

```go
func (r *NamespaceSyncReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    nsSync := &v1alpha1.NamespaceSync{}
    if err := r.Get(ctx, req.NamespacedName, nsSync); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // Build secondary client
    secondaryClient, err := r.buildSecondaryClient(ctx, nsSync)
    if err != nil {
        // Set Ready=False, reason=SecondaryUnavailable
        return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
    }

    // List primary namespaces matching selector
    selector, _ := metav1.LabelSelectorAsSelector(&nsSync.Spec.LabelSelector)
    primaryNamespaces := &corev1.NamespaceList{}
    r.List(ctx, primaryNamespaces, client.MatchingLabelsSelector{Selector: selector})

    // List secondary namespaces
    secondaryNamespaces := &corev1.NamespaceList{}
    secondaryClient.List(ctx, secondaryNamespaces)

    // Sync: create missing, delete orphaned
    // ... (create namespaces on secondary that exist on primary but not secondary)
    // ... (delete namespaces on secondary that don't exist on primary, skip system ns)

    // Update status
    nsSync.Status.SyncedNamespaces = int32(len(primaryNamespaces.Items))
    nsSync.Status.LastSyncTime = &metav1.Time{Time: time.Now()}
    meta.SetStatusCondition(&nsSync.Status.Conditions, metav1.Condition{
        Type: "Ready", Status: metav1.ConditionTrue, Reason: "SyncComplete",
    })
    return ctrl.Result{}, r.Status().Update(ctx, nsSync)
}
```

- [ ] **Step 5: Add Namespace watch with mapper**

In `SetupWithManager`, watch Namespaces and map to NamespaceSync reconcile requests:

```go
func (r *NamespaceSyncReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&v1alpha1.NamespaceSync{}).
        Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.mapNamespaceToSync)).
        Complete(r)
}
```

- [ ] **Step 6: Add gated sub-CR creation to APIShardReconciler**

In `apishard_controller.go`, after confirming `SecondaryHealthy=True`:
- Create `NamespaceSync` CR in `spec.targetNamespace` with owner reference to APIShard
- Set `spec.secondaryConnection` from the admin kubeconfig
- Set `spec.labelSelector` from `APIShard.spec.namespaceSync.labelSelector`

- [ ] **Step 7: Add status aggregation**

In `apishard_controller.go`, read `NamespaceSync` status and surface as `NamespaceSyncReady` condition on APIShard.

- [ ] **Step 8: Run tests**

```bash
cd operator
make manifests generate
make test
```

Expected: All tests pass.

- [ ] **Step 9: Commit**

```bash
git add operator/
git commit -m "feat(operator): implement NamespaceSync controller"
```

---

## Task 6: Define WebhookSync CRD and implement reconciler

**Files:**
- Create: `operator/api/v1alpha1/webhooksync_types.go`
- Create: `operator/internal/controller/webhooksync_controller.go`
- Create: `operator/internal/controller/webhooksync_controller_test.go`
- Modify: `operator/internal/controller/apishard_controller.go` (gated sub-CR creation)
- Modify: `operator/cmd/main.go` (register controller)

**Interfaces:**
- Consumes: Secondary client from Task 3, webhook configs on primary
- Produces: `WebhookSync` CRD, `WebhookSyncReconciler` that mirrors webhooks with service→url transform. APIShard reconciler creates `WebhookSync` sub-CR. Sets `WebhookSyncReady` condition on parent.

- [ ] **Step 1: Define WebhookSync types**

Create `operator/api/v1alpha1/webhooksync_types.go`:

```go
package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type WebhookSyncSpec struct {
    SecondaryConnection SecondaryConnectionSpec `json:"secondaryConnection"`
    APIGroups           []string               `json:"apiGroups"`
}

type SyncedWebhookCounts struct {
    Validating int32 `json:"validating,omitempty"`
    Mutating   int32 `json:"mutating,omitempty"`
}

type WebhookSyncStatus struct {
    Conditions     []metav1.Condition  `json:"conditions,omitempty"`
    SyncedWebhooks SyncedWebhookCounts `json:"syncedWebhooks,omitempty"`
    LastSyncTime   *metav1.Time        `json:"lastSyncTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type WebhookSync struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec   WebhookSyncSpec   `json:"spec,omitempty"`
    Status WebhookSyncStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type WebhookSyncList struct {
    metav1.TypeMeta `json:",inline"`
    metav1.ListMeta `json:"metadata,omitempty"`
    Items           []WebhookSync `json:"items"`
}

func init() {
    SchemeBuilder.Register(&WebhookSync{}, &WebhookSyncList{})
}
```

- [ ] **Step 2: Write test for webhook service→url transform**

```go
var _ = Describe("WebhookSyncReconciler", func() {
    It("should transform clientConfig.service to clientConfig.url", func() {
        // Create a VWC on primary targeting tekton.dev
        vwc := &admissionregistrationv1.ValidatingWebhookConfiguration{
            ObjectMeta: metav1.ObjectMeta{Name: "validation.webhook.pipeline.tekton.dev"},
            Webhooks: []admissionregistrationv1.ValidatingWebhook{{
                Name: "validation.webhook.pipeline.tekton.dev",
                ClientConfig: admissionregistrationv1.WebhookClientConfig{
                    Service: &admissionregistrationv1.ServiceReference{
                        Name: "tekton-pipelines-webhook", Namespace: "tekton-pipelines",
                        Port: ptr.To(int32(443)), Path: ptr.To("/defaulting"),
                    },
                    CABundle: []byte("ca-data"),
                },
                Rules: []admissionregistrationv1.RuleWithOperations{{
                    Rule: admissionregistrationv1.Rule{
                        APIGroups: []string{"tekton.dev"},
                        Resources: []string{"pipelineruns"},
                    },
                }},
                SideEffects:             ptr.To(admissionregistrationv1.SideEffectClassNone),
                AdmissionReviewVersions: []string{"v1"},
            }},
        }
        Expect(k8sClient.Create(ctx, vwc)).To(Succeed())

        // Create WebhookSync CR
        // ... verify on secondary the webhook has url instead of service
    })
})
```

- [ ] **Step 3: Implement WebhookSyncReconciler**

Core logic:
- List VWC + MWC on primary
- Filter: for each webhook, check if any rule's `apiGroups` intersects with `spec.apiGroups`
- Transform: replace `clientConfig.service` with `clientConfig.url` = `https://<service>.<namespace>.svc:<port><path>`
- Apply to secondary (create or update)
- Delete from secondary if no longer matching on primary
- Update status counts

- [ ] **Step 4: Add webhook config watches**

Watch `ValidatingWebhookConfiguration` and `MutatingWebhookConfiguration`, map changes to `WebhookSync` reconcile requests.

- [ ] **Step 5: Add gated creation and status aggregation in APIShard reconciler**

Same pattern as NamespaceSync: create `WebhookSync` sub-CR when secondary is healthy, aggregate status as `WebhookSyncReady`.

- [ ] **Step 6: Run tests**

```bash
cd operator
make manifests generate
make test
```

Expected: All tests pass.

- [ ] **Step 7: Commit**

```bash
git add operator/
git commit -m "feat(operator): implement WebhookSync controller with service-to-url transform"
```

---

## Task 7: Storage backend options (InClusterPostgreSQL + external PostgreSQL)

**Files:**
- Modify: `operator/internal/controller/apishard_controller.go`
- Modify: `operator/internal/controller/apishard_controller_test.go`

**Interfaces:**
- Consumes: `APIShard` spec (storage section)
- Produces: Reconciler handles all three storage types. `InClusterPostgreSQL` creates PostgreSQL Deployment + Service + credentials Secret. External `PostgreSQL` validates Secret and configures Kine DSN.

- [ ] **Step 1: Write test for InClusterPostgreSQL deployment**

```go
It("should deploy PostgreSQL when storage type is InClusterPostgreSQL", func() {
    shard := &v1alpha1.APIShard{
        ObjectMeta: metav1.ObjectMeta{Name: "pg-shard"},
        Spec: v1alpha1.APIShardSpec{
            TargetNamespace: "pg-test-ns",
            Storage: v1alpha1.StorageSpec{Type: v1alpha1.StorageTypeInClusterPostgreSQL},
            // ...
        },
    }
    Expect(k8sClient.Create(ctx, shard)).To(Succeed())

    pgDeployment := &appsv1.Deployment{}
    Eventually(func() error {
        return k8sClient.Get(ctx, types.NamespacedName{
            Name: "pg-shard-postgresql", Namespace: "pg-test-ns",
        }, pgDeployment)
    }, timeout, interval).Should(Succeed())
})
```

- [ ] **Step 2: Implement InClusterPostgreSQL mode**

Add to reconciler step 3:
- Create credentials Secret (`<shard>-postgresql-credentials`) with POSTGRES_USER, POSTGRES_PASSWORD, POSTGRES_DB
- Create PostgreSQL Deployment (image: `postgres:16`, envFrom Secret, volume, probes)
- Create PostgreSQL Service (port 5432)
- Configure Kine with `--endpoint=postgres://<user>:<pass>@<shard>-postgresql.<ns>.svc:5432/kine?sslmode=disable`

- [ ] **Step 3: Implement external PostgreSQL mode**

Add to reconciler step 3:
- Validate that `spec.storage.connectionSecretRef` exists and has the specified key
- Read DSN from Secret
- Configure Kine with `--endpoint=<dsn>`

- [ ] **Step 4: Run tests**

```bash
cd operator
make test
```

Expected: All tests pass.

- [ ] **Step 5: Commit**

```bash
git add operator/
git commit -m "feat(operator): support InClusterPostgreSQL and external PostgreSQL storage"
```

---

## Task 8: cert-manager integration

**Files:**
- Modify: `operator/internal/controller/apishard_controller.go`
- Modify: `operator/internal/controller/apishard_controller_test.go`

**Interfaces:**
- Consumes: `APIShard` spec
- Produces: Reconciler creates cert-manager `Certificate` resources for serving cert and front-proxy CA. Waits for certificate Secrets to be populated before deploying secondary.

- [ ] **Step 1: Write test for Certificate creation**

```go
It("should create cert-manager Certificate for serving", func() {
    shard := &v1alpha1.APIShard{...}
    Expect(k8sClient.Create(ctx, shard)).To(Succeed())

    cert := &certmanagerv1.Certificate{}
    Eventually(func() error {
        return k8sClient.Get(ctx, types.NamespacedName{
            Name: "test-shard-serving-cert", Namespace: "test-ns",
        }, cert)
    }, timeout, interval).Should(Succeed())

    Expect(cert.Spec.DNSNames).To(ContainElement("test-shard-apiserver.test-ns.svc"))
})
```

- [ ] **Step 2: Implement Certificate resource creation**

Add to reconciler step 2:
- Create a self-signed `ClusterIssuer` (named `kube-shard-selfsigned`) if not exists
- Create `Certificate` for serving: `<shard>-serving-cert` with SANs for the Service
- Create `Certificate` for front-proxy CA: `<shard>-front-proxy-ca`
- Generate SA signing key pair as a regular Secret: `<shard>-sa-signing`
- Wait for Certificate Secrets to be populated (check Secret exists and has `tls.crt` key)
- If not ready: set `SecondaryHealthy=False`, requeue after 5s

- [ ] **Step 3: Update secondary Deployment to mount cert Secrets**

Update the secondary kube-apiserver Deployment to mount:
- `<shard>-serving-cert` → `/etc/kube/pki/serving.crt`, `/etc/kube/pki/serving.key`
- `<shard>-front-proxy-ca` → `/etc/kube/pki/front-proxy-ca.crt`
- `<shard>-sa-signing` → `/etc/kube/pki/sa-signing.key`, `/etc/kube/pki/sa-signing.pub`
- `<shard>-authz-config` ConfigMap → `/etc/kube/authz/webhook-config.yaml`

- [ ] **Step 4: Run tests**

```bash
cd operator
make test
```

Expected: All tests pass (envtest may need cert-manager CRDs installed in test suite setup).

- [ ] **Step 5: Commit**

```bash
git add operator/
git commit -m "feat(operator): integrate cert-manager for TLS certificate management"
```

---

## Task 9: Create sample CRs and Makefile targets

**Files:**
- Create: `operator/config/samples/tekton-shard-sqlite.yaml`
- Create: `operator/config/samples/tekton-shard-postgresql.yaml`
- Modify: `operator/Makefile`

**Interfaces:**
- Consumes: All types defined in previous tasks
- Produces: Ready-to-use sample manifests and Makefile targets for common operations

- [ ] **Step 1: Create SQLite sample**

```yaml
# operator/config/samples/tekton-shard-sqlite.yaml
apiVersion: kube-shard.konflux-ci.dev/v1alpha1
kind: APIShard
metadata:
  name: tekton-shard
spec:
  targetNamespace: tekton-apiserver
  apiGroups:
  - group: tekton.dev
    versions: ["v1", "v1beta1", "v1alpha1"]
  - group: resolution.tekton.dev
    versions: ["v1beta1", "v1alpha1"]
  storage:
    type: SQLite
  namespaceSync:
    labelSelector:
      matchLabels:
        konflux.dev/type: tenant
  secondary:
    replicas: 1
    image: registry.k8s.io/kube-apiserver:v1.36.2
    resources:
      requests:
        cpu: 250m
        memory: 512Mi
      limits:
        memory: 1Gi
  kine:
    replicas: 1
    image: rancher/kine:v0.14.14
```

- [ ] **Step 2: Create PostgreSQL sample**

Same as above but with `storage.type: InClusterPostgreSQL`.

- [ ] **Step 3: Add Makefile targets**

Ensure Makefile has:
- `make test` — unit/envtest tests
- `make test-e2e` — e2e tests (placeholder for Task 10)
- `make build` — build binary
- `make docker-build` — build container image
- `make deploy` — deploy to cluster
- `make undeploy` — remove from cluster
- `make install` — install CRDs
- `make uninstall` — uninstall CRDs

- [ ] **Step 4: Commit**

```bash
git add operator/
git commit -m "feat(operator): add sample CRs and finalize Makefile targets"
```

---

## Task 10: E2E test suite

**Files:**
- Create: `operator/test/e2e/e2e_suite_test.go`
- Create: `operator/test/e2e/apishard_test.go`
- Create: `operator/test/e2e/namespacesync_test.go`
- Create: `operator/test/e2e/webhooksync_test.go`

**Interfaces:**
- Consumes: All CRDs and controllers from previous tasks, running kind cluster with cert-manager
- Produces: E2E test suite validating full operator lifecycle. Run via `make test-e2e`.

- [ ] **Step 1: Set up e2e suite**

```go
// operator/test/e2e/e2e_suite_test.go
func TestE2E(t *testing.T) {
    RegisterFailHandler(Fail)
    RunSpecs(t, "E2E Suite")
}

var _ = BeforeSuite(func() {
    // Assumes operator is deployed and cert-manager is installed
    // Build k8s client from KUBECONFIG
})
```

- [ ] **Step 2: Write APIShard lifecycle test**

Test: create APIShard → wait for Ready → verify Deployments exist → verify admin kubeconfig works → delete APIShard → verify cleanup.

- [ ] **Step 3: Write CRD conflict detection test**

Test: create CRD on primary for aggregated group → verify status shows `CRDConflictDetected=True` → delete CRD → verify `CRDConflictDetected=False`.

- [ ] **Step 4: Write namespace sync test**

Test: create namespace with matching label → verify it appears on secondary → delete from primary → verify removed from secondary.

- [ ] **Step 5: Write webhook sync test**

Test: create VWC targeting aggregated API group → verify mirrored on secondary with url transform → update caBundle → verify synced.

- [ ] **Step 6: Write degradation and recovery test**

Test: kill secondary pod → verify status degrades → pod recovers → verify status returns to Ready.

- [ ] **Step 7: Run e2e tests**

```bash
cd operator
make test-e2e
```

Expected: All e2e tests pass.

- [ ] **Step 8: Commit**

```bash
git add operator/
git commit -m "feat(operator): add e2e test suite for full lifecycle validation"
```
