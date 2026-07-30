package llm

import "time"

// Platform identifies how enable_think is serialized in chat completion requests.
type Platform string

const (
	PlatformDashScope        Platform = "dashscope"
	PlatformDoubao           Platform = "doubao"
	PlatformZhipu            Platform = "zhipu"
	PlatformKimi             Platform = "kimi"
	PlatformDeepSeek         Platform = "deepseek"
	PlatformVLLM             Platform = "vllm"
	PlatformOllama           Platform = "ollama"
	PlatformOpenAICompatible Platform = "openai_compatible"
)

const (
	ResponseFormatJSONObject = "json_object"
	ResponseFormatJSONSchema = "json_schema"
)

var AllPlatforms = []Platform{
	PlatformDashScope,
	PlatformDoubao,
	PlatformZhipu,
	PlatformKimi,
	PlatformDeepSeek,
	PlatformVLLM,
	PlatformOllama,
	PlatformOpenAICompatible,
}

func (p Platform) Valid() bool {
	for _, v := range AllPlatforms {
		if p == v {
			return true
		}
	}
	return false
}

// ModelParams is per-purpose model settings stored on a provider.
type ModelParams struct {
	Model           string  `json:"model"`
	Temperature     float64 `json:"temperature"`
	MaxInputTokens  int     `json:"input_max_tokens"`
	MaxOutputTokens int     `json:"output_max_tokens"`
	EnableThink     bool    `json:"enable_think"`
}

// ProviderRuntime is the in-memory shape used by OpenAIClient.
type ProviderRuntime struct {
	ProviderID     string
	Platform       Platform
	BaseURL        string
	APIKey         string
	TimeoutSeconds int
	MaxRetries     int
	ResponseFormat string
	Models         map[string]ModelParams
}

func (p *ProviderRuntime) TimeoutDuration() time.Duration {
	if p.TimeoutSeconds > 0 {
		return time.Duration(p.TimeoutSeconds) * time.Second
	}
	return 120 * time.Second
}

func (p *ProviderRuntime) ModelForPurpose(purpose string) ModelParams {
	if p == nil || p.Models == nil {
		return ModelParams{}
	}
	if m, ok := p.Models[purpose]; ok && m.Model != "" {
		return m
	}
	if m, ok := p.Models["default"]; ok {
		return m
	}
	return ModelParams{}
}

// StaticPurposeModels supplies ModelForPurpose for tests without a database.
type StaticPurposeModels map[string]ModelParams

func (s StaticPurposeModels) ModelForPurpose(purpose string) (ModelParams, error) {
	if m, ok := s[purpose]; ok && m.Model != "" {
		return m, nil
	}
	if m, ok := s["default"]; ok {
		return m, nil
	}
	return ModelParams{}, ErrNotConfigured
}
