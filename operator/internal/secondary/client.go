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

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/log"
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

// ClientProvider manages clients for secondary API servers, caching them by shard name.
type ClientProvider struct {
	mu      sync.RWMutex
	clients map[string]*clientEntry
}

type clientEntry struct {
	client    kubernetes.Interface
	restCfg   *rest.Config
	createdAt time.Time
}

func NewClientProvider() *ClientProvider {
	return &ClientProvider{
		clients: make(map[string]*clientEntry),
	}
}

// GetOrCreate returns an existing client or creates a new one for the given shard.
func (p *ClientProvider) GetOrCreate(shardName string, cfg ClientConfig) (kubernetes.Interface, *rest.Config, error) {
	p.mu.RLock()
	entry, exists := p.clients[shardName]
	p.mu.RUnlock()

	if exists {
		return entry.client, entry.restCfg, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock
	if entry, exists = p.clients[shardName]; exists {
		return entry.client, entry.restCfg, nil
	}

	restCfg, err := buildRESTConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("building REST config for shard %s: %w", shardName, err)
	}

	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("creating client for shard %s: %w", shardName, err)
	}

	p.clients[shardName] = &clientEntry{
		client:    client,
		restCfg:   restCfg,
		createdAt: time.Now(),
	}

	return client, restCfg, nil
}

// Invalidate removes a cached client for the given shard, forcing re-creation on next access.
func (p *ClientProvider) Invalidate(shardName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.clients, shardName)
}

func buildRESTConfig(cfg ClientConfig) (*rest.Config, error) {
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
		restCfg.TLSClientConfig.CertData = cfg.ClientCert
		restCfg.TLSClientConfig.KeyData = cfg.ClientKey
	}

	return restCfg, nil
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
	defer resp.Body.Close()

	healthy := resp.StatusCode == http.StatusOK
	if !healthy {
		logger.V(1).Info("Health check returned non-200", "endpoint", endpoint, "status", resp.StatusCode)
	}
	return healthy, nil
}
