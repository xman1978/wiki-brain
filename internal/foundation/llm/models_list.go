package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
)

type modelsListResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

type ollamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// ListModels returns model IDs from the provider's list-models API.
func (c *OpenAIClient) ListModels(ctx context.Context) ([]string, error) {
	if c.provider.Platform == PlatformOllama {
		return c.listOllamaModels(ctx)
	}
	return c.listOpenAIModels(ctx)
}

func (c *OpenAIClient) listOpenAIModels(ctx context.Context) ([]string, error) {
	url := strings.TrimRight(c.provider.BaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if c.provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.provider.APIKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: list models: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: list models status %d: %s", ErrModelError, resp.StatusCode, truncate(string(body), 200))
	}

	var parsed modelsListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("llm: decode models list: %w", err)
	}
	ids := make([]string, 0, len(parsed.Data))
	for _, item := range parsed.Data {
		if item.ID != "" {
			ids = append(ids, item.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (c *OpenAIClient) listOllamaModels(ctx context.Context) ([]string, error) {
	base := strings.TrimRight(c.provider.BaseURL, "/")
	base = strings.TrimSuffix(base, "/v1")
	url := base + "/api/tags"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if c.provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.provider.APIKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if os.IsTimeout(err) {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("llm: ollama tags: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// Fall back to OpenAI-compatible /v1/models
		return c.listOpenAIModels(ctx)
	}

	var parsed ollamaTagsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return c.listOpenAIModels(ctx)
	}
	names := make([]string, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		if m.Name != "" {
			names = append(names, m.Name)
		}
	}
	if len(names) == 0 {
		return c.listOpenAIModels(ctx)
	}
	sort.Strings(names)
	return names, nil
}

// ListModelsForRuntime lists models using a transient provider config (unsaved form).
func ListModelsForRuntime(ctx context.Context, rt *ProviderRuntime) ([]string, error) {
	if rt == nil || strings.TrimSpace(rt.BaseURL) == "" {
		return nil, fmt.Errorf("llm: base_url required")
	}
	if rt.TimeoutSeconds <= 0 {
		rt.TimeoutSeconds = 120
	}
	c, err := NewOpenAIClient(rt, "")
	if err != nil {
		return nil, err
	}
	return c.ListModels(ctx)
}
