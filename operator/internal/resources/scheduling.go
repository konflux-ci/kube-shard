package resources

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

func isColocateEnabled(shard *kubeshardv1alpha1.APIShard) bool {
	return shard.Spec.ColocateComponents == nil || *shard.Spec.ColocateComponents
}

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
								"app.kubernetes.io/instance":  shard.Name,
								"app.kubernetes.io/component": "apiserver",
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
								"app.kubernetes.io/instance":  shard.Name,
								"app.kubernetes.io/component": "storage",
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
								"app.kubernetes.io/instance":  shard.Name,
								"app.kubernetes.io/component": "storage",
							},
						},
						TopologyKey: "kubernetes.io/hostname",
					},
				},
			},
		},
	}
}
