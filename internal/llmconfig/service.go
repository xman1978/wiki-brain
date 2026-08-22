package llmconfig

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

type Service struct {
	store  *Store
	router *llm.RoutingClient
}

func NewService(store *Store, router *llm.RoutingClient) *Service {
	return &Service{store: store, router: router}
}

func (s *Service) ReloadRouter(ctx context.Context) error {
	_ = ctx
	providers, err := s.store.ListProviders()
	if err != nil {
		return err
	}
	bindings, err := s.store.ListBindings()
	if err != nil {
		return err
	}
	snap := llm.RouteSnapshot{
		Providers: make(map[string]*llm.ProviderRuntime, len(providers)),
	}
	for _, p := range providers {
		snap.Providers[p.ProviderID] = ProviderToRuntime(p)
	}
	snap = BindingsToRouteSnapshot(bindings, snap.Providers)
	return s.router.Reload(snap)
}

func (s *Service) BootstrapFromYAML(bootstrap *config.BootstrapLLM) error {
	if bootstrap == nil {
		return nil
	}
	n, err := s.store.CountProviders()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	platform := InferPlatform(bootstrap.BaseURL)
	p := Provider{
		ProviderID:     uuid.NewString(),
		Name:           "默认",
		Platform:       platform,
		BaseURL:        bootstrap.BaseURL,
		APIKey:         bootstrap.APIKey,
		TimeoutSeconds: bootstrap.TimeoutSeconds,
		MaxRetries:     bootstrap.MaxRetries,
		Models:         map[string]llm.ModelParams{},
	}
	if p.TimeoutSeconds <= 0 {
		p.TimeoutSeconds = 120
	}
	if p.MaxRetries < 0 {
		p.MaxRetries = 3
	}
	if err := ValidateProvider(&p); err != nil {
		slog.Error("llm bootstrap from config.yml failed", "error", err)
		return nil
	}
	if err := s.store.InsertProvider(p); err != nil {
		return err
	}
	bindings := make(map[string]PurposeBinding)
	for _, purpose := range PurposeList {
		mc := llm.ModelParams{}
		if m, ok := bootstrap.Models[purpose]; ok {
			mc = llm.ModelParams{
				Model:           m.Model,
				Temperature:     m.Temperature,
				MaxInputTokens:  m.MaxInputTokens,
				MaxOutputTokens: m.MaxOutputTokens,
				EnableThink:     m.Thinking,
			}
		} else if m, ok := bootstrap.Models["default"]; ok {
			mc = llm.ModelParams{
				Model:           m.Model,
				Temperature:     m.Temperature,
				MaxInputTokens:  m.MaxInputTokens,
				MaxOutputTokens: m.MaxOutputTokens,
				EnableThink:     m.Thinking,
			}
		}
		bindings[purpose] = PurposeBinding{ProviderID: p.ProviderID, ModelParams: mc}
	}
	if err := s.store.SetBindings(bindings); err != nil {
		return err
	}
	slog.Info("llm: imported provider from config.yml", "provider_id", p.ProviderID, "platform", platform)
	return s.ReloadRouter(context.Background())
}

func SnapshotFromBootstrap(b *config.BootstrapLLM) llm.RouteSnapshot {
	empty := llm.RouteSnapshot{
		Routes:    map[string]llm.PurposeRoute{},
		Providers: map[string]*llm.ProviderRuntime{},
	}
	if b == nil {
		return empty
	}
	models := make(map[string]llm.ModelParams)
	for k, m := range b.Models {
		models[k] = llm.ModelParams{
			Model:           m.Model,
			Temperature:     m.Temperature,
			MaxInputTokens:  m.MaxInputTokens,
			MaxOutputTokens: m.MaxOutputTokens,
			EnableThink:     m.Thinking,
		}
	}
	id := "bootstrap"
	rt := &llm.ProviderRuntime{
		ProviderID:     id,
		Platform:       InferPlatform(b.BaseURL),
		BaseURL:        b.BaseURL,
		APIKey:         b.APIKey,
		TimeoutSeconds: b.TimeoutSeconds,
		MaxRetries:     b.MaxRetries,
		ResponseFormat: llm.ResponseFormatJSONObject,
		Models:         map[string]llm.ModelParams{},
	}
	if rt.TimeoutSeconds <= 0 {
		rt.TimeoutSeconds = 120
	}
	routes := make(map[string]llm.PurposeRoute)
	for _, purpose := range PurposeList {
		mc := models[purpose]
		if mc.Model == "" {
			mc = models["default"]
		}
		routes[purpose] = llm.PurposeRoute{ProviderID: id, ModelParams: mc}
	}
	return llm.RouteSnapshot{Routes: routes, Providers: map[string]*llm.ProviderRuntime{id: rt}}
}

func NewRoutingFromBootstrap(b *config.BootstrapLLM, promptsDir string) (*llm.RoutingClient, error) {
	r := llm.NewRoutingClient(promptsDir)
	if err := r.Reload(SnapshotFromBootstrap(b)); err != nil {
		return nil, err
	}
	return r, nil
}

func InferPlatform(baseURL string) llm.Platform {
	u := strings.ToLower(baseURL)
	switch {
	case strings.Contains(u, "dashscope"):
		return llm.PlatformDashScope
	case strings.Contains(u, "volces"), strings.Contains(u, "doubao"):
		return llm.PlatformDoubao
	case strings.Contains(u, "bigmodel"), strings.Contains(u, "zhipu"):
		return llm.PlatformZhipu
	case strings.Contains(u, "moonshot"):
		return llm.PlatformKimi
	case strings.Contains(u, "deepseek"):
		return llm.PlatformDeepSeek
	case strings.Contains(u, "11434"), strings.Contains(u, "ollama"):
		return llm.PlatformOllama
	default:
		return llm.PlatformOpenAICompatible
	}
}

func (s *Service) ListProviders() ([]Provider, error) {
	return s.store.ListProviders()
}

func (s *Service) GetProvider(id string) (Provider, error) {
	return s.store.GetProvider(id)
}

func (s *Service) CreateProvider(p Provider) (Provider, error) {
	if p.ProviderID == "" {
		p.ProviderID = uuid.NewString()
	}
	if err := ValidateProvider(&p); err != nil {
		return Provider{}, err
	}
	p.Models = map[string]llm.ModelParams{}
	if err := s.store.InsertProvider(p); err != nil {
		return Provider{}, err
	}
	if err := s.ReloadRouter(context.Background()); err != nil {
		return Provider{}, err
	}
	return s.store.GetProvider(p.ProviderID)
}

// MaskedAPIKey is what the API returns in place of a provider's real key, and
// what the update form re-submits unchanged when the user never touched the
// field — UpdateProvider treats it as "keep the existing key" rather than
// overwriting the stored key with the literal mask string.
const MaskedAPIKey = "********"

func (s *Service) UpdateProvider(p Provider) (Provider, error) {
	if p.APIKey == MaskedAPIKey {
		existing, err := s.store.GetProvider(p.ProviderID)
		if err != nil {
			return Provider{}, err
		}
		p.APIKey = existing.APIKey
	}
	if err := ValidateProvider(&p); err != nil {
		return Provider{}, err
	}
	p.Models = map[string]llm.ModelParams{}
	if err := s.store.UpdateProvider(p); err != nil {
		return Provider{}, err
	}
	if err := s.ReloadRouter(context.Background()); err != nil {
		return Provider{}, err
	}
	return s.store.GetProvider(p.ProviderID)
}

func (s *Service) DeleteProvider(id string) error {
	if err := s.store.DeleteProvider(id); err != nil {
		return err
	}
	return s.ReloadRouter(context.Background())
}

func (s *Service) GetBindings() (map[string]PurposeBinding, error) {
	return s.store.ListBindings()
}

func (s *Service) SetBindings(bindings map[string]PurposeBinding) error {
	for purpose, b := range bindings {
		if err := ValidatePurposeBinding(&b); err != nil {
			return fmt.Errorf("purpose %s: %w", purpose, err)
		}
		if _, err := s.store.GetProvider(b.ProviderID); err != nil {
			return err
		}
	}
	if err := s.store.SetBindings(bindings); err != nil {
		return err
	}
	return s.ReloadRouter(context.Background())
}

func (s *Service) TestProvider(ctx context.Context, id string) (int, error) {
	c, ok := s.router.ClientForProvider(id)
	if !ok {
		return 0, ErrNotFound
	}
	models, err := c.ListModels(ctx)
	if err != nil {
		return 0, err
	}
	return len(models), nil
}

func (s *Service) ListProviderModels(ctx context.Context, id string) ([]string, error) {
	c, ok := s.router.ClientForProvider(id)
	if !ok {
		return nil, ErrNotFound
	}
	return c.ListModels(ctx)
}

type DiscoverModelsRequest struct {
	BaseURL  string       `json:"base_url"`
	APIKey   string       `json:"api_key"`
	Platform llm.Platform `json:"platform"`
}

func (s *Service) DiscoverModels(ctx context.Context, req DiscoverModelsRequest) ([]string, error) {
	rt := &llm.ProviderRuntime{
		BaseURL:        strings.TrimSpace(req.BaseURL),
		APIKey:         req.APIKey,
		Platform:       req.Platform,
		TimeoutSeconds: 120,
	}
	if !rt.Platform.Valid() {
		rt.Platform = llm.PlatformOpenAICompatible
	}
	return llm.ListModelsForRuntime(ctx, rt)
}
