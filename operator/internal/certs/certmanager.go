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

// Package certs provides cert-manager Certificate and Issuer resource builders
// for the kube-shard operator. When cert-manager is available in the cluster,
// these resources automate TLS certificate lifecycle for the secondary API server.
package certs

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
	"github.com/konflux-ci/kube-shard/operator/internal/resources"
)

func IssuerName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-ca-issuer", shard.Name)
}

func CACertificateName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-front-proxy-ca", shard.Name)
}

func ServingCertificateName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-serving-cert", shard.Name)
}

// PKISecretName returns the name of the Secret that cert-manager creates for the serving cert.
// This is the same secret the secondary API server mounts.
func PKISecretName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-pki", shard.Name)
}

// BuildSelfSignedIssuer creates a self-signed Issuer for generating the CA.
func BuildSelfSignedIssuer(shard *kubeshardv1alpha1.APIShard) *unstructured.Unstructured {
	issuer := &unstructured.Unstructured{}
	issuer.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Issuer",
	})
	issuer.SetName(fmt.Sprintf("%s-selfsigned", shard.Name))
	issuer.SetNamespace(shard.Spec.TargetNamespace)
	issuer.SetLabels(certLabels(shard))

	_ = unstructured.SetNestedMap(issuer.Object, map[string]interface{}{}, "spec", "selfSigned")

	return issuer
}

// BuildCACertificate creates a Certificate resource for the CA.
func BuildCACertificate(shard *kubeshardv1alpha1.APIShard) *unstructured.Unstructured {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	})
	cert.SetName(CACertificateName(shard))
	cert.SetNamespace(shard.Spec.TargetNamespace)
	cert.SetLabels(certLabels(shard))

	cert.Object["spec"] = map[string]interface{}{
		"isCA":       true,
		"commonName": fmt.Sprintf("%s-ca", shard.Name),
		"secretName": fmt.Sprintf("%s-ca-keypair", shard.Name),
		"duration":   "87600h", // 10 years
		"issuerRef": map[string]interface{}{
			"name":  fmt.Sprintf("%s-selfsigned", shard.Name),
			"kind":  "Issuer",
			"group": "cert-manager.io",
		},
	}

	return cert
}

// BuildCAIssuer creates an Issuer backed by the CA certificate.
func BuildCAIssuer(shard *kubeshardv1alpha1.APIShard) *unstructured.Unstructured {
	issuer := &unstructured.Unstructured{}
	issuer.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Issuer",
	})
	issuer.SetName(IssuerName(shard))
	issuer.SetNamespace(shard.Spec.TargetNamespace)
	issuer.SetLabels(certLabels(shard))

	issuer.Object["spec"] = map[string]interface{}{
		"ca": map[string]interface{}{
			"secretName": fmt.Sprintf("%s-ca-keypair", shard.Name),
		},
	}

	return issuer
}

// BuildServingCertificate creates the TLS serving certificate for the secondary API server.
func BuildServingCertificate(shard *kubeshardv1alpha1.APIShard) *unstructured.Unstructured {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	})
	cert.SetName(ServingCertificateName(shard))
	cert.SetNamespace(shard.Spec.TargetNamespace)
	cert.SetLabels(certLabels(shard))

	svcName := resources.SecondaryServiceName(shard)
	ns := shard.Spec.TargetNamespace

	cert.Object["spec"] = map[string]interface{}{
		"secretName":  PKISecretName(shard),
		"duration":    "8760h", // 1 year
		"renewBefore": "720h",  // 30 days
		"dnsNames": []interface{}{
			svcName,
			fmt.Sprintf("%s.%s", svcName, ns),
			fmt.Sprintf("%s.%s.svc", svcName, ns),
			fmt.Sprintf("%s.%s.svc.cluster.local", svcName, ns),
		},
		"issuerRef": map[string]interface{}{
			"name":  IssuerName(shard),
			"kind":  "Issuer",
			"group": "cert-manager.io",
		},
	}

	return cert
}

// AdminClientCertificateName returns the name of the admin client Certificate resource.
func AdminClientCertificateName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-admin-client", shard.Name)
}

// AdminClientSecretName returns the name of the Secret holding the admin client certificate.
func AdminClientSecretName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-admin-client-cert", shard.Name)
}

// BuildAdminClientCertificate creates a client certificate for operator-to-secondary
// authentication. The CN is set to "kube-shard-admin" and the Organization to
// "system:masters" so that the secondary kube-apiserver treats the holder as a
// cluster-admin via its built-in RBAC bindings.
func BuildAdminClientCertificate(shard *kubeshardv1alpha1.APIShard) *unstructured.Unstructured {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	})
	cert.SetName(AdminClientCertificateName(shard))
	cert.SetNamespace(shard.Spec.TargetNamespace)
	cert.SetLabels(certLabels(shard))

	cert.Object["spec"] = map[string]interface{}{
		"secretName":  AdminClientSecretName(shard),
		"commonName":  "kube-shard-admin",
		"duration":    "8760h",
		"renewBefore": "720h",
		"usages": []interface{}{
			"client auth",
		},
		"subject": map[string]interface{}{
			"organizations": []interface{}{
				"system:masters",
			},
		},
		"issuerRef": map[string]interface{}{
			"name":  IssuerName(shard),
			"kind":  "Issuer",
			"group": "cert-manager.io",
		},
	}

	return cert
}

func certLabels(shard *kubeshardv1alpha1.APIShard) map[string]string {
	return map[string]string{
		resources.LabelManagedBy: resources.ManagedByValue,
		resources.LabelInstance:  shard.Name,
		resources.LabelComponent: "certificates",
	}
}
