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

package resources

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

// RestrictedPodSecurityContext returns a PodSecurityContext that conforms to the
// Kubernetes Restricted pod security standard and the OpenShift restricted-v2 SCC.
// It does not set RunAsUser so that OpenShift can assign a UID from the
// namespace's allocated range (MustRunAsRange).
func RestrictedPodSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

// RestrictedContainerSecurityContext returns a SecurityContext that conforms to
// the Kubernetes Restricted pod security standard and the OpenShift
// restricted-v2 SCC.
func RestrictedContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(true),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

// RestrictedContainerSecurityContextWritableRoot returns a SecurityContext like
// RestrictedContainerSecurityContext but with ReadOnlyRootFilesystem set to
// false. Use for images whose entrypoints must modify system files at startup
// (e.g., Red Hat PostgreSQL images use an NSS wrapper that writes /etc/passwd).
func RestrictedContainerSecurityContextWritableRoot() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(false),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

// APIServerPodSecurityContext returns a PodSecurityContext for the secondary
// kube-apiserver. Identical to RestrictedPodSecurityContext; kept separate so
// kube-apiserver-specific constraints can diverge independently if needed.
func APIServerPodSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

// APIServerContainerSecurityContext returns a SecurityContext for the secondary
// kube-apiserver container. The upstream kube-apiserver binary carries a
// cap_net_bind_service file capability (set in the official image build). This
// has two implications:
//
//  1. AllowPrivilegeEscalation must be true — the kernel's no_new_privs check
//     rejects exec() of any binary with file capabilities.
//  2. NET_BIND_SERVICE must be added back after dropping ALL — the bounding set
//     must include the file capability or exec() fails with EPERM.
//
// On OpenShift this requires a custom SCC (see BuildAPIServerSCC).
func APIServerContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(true),
		ReadOnlyRootFilesystem:   ptr.To(true),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
			Add:  []corev1.Capability{"NET_BIND_SERVICE"},
		},
	}
}
