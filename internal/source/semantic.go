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

	// 第三阶段：递归细化超长叶节点（L1→L2→L3，直到叶节点 ≤ segmentMaxChars 或达到 L3）
	result := refineOversizeLeaves(ctx, client, sourceID, lines, outlines, singlePassMaxRunes, segmentMaxChars)
	return result, nil
}

// refineOversizeLeaves 递归细化超长叶节点。
// level < 3：调用 LLM 细化为子层级。
// level = 3 且仍超限：按 Markdown 元素边界切成多个同级 L3 节点（不调 LLM）。
func refineOversizeLeaves(ctx context.Context, client llm.LLMClient, sourceID string, lines []string, outlines []Outline, maxRunes, segmentMaxChars int) []Outline {
	var result []Outline

	for i := range outlines {
		o := &outlines[i]
		nodeContent := extractLineRange(lines, o.LineStart, o.LineEnd)
		isLeaf := !hasChildren(outlines, o.OutlineID)

		if isLeaf && RuneCount(nodeContent) > segmentMaxChars {
			if o.Level < 3 {
				children, err := refineNode(ctx, client, sourceID, lines, o, maxRunes, segmentMaxChars)
				if err != nil {
					slog.Warn("outline refinement failed, keeping parent", "outline_id", o.OutlineID, "error", err)
					result = append(result, *o)
					continue
				}
				if len(children) > 0 {
					for j := range children {
						children[j].ParentID = sql.NullString{String: o.OutlineID, Valid: true}
					}
					result = append(result, *o)
					refined := refineOversizeLeaves(ctx, client, sourceID, lines, children, maxRunes, segmentMaxChars)
					result = append(result, refined...)
				} else {
					result = append(result, *o)
				}
			} else {
				// L3 仍超限：调 LLM 切成多个同级 L3（不增加层级）
				siblings, err := splitLeafBySemantic(ctx, client, sourceID, lines, o, maxRunes, segmentMaxChars)
				if err != nil {
					slog.Warn("L3 semantic split failed, falling back to markdown split",
						"outline_id", o.OutlineID, "error", err)
					siblings = splitLeafByMarkdown(sourceID, lines, o, segmentMaxChars)
				}
				result = append(result, siblings...)
			}
		} else {
			result = append(result, *o)
		}
	}

	return result
}

// splitLeafBySemantic 调 LLM 将超长 L3 叶节点切成多个同级 L3 节点（每个有独立标题和关键词）。
// 内容可能超过单次 LLM 输入限制，按 Markdown 边界分窗口逐段提取后合并。
func splitLeafBySemantic(ctx context.Context, client llm.LLMClient, sourceID string, lines []string, leaf *Outline, maxRunes, segmentMaxChars int) ([]Outline, error) {
	nodeContent := extractLineRange(lines, leaf.LineStart, leaf.LineEnd)
	totalLines := len(lines)

	var allSections []llmSection

	if RuneCount(nodeContent) <= maxRunes {
		// 单次调用
		data, err := client.CompleteJSON(ctx, "outline_semantic_chunk.md", map[string]string{
			"json_schema":      jsonSchemaExample,
			"chunk_line_start": fmt.Sprintf("%d", leaf.LineStart),
			"chunk_line_end":   fmt.Sprintf("%d", leaf.LineEnd),
			"target_level":     fmt.Sprintf("%d", leaf.Level),
			"parent_title":     leaf.Title,
			"chunk_content":    nodeContent,
		}, "extraction")
		if err != nil {
			return nil, err
		}

		var output llmSectionsOutput
		if err := json.Unmarshal(data, &output); err != nil {
			return nil, err
		}
		allSections = output.Sections
	} else {
		// 分窗口调用
		nodeLines := strings.Split(nodeContent, "\n")
		windows := splitWindowsByMarkdown(nodeLines, maxRunes, 0.05)

		for _, w := range windows {
			chunkContent := extractLineRange(nodeLines, w.StartLine, w.EndLine)
			absStart := leaf.LineStart + w.StartLine - 1
			absEnd := leaf.LineStart + w.EndLine - 1

			data, err := client.CompleteJSON(ctx, "outline_semantic_chunk.md", map[string]string{
				"json_schema":      jsonSchemaExample,
				"chunk_line_start": fmt.Sprintf("%d", absStart),
				"chunk_line_end":   fmt.Sprintf("%d", absEnd),
				"target_level":     fmt.Sprintf("%d", leaf.Level),
				"parent_title":     leaf.Title,
				"chunk_content":    chunkContent,
			}, "extraction")
			if err != nil {
				slog.Warn("L3 chunk split failed", "error", err)
				continue
			}

			var output llmSectionsOutput
			if err := json.Unmarshal(data, &output); err != nil {
				continue
			}
			allSections = append(allSections, output.Sections...)
		}
	}

	if len(allSections) == 0 {
		return nil, fmt.Errorf("LLM returned no sections for L3 split")
	}

	allSections = repairSections(allSections, totalLines)

	var siblings []Outline
	for i, s := range allSections {
		ls, le := s.LineStart, s.LineEnd
		if ls < leaf.LineStart {
			ls = leaf.LineStart
		}
		if le > leaf.LineEnd {
			le = leaf.LineEnd
		}
		if ls > le {
			continue
		}
		summary := normalizeSummary(s.Summary)
		siblings = append(siblings, Outline{
			OutlineID: uuid.New().String(),
			SourceID:  sourceID,
			ParentID:  leaf.ParentID,
			Level:     leaf.Level,
			Title:     s.Title,
			Summary:   sql.NullString{String: summary, Valid: summary != ""},
			LineStart: ls,
			LineEnd:   le,
			NodeType:  "semantic",
			Position:  leaf.Position + i,
		})
	}

	slog.Info("L3 semantic split", "title", leaf.Title, "parts", len(siblings),
		"range", fmt.Sprintf("%d-%d", leaf.LineStart, leaf.LineEnd))
	return siblings, nil
}

// splitLeafByMarkdown 将超长 L3 叶节点按 Markdown 元素边界切分为多个同级节点（LLM 降级方案）。
func splitLeafByMarkdown(sourceID string, lines []string, leaf *Outline, segmentMaxChars int) []Outline {
	nodeLines := lines[leaf.LineStart-1 : leaf.LineEnd]
	windows := splitWindowsByMarkdown(nodeLines, segmentMaxChars, 0)

	if len(windows) <= 1 {
		return []Outline{*leaf}
	}

	var siblings []Outline
	for i, w := range windows {
		absStart := leaf.LineStart + w.StartLine - 1
		absEnd := leaf.LineStart + w.EndLine - 1

		title := leaf.Title
		if len(windows) > 1 {
			title = fmt.Sprintf("%s（%d/%d）", leaf.Title, i+1, len(windows))
		}

		sibling := Outline{
			OutlineID: uuid.New().String(),
			SourceID:  sourceID,
			ParentID:  leaf.ParentID,
			Level:     leaf.Level,
			Title:     title,
			Summary:   leaf.Summary,
			LineStart: absStart,
			LineEnd:   absEnd,
			NodeType:  leaf.NodeType,
		}
		siblings = append(siblings, sibling)
	}

	// 更新 position
	for i := range siblings {
		siblings[i].Position = leaf.Position + i
	}

	slog.Info("split oversize L3 leaf", "title", leaf.Title, "parts", len(siblings),
		"range", fmt.Sprintf("%d-%d", leaf.LineStart, leaf.LineEnd))
	return siblings
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
	windows := splitWindowsByMarkdown(lines, windowMaxRunes, 0.05)

	slog.Info("semantic outline: window split", "windows", len(windows), "total_lines", totalLines)

	var sketches []localSketchOutput
	for i, w := range windows {
		content := extractLineRange(lines, w.StartLine, w.EndLine)

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

	data, err := client.CompleteJSON(ctx, "outline_semantic_full.md", map[string]string{
		"json_schema":      jsonSchemaExample,
		"total_lines":      totalLines,
		"document_content": content,
	}, "extraction")
	if err != nil {
		return nil, fmt.Errorf("semantic outline single pass: %w", err)
	}

	return parseSectionsToOutlines(data, sourceID, len(lines), "semantic")
}

func refineNode(ctx context.Context, client llm.LLMClient, sourceID string, lines []string, parent *Outline, maxRunes, segmentMaxChars int) ([]Outline, error) {
	nodeContent := extractLineRange(lines, parent.LineStart, parent.LineEnd)
	childLevel := parent.Level + 1
	if childLevel > 3 {
		return nil, nil
	}

	var result []Outline
	nodeRunes := RuneCount(nodeContent)

	if nodeRunes <= maxRunes {
		children, err := refineChunk(ctx, client, sourceID, nodeContent, parent, childLevel, parent.LineStart, parent.LineEnd, len(lines))
		if err != nil {
			return nil, err
		}
		result = children
	} else {
		nodeLines := strings.Split(nodeContent, "\n")
		chunks := splitWindowsByMarkdown(nodeLines, maxRunes, 0.05)

		for _, chunk := range chunks {
			// chunk 行号是相对于 nodeLines 的 1-based
			chunkContent := extractLineRange(nodeLines, chunk.StartLine, chunk.EndLine)
			absStart := parent.LineStart + chunk.StartLine - 1
			absEnd := parent.LineStart + chunk.EndLine - 1

			children, err := refineChunk(ctx, client, sourceID, chunkContent, parent, childLevel, absStart, absEnd, len(lines))
			if err != nil {
				slog.Warn("chunk refinement failed", "error", err)
			} else {
				result = append(result, children...)
			}
		}

		result = deduplicateOverlap(result)
	}

	for i := range result {
		result[i].Level = childLevel
		// Clamp child line range to parent bounds
		if result[i].LineStart < parent.LineStart {
			result[i].LineStart = parent.LineStart
		}
		if result[i].LineEnd > parent.LineEnd {
			result[i].LineEnd = parent.LineEnd
		}
	}

	// Drop children whose range became invalid after clamping
	var valid []Outline
	for _, o := range result {
		if o.LineStart <= o.LineEnd {
			valid = append(valid, o)
		}
	}

	return valid, nil
}

func refineChunk(ctx context.Context, client llm.LLMClient, sourceID, content string, parent *Outline, targetLevel, absStart, absEnd, totalLines int) ([]Outline, error) {
	data, err := client.CompleteJSON(ctx, "outline_semantic_chunk.md", map[string]string{
		"json_schema":      jsonSchemaExample,
		"chunk_line_start": fmt.Sprintf("%d", absStart),
		"chunk_line_end":   fmt.Sprintf("%d", absEnd),
		"target_level":     fmt.Sprintf("%d", targetLevel),
		"parent_title":     parent.Title,
		"chunk_content":    content,
	}, "extraction")
	if err != nil {
		return nil, err
	}

	return parseSectionsToOutlines(data, sourceID, totalLines, "semantic")
}

func RefineLeafNodes(ctx context.Context, client llm.LLMClient, sourceID, content string, existingOutlines []Outline, mc config.ModelConfig, segmentMaxChars int) ([]Outline, error) {
	lines := strings.Split(content, "\n")
	leaves := findLeafNodes(existingOutlines)
	maxRunes := int(float64(estimateUsableInputTokens(mc.MaxInputTokens)) * runesPerToken)

	var newOutlines []Outline
	for _, leaf := range leaves {
		nodeContent := extractLineRange(lines, leaf.LineStart, leaf.LineEnd)
		if RuneCount(nodeContent) <= segmentMaxChars {
			continue
		}

		children, err := refineNode(ctx, client, sourceID, lines, &leaf, maxRunes, segmentMaxChars)
		if err != nil {
			slog.Warn("leaf refinement failed", "outline_id", leaf.OutlineID, "error", err)
			continue
		}
		for i := range children {
			children[i].ParentID = sql.NullString{String: leaf.OutlineID, Valid: true}
			children[i].NodeType = "semantic"
		}
		// Recursively refine children that are still oversized
		children = refineOversizeLeaves(ctx, client, sourceID, lines, children, maxRunes, segmentMaxChars)
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

func deduplicateOverlap(outlines []Outline) []Outline {
	if len(outlines) <= 1 {
		return outlines
	}

	var result []Outline
	result = append(result, outlines[0])

	for i := 1; i < len(outlines); i++ {
		prev := result[len(result)-1]
		curr := outlines[i]

		if curr.LineStart <= prev.LineEnd && curr.Title == prev.Title {
			if curr.LineEnd > prev.LineEnd {
				result[len(result)-1].LineEnd = curr.LineEnd
			}
			continue
		}
		result = append(result, curr)
	}

	return result
}
