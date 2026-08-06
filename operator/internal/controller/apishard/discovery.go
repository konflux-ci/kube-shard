package apishard

import (
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GroupVersionDiscoverer is the subset of the Kubernetes discovery API needed
// to probe for a specific group-version's resources. It is satisfied by
// discovery.DiscoveryClient and by test fakes.
type GroupVersionDiscoverer interface {
	ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error)
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
