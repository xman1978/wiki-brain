package llm

import (
	"context"
	"sync"
)

// PurposeModels resolves model parameters for batching outside LLM HTTP calls.
type PurposeModels interface {
	ModelForPurpose(purpose string) (ModelParams, error)
}

// PurposeRoute binds a purpose to a provider and per-purpose model parameters.
type PurposeRoute struct {
	ProviderID  string
	ModelParams ModelParams
}

// RoutingClient routes LLM calls by purpose → provider binding.
type RoutingClient struct {
	promptsDir string

	mu        sync.RWMutex
	routes    map[string]PurposeRoute // purpose → route
	clients   map[string]*OpenAIClient
	providers map[string]*ProviderRuntime
}

func NewRoutingClient(promptsDir string) *RoutingClient {
	return &RoutingClient{
		promptsDir: promptsDir,
		routes:     make(map[string]PurposeRoute),
		clients:    make(map[string]*OpenAIClient),
		providers:  make(map[string]*ProviderRuntime),
	}
}

type RouteSnapshot struct {
	Routes    map[string]PurposeRoute
	Providers map[string]*ProviderRuntime
}

func (r *RoutingClient) Reload(snap RouteSnapshot) error {
	clients := make(map[string]*OpenAIClient, len(snap.Providers))
	for id, p := range snap.Providers {
		c, err := NewOpenAIClient(p, r.promptsDir)
		if err != nil {
			return err
		}
		clients[id] = c
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = snap.Routes
	r.providers = snap.Providers
	r.clients = clients
	return nil
}

func (r *RoutingClient) resolve(purpose string) (*OpenAIClient, ModelParams, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	route, ok := r.routes[purpose]
	if !ok || route.ProviderID == "" {
		return nil, ModelParams{}, ErrNotConfigured
	}
	if route.ModelParams.Model == "" {
		return nil, ModelParams{}, ErrNotConfigured
	}
	c, ok := r.clients[route.ProviderID]
	if !ok || c == nil {
		return nil, ModelParams{}, ErrNotConfigured
	}
	return c, route.ModelParams, nil
}

func (r *RoutingClient) Complete(ctx context.Context, promptFile string, vars map[string]string, purpose string) (string, error) {
	c, mc, err := r.resolve(purpose)
	if err != nil {
		return "", err
	}
	return c.CompleteWithParams(ctx, promptFile, vars, mc)
}

func (r *RoutingClient) CompleteJSON(ctx context.Context, promptFile string, vars map[string]string, purpose string) ([]byte, error) {
	c, mc, err := r.resolve(purpose)
	if err != nil {
		return nil, err
	}
	return c.CompleteJSONWithParams(ctx, promptFile, vars, mc)
}

func (r *RoutingClient) CompleteStream(ctx context.Context, promptFile string, vars map[string]string, purpose string) (<-chan StreamChunk, error) {
	c, mc, err := r.resolve(purpose)
	if err != nil {
		return nil, err
	}
	return c.CompleteStreamWithParams(ctx, promptFile, vars, mc)
}

func (r *RoutingClient) ModelForPurpose(purpose string) (ModelParams, error) {
	_, mc, err := r.resolve(purpose)
	return mc, err
}

func (r *RoutingClient) ClientForProvider(providerID string) (*OpenAIClient, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[providerID]
	return c, ok
}
