//go:build integration

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/llmconfig"
)

func loadTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("../../../config/config.yml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func testOpenAIClient(t *testing.T, cfg *config.Config) *OpenAIClient {
	t.Helper()
	snap := llmconfig.SnapshotFromBootstrap(cfg.BootstrapLLM)
	rt := snap.Providers["bootstrap"]
	if rt == nil {
		t.Fatal("no bootstrap LLM in config.yml")
	}
	client, err := NewOpenAIClient(rt, "../../../config/prompts")
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}
	return client
}

func TestOpenAIClient_Complete(t *testing.T) {
	cfg := loadTestConfig(t)
	client := testOpenAIClient(t, cfg)

	result, err := client.Complete(context.Background(), "source_summary.md", map[string]string{
		"title":              "Go 编程入门",
		"top_outline_titles": "变量\n函数\n并发",
		"first_paragraph":    "Go 是一门由 Google 开发的编程语言，以简洁高效著称。",
	}, "default")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if strings.TrimSpace(result) == "" {
		t.Fatal("empty result")
	}
	t.Logf("result: %s", result)
}

func TestOpenAIClient_CompleteJSON(t *testing.T) {
	cfg := loadTestConfig(t)
	client := testOpenAIClient(t, cfg)

	data, err := client.CompleteJSON(context.Background(), "source_domain_match.md", map[string]string{
		"title":       "测试",
		"summary":     "摘要",
		"domain_list": "[it] IT：信息技术",
	}, "classification")
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid json: %v\nraw: %s", err, data)
	}
	t.Logf("parsed: %v", parsed)
}

func TestOpenAIClient_CompleteStream(t *testing.T) {
	cfg := loadTestConfig(t)
	client := testOpenAIClient(t, cfg)

	ch, err := client.CompleteStream(context.Background(), "source_summary.md", map[string]string{
		"title":              "流式测试",
		"top_outline_titles": "A",
		"first_paragraph":    "测试段落",
	}, "default")
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	var parts []string
	for chunk := range ch {
		switch chunk.Type {
		case ChunkError:
			t.Fatalf("stream error: %v", chunk.Err)
		case ChunkContent:
			parts = append(parts, chunk.Content)
		case ChunkDone:
			goto done
		}
	}
done:
	if len(parts) == 0 {
		t.Fatal("no content chunks")
	}
	fmt.Println(strings.Join(parts, ""))
}
