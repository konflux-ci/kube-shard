package resources

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

const (
	LabelName      = "app.kubernetes.io/name"
	LabelInstance  = "app.kubernetes.io/instance"
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelComponent = "app.kubernetes.io/component"

	ManagedByValue     = "kube-shard-operator"
	ComponentAPIServer = "apiserver"
	ComponentStorage   = "storage"
	ComponentDatabase  = "database"

	NameKine      = "kine"
	NameAPIServer = "kube-apiserver"

	CACertKey = "ca.crt"

	// LabelOTelConfig is used by the hashedconfigmap utility to track OTel Collector ConfigMaps.
	LabelOTelConfig = "kube-shard.konflux-ci.dev/otel-config"
)

// isColocateEnabled returns true if component colocation is enabled for the shard.
// Colocation is enabled by default when ColocateComponents is nil.
func isColocateEnabled(shard *kubeshardv1alpha1.APIShard) bool {
	return shard.Spec.ColocateComponents == nil || *shard.Spec.ColocateComponents
}

// BuildSecondaryAffinity returns the pod affinity/anti-affinity rules for the
// secondary apiserver deployment. It spreads replicas across nodes and optionally
// colocates them with the storage (Kine) pods. Returns nil if no rules apply.
func BuildSecondaryAffinity(shard *kubeshardv1alpha1.APIShard) *corev1.Affinity {
	replicas := shard.Spec.Secondary.Replicas
	if replicas == 0 {
		replicas = 1
	}
	colocate := isColocateEnabled(shard)

	if replicas <= 1 && !colocate {
		return nil
	}

	affinity := &corev1.Affinity{}

	if replicas > 1 {
		affinity.PodAntiAffinity = &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
				{
					Weight: 100,
					PodAffinityTerm: corev1.PodAffinityTerm{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								LabelInstance:  shard.Name,
								LabelComponent: ComponentAPIServer,
							},
						},
						TopologyKey: "kubernetes.io/hostname",
					},
				},
			},
		}
	}

	if colocate {
		affinity.PodAffinity = &corev1.PodAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
				{
					Weight: 80,
					PodAffinityTerm: corev1.PodAffinityTerm{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								LabelInstance:  shard.Name,
								LabelComponent: ComponentStorage,
							},
						},
						TopologyKey: "kubernetes.io/hostname",
					},
				},
			},
		}
	}

	return affinity
}

// BuildKineAffinity returns the pod anti-affinity rules for the Kine deployment
// to spread replicas across different nodes. Returns nil for single-replica deployments.
func BuildKineAffinity(shard *kubeshardv1alpha1.APIShard) *corev1.Affinity {
	replicas := shard.Spec.Kine.Replicas
	if replicas == 0 {
		replicas = 1
	}

	if replicas <= 1 {
		return nil
	}

	return &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
				{
					Weight: 100,
					PodAffinityTerm: corev1.PodAffinityTerm{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								LabelInstance:  shard.Name,
								LabelComponent: ComponentStorage,
							},
						},
						TopologyKey: "kubernetes.io/hostname",
					},
				},
			},
		},
	}
}
