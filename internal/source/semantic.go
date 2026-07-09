package source

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

const jsonSchemaExample = `{
  "sections": [
    {"title": "章节标题", "summary": "关键词1,关键词2,关键词3", "line_start": 1, "line_end": 42, "level": 1}
  ]
}`

const localSketchSchemaExample = `{
  "outline_units": [
    {"title": "章节标题", "summary": "关键词1,关键词2,关键词3", "line_start": 1, "line_end": 42}
  ],
  "starts_mid_section": false,
  "ends_mid_section": true,
  "start_topic": null,
  "end_topic": "下一章节主题"
}`

type llmSection struct {
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Level     int    `json:"level"`
}

type llmSectionsOutput struct {
	Sections []llmSection `json:"sections"`
}

type localSketchUnit struct {
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
}

type localSketchOutput struct {
	OutlineUnits     []localSketchUnit `json:"outline_units"`
	StartsMidSection bool              `json:"starts_mid_section"`
	EndsMidSection   bool              `json:"ends_mid_section"`
	StartTopic       *string           `json:"start_topic"`
	EndTopic         *string           `json:"end_topic"`
}

// GenerateSemanticOutlines 生成语义 outline。
// maxInputTokens 决定单次 LLM 调用的输入上限：
// - 全文 token 数 ≤ 可用 token 数 → 单遍
// - 超出 → 分窗口提取局部草图 + 全局合并
func GenerateSemanticOutlines(ctx context.Context, client llm.LLMClient, sourceID, content string, mc config.ModelConfig, segmentMaxChars int) ([]Outline, error) {
	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	totalRunes := RuneCount(content)

	usableTokens := estimateUsableInputTokens(mc.MaxInputTokens)
	singlePassMaxRunes := int(float64(usableTokens) * runesPerToken)

	if totalRunes <= singlePassMaxRunes {
		return singlePassOutline(ctx, client, sourceID, content)
	}

	slog.Info("semantic outline: using hierarchical compression",
		"source_id", sourceID, "total_runes", totalRunes, "single_pass_max", singlePassMaxRunes)

	// 第一阶段：分窗口提取局部结构草图
	sketches, err := extractLocalSketches(ctx, client, lines, totalLines, usableTokens)
	if err != nil {
		return nil, fmt.Errorf("extract local sketches: %w", err)
	}

	// 第二阶段：全局合并
	outlines, err := mergeSketchesToOutline(ctx, client, sourceID, sketches, totalLines, usableTokens)
	if err != nil {
		return nil, fmt.Errorf("merge sketches: %w", err)
	}

	// 第三阶段：清理仍超长的叶节点（splitOversizeLeaf 一次性切完，不需要递归）
	result := refineOversizeLeaves(ctx, client, sourceID, lines, outlines, segmentMaxChars, mc)
	return result, nil
}

// refineOversizeLeaves 把 outlines 里内容仍超过 segmentMaxChars 的叶节点，
// 通过 splitOversizeLeaf 切成语义子节点。不需要递归：splitOversizeLeaf 切出
// 的每个区块本身已经 ≤ segmentMaxChars（唯一例外是单个超限的不可分割元素，
// 按"不可分割元素即使超长也不切"的要求，这个例外就是最终结果，不再进一步
// 处理）。
func refineOversizeLeaves(ctx context.Context, client llm.LLMClient, sourceID string, lines []string, outlines []Outline, segmentMaxChars int, mc config.ModelConfig) []Outline {
	var result []Outline

	for i := range outlines {
		o := &outlines[i]
		nodeContent := extractLineRange(lines, o.LineStart, o.LineEnd)
		isLeaf := !hasChildren(outlines, o.OutlineID)

		if isLeaf && RuneCount(nodeContent) > segmentMaxChars {
			children := splitOversizeLeaf(ctx, client, sourceID, lines, o, segmentMaxChars, mc)
			if len(children) > 0 {
				for j := range children {
					children[j].ParentID = sql.NullString{String: o.OutlineID, Valid: true}
				}
				result = append(result, *o)
				result = append(result, children...)
				continue
			}
		}
		result = append(result, *o)
	}

	return result
}

const outlineLeafTitlesSchemaExample = `{
  "titles": [
    {"index": 1, "title": "章节标题"}
  ]
}`

type leafTitleResult struct {
	Index int    `json:"index"`
	Title string `json:"title"`
}

type leafTitlesOutput struct {
	Titles []leafTitleResult `json:"titles"`
}

// splitOversizeLeaf deterministically partitions an oversized leaf's content
// into windows that jointly tile [leaf.LineStart, leaf.LineEnd] with zero
// overlap (splitWindowsByMarkdown with avoidCuttingIndivisible=true
// guarantees this — see markdown_split.go), then asks the model only for a
// title per window. The model never reports a line range here, so it cannot
// violate the non-overlap/full-coverage invariant the old design asked it to
// maintain on its own (docs/impl/mvp/source.md 6.5).
//
// Returns nil if the leaf can't be usefully split further (a single window
// covering the whole range — e.g. one giant indivisible table), so the
// caller keeps the original leaf as-is.
func splitOversizeLeaf(ctx context.Context, client llm.LLMClient, sourceID string, lines []string, leaf *Outline, segmentMaxChars int, mc config.ModelConfig) []Outline {
	nodeLines := lines[leaf.LineStart-1 : leaf.LineEnd]
	windows := splitWindowsByMarkdown(nodeLines, segmentMaxChars, 0, true)
	if len(windows) <= 1 {
		return nil
	}

	children := make([]Outline, len(windows))
	for i, w := range windows {
		children[i] = Outline{
			OutlineID: uuid.New().String(),
			SourceID:  sourceID,
			Level:     leaf.Level + 1,
			LineStart: leaf.LineStart + w.StartLine - 1,
			LineEnd:   leaf.LineStart + w.EndLine - 1,
			NodeType:  "semantic",
		}
	}

	assignLeafTitles(ctx, client, sourceID, lines, children, mc)

	for i := range children {
		children[i].Position = i
	}

	slog.Info("split oversize leaf", "title", leaf.Title, "parts", len(children),
		"range", fmt.Sprintf("%d-%d", leaf.LineStart, leaf.LineEnd))
	return children
}

// assignLeafTitles batches children (line ranges already fixed by
// splitOversizeLeaf) into LLM calls sized to mc.MaxInputTokens — mirroring
// GenerateOutlineSummaries' batching in outline_summary.go — and asks only
// for a title per block, never a line range. A block whose title the model
// omits, or whose whole batch call fails, gets a fallback title derived from
// its own first line rather than being dropped: a labeling failure degrades
// to a plain title, it never loses the block.
func assignLeafTitles(ctx context.Context, client llm.LLMClient, sourceID string, lines []string, children []Outline, mc config.ModelConfig) {
	usableTokens := estimateUsableInputTokens(mc.MaxInputTokens)
	availableRunes := int(float64(usableTokens) * runesPerToken)

	type block struct {
		idx     int
		text    string
		runeLen int
	}
	blocks := make([]block, len(children))
	for i, c := range children {
		text := extractLineRange(lines, c.LineStart, c.LineEnd)
		blocks[i] = block{idx: i, text: text, runeLen: RuneCount(text)}
	}

	var batches [][]block
	var currentBatch []block
	currentRunes := 0
	for _, b := range blocks {
		if len(currentBatch) > 0 && currentRunes+b.runeLen > availableRunes {
			batches = append(batches, currentBatch)
			currentBatch = nil
			currentRunes = 0
		}
		currentBatch = append(currentBatch, b)
		currentRunes += b.runeLen
	}
	if len(currentBatch) > 0 {
		batches = append(batches, currentBatch)
	}

	for _, batch := range batches {
		var sb strings.Builder
		for _, b := range batch {
			fmt.Fprintf(&sb, "[%d]\n%s\n\n", b.idx+1, b.text)
		}

		data, err := client.CompleteJSON(ctx, "outline_semantic_chunk.md", map[string]string{
			"blocks":      sb.String(),
			"json_schema": outlineLeafTitlesSchemaExample,
		}, "extraction")
		if err != nil {
			slog.Warn("leaf title batch failed", "source_id", sourceID, "error", err)
			continue
		}

		var output leafTitlesOutput
		if err := json.Unmarshal(data, &output); err != nil {
			slog.Warn("leaf title batch parse failed", "source_id", sourceID, "error", err)
			continue
		}

		for _, t := range output.Titles {
			pos := t.Index - 1
			if pos < 0 || pos >= len(children) || strings.TrimSpace(t.Title) == "" {
				continue
			}
			children[pos].Title = t.Title
		}
	}

	for i := range children {
		if strings.TrimSpace(children[i].Title) == "" {
			children[i].Title = fallbackTitle(lines, children[i].LineStart)
		}
	}
}

// fallbackTitle derives a short title from a block's own first non-blank
// line when the labeling LLM call fails or omits it. Every leaf downstream
// (findLeaves in internal/unit, outline FTS) needs a usable Title, so a
// labeling failure must never leave one empty.
func fallbackTitle(lines []string, lineStart int) string {
	for i := lineStart; i <= len(lines); i++ {
		t := strings.TrimSpace(lines[i-1])
		if t != "" {
			r := []rune(t)
			if len(r) > 20 {
				return string(r[:20])
			}
			return t
		}
	}
	return "（内容片段）"
}

func hasChildren(outlines []Outline, parentID string) bool {
	for _, o := range outlines {
		if o.ParentID.Valid && o.ParentID.String == parentID {
			return true
		}
	}
	return false
}

// estimateUsableInputTokens 计算单次调用可用于内容的 token 数（扣除 prompt 和输出预留）
func estimateUsableInputTokens(maxInputTokens int) int {
	if maxInputTokens <= 0 {
		maxInputTokens = 4096
	}
	// 预留 prompt 模板 + JSON schema 约 500 tokens
	usable := int(float64(maxInputTokens) * 0.75)
	if usable < 500 {
		usable = 500
	}
	return usable
}

// extractLocalSketches 将文档按 Markdown 元素边界分窗口，每个窗口提取局部结构草图
func extractLocalSketches(ctx context.Context, client llm.LLMClient, lines []string, totalLines, usableTokens int) ([]localSketchOutput, error) {
	windowMaxRunes := int(float64(usableTokens) * runesPerToken)
	windows := splitWindowsByMarkdown(lines, windowMaxRunes, 0.05, false)

	slog.Info("semantic outline: window split", "windows", len(windows), "total_lines", totalLines)

	var sketches []localSketchOutput
	for i, w := range windows {
		content := numberedLineRange(lines, w.StartLine, w.EndLine)

		data, err := client.CompleteJSON(ctx, "outline_local_sketch.md", map[string]string{
			"window_line_start": fmt.Sprintf("%d", w.StartLine),
			"window_line_end":   fmt.Sprintf("%d", w.EndLine),
			"total_lines":       fmt.Sprintf("%d", totalLines),
			"window_content":    content,
			"json_schema":       localSketchSchemaExample,
		}, "extraction")
		if err != nil {
			slog.Warn("local sketch extraction failed", "window", i+1, "error", err)
			continue
		}

		var sketch localSketchOutput
		if err := json.Unmarshal(data, &sketch); err != nil {
			slog.Warn("local sketch parse failed", "window", i+1, "error", err)
			continue
		}

		// 修正行号范围
		for j := range sketch.OutlineUnits {
			u := &sketch.OutlineUnits[j]
			if u.LineStart < w.StartLine {
				u.LineStart = w.StartLine
			}
			if u.LineEnd > w.EndLine {
				u.LineEnd = w.EndLine
			}
		}

		sketches = append(sketches, sketch)
		slog.Info("local sketch extracted", "window", i+1, "units", len(sketch.OutlineUnits),
			"range", fmt.Sprintf("%d-%d", w.StartLine, w.EndLine))
	}

	if len(sketches) == 0 {
		return nil, fmt.Errorf("all window sketch extractions failed")
	}

	return sketches, nil
}

// mergeSketchesToOutline 将所有局部草图合并为全局 outline
func mergeSketchesToOutline(ctx context.Context, client llm.LLMClient, sourceID string, sketches []localSketchOutput, totalLines, usableTokens int) ([]Outline, error) {
	// 构建局部草图的文本表示
	var sb strings.Builder
	for i, sketch := range sketches {
		fmt.Fprintf(&sb, "--- 窗口 %d ---\n", i+1)
		fmt.Fprintf(&sb, "starts_mid_section: %v\n", sketch.StartsMidSection)
		fmt.Fprintf(&sb, "ends_mid_section: %v\n", sketch.EndsMidSection)
		if sketch.StartTopic != nil {
			fmt.Fprintf(&sb, "start_topic: %s\n", *sketch.StartTopic)
		}
		if sketch.EndTopic != nil {
			fmt.Fprintf(&sb, "end_topic: %s\n", *sketch.EndTopic)
		}
		for _, u := range sketch.OutlineUnits {
			fmt.Fprintf(&sb, "  %s (line %d-%d) %s\n", u.Title, u.LineStart, u.LineEnd, u.Summary)
		}
		sb.WriteString("\n")
	}
	sketchText := sb.String()

	// 检查草图合并文本是否超出模型输入限制
	sketchTokens := int(float64(RuneCount(sketchText)) / runesPerToken)
	if sketchTokens > usableTokens {
		slog.Warn("sketch text exceeds model input, using rule-based merge",
			"sketch_tokens", sketchTokens, "usable_tokens", usableTokens)
		return ruleBasedMerge(sketches, sourceID, totalLines), nil
	}

	data, err := client.CompleteJSON(ctx, "outline_global_merge.md", map[string]string{
		"total_lines":    fmt.Sprintf("%d", totalLines),
		"local_sketches": sketchText,
		"json_schema":    jsonSchemaExample,
	}, "extraction")
	if err != nil {
		slog.Warn("LLM global merge failed, falling back to rule-based merge", "error", err)
		return ruleBasedMerge(sketches, sourceID, totalLines), nil
	}

	return parseSectionsToOutlines(data, sourceID, totalLines, "semantic")
}

// ruleBasedMerge 当草图文本超出模型输入限制时，用规则合并（不调用 LLM）
func ruleBasedMerge(sketches []localSketchOutput, sourceID string, totalLines int) []Outline {
	var allSections []llmSection
	for _, sketch := range sketches {
		for _, u := range sketch.OutlineUnits {
			allSections = append(allSections, llmSection{
				Title:     u.Title,
				Summary:   u.Summary,
				LineStart: u.LineStart,
				LineEnd:   u.LineEnd,
				Level:     1,
			})
		}
	}

	allSections = repairSections(allSections, totalLines)

	var outlines []Outline
	for _, s := range allSections {
		summary := normalizeSummary(s.Summary)
		outlines = append(outlines, Outline{
			OutlineID: uuid.New().String(),
			SourceID:  sourceID,
			Level:     s.Level,
			Title:     s.Title,
			Summary:   sql.NullString{String: summary, Valid: summary != ""},
			LineStart: s.LineStart,
			LineEnd:   s.LineEnd,
			NodeType:  "semantic",
		})
	}

	assignParentsAndPositions(outlines)
	return outlines
}

func singlePassOutline(ctx context.Context, client llm.LLMClient, sourceID, content string) ([]Outline, error) {
	lines := strings.Split(content, "\n")
	totalLines := fmt.Sprintf("%d", len(lines))

	numbered := numberedLineRange(lines, 1, len(lines))

	data, err := client.CompleteJSON(ctx, "outline_semantic_full.md", map[string]string{
		"json_schema":      jsonSchemaExample,
		"total_lines":      totalLines,
		"document_content": numbered,
	}, "extraction")
	if err != nil {
		return nil, fmt.Errorf("semantic outline single pass: %w", err)
	}

	return parseSectionsToOutlines(data, sourceID, len(lines), "semantic")
}

// RefineLeafNodes 细化条件 E（叶节点过长，结构 outline 其余部分正常）触发的
// 超长叶节点：对每个仍超长的叶节点调用一次 splitOversizeLeaf，不需要像旧版
// 那样递归清理——splitOversizeLeaf 的输出本身已经满足大小要求。
func RefineLeafNodes(ctx context.Context, client llm.LLMClient, sourceID, content string, existingOutlines []Outline, mc config.ModelConfig, segmentMaxChars int) ([]Outline, error) {
	lines := strings.Split(content, "\n")
	leaves := findLeafNodes(existingOutlines)

	var newOutlines []Outline
	for _, leaf := range leaves {
		nodeContent := extractLineRange(lines, leaf.LineStart, leaf.LineEnd)
		if RuneCount(nodeContent) <= segmentMaxChars {
			continue
		}

		children := splitOversizeLeaf(ctx, client, sourceID, lines, &leaf, segmentMaxChars, mc)
		for i := range children {
			children[i].ParentID = sql.NullString{String: leaf.OutlineID, Valid: true}
		}
		newOutlines = append(newOutlines, children...)
	}

	return newOutlines, nil
}

// normalizeSummary 将模型输出的关键词统一为空格分隔格式。
// 模型可能用逗号、顿号、中文逗号等分隔，统一替换为空格。
func normalizeSummary(s string) string {
	s = strings.ReplaceAll(s, "，", " ")
	s = strings.ReplaceAll(s, ",", " ")
	s = strings.ReplaceAll(s, "、", " ")
	s = strings.ReplaceAll(s, "；", " ")
	s = strings.ReplaceAll(s, ";", " ")
	// 压缩连续空格
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}

func parseSectionsToOutlines(data []byte, sourceID string, totalLines int, nodeType string) ([]Outline, error) {
	var output llmSectionsOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return nil, fmt.Errorf("parse sections: %w", err)
	}

	output.Sections = repairSections(output.Sections, totalLines)

	var outlines []Outline
	for _, s := range output.Sections {
		summary := normalizeSummary(s.Summary)
		o := Outline{
			OutlineID: uuid.New().String(),
			SourceID:  sourceID,
			Level:     s.Level,
			Title:     s.Title,
			Summary:   sql.NullString{String: summary, Valid: summary != ""},
			LineStart: s.LineStart,
			LineEnd:   s.LineEnd,
			NodeType:  nodeType,
		}
		outlines = append(outlines, o)
	}

	assignParentsAndPositions(outlines)
	return outlines, nil
}

func repairSections(sections []llmSection, totalLines int) []llmSection {
	var valid []llmSection
	for _, s := range sections {
		if strings.TrimSpace(s.Title) == "" {
			continue
		}
		if s.LineStart < 1 {
			s.LineStart = 1
		}
		if s.LineEnd > totalLines {
			s.LineEnd = totalLines
		}
		if s.LineStart > s.LineEnd {
			continue
		}
		if s.Level < 1 {
			s.Level = 1
		}
		if s.Level > 3 {
			s.Level = 3
		}
		valid = append(valid, s)
	}

	if len(valid) == 0 {
		return valid
	}

	sort.Slice(valid, func(i, j int) bool {
		if valid[i].LineStart != valid[j].LineStart {
			return valid[i].LineStart < valid[j].LineStart
		}
		return valid[i].Level < valid[j].Level
	})

	for i := 1; i < len(valid); i++ {
		prev := &valid[i-1]
		curr := &valid[i]

		if prev.Level != curr.Level {
			continue
		}

		if curr.LineStart <= prev.LineEnd {
			prev.LineEnd = curr.LineStart - 1
			if prev.LineEnd < prev.LineStart {
				prev.LineEnd = prev.LineStart
			}
		} else if curr.LineStart > prev.LineEnd+1 {
			prev.LineEnd = curr.LineStart - 1
		}
	}

	minLevel := valid[0].Level
	for _, s := range valid {
		if s.Level < minLevel {
			minLevel = s.Level
		}
	}
	for i := len(valid) - 1; i >= 0; i-- {
		if valid[i].Level == minLevel {
			if valid[i].LineEnd < totalLines {
				valid[i].LineEnd = totalLines
			}
			break
		}
	}

	var result []llmSection
	for _, s := range valid {
		if s.LineStart <= s.LineEnd {
			result = append(result, s)
		}
	}

	return result
}

func estimateMaxLines(lines []string, maxRunes int) int {
	total := 0
	for i, l := range lines {
		total += RuneCount(l) + 1
		if total > maxRunes {
			if i == 0 {
				return 1
			}
			return i
		}
	}
	return len(lines)
}

