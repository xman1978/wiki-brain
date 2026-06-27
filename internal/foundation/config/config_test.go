package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromExplicitPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")
	content := `
llm:
  base_url: "http://localhost:11434"
  api_key: "test-key"
  timeout_seconds: 60
  max_retries: 2
  models:
    default:
      model: "qwen3-30b"
      temperature: 0.2
      max_input_tokens: 4096
      max_output_tokens: 4096
    extraction:
      model: "qwen3-30b"
      temperature: 0
      max_input_tokens: 4096
      max_output_tokens: 4096
server:
  port: 9090
  read_timeout: "10s"
  write_timeout: "20s"
database:
  path: "data/test.db"
index:
  path: "data/searchindex"
queue:
  buffer_size: 50
source:
  upload_dir: "data/sources"
  segment_max_chars: 4000
  min_segment_chars: 400
retrieval:
  outline_fts_min_score: 0.5
  rerank_top_n: 20
study:
  schedule_interval: "1h"
  candidate_confident_min: 5
  candidate_ratio_min: 0.6
  wiki_kp_min: 4
  wiki_confident_min: 8
  gap_hit_threshold: 3
  scan_batch_size: 200
  report_period_days: 30
  report_max_keep: 10
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.LLM.BaseURL != "http://localhost:11434" {
		t.Errorf("base_url = %q", cfg.LLM.BaseURL)
	}
	if cfg.LLM.TimeoutSeconds != 60 {
		t.Errorf("timeout_seconds = %d, want 60", cfg.LLM.TimeoutSeconds)
	}
	if cfg.LLM.MaxRetries != 2 {
		t.Errorf("max_retries = %d, want 2", cfg.LLM.MaxRetries)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("port = %d, want 9090", cfg.Server.Port)
	}
	if cfg.Queue.BufferSize != 50 {
		t.Errorf("buffer_size = %d, want 50", cfg.Queue.BufferSize)
	}
	if cfg.Study.CandidateRatioMin != 0.6 {
		t.Errorf("candidate_ratio_min = %f, want 0.6", cfg.Study.CandidateRatioMin)
	}
}

func TestLoadFileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/config.yml")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestEnvConfigPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "env-config.yml")
	content := `
llm:
  base_url: "http://test"
  api_key: "k"
  timeout_seconds: 30
  models:
    default:
      model: "m"
server:
  port: 1234
database:
  path: "test.db"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WIKI_CONFIG_PATH", cfgPath)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.LLM.BaseURL != "http://test" {
		t.Errorf("base_url = %q", cfg.LLM.BaseURL)
	}
}

func TestEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")
	content := `
llm:
  base_url: "http://old"
  api_key: "k"
  timeout_seconds: 30
  models:
    default:
      model: "m"
server:
  port: 8080
database:
  path: "old.db"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WB_SERVER_PORT", "9999")
	t.Setenv("WB_DATABASE_PATH", "new.db")

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Server.Port != 9999 {
		t.Errorf("port = %d, want 9999", cfg.Server.Port)
	}
	if cfg.Database.Path != "new.db" {
		t.Errorf("db path = %q, want new.db", cfg.Database.Path)
	}
}

func TestAPIKeyResolveFromEnv(t *testing.T) {
	t.Setenv("MY_SECRET_KEY", "resolved-value")

	cfg := &LLMConfig{APIKey: "MY_SECRET_KEY"}
	if got := cfg.ResolvedAPIKey(); got != "resolved-value" {
		t.Errorf("ResolvedAPIKey = %q, want resolved-value", got)
	}
}

func TestAPIKeyDirectValue(t *testing.T) {
	cfg := &LLMConfig{APIKey: "sk-direct-key-12345"}
	if got := cfg.ResolvedAPIKey(); got != "sk-direct-key-12345" {
		t.Errorf("ResolvedAPIKey = %q, want sk-direct-key-12345", got)
	}
}

func TestModelForPurpose(t *testing.T) {
	cfg := &LLMConfig{
		Models: map[string]ModelConfig{
			"default":    {Model: "default-m", Temperature: 0.2, MaxOutputTokens: 4096},
			"extraction": {Model: "extract-m", Temperature: 0, MaxOutputTokens: 4096},
		},
	}

	mc := cfg.ModelForPurpose("extraction")
	if mc.Model != "extract-m" {
		t.Errorf("extraction model = %q, want extract-m", mc.Model)
	}
	if mc.Temperature != 0 {
		t.Errorf("extraction temperature = %f, want 0", mc.Temperature)
	}

	mc = cfg.ModelForPurpose("reasoning")
	if mc.Model != "default-m" {
		t.Errorf("reasoning (fallback) model = %q, want default-m", mc.Model)
	}

	mc = cfg.ModelForPurpose("default")
	if mc.Model != "default-m" {
		t.Errorf("default model = %q, want default-m", mc.Model)
	}
}

func TestTimeoutDuration(t *testing.T) {
	cfg := &LLMConfig{TimeoutSeconds: 120}
	if got := cfg.TimeoutDuration().Seconds(); got != 120 {
		t.Errorf("timeout = %f, want 120", got)
	}

	cfg2 := &LLMConfig{}
	if got := cfg2.TimeoutDuration().Seconds(); got != 120 {
		t.Errorf("default timeout = %f, want 120", got)
	}
}
