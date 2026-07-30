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

	if cfg.BootstrapLLM == nil {
		t.Fatal("expected BootstrapLLM from llm section")
	}
	if cfg.BootstrapLLM.BaseURL != "http://localhost:11434" {
		t.Errorf("base_url = %q", cfg.BootstrapLLM.BaseURL)
	}
	if cfg.BootstrapLLM.TimeoutSeconds != 60 {
		t.Errorf("timeout_seconds = %d, want 60", cfg.BootstrapLLM.TimeoutSeconds)
	}
	if cfg.BootstrapLLM.MaxRetries != 2 {
		t.Errorf("max_retries = %d, want 2", cfg.BootstrapLLM.MaxRetries)
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
	if cfg.BootstrapLLM == nil || cfg.BootstrapLLM.BaseURL != "http://test" {
		t.Errorf("bootstrap base_url = %v", cfg.BootstrapLLM)
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
