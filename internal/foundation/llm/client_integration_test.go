//go:build integration

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
)

func loadTestConfig(t *testing.T) *config.Config {
	t.Helper()
	// 集成测试需要从项目根加载 config
	cfg, err := config.Load("../../../config/config.yml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func TestOpenAIClient_Complete(t *testing.T) {
	cfg := loadTestConfig(t)
	client, err := NewOpenAIClient(&cfg.LLM, "../../../config/prompts")
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}

	result, err := client.Complete(context.Background(), "source_summary.md", map[string]string{
		"title":              "Go 编程入门",
		"top_outline_titles": "变量\n函数\n并发",
		"first_paragraph":    "Go 是一门由 Google 开发的编程语言，以简洁高效著称。",
	}, "default")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	t.Logf("Summary: %s", result)
	if len(result) == 0 {
		t.Error("empty result")
	}
}

func TestOpenAIClient_CompleteJSON(t *testing.T) {
	cfg := loadTestConfig(t)
	client, err := NewOpenAIClient(&cfg.LLM, "../../../config/prompts")
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}

	data, err := client.CompleteJSON(context.Background(), "source_domain_match.md", map[string]string{
		"title":       "Kubernetes 部署指南",
		"summary":     "介绍 Kubernetes 集群部署和配置管理",
		"domain_list": "[software-engineering] 软件工程：软件设计、开发、部署和运维\n[finance] 金融：投资、风控、财务管理",
	}, "default")
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	t.Logf("Domain match result: %s", string(data))
	if _, ok := result["domain_id"]; !ok {
		t.Error("missing domain_id field")
	}
}

func TestOpenAIClient_CompleteJSON_SemanticOutline(t *testing.T) {
	cfg := loadTestConfig(t)
	client, err := NewOpenAIClient(&cfg.LLM, "../../../config/prompts")
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}

	content := `公司全体：
公司现提倡以完成工作目标为主的管理理念，为此简化考勤管理。
1、关于打卡时间
坐班岗位上班打卡时间晚于9：00，在9：30前的不记迟到。
2、关于出差
简化出差申请流程，员工提交后即可生效。
3、关于加班
公司不提倡非必要的加班。加班申请只适用于公休日和节假日。
4、关于考勤奖惩
取消满勤奖及迟到、早退罚款等内容。
5、关于考勤记录
人力行政部不再单独发布考勤统计情况。`

	lines := strings.Split(content, "\n")
	jsonSchema := `{
  "sections": [
    {"title": "章节标题", "summary": "关键词1 关键词2 关键词3", "line_start": 1, "line_end": 42, "level": 1}
  ]
}`

	data, err := client.CompleteJSON(context.Background(), "outline_semantic_full.md", map[string]string{
		"json_schema":      jsonSchema,
		"total_lines":      fmt.Sprintf("%d", len(lines)),
		"document_content": content,
	}, "extraction")
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}

	t.Logf("Semantic outline: %s", string(data))

	var result struct {
		Sections []struct {
			Title     string `json:"title"`
			LineStart int    `json:"line_start"`
			LineEnd   int    `json:"line_end"`
			Level     int    `json:"level"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Sections) == 0 {
		t.Error("no sections returned")
	}
	for _, s := range result.Sections {
		t.Logf("  [L%d] %s (%d-%d)", s.Level, s.Title, s.LineStart, s.LineEnd)
	}
}
