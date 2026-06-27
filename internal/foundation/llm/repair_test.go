package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
)

func TestCompleteJSON_RepairMissingField(t *testing.T) {
	// Simulate: first call returns JSON missing required "summary" field,
	// second call (repair) returns the fixed version.
	var callCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")

		if n == 1 {
			// First call: return JSON with missing "summary" in one section
			w.Write([]byte(`{"choices":[{"message":{"content":"{\"sections\":[{\"title\":\"Intro\",\"line_start\":1,\"line_end\":10,\"level\":1}]}"}}]}`))
		} else {
			// Repair call: return fixed JSON with summary added
			w.Write([]byte(`{"choices":[{"message":{"content":"{\"sections\":[{\"title\":\"Intro\",\"summary\":\"overview introduction\",\"line_start\":1,\"line_end\":10,\"level\":1}]}"}}]}`))
		}
	}))
	defer server.Close()

	// Create a temp prompt file with schema requiring "summary"
	tmpDir := t.TempDir()
	promptContent := `---
version: v1
---

## System

Test system.

## User

Test user: {{content}}

## Schema

` + "```json\n" + `{
  "type": "object",
  "required": ["sections"],
  "properties": {
    "sections": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["title", "summary", "line_start", "line_end", "level"],
        "properties": {
          "title": {"type": "string", "minLength": 1},
          "summary": {"type": "string"},
          "line_start": {"type": "integer", "minimum": 1},
          "line_end": {"type": "integer", "minimum": 1},
          "level": {"type": "integer", "minimum": 1, "maximum": 3}
        }
      }
    }
  }
}
` + "```"

	os.WriteFile(filepath.Join(tmpDir, "test_outline.md"), []byte(promptContent), 0644)

	cfg := &config.LLMConfig{
		BaseURL:        server.URL,
		APIKey:         "test-key",
		TimeoutSeconds: 30,
		MaxRetries:     0,
		Models: map[string]config.ModelConfig{
			"default": {Model: "test-model", Temperature: 0.2, MaxOutputTokens: 4096},
		},
	}

	client, err := NewOpenAIClient(cfg, tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	data, err := client.CompleteJSON(context.Background(), "test_outline.md", map[string]string{
		"content": "test",
	}, "default")
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}

	// Should have made 2 calls: original + repair
	if callCount.Load() != 2 {
		t.Errorf("call count = %d, want 2", callCount.Load())
	}

	// Result should contain the repaired "summary" field
	if !contains(string(data), "summary") {
		t.Errorf("repaired result should contain summary: %s", string(data))
	}
}

func TestCompleteJSON_NoRepairWhenValid(t *testing.T) {
	var callCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"sections\":[{\"title\":\"A\",\"summary\":\"k1 k2\",\"line_start\":1,\"line_end\":10,\"level\":1}]}"}}]}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	promptContent := `---
version: v1
---

## System

Test.

## User

{{content}}

## Schema

` + "```json\n" + `{
  "type": "object",
  "required": ["sections"],
  "properties": {
    "sections": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["title", "summary", "line_start", "line_end", "level"],
        "properties": {
          "title": {"type": "string"},
          "summary": {"type": "string"},
          "line_start": {"type": "integer"},
          "line_end": {"type": "integer"},
          "level": {"type": "integer"}
        }
      }
    }
  }
}
` + "```"

	os.WriteFile(filepath.Join(tmpDir, "valid.md"), []byte(promptContent), 0644)

	cfg := &config.LLMConfig{
		BaseURL:        server.URL,
		APIKey:         "test-key",
		TimeoutSeconds: 30,
		MaxRetries:     0,
		Models: map[string]config.ModelConfig{
			"default": {Model: "test-model", Temperature: 0.2, MaxOutputTokens: 4096},
		},
	}

	client, _ := NewOpenAIClient(cfg, tmpDir)
	_, err := client.CompleteJSON(context.Background(), "valid.md", map[string]string{"content": "test"}, "default")
	if err != nil {
		t.Fatalf("should succeed: %v", err)
	}

	// Only 1 call — no repair needed
	if callCount.Load() != 1 {
		t.Errorf("call count = %d, want 1 (no repair needed)", callCount.Load())
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
