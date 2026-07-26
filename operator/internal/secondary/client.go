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

package secondary

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

// ClientConfig holds the configuration needed to build a client for a secondary API server.
type ClientConfig struct {
	Host   string
	CACert []byte
	Token  string
	// ClientCert and ClientKey for mTLS (alternative to token).
	ClientCert []byte
	ClientKey  []byte
}

// ClientProvider manages controller-runtime clients for secondary API servers, caching by shard name.
type ClientProvider struct {
	mu      sync.RWMutex
	clients map[string]*clientEntry
	scheme  *runtime.Scheme
}

type clientEntry struct {
	client    client.Client
	host      string
	createdAt time.Time
}

func NewClientProvider(scheme *runtime.Scheme) *ClientProvider {
	return &ClientProvider{
		clients: make(map[string]*clientEntry),
		scheme:  scheme,
	}
}

// GetOrCreate returns an existing client or creates a new one for the given shard.
// If the endpoint (Host) has changed since the client was cached, the old client
// is invalidated and a new one is created.
func (p *ClientProvider) GetOrCreate(shardName string, cfg ClientConfig) (client.Client, error) {
	p.mu.RLock()
	entry, exists := p.clients[shardName]
	p.mu.RUnlock()

	if exists && entry.host == cfg.Host {
		return entry.client, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock
	if entry, exists = p.clients[shardName]; exists && entry.host == cfg.Host {
		return entry.client, nil
	}

	restCfg := buildRESTConfig(cfg)

	c, err := client.New(restCfg, client.Options{Scheme: p.scheme})
	if err != nil {
		return nil, fmt.Errorf("creating client for shard %s: %w", shardName, err)
	}

	p.clients[shardName] = &clientEntry{
		client:    c,
		host:      cfg.Host,
		createdAt: time.Now(),
	}

	return c, nil
}

// Invalidate removes a cached client for the given shard, forcing re-creation on next access.
func (p *ClientProvider) Invalidate(shardName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.clients, shardName)
}

// BuildClient reads connection credentials from the cluster and returns a
// cached controller-runtime client for the secondary API server described by
// conn. The cacheKey must be unique per logical consumer (e.g. namespace/name).
func (p *ClientProvider) BuildClient(ctx context.Context, reader client.Reader, conn kubeshardv1alpha1.SecondaryConnectionSpec, namespace, cacheKey string) (client.Client, error) {
	var caSecret corev1.Secret
	if err := reader.Get(ctx, types.NamespacedName{
		Name:      conn.CASecretRef.Name,
		Namespace: namespace,
	}, &caSecret); err != nil {
		return nil, fmt.Errorf("reading CA secret %s: %w", conn.CASecretRef.Name, err)
	}
	caCert := caSecret.Data["ca.crt"]
	if len(caCert) == 0 {
		caCert = caSecret.Data["tls.crt"]
	}
	if len(caCert) == 0 {
		return nil, fmt.Errorf("CA secret %s has no ca.crt or tls.crt key", conn.CASecretRef.Name)
	}

	var authSecret corev1.Secret
	if err := reader.Get(ctx, types.NamespacedName{
		Name:      conn.AuthSecretRef.Name,
		Namespace: namespace,
	}, &authSecret); err != nil {
		return nil, fmt.Errorf("reading auth secret %s: %w", conn.AuthSecretRef.Name, err)
	}
	token := string(authSecret.Data["token"])
	if token == "" {
		return nil, fmt.Errorf("auth secret %s missing key token", conn.AuthSecretRef.Name)
	}

	port := conn.ServiceRef.Port
	if port == 0 {
		port = 443
	}
	host := fmt.Sprintf("https://%s.%s.svc:%d",
		conn.ServiceRef.Name, conn.ServiceRef.Namespace, port)

	return p.GetOrCreate(cacheKey, ClientConfig{
		Host:   host,
		CACert: caCert,
		Token:  token,
	})
}

func buildRESTConfig(cfg ClientConfig) *rest.Config {
	restCfg := &rest.Config{
		Host: cfg.Host,
	}

	if len(cfg.CACert) > 0 {
		restCfg.TLSClientConfig = rest.TLSClientConfig{
			CAData: cfg.CACert,
		}
	} else {
		restCfg.TLSClientConfig = rest.TLSClientConfig{
			Insecure: true,
		}
	}

	if cfg.Token != "" {
		restCfg.BearerToken = cfg.Token
	} else if len(cfg.ClientCert) > 0 && len(cfg.ClientKey) > 0 {
		restCfg.CertData = cfg.ClientCert
		restCfg.KeyData = cfg.ClientKey
	}

	return restCfg
}

// HealthChecker performs health checks against a secondary API server.
type HealthChecker struct {
	httpClient *http.Client
}

func NewHealthChecker(caCert []byte) *HealthChecker {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	if len(caCert) > 0 {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(caCert)
		transport.TLSClientConfig.RootCAs = pool
	} else {
		transport.TLSClientConfig.InsecureSkipVerify = true
	}

	return &HealthChecker{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   5 * time.Second,
		},
	}
}

// CheckHealth performs a /healthz check against the secondary API server.
func (h *HealthChecker) CheckHealth(ctx context.Context, endpoint string) (bool, error) {
	logger := log.FromContext(ctx)

	url := endpoint + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("creating health check request: %w", err)
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		logger.V(1).Info("Health check failed", "endpoint", endpoint, "error", err)
		return false, nil
	}
	defer func() { _ = resp.Body.Close() }()

	healthy := resp.StatusCode == http.StatusOK
	if !healthy {
		logger.V(1).Info("Health check returned non-200", "endpoint", endpoint, "status", resp.StatusCode)
	}
	return healthy, nil
}
