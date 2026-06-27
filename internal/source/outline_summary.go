package source

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

const (
	// 每个 rune 约消耗的 token 数（中文偏高，取保守估计）
	runesPerToken = 1.5
	// prompt 模板本身预留的 token 数
	promptOverheadTokens = 300
)

const outlineSummarySchemaExample = `{
  "summaries": [
    {"id": "outline-id-1", "summary": "关键词1,关键词2,关键词3"}
  ]
}`

type outlineSummaryOutput struct {
	Summaries []struct {
		ID      string `json:"id"`
		Summary string `json:"summary"`
	} `json:"summaries"`
}

// GenerateOutlineSummaries 为结构 outline 节点批量生成关键词 summary。
// 按模型 max_input_tokens 分批，每批一次 LLM 调用。
func GenerateOutlineSummaries(ctx context.Context, client llm.LLMClient, outlines []Outline, content string, mc config.ModelConfig) {
	lines := strings.Split(content, "\n")

	// 筛选需要生成 summary 的节点（structural 且 summary 为空）
	var targets []Outline
	for _, o := range outlines {
		if !o.Summary.Valid || strings.TrimSpace(o.Summary.String) == "" {
			targets = append(targets, o)
		}
	}
	if len(targets) == 0 {
		return
	}

	// 为每个节点准备完整内容（语义 outline 已保证叶节点 ≤ segment_max_chars）
	type nodeSnippet struct {
		outline  Outline
		text     string
		runeLen  int
	}

	var snippets []nodeSnippet
	for _, o := range targets {
		text := extractLineRange(lines, o.LineStart, o.LineEnd)
		snippets = append(snippets, nodeSnippet{
			outline: o,
			text:    text,
			runeLen: RuneCount(text),
		})
	}

	// 按 max_input_tokens 分批
	maxInputTokens := mc.MaxInputTokens
	if maxInputTokens <= 0 {
		maxInputTokens = 4096
	}
	availableTokens := maxInputTokens - promptOverheadTokens

	var batches [][]nodeSnippet
	var currentBatch []nodeSnippet
	currentTokens := 0

	for _, s := range snippets {
		// 标题 + 内容片段的估算 token 数
		nodeTokens := int(float64(RuneCount(s.outline.Title)+s.runeLen) / runesPerToken)
		if nodeTokens < 10 {
			nodeTokens = 10
		}

		if len(currentBatch) > 0 && currentTokens+nodeTokens > availableTokens {
			batches = append(batches, currentBatch)
			currentBatch = nil
			currentTokens = 0
		}
		currentBatch = append(currentBatch, s)
		currentTokens += nodeTokens
	}
	if len(currentBatch) > 0 {
		batches = append(batches, currentBatch)
	}

	slog.Info("outline summary generation", "total_nodes", len(targets), "batches", len(batches))

	// 逐批调用 LLM
	results := make(map[string]string) // outline_id -> summary

	for i, batch := range batches {
		var sb strings.Builder
		for _, s := range batch {
			fmt.Fprintf(&sb, "[%s] %s\n%s\n\n", s.outline.OutlineID, s.outline.Title, s.text)
		}

		data, err := client.CompleteJSON(ctx, "outline_summary.md", map[string]string{
			"sections":    sb.String(),
			"json_schema": outlineSummarySchemaExample,
		}, "extraction")
		if err != nil {
			slog.Warn("outline summary batch failed", "batch", i+1, "error", err)
			continue
		}

		var output outlineSummaryOutput
		if err := json.Unmarshal(data, &output); err != nil {
			slog.Warn("outline summary parse failed", "batch", i+1, "error", err)
			continue
		}

		for _, s := range output.Summaries {
			if s.ID != "" && s.Summary != "" {
				results[s.ID] = normalizeSummary(s.Summary)
			}
		}
	}

	// 回写到 outlines
	for i := range outlines {
		if summary, ok := results[outlines[i].OutlineID]; ok {
			outlines[i].Summary = sql.NullString{String: summary, Valid: true}
		}
	}

	slog.Info("outline summary completed", "generated", len(results), "total", len(targets))
}
