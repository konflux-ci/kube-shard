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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	// +kubebuilder:validation:Enum=SQLite;InClusterPostgreSQL;PostgreSQL
	Type                StorageType            `json:"type"`
	ConnectionSecretRef *SecretKeyReference    `json:"connectionSecretRef,omitempty"`
	InCluster           *InClusterStorage      `json:"inCluster,omitempty"`
	Monitoring          *StorageMonitoringSpec `json:"monitoring,omitempty"`
}

// StorageMonitoringSpec configures metrics collection for the storage backend.
// The operator deploys an OpenTelemetry Collector that scrapes the database and
// exposes Prometheus metrics. The appropriate backend-specific section must match
// the storage type; mismatched sections are ignored.
type StorageMonitoringSpec struct {
	// Enabled enables metrics collection for the storage backend.
	Enabled bool `json:"enabled"`
	// CollectionInterval for standard storage metrics. Default: "30s".
	// +optional
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('0s')",message="duration must be a valid non-negative Go duration"
	CollectionInterval string `json:"collectionInterval,omitempty"`
	// CACertSecret references a Secret containing the CA certificate used to
	// verify the PostgreSQL server's TLS certificate. The Key field specifies
	// which key in the Secret holds the certificate (defaults to "ca.crt").
	// Required when the PostgreSQL connection uses TLS.
	// +optional
	CACertSecret *SecretKeyReference `json:"caCertSecret,omitempty"`
	// PostgreSQL holds settings specific to PostgreSQL monitoring.
	// Applicable when storage type is InClusterPostgreSQL or PostgreSQL.
	// +optional
	PostgreSQL *PostgreSQLMonitoringSpec `json:"postgresql,omitempty"`
}

// PostgreSQLMonitoringSpec contains PostgreSQL-specific monitoring settings.
type PostgreSQLMonitoringSpec struct {
	// BloatInterval controls how often reclaimable-space queries run (pgstattuple).
	// This performs a full table scan so should not be too frequent. Default: "5m".
	// For external PostgreSQL, the pgstattuple extension must be pre-installed
	// by the database administrator.
	// +optional
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('0s')",message="duration must be a valid non-negative Go duration"
	BloatInterval string `json:"bloatInterval,omitempty"`
}

// SecretKeyReference identifies a specific key within a Secret.
// The Secret must reside in the APIShard's targetNamespace (same namespace as Kine).
type SecretKeyReference struct {
	// Name of the Secret.
	Name string `json:"name"`
	// Key within the Secret that contains the Kine-compatible connection string.
	Key string `json:"key"`
}

type InClusterStorage struct {
	Resources   corev1.ResourceRequirements `json:"resources,omitempty"`
	Persistence *PersistenceSpec            `json:"persistence,omitempty"`
}

// PersistenceSpec configures persistent volume storage for in-cluster backends.
// When specified, a PVC is used instead of emptyDir, ensuring data survives pod restarts.
type PersistenceSpec struct {
	// StorageClassName is the name of the StorageClass to use for the PVC.
	// If not specified, the cluster default StorageClass is used.
	StorageClassName *string `json:"storageClassName,omitempty"`
	// Size is the requested storage capacity.
	Size resource.Quantity `json:"size"`
}

type NamespaceSyncConfig struct {
	LabelSelector metav1.LabelSelector `json:"labelSelector"`
}

type SecondarySpec struct {
	// +kubebuilder:default=1
	Replicas  int32                       `json:"replicas,omitempty"`
	Image     string                      `json:"image,omitempty"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// NodeSelector constrains the secondary apiserver pods to nodes
	// matching the specified labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Tolerations allows the secondary apiserver pods to schedule onto
	// nodes with matching taints.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// TopologySpreadConstraints controls how secondary apiserver pods
	// are distributed across topology domains (zones, racks, etc.).
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
}

// ConnectionPoolConfig tunes the SQL connection pool that Kine maintains
// against the backing database (SQLite or PostgreSQL).
type ConnectionPoolConfig struct {
	// MaxIdleConnections is the maximum number of idle (kept-alive but unused)
	// connections in the pool. Higher values reduce connection setup latency
	// under bursty workloads at the cost of memory.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxIdleConnections *int32 `json:"maxIdleConnections,omitempty"`
	// MaxOpenConnections is the maximum number of concurrent open connections
	// to the database (both active and idle). Set to 0 for unlimited.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxOpenConnections *int32 `json:"maxOpenConnections,omitempty"`
	// MaxLifetime is the maximum duration a connection may be reused before
	// being closed and replaced. Helps balance load across database replicas
	// and recover from transient network issues.
	// Value is a Go duration string (e.g. "30m", "1h").
	// +optional
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('0s')",message="duration must be a valid non-negative Go duration"
	MaxLifetime *metav1.Duration `json:"maxLifetime,omitempty"`
}

// CompactionConfig controls Kine's background compaction, which removes
// obsolete key revisions from the database to keep storage size bounded.
type CompactionConfig struct {
	// Interval is how often Kine runs its own compaction cycle. When set
	// to "0s" (default), Kine does not run autonomous compaction — the
	// apiserver triggers compaction via etcd's Compact RPC instead. Set
	// a positive duration only if you need Kine to compact independently
	// of the apiserver.
	// Value is a Go duration string (e.g. "0s", "5m", "1h").
	// +optional
	// +kubebuilder:default="0s"
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('0s')",message="duration must be a valid non-negative Go duration"
	Interval *metav1.Duration `json:"interval,omitempty"`
	// MinRetain is the minimum number of historical revisions to preserve
	// per key during compaction. Higher values allow longer watch histories
	// at the cost of storage.
	// +optional
	// +kubebuilder:default=1000
	// +kubebuilder:validation:Minimum=0
	MinRetain *int64 `json:"minRetain,omitempty"`
	// BatchSize is the number of obsolete revisions to delete per compaction
	// cycle. Larger batches compact faster but may increase database lock
	// contention. Kine default is 1000; the operator default (500) reduces
	// lock contention under heavy write load.
	// +optional
	// +kubebuilder:default=500
	// +kubebuilder:validation:Minimum=0
	BatchSize *int64 `json:"batchSize,omitempty"`
	// Timeout is the maximum duration Kine allows for a single compaction
	// transaction. Under heavy write load with large databases, the Kine
	// default (5s) may be too short, causing compaction to fail. The
	// operator defaults to 30s to give the query time to complete under
	// contention.
	// Value is a Go duration string (e.g. "30s", "1m").
	// +optional
	// +kubebuilder:default="30s"
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('0s')",message="duration must be a valid non-negative Go duration"
	Timeout *metav1.Duration `json:"timeout,omitempty"`
}

// KineSpec configures the Kine deployment that translates etcd gRPC calls
// into SQL queries against the backing database.
type KineSpec struct {
	// +kubebuilder:default=1
	Replicas  int32                       `json:"replicas,omitempty"`
	Image     string                      `json:"image,omitempty"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// NodeSelector constrains Kine pods to nodes matching the specified labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Tolerations allows Kine pods to schedule onto nodes with matching taints.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// TopologySpreadConstraints controls how Kine pods are distributed
	// across topology domains (zones, racks, etc.).
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	// ConnectionPool tunes the SQL connection pool parameters.
	// +optional
	ConnectionPool *ConnectionPoolConfig `json:"connectionPool,omitempty"`
	// Compaction controls the background revision cleanup process.
	// +optional
	Compaction *CompactionConfig `json:"compaction,omitempty"`
	// PollBatchSize is the maximum number of events Kine fetches per database
	// poll cycle. Larger values improve throughput for high-change-rate
	// workloads at the cost of per-poll latency.
	// +optional
	// +kubebuilder:validation:Minimum=0
	PollBatchSize *int64 `json:"pollBatchSize,omitempty"`
	// WatchProgressNotifyInterval controls how often Kine sends bookmark
	// (progress notification) events to active watchers, allowing them to
	// advance their resource version even when no real changes occur.
	// Value is a Go duration string (e.g. "10s", "1m").
	// +optional
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('0s')",message="duration must be a valid non-negative Go duration"
	WatchProgressNotifyInterval *metav1.Duration `json:"watchProgressNotifyInterval,omitempty"`
}

// MonitoringSpec configures Prometheus metrics collection for the shard.
type MonitoringSpec struct {
	// PrometheusServiceAccountName is the name of the ServiceAccount used by
	// Prometheus for scraping. When empty, defaults to "prometheus-k8s".
	// +optional
	PrometheusServiceAccountName string `json:"prometheusServiceAccountName,omitempty"`
	// PrometheusNamespace is the namespace where the Prometheus ServiceAccount
	// resides. When empty, defaults to "openshift-monitoring".
	// +optional
	PrometheusNamespace string `json:"prometheusNamespace,omitempty"`
}

type APIShardSpec struct {
	TargetNamespace string              `json:"targetNamespace"`
	APIGroups       []APIGroupSpec      `json:"apiGroups"`
	Storage         StorageSpec         `json:"storage"`
	NamespaceSync   NamespaceSyncConfig `json:"namespaceSync"`
	Secondary       SecondarySpec       `json:"secondary,omitempty"`
	Kine            KineSpec            `json:"kine,omitempty"`

	// Monitoring configures Prometheus metrics collection.
	// +optional
	Monitoring *MonitoringSpec `json:"monitoring,omitempty"`

	// ColocateComponents enables co-location of apiserver and Kine pods
	// on the same nodes, combined with topology-aware routing
	// (trafficDistribution: PreferSameNode) on the Kine Service.
	// This minimizes gRPC latency between the apiserver and Kine.
	// NOTE: Co-location relies on soft podAffinity and cannot override
	// hard scheduling constraints. If the secondary and Kine specs use
	// divergent nodeSelector or tolerations (e.g., pinning only one
	// component to infra nodes), co-location will be silently defeated.
	// +optional
	// +kubebuilder:default=true
	ColocateComponents *bool `json:"colocateComponents,omitempty"`
}

type ConnectionSecretReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type APIShardStatus struct {
	Phase                 string                     `json:"phase,omitempty"`
	Message               string                     `json:"message,omitempty"`
	ConnectionSecret      *ConnectionSecretReference `json:"connectionSecret,omitempty"`
	SecondaryEndpoint     string                     `json:"secondaryEndpoint,omitempty"`
	RegisteredAPIServices []string                   `json:"registeredAPIServices,omitempty"`
	Conditions            []metav1.Condition         `json:"conditions,omitempty"`
	ObservedGeneration    int64                      `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.targetNamespace`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`,priority=1
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.secondaryEndpoint`
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
