package apishard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
)

type fakeGVDiscoverer struct {
	resources *metav1.APIResourceList
	err       error
}

func (f *fakeGVDiscoverer) ServerResourcesForGroupVersion(_ string) (*metav1.APIResourceList, error) {
	return f.resources, f.err
}

// TestDiscoverServiceMonitor_Present verifies that DiscoverServiceMonitor
// returns true when the monitoring.coreos.com/v1 group contains ServiceMonitor.
func TestDiscoverServiceMonitor_Present(t *testing.T) {
	g := NewGomegaWithT(t)
	client := &fakeGVDiscoverer{
		resources: &metav1.APIResourceList{
			GroupVersion: "monitoring.coreos.com/v1",
			APIResources: []metav1.APIResource{
				{Kind: "Prometheus"},
				{Kind: "ServiceMonitor"},
				{Kind: "Alertmanager"},
			},
		},
	}

	found, err := DiscoverServiceMonitor(client)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
}

// TestDiscoverServiceMonitor_GroupExistsWithoutServiceMonitor verifies that
// DiscoverServiceMonitor returns false when the monitoring group exists but
// does not contain a ServiceMonitor resource.
func TestDiscoverServiceMonitor_GroupExistsWithoutServiceMonitor(t *testing.T) {
	g := NewGomegaWithT(t)
	client := &fakeGVDiscoverer{
		resources: &metav1.APIResourceList{
			GroupVersion: "monitoring.coreos.com/v1",
			APIResources: []metav1.APIResource{
				{Kind: "Prometheus"},
				{Kind: "Alertmanager"},
			},
		},
	}

	found, err := DiscoverServiceMonitor(client)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeFalse())
}

// TestDiscoverServiceMonitor_GroupNotFound verifies that DiscoverServiceMonitor
// returns false (not an error) when the monitoring group is not registered and
// the API server returns a NotFound status error.
func TestDiscoverServiceMonitor_GroupNotFound(t *testing.T) {
	g := NewGomegaWithT(t)
	client := &fakeGVDiscoverer{
		err: apierrors.NewNotFound(
			schema.GroupResource{Group: "monitoring.coreos.com", Resource: ""},
			"monitoring.coreos.com/v1",
		),
	}

	found, err := DiscoverServiceMonitor(client)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeFalse())
}

// TestDiscoverServiceMonitor_NoMatchError verifies that DiscoverServiceMonitor
// returns false (not an error) when the REST mapper reports no match for the
// monitoring group.
func TestDiscoverServiceMonitor_NoMatchError(t *testing.T) {
	g := NewGomegaWithT(t)
	client := &fakeGVDiscoverer{
		err: &meta.NoResourceMatchError{
			PartialResource: schema.GroupVersionResource{
				Group:   "monitoring.coreos.com",
				Version: "v1",
			},
		},
	}

	found, err := DiscoverServiceMonitor(client)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeFalse())
}

// TestDiscoverServiceMonitor_UnexpectedError verifies that DiscoverServiceMonitor
// propagates unexpected errors from the discovery client.
func TestDiscoverServiceMonitor_UnexpectedError(t *testing.T) {
	g := NewGomegaWithT(t)
	client := &fakeGVDiscoverer{
		err: fmt.Errorf("connection refused"),
	}

	found, err := DiscoverServiceMonitor(client)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("connection refused"))
	g.Expect(found).To(BeFalse())
}

// TestDiscoverServiceMonitor_EmptyResources verifies that DiscoverServiceMonitor
// returns false when the group version exists but has no resources.
func TestDiscoverServiceMonitor_EmptyResources(t *testing.T) {
	g := NewGomegaWithT(t)
	client := &fakeGVDiscoverer{
		resources: &metav1.APIResourceList{
			GroupVersion: "monitoring.coreos.com/v1",
			APIResources: []metav1.APIResource{},
		},
	}

	found, err := DiscoverServiceMonitor(client)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeFalse())
}

// TestDiscoverSCC_Present verifies that DiscoverSCC returns true when
// security.openshift.io/v1 contains SecurityContextConstraints.
func TestDiscoverSCC_Present(t *testing.T) {
	g := NewGomegaWithT(t)
	client := &fakeGVDiscoverer{
		resources: &metav1.APIResourceList{
			GroupVersion: "security.openshift.io/v1",
			APIResources: []metav1.APIResource{
				{Kind: "SecurityContextConstraints"},
			},
		},
	}

	found, err := DiscoverSCC(client)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
}

// TestDiscoverSCC_GroupNotFound verifies that DiscoverSCC returns false
// when the security.openshift.io API group is not available (non-OpenShift cluster).
func TestDiscoverSCC_GroupNotFound(t *testing.T) {
	g := NewGomegaWithT(t)
	client := &fakeGVDiscoverer{
		err: apierrors.NewNotFound(
			schema.GroupResource{Group: "security.openshift.io", Resource: ""},
			"security.openshift.io/v1",
		),
	}

	found, err := DiscoverSCC(client)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeFalse())
}

// TestDiscoverPrimaryIssuer_Success verifies that the issuer is extracted from
// the OIDC discovery JSON response.
func TestDiscoverPrimaryIssuer_Success(t *testing.T) {
	g := NewGomegaWithT(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": "https://oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := &rest.Config{Host: srv.URL}
	issuer, err := DiscoverPrimaryIssuer(cfg)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(issuer).To(Equal("https://oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE"))
}

// TestDiscoverPrimaryIssuer_EmptyIssuer verifies that an error is returned
// when the OIDC response has an empty issuer field.
func TestDiscoverPrimaryIssuer_EmptyIssuer(t *testing.T) {
	g := NewGomegaWithT(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer": ""}`))
	}))
	defer srv.Close()

	cfg := &rest.Config{Host: srv.URL}
	_, err := DiscoverPrimaryIssuer(cfg)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("empty issuer"))
}

// TestDiscoverPrimaryIssuer_ServerError verifies that a non-200 response
// results in an error.
func TestDiscoverPrimaryIssuer_ServerError(t *testing.T) {
	g := NewGomegaWithT(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := &rest.Config{Host: srv.URL}
	_, err := DiscoverPrimaryIssuer(cfg)
	g.Expect(err).To(HaveOccurred())
}
