package source

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

const (
	// 每个 rune 约消耗的 token 数（中文偏高，取保守估计）
	runesPerToken = 1.5
	// prompt 模板本身预留的 token 数
	promptOverheadTokens = 300
	// outlineSummaryConcurrencyDefault is used whenever the caller passes a
	// concurrency <= 0.
	outlineSummaryConcurrencyDefault = 2
)

type outlineSummaryOutput struct {
	Summaries []struct {
		ID      string `json:"id"`
		Summary string `json:"summary"`
	} `json:"summaries"`
}

// outlineSummaryMaxRounds bounds the retry-on-omission loop in
// GenerateOutlineSummaries: 1 initial pass + this many retries. A retried
// node is always resent alone (batch size 1), which both maximizes the
// model's attention on it and rules out "dropped while sharing a batch with
// other nodes" as the failure mode a second time.
const outlineSummaryMaxRounds = 2

type nodeSnippet struct {
	outline Outline
	text    string
	runeLen int
}

// GenerateOutlineSummaries 为结构 outline 节点批量生成关键词 summary。
// 按模型 max_input_tokens 分批，批次之间互不依赖，并发跑（concurrency <= 0
// 时用 outlineSummaryConcurrencyDefault）。
//
// LLM 批量返回的 JSON 有时会漏掉批次里的某一个 id（不是报错，只是那一条没生成），
// 此时该节点 summary 会被静默留空、且没有任何单条告警（历史案例见
// docs/impl/v1/retrieval.md 目录检索一节）。这里在第一轮批量完成后，对仍缺
// summary 的节点单独（每节点一次调用）重试最多 outlineSummaryMaxRounds-1
// 次；仍失败的节点会打印 slog.Warn 并保持空，交由后续人工/RegenerateOutlineSummary
// 处理，不在这里无限重试。
func GenerateOutlineSummaries(ctx context.Context, client llm.LLMClient, outlines []Outline, content string, mc llm.ModelParams, concurrency int) {
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

	if concurrency <= 0 {
		concurrency = outlineSummaryConcurrencyDefault
	}
	maxInputTokens := mc.MaxInputTokens
	if maxInputTokens <= 0 {
		maxInputTokens = 4096
	}
	availableTokens := maxInputTokens - promptOverheadTokens

	allResults := make(map[string]string) // outline_id -> summary, across all rounds
	round := 0
	for {
		round++
		var snippets []nodeSnippet
		for _, o := range targets {
			if _, done := allResults[o.OutlineID]; done {
				continue
			}
			text := extractLineRange(lines, o.LineStart, o.LineEnd)
			snippets = append(snippets, nodeSnippet{outline: o, text: text, runeLen: RuneCount(text)})
		}
		if len(snippets) == 0 {
			break
		}

		// 第一轮按 token 预算正常分批；重试轮次每节点单独一批，见上方注释。
		var batches [][]nodeSnippet
		if round == 1 {
			batches = batchSnippets(snippets, availableTokens)
		} else {
			for _, s := range snippets {
				batches = append(batches, []nodeSnippet{s})
			}
		}

		slog.Info("outline summary generation", "round", round, "nodes", len(snippets), "batches", len(batches))
		roundResults := runOutlineSummaryBatches(ctx, client, batches, concurrency)
		for id, summary := range roundResults {
			allResults[id] = summary
		}

		if round >= outlineSummaryMaxRounds {
			break
		}
	}

	// 回写到 outlines
	for i := range outlines {
		if summary, ok := allResults[outlines[i].OutlineID]; ok {
			outlines[i].Summary = sql.NullString{String: summary, Valid: true}
		}
	}

	for _, o := range targets {
		if _, ok := allResults[o.OutlineID]; !ok {
			slog.Warn("outline summary still missing after retries", "outline_id", o.OutlineID, "title", o.Title)
		}
	}

	slog.Info("outline summary completed", "generated", len(allResults), "total", len(targets))
}

func batchSnippets(snippets []nodeSnippet, availableTokens int) [][]nodeSnippet {
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
	return batches
}

// runOutlineSummaryBatches 跑一轮批次（批次彼此独立，并发跑；只有写 results
// 这一步需要互斥），返回这一轮拿到的 outline_id -> summary。
func runOutlineSummaryBatches(ctx context.Context, client llm.LLMClient, batches [][]nodeSnippet, concurrency int) map[string]string {
	results := make(map[string]string)
	var mu sync.Mutex
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, batch := range batches {
		i, batch := i, batch
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			var sb strings.Builder
			for _, s := range batch {
				fmt.Fprintf(&sb, "[%s] %s\n%s\n\n", s.outline.OutlineID, s.outline.Title, s.text)
			}

			data, err := client.CompleteJSON(ctx, "outline_summary.md", map[string]string{
				"sections": sb.String(),
			}, "extraction")
			if err != nil {
				slog.Warn("outline summary batch failed", "batch", i+1, "error", err)
				return
			}

			var output outlineSummaryOutput
			if err := json.Unmarshal(data, &output); err != nil {
				slog.Warn("outline summary parse failed", "batch", i+1, "error", err)
				return
			}

			mu.Lock()
			defer mu.Unlock()
			for _, s := range output.Summaries {
				if s.ID != "" && s.Summary != "" {
					results[s.ID] = normalizeSummary(s.Summary)
				}
			}
		}()
	}
	wg.Wait()
	return results
}
