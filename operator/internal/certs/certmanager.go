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

// KineCAIssuerName returns the name of the CA-backed Issuer dedicated to Kine
// mTLS certificates. Using a separate CA restricts Kine's trust boundary so
// that only certificates issued by this CA (the etcd client cert) can
// authenticate to Kine — not the admin client cert or any other shard cert.
func KineCAIssuerName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-kine-ca-issuer", shard.Name)
}

// KineCACertificateName returns the name of the Kine CA Certificate resource.
func KineCACertificateName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-kine-ca", shard.Name)
}

// KineCASecretName returns the name of the Secret holding the Kine CA key pair.
func KineCASecretName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-kine-ca-keypair", shard.Name)
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

// BuildKineSelfSignedIssuer creates a self-signed Issuer for generating the
// Kine-dedicated CA. This is separate from the shard-wide self-signed issuer
// so that the Kine CA is fully independent.
func BuildKineSelfSignedIssuer(shard *kubeshardv1alpha1.APIShard) *unstructured.Unstructured {
	issuer := &unstructured.Unstructured{}
	issuer.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Issuer",
	})
	issuer.SetName(fmt.Sprintf("%s-kine-selfsigned", shard.Name))
	issuer.SetNamespace(shard.Spec.TargetNamespace)
	issuer.SetLabels(certLabels(shard))

	_ = unstructured.SetNestedMap(issuer.Object, map[string]interface{}{}, "spec", "selfSigned")

	return issuer
}

// BuildKineCACertificate creates the CA Certificate for the Kine-dedicated
// certificate chain. This CA signs the Kine serving certificate and the etcd
// client certificate but not the admin or API server serving certificates,
// restricting Kine's mTLS trust boundary.
func BuildKineCACertificate(shard *kubeshardv1alpha1.APIShard) *unstructured.Unstructured {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	})
	cert.SetName(KineCACertificateName(shard))
	cert.SetNamespace(shard.Spec.TargetNamespace)
	cert.SetLabels(certLabels(shard))

	cert.Object["spec"] = map[string]interface{}{
		"isCA":       true,
		"commonName": fmt.Sprintf("%s-kine-ca", shard.Name),
		"secretName": KineCASecretName(shard),
		"duration":   "87600h", // 10 years
		"issuerRef": map[string]interface{}{
			"name":  fmt.Sprintf("%s-kine-selfsigned", shard.Name),
			"kind":  "Issuer",
			"group": "cert-manager.io",
		},
	}

	return cert
}

// BuildKineCAIssuer creates an Issuer backed by the Kine CA certificate.
// Only the Kine serving certificate and the etcd client certificate are
// issued by this Issuer, isolating Kine's trust boundary from the rest of
// the shard's PKI.
func BuildKineCAIssuer(shard *kubeshardv1alpha1.APIShard) *unstructured.Unstructured {
	issuer := &unstructured.Unstructured{}
	issuer.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Issuer",
	})
	issuer.SetName(KineCAIssuerName(shard))
	issuer.SetNamespace(shard.Spec.TargetNamespace)
	issuer.SetLabels(certLabels(shard))

	issuer.Object["spec"] = map[string]interface{}{
		"ca": map[string]interface{}{
			"secretName": KineCASecretName(shard),
		},
	}

	return issuer
}

// BuildServingCertificate creates the TLS serving certificate for the secondary API server.
func BuildServingCertificate(shard *kubeshardv1alpha1.APIShard) *unstructured.Unstructured {
	return buildServingCert(
		shard,
		ServingCertificateName(shard),
		PKISecretName(shard),
		resources.SecondaryServiceName(shard),
		IssuerName(shard),
	)
}

// KineServingCertificateName returns the name of the Kine serving Certificate resource.
func KineServingCertificateName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-kine-serving", shard.Name)
}

// KineServingSecretName returns the name of the Secret holding the Kine serving certificate.
func KineServingSecretName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-kine-serving-cert", shard.Name)
}

// BuildKineServingCertificate creates the TLS serving certificate for the Kine
// gRPC endpoint. The certificate is issued by the Kine-dedicated CA (not the
// shard-wide CA) to restrict the mTLS trust boundary. It includes DNS SANs
// matching the Kine Service FQDN so the secondary kube-apiserver can verify
// the server identity.
func BuildKineServingCertificate(shard *kubeshardv1alpha1.APIShard) *unstructured.Unstructured {
	return buildServingCert(
		shard,
		KineServingCertificateName(shard),
		KineServingSecretName(shard),
		resources.KineServiceName(shard),
		KineCAIssuerName(shard),
	)
}

// PostgreSQLCAIssuerName returns the name of the CA-backed Issuer dedicated to
// in-cluster PostgreSQL server TLS certificates.
func PostgreSQLCAIssuerName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql-ca-issuer", shard.Name)
}

// PostgreSQLCACertificateName returns the name of the PostgreSQL CA Certificate resource.
func PostgreSQLCACertificateName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql-ca", shard.Name)
}

// PostgreSQLCASecretName returns the name of the Secret holding the PostgreSQL CA key pair.
// Kine mounts ca.crt from this secret to verify the PostgreSQL server certificate.
func PostgreSQLCASecretName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql-ca-keypair", shard.Name)
}

// PostgreSQLServingCertificateName returns the name of the PostgreSQL serving Certificate resource.
func PostgreSQLServingCertificateName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql-serving", shard.Name)
}

// PostgreSQLServingSecretName returns the name of the Secret holding the PostgreSQL serving certificate.
func PostgreSQLServingSecretName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql-serving-cert", shard.Name)
}

// BuildPostgreSQLSelfSignedIssuer creates a self-signed Issuer for generating the
// PostgreSQL-dedicated CA. This is separate from the shard-wide and Kine CA chains.
func BuildPostgreSQLSelfSignedIssuer(shard *kubeshardv1alpha1.APIShard) *unstructured.Unstructured {
	issuer := &unstructured.Unstructured{}
	issuer.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Issuer",
	})
	issuer.SetName(fmt.Sprintf("%s-postgresql-selfsigned", shard.Name))
	issuer.SetNamespace(shard.Spec.TargetNamespace)
	issuer.SetLabels(certLabels(shard))

	_ = unstructured.SetNestedMap(issuer.Object, map[string]interface{}{}, "spec", "selfSigned")

	return issuer
}

// BuildPostgreSQLCACertificate creates the CA Certificate for the PostgreSQL-dedicated
// certificate chain used to sign the in-cluster PostgreSQL server certificate.
func BuildPostgreSQLCACertificate(shard *kubeshardv1alpha1.APIShard) *unstructured.Unstructured {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	})
	cert.SetName(PostgreSQLCACertificateName(shard))
	cert.SetNamespace(shard.Spec.TargetNamespace)
	cert.SetLabels(certLabels(shard))

	cert.Object["spec"] = map[string]interface{}{
		"isCA":       true,
		"commonName": fmt.Sprintf("%s-postgresql-ca", shard.Name),
		"secretName": PostgreSQLCASecretName(shard),
		"duration":   "87600h", // 10 years
		"issuerRef": map[string]interface{}{
			"name":  fmt.Sprintf("%s-postgresql-selfsigned", shard.Name),
			"kind":  "Issuer",
			"group": "cert-manager.io",
		},
	}

	return cert
}

// BuildPostgreSQLCAIssuer creates an Issuer backed by the PostgreSQL CA certificate.
func BuildPostgreSQLCAIssuer(shard *kubeshardv1alpha1.APIShard) *unstructured.Unstructured {
	issuer := &unstructured.Unstructured{}
	issuer.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Issuer",
	})
	issuer.SetName(PostgreSQLCAIssuerName(shard))
	issuer.SetNamespace(shard.Spec.TargetNamespace)
	issuer.SetLabels(certLabels(shard))

	issuer.Object["spec"] = map[string]interface{}{
		"ca": map[string]interface{}{
			"secretName": PostgreSQLCASecretName(shard),
		},
	}

	return issuer
}

// BuildPostgreSQLServingCertificate creates the TLS serving certificate for
// in-cluster PostgreSQL. The certificate includes DNS SANs matching the
// PostgreSQL Service FQDN so Kine can verify the server identity with verify-full.
func BuildPostgreSQLServingCertificate(shard *kubeshardv1alpha1.APIShard) *unstructured.Unstructured {
	return buildServingCert(
		shard,
		PostgreSQLServingCertificateName(shard),
		PostgreSQLServingSecretName(shard),
		resources.PostgreSQLServiceName(shard),
		PostgreSQLCAIssuerName(shard),
	)
}

// buildServingCert constructs a cert-manager Certificate for TLS serving,
// parameterized by the certificate name, secret name, service name, and issuer name.
func buildServingCert(
	shard *kubeshardv1alpha1.APIShard,
	certName, secretName, svcName, issuerName string,
) *unstructured.Unstructured {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	})
	cert.SetName(certName)
	cert.SetNamespace(shard.Spec.TargetNamespace)
	cert.SetLabels(certLabels(shard))

	ns := shard.Spec.TargetNamespace

	cert.Object["spec"] = map[string]interface{}{
		"secretName":  secretName,
		"duration":    "8760h",
		"renewBefore": "720h",
		"dnsNames": []interface{}{
			svcName,
			fmt.Sprintf("%s.%s", svcName, ns),
			fmt.Sprintf("%s.%s.svc", svcName, ns),
			fmt.Sprintf("%s.%s.svc.cluster.local", svcName, ns),
		},
		"issuerRef": map[string]interface{}{
			"name":  issuerName,
			"kind":  "Issuer",
			"group": "cert-manager.io",
		},
	}

	return cert
}

// EtcdClientCertificateName returns the name of the etcd client Certificate resource.
func EtcdClientCertificateName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-etcd-client", shard.Name)
}

// EtcdClientSecretName returns the name of the Secret holding the etcd client certificate.
func EtcdClientSecretName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-etcd-client-cert", shard.Name)
}

// BuildEtcdClientCertificate creates a client certificate used by the secondary
// kube-apiserver to authenticate to Kine over mTLS. The certificate is issued
// by the Kine-dedicated CA (not the shard-wide CA) so that Kine only trusts
// this specific client identity, with the clientAuth extended key usage.
func BuildEtcdClientCertificate(shard *kubeshardv1alpha1.APIShard) *unstructured.Unstructured {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	})
	cert.SetName(EtcdClientCertificateName(shard))
	cert.SetNamespace(shard.Spec.TargetNamespace)
	cert.SetLabels(certLabels(shard))

	cert.Object["spec"] = map[string]interface{}{
		"secretName":  EtcdClientSecretName(shard),
		"commonName":  fmt.Sprintf("%s-etcd-client", shard.Name),
		"duration":    "8760h",
		"renewBefore": "720h",
		"usages": []interface{}{
			"client auth",
		},
		"issuerRef": map[string]interface{}{
			"name":  KineCAIssuerName(shard),
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
