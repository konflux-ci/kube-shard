package apishard

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// GroupVersionDiscoverer is the subset of the Kubernetes discovery API needed
// to probe for a specific group-version's resources. It is satisfied by
// discovery.DiscoveryClient and by test fakes.
type GroupVersionDiscoverer interface {
	ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error)
}

// DiscoverPrimaryIssuer fetches the primary cluster's service-account-issuer
// from the OIDC discovery endpoint (/.well-known/openid-configuration). This
// value is needed so the secondary apiserver's --api-audiences accepts tokens
// issued by the primary.
func DiscoverPrimaryIssuer(cfg *rest.Config) (string, error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("creating clientset: %w", err)
	}

	body, err := clientset.Discovery().RESTClient().
		Get().
		AbsPath("/.well-known/openid-configuration").
		DoRaw(context.Background())
	if err != nil {
		return "", fmt.Errorf("fetching OIDC discovery: %w", err)
	}

	var oidc struct {
		Issuer string `json:"issuer"`
	}
	if err := json.Unmarshal(body, &oidc); err != nil {
		return "", fmt.Errorf("parsing OIDC discovery response: %w", err)
	}
	if oidc.Issuer == "" {
		return "", fmt.Errorf("OIDC discovery response has empty issuer")
	}
	return oidc.Issuer, nil
}

// DiscoverServiceMonitor checks whether the ServiceMonitor CRD from the
// Prometheus Operator is installed on the cluster. It returns true when the
// CRD is present, false when it is absent, and a non-nil error only for
// unexpected discovery failures (callers should treat an error as "unknown").
func DiscoverServiceMonitor(client GroupVersionDiscoverer) (bool, error) {
	apiResourceList, err := client.ServerResourcesForGroupVersion("monitoring.coreos.com/v1")
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return false, nil
		}
		return false, fmt.Errorf("querying monitoring.coreos.com/v1: %w", err)
	}

	for _, r := range apiResourceList.APIResources {
		if r.Kind == "ServiceMonitor" {
			return true, nil
		}
	}
	return false, nil
}

// DiscoverSCC checks whether the SecurityContextConstraints API from OpenShift
// is available on the cluster. It returns true when the API is present, false
// when absent, and a non-nil error only for unexpected discovery failures.
func DiscoverSCC(client GroupVersionDiscoverer) (bool, error) {
	apiResourceList, err := client.ServerResourcesForGroupVersion("security.openshift.io/v1")
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return false, nil
		}
		return false, fmt.Errorf("querying security.openshift.io/v1: %w", err)
	}

	for _, r := range apiResourceList.APIResources {
		if r.Kind == "SecurityContextConstraints" {
			return true, nil
		}
	}
	return false, nil
}
