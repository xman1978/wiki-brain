package source

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blevesearch/bleve/v2"
	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/progress"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
)

type Service struct {
	store       *Store
	fileView    FileViewClient
	llmClient   llm.LLMClient
	outlineIdx  bleve.Index
	unitsIdx    bleve.Index
	pointsIdx   bleve.Index
	queue       *queue.Queue
	cfg         *config.Config
	baseDir     string
	broadcaster *progress.Broadcaster
}

func NewService(store *Store, fv FileViewClient, lc llm.LLMClient, outlineIdx bleve.Index, q *queue.Queue, cfg *config.Config, baseDir string) *Service {
	return &Service{
		store:      store,
		fileView:   fv,
		llmClient:  lc,
		outlineIdx: outlineIdx,
		queue:      q,
		cfg:        cfg,
		baseDir:    baseDir,
	}
}

func (s *Service) Store() *Store {
	return s.store
}

func (s *Service) SetUnitIndexes(unitsIdx, pointsIdx bleve.Index) {
	s.unitsIdx = unitsIdx
	s.pointsIdx = pointsIdx
}

func (s *Service) SetBroadcaster(b *progress.Broadcaster) {
	s.broadcaster = b
}

func (s *Service) Broadcaster() *progress.Broadcaster {
	return s.broadcaster
}

func (s *Service) emit(sourceID string, evt progress.Event) {
	if s.broadcaster != nil {
		s.broadcaster.Emit(sourceID, evt)
	}
}

type OutlineIndexDoc struct {
	OutlineID string `json:"outline_id"`
	SourceID  string `json:"source_id"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	Level     int    `json:"level"`
	NodeType  string `json:"node_type"`
}

// Import handles the full upload flow: save file, create source record.
// Returns the source for the caller; processing happens async.
func (s *Service) Import(ctx context.Context, fileName string, fileReader io.Reader) (*Source, error) {
	sourceID := uuid.New().String()
	ext := strings.ToLower(filepath.Ext(fileName))

	if !IsSupportedFormat(fileName) {
		return nil, fmt.Errorf("unsupported format: %s", ext)
	}

	format := DetectFormat(fileName)

	originalDir := filepath.Join(s.baseDir, "data", "sources", "original")
	originalPath := filepath.Join(originalDir, sourceID+ext)

	f, err := os.Create(originalPath)
	if err != nil {
		return nil, fmt.Errorf("create original file: %w", err)
	}
	if _, err := io.Copy(f, fileReader); err != nil {
		f.Close()
		return nil, fmt.Errorf("write original file: %w", err)
	}
	f.Close()

	markdownPath := filepath.Join("data", "sources", "markdown", sourceID+".md")

	src := &Source{
		SourceID:     sourceID,
		Title:        strings.TrimSuffix(fileName, filepath.Ext(fileName)),
		Format:       format,
		FileName:     fileName,
		OriginalPath: filepath.Join("data", "sources", "original", sourceID+ext),
		MarkdownPath: markdownPath,
		Status:       "pending",
	}

	if err := s.store.Create(src); err != nil {
		return nil, fmt.Errorf("create source: %w", err)
	}

	ok := s.queue.Enqueue(queue.Task{
		Type:    queue.TaskTypeSourceProcess,
		Payload: queue.SourceTask{SourceID: sourceID},
	})
	if !ok {
		slog.Error("failed to enqueue source process", "source_id", sourceID)
	}

	return src, nil
}

// Process runs the full source processing pipeline.
func (s *Service) Process(ctx context.Context, sourceID string) error {
	src, err := s.store.GetByID(sourceID)
	if err != nil {
		return fmt.Errorf("get source: %w", err)
	}

	if err := s.store.UpdateStatus(sourceID, "processing", nil); err != nil {
		return err
	}

	// Step 2: Format conversion
	stepStart := time.Now()
	s.emit(sourceID, progress.Event{Step: progress.StepFormatConvert, Status: progress.StatusStarted, Message: "格式转换"})
	if err := s.convertToMarkdown(ctx, src); err != nil {
		errMsg := err.Error()
		s.store.UpdateStatus(sourceID, "failed", &errMsg)
		s.emit(sourceID, progress.Event{Step: progress.StepFormatConvert, Status: progress.StatusFailed, Error: errMsg})
		return err
	}
	s.emit(sourceID, progress.Event{Step: progress.StepFormatConvert, Status: progress.StatusCompleted, ElapsedMs: time.Since(stepStart).Milliseconds()})

	// Step 3: Normalize markdown
	stepStart = time.Now()
	s.emit(sourceID, progress.Event{Step: progress.StepNormalize, Status: progress.StatusStarted, Message: "Markdown 规范化"})
	mdPath := filepath.Join(s.baseDir, src.MarkdownPath)
	content, err := os.ReadFile(mdPath)
	if err != nil {
		errMsg := fmt.Sprintf("read markdown: %v", err)
		s.store.UpdateStatus(sourceID, "failed", &errMsg)
		s.emit(sourceID, progress.Event{Step: progress.StepNormalize, Status: progress.StatusFailed, Error: errMsg})
		return fmt.Errorf("read markdown: %w", err)
	}

	normalized := NormalizeMarkdown(string(content))
	if err := os.WriteFile(mdPath, []byte(normalized), 0644); err != nil {
		errMsg := fmt.Sprintf("write normalized: %v", err)
		s.store.UpdateStatus(sourceID, "failed", &errMsg)
		s.emit(sourceID, progress.Event{Step: progress.StepNormalize, Status: progress.StatusFailed, Error: errMsg})
		return fmt.Errorf("write normalized: %w", err)
	}

	wordCount := utf8.RuneCountInString(normalized)
	s.store.UpdateWordCount(sourceID, wordCount)
	s.emit(sourceID, progress.Event{Step: progress.StepNormalize, Status: progress.StatusCompleted, ElapsedMs: time.Since(stepStart).Milliseconds()})

	// Step 4: Extract structural outlines
	stepStart = time.Now()
	s.emit(sourceID, progress.Event{Step: progress.StepOutlineStructural, Status: progress.StatusStarted, Message: "结构目录提取"})
	structOutlines := ExtractStructuralOutlines(sourceID, normalized)

	s.emit(sourceID, progress.Event{Step: progress.StepOutlineStructural, Status: progress.StatusCompleted, ElapsedMs: time.Since(stepStart).Milliseconds()})

	// Step 5: Check semantic trigger + generate semantic outlines if needed
	trigger := CheckSemanticTrigger(structOutlines, normalized, src.Format, s.cfg.Source.SegmentMaxChars)

	var allOutlines []Outline
	outlineType := "structural"

	if trigger.Triggered {
		stepStart = time.Now()
		s.emit(sourceID, progress.Event{Step: progress.StepOutlineSemantic, Status: progress.StatusStarted, Message: "语义目录细化"})
		slog.Info("semantic outline triggered", "source_id", sourceID, "reasons", trigger.Reasons)

		hasE := false
		onlyE := true
		for _, r := range trigger.Reasons {
			if strings.HasPrefix(r, "E:") || strings.HasPrefix(r, "F+E:") {
				hasE = true
			} else {
				onlyE = false
			}
		}

		if hasE && onlyE && len(structOutlines) > 0 {
			newNodes, err := RefineLeafNodes(ctx, s.llmClient, sourceID, normalized, structOutlines, s.cfg.LLM.ModelForPurpose("extraction"), s.cfg.Source.SegmentMaxChars)
			if err != nil {
				slog.Warn("leaf refinement failed", "source_id", sourceID, "error", err)
			}
			allOutlines = append(structOutlines, newNodes...)
			if len(newNodes) > 0 {
				outlineType = "mixed"
			}
		} else {
			semanticOutlines, err := GenerateSemanticOutlines(ctx, s.llmClient, sourceID, normalized, s.cfg.LLM.ModelForPurpose("extraction"), s.cfg.Source.SegmentMaxChars)
			if err != nil {
				slog.Warn("semantic outline generation failed, using structural", "source_id", sourceID, "error", err)
				allOutlines = structOutlines
			} else {
				allOutlines = semanticOutlines
				outlineType = "semantic"
			}
		}
		s.emit(sourceID, progress.Event{Step: progress.StepOutlineSemantic, Status: progress.StatusCompleted, ElapsedMs: time.Since(stepStart).Milliseconds()})
	} else {
		allOutlines = structOutlines
	}

	// Step 4.5: Generate keyword summaries for outline nodes that lack summary.
	// 此时 outline 结构已完全确定（含语义细化），叶节点内容 ≤ segment_max_chars，
	// 每个节点内容天然在模型输入限制内，按 max_input_tokens 分批即可。
	stepStart = time.Now()
	s.emit(sourceID, progress.Event{Step: progress.StepOutlineSummary, Status: progress.StatusStarted, Message: "目录摘要生成"})
	if len(allOutlines) > 0 {
		mc := s.cfg.LLM.ModelForPurpose("extraction")
		GenerateOutlineSummaries(ctx, s.llmClient, allOutlines, normalized, mc)
	}
	s.emit(sourceID, progress.Event{Step: progress.StepOutlineSummary, Status: progress.StatusCompleted, ElapsedMs: time.Since(stepStart).Milliseconds()})

	// Write all outlines to DB
	if err := s.store.InsertOutlines(allOutlines); err != nil {
		errMsg := fmt.Sprintf("insert outlines: %v", err)
		s.store.UpdateStatus(sourceID, "failed", &errMsg)
		return err
	}

	if err := s.store.UpdateOutlineType(sourceID, outlineType); err != nil {
		slog.Warn("update outline_type failed", "error", err)
	}

	// Step 7: Generate summary
	stepStart = time.Now()
	s.emit(sourceID, progress.Event{Step: progress.StepSourceSummary, Status: progress.StatusStarted, Message: "文档摘要生成"})
	s.generateSummary(ctx, sourceID, src.Title, normalized, allOutlines)
	s.emit(sourceID, progress.Event{Step: progress.StepSourceSummary, Status: progress.StatusCompleted, ElapsedMs: time.Since(stepStart).Milliseconds()})

	// Step 8: Domain matching
	stepStart = time.Now()
	s.emit(sourceID, progress.Event{Step: progress.StepDomainMatch, Status: progress.StatusStarted, Message: "领域匹配"})
	s.matchDomain(ctx, sourceID)
	s.emit(sourceID, progress.Event{Step: progress.StepDomainMatch, Status: progress.StatusCompleted, ElapsedMs: time.Since(stepStart).Milliseconds()})

	// Step 9: Write to Bleve index
	s.indexOutlines(allOutlines)

	// Step 10: Mark completed
	if err := s.store.UpdateStatus(sourceID, "completed", nil); err != nil {
		return err
	}

	// Auto-trigger unit extraction
	ok := s.queue.Enqueue(queue.Task{
		Type:    queue.TaskTypeUnitExtract,
		Payload: queue.UnitTask{SourceID: sourceID},
	})
	if !ok {
		slog.Error("failed to enqueue unit extraction", "source_id", sourceID)
	}

	return nil
}

func (s *Service) convertToMarkdown(ctx context.Context, src *Source) error {
	mdFullPath := filepath.Join(s.baseDir, src.MarkdownPath)

	// If markdown already exists (e.g. retry), skip conversion
	if info, err := os.Stat(mdFullPath); err == nil && info.Size() > 0 {
		return nil
	}

	originalFullPath := filepath.Join(s.baseDir, src.OriginalPath)
	md, err := s.fileView.ConvertToMarkdown(ctx, originalFullPath)
	if err != nil {
		return fmt.Errorf("fileview convert: %w", err)
	}

	if err := os.WriteFile(mdFullPath, md, 0644); err != nil {
		return fmt.Errorf("write markdown: %w", err)
	}

	// Optional HTML preview
	html, err := s.fileView.ConvertToHTML(ctx, originalFullPath)
	if err != nil {
		slog.Warn("HTML preview generation failed", "source_id", src.SourceID, "error", err)
	} else if len(html) > 0 {
		htmlPath := filepath.Join(s.baseDir, "data", "sources", "html", src.SourceID+".html")
		if err := os.WriteFile(htmlPath, html, 0644); err != nil {
			slog.Warn("write HTML preview failed", "error", err)
		} else {
			relHTMLPath := filepath.Join("data", "sources", "html", src.SourceID+".html")
			s.store.db.Exec("UPDATE sources SET html_path = ?, updated_at = CURRENT_TIMESTAMP WHERE source_id = ?",
				relHTMLPath, src.SourceID)
		}
	}

	return nil
}

func (s *Service) generateSummary(ctx context.Context, sourceID, title, content string, outlines []Outline) {
	// Build top outline titles
	var topTitles []string
	for _, o := range outlines {
		if o.Level == 1 {
			topTitles = append(topTitles, o.Title)
		}
	}
	topOutlineTitles := strings.Join(topTitles, "\n")
	if topOutlineTitles == "" {
		topOutlineTitles = "（无顶层目录）"
	}

	// Extract first 300 chars (excluding heading lines)
	firstParagraph := extractFirstParagraph(content, 300)

	summary, err := s.llmClient.Complete(ctx, "source_summary.md", map[string]string{
		"title":              title,
		"top_outline_titles": topOutlineTitles,
		"first_paragraph":    firstParagraph,
	}, "extraction")

	if err != nil {
		slog.Warn("summary generation failed, using outline keywords", "source_id", sourceID, "error", err)
		// Fallback: concatenate L1 outline summaries
		var keywords []string
		for _, o := range outlines {
			if o.Level == 1 && o.Summary.Valid {
				keywords = append(keywords, o.Summary.String)
			}
		}
		summary = strings.Join(keywords, " ")
	}

	summary = strings.TrimSpace(summary)
	if summary != "" {
		if err := s.store.UpdateSummary(sourceID, summary); err != nil {
			slog.Warn("update summary failed", "error", err)
		}
	}
}

func (s *Service) matchDomain(ctx context.Context, sourceID string) {
	src, err := s.store.GetByID(sourceID)
	if err != nil {
		slog.Warn("match domain: get source failed", "error", err)
		return
	}

	domains, err := s.store.ListDomains()
	if err != nil {
		slog.Warn("match domain: list domains failed", "error", err)
		return
	}
	if len(domains) == 0 {
		return
	}

	var domainList strings.Builder
	for _, d := range domains {
		fmt.Fprintf(&domainList, "[%s] %s：%s\n", d.DomainID, d.Name, d.Description)
	}

	summary := ""
	if src.Summary.Valid {
		summary = src.Summary.String
	}

	output, err := s.llmClient.CompleteJSON(ctx, "source_domain_match.md", map[string]string{
		"title":       src.Title,
		"summary":     summary,
		"domain_list": domainList.String(),
	}, "extraction")
	if err != nil {
		slog.Warn("domain match LLM failed", "source_id", sourceID, "error", err)
		return
	}

	var result struct {
		DomainID *string `json:"domain_id"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		slog.Warn("domain match parse failed", "error", err)
		return
	}

	if result.DomainID != nil && *result.DomainID != "" {
		exists, err := s.store.DomainExists(*result.DomainID)
		if err != nil {
			slog.Warn("domain exists check failed", "error", err)
			return
		}
		if exists {
			s.store.UpdateDomainID(sourceID, result.DomainID)
		}
	}
}

func (s *Service) indexOutlines(outlines []Outline) {
	batch := s.outlineIdx.NewBatch()
	for _, o := range outlines {
		doc := OutlineIndexDoc{
			OutlineID: o.OutlineID,
			SourceID:  o.SourceID,
			Title:     o.Title,
			Level:     o.Level,
			NodeType:  o.NodeType,
		}
		if o.Summary.Valid {
			doc.Summary = o.Summary.String
		}
		if err := batch.Index(o.OutlineID, doc); err != nil {
			slog.Error("index outline failed", "outline_id", o.OutlineID, "error", err)
		}
	}
	if err := s.outlineIdx.Batch(batch); err != nil {
		slog.Error("batch index outlines failed", "error", err)
	}
}

// Retry re-processes a failed source.
// Delete 删除失败状态的 Source 及其关联资源（文件、DB 记录、Bleve 索引）。
// 仅对 status=failed 的 Source 生效；已完成的 Source 删除在 lifecycle 模块中实现。
func (s *Service) Delete(sourceID string) error {
	src, err := s.store.GetByID(sourceID)
	if err != nil {
		return fmt.Errorf("source not found: %w", err)
	}
	if src.Status != "failed" {
		return fmt.Errorf("only failed sources can be deleted (current: %s)", src.Status)
	}

	// 1. 从 Bleve 索引删除
	outlineIDs, err := s.store.GetOutlineIDs(sourceID)
	if err != nil {
		slog.Warn("get outline IDs for index cleanup failed", "error", err)
	} else if len(outlineIDs) > 0 {
		batch := s.outlineIdx.NewBatch()
		for _, id := range outlineIDs {
			batch.Delete(id)
		}
		if err := s.outlineIdx.Batch(batch); err != nil {
			slog.Warn("delete outlines from index failed", "error", err)
		}
	}

	if s.unitsIdx != nil {
		unitIDs, err := s.store.GetUnitIDs(sourceID)
		if err != nil {
			slog.Warn("get unit IDs for index cleanup failed", "error", err)
		} else if len(unitIDs) > 0 {
			batch := s.unitsIdx.NewBatch()
			for _, id := range unitIDs {
				batch.Delete(id)
			}
			if err := s.unitsIdx.Batch(batch); err != nil {
				slog.Warn("delete units from index failed", "error", err)
			}
		}
	}

	if s.pointsIdx != nil {
		pointIDs, err := s.store.GetPointIDs(sourceID)
		if err != nil {
			slog.Warn("get point IDs for index cleanup failed", "error", err)
		} else if len(pointIDs) > 0 {
			batch := s.pointsIdx.NewBatch()
			for _, id := range pointIDs {
				batch.Delete(id)
			}
			if err := s.pointsIdx.Batch(batch); err != nil {
				slog.Warn("delete points from index failed", "error", err)
			}
		}
	}

	// 2. 删除文件系统上的文件
	filesToDelete := []string{
		filepath.Join(s.baseDir, src.OriginalPath),
		filepath.Join(s.baseDir, src.MarkdownPath),
	}
	if src.HTMLPath.Valid {
		filesToDelete = append(filesToDelete, filepath.Join(s.baseDir, src.HTMLPath.String))
	}
	for _, f := range filesToDelete {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			slog.Warn("delete file failed", "path", f, "error", err)
		}
	}

	// 3. 删除 DB 记录（含关联的 outlines、KU、KP、KPN）
	if err := s.store.DeleteSource(sourceID); err != nil {
		return fmt.Errorf("delete source: %w", err)
	}

	slog.Info("source deleted", "source_id", sourceID, "title", src.Title)
	return nil
}

func (s *Service) Retry(ctx context.Context, sourceID string) error {
	src, err := s.store.GetByID(sourceID)
	if err != nil {
		return err
	}
	if src.Status != "failed" {
		return fmt.Errorf("source %s is not in failed state (current: %s)", sourceID, src.Status)
	}

	// Clear existing outlines
	if err := s.store.DeleteOutlines(sourceID); err != nil {
		return err
	}

	if err := s.store.UpdateStatus(sourceID, "pending", nil); err != nil {
		return err
	}

	// Check if markdown already exists
	mdPath := filepath.Join(s.baseDir, src.MarkdownPath)
	info, err := os.Stat(mdPath)
	if err == nil && info.Size() > 0 {
		// Skip FileView, start from step 4 via Process
		// But Process also does conversion — we need to mark that md is ready
		// Just re-enqueue the whole process; convertToMarkdown handles md files as no-op for .md sources
	}

	ok := s.queue.Enqueue(queue.Task{
		Type:    queue.TaskTypeSourceProcess,
		Payload: queue.SourceTask{SourceID: sourceID},
	})
	if !ok {
		return fmt.Errorf("failed to enqueue retry")
	}

	return nil
}

func (s *Service) GetMarkdown(sourceID string) (string, error) {
	src, err := s.store.GetByID(sourceID)
	if err != nil {
		return "", err
	}
	mdPath := filepath.Join(s.baseDir, src.MarkdownPath)
	data, err := os.ReadFile(mdPath)
	if err != nil {
		return "", fmt.Errorf("read markdown: %w", err)
	}
	return string(data), nil
}

func (s *Service) GetHTMLPreview(sourceID string) (string, error) {
	src, err := s.store.GetByID(sourceID)
	if err != nil {
		return "", err
	}

	if src.HTMLPath.Valid {
		htmlPath := filepath.Join(s.baseDir, src.HTMLPath.String)
		data, err := os.ReadFile(htmlPath)
		if err == nil {
			return string(data), nil
		}
	}

	// Fallback: return markdown as safe HTML
	md, err := s.GetMarkdown(sourceID)
	if err != nil {
		return "", err
	}
	escaped := strings.ReplaceAll(md, "<", "&lt;")
	escaped = strings.ReplaceAll(escaped, ">", "&gt;")
	return "<pre>" + escaped + "</pre>", nil
}

func extractFirstParagraph(content string, maxRunes int) string {
	lines := strings.Split(content, "\n")
	var sb strings.Builder
	count := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == "" {
			continue
		}

		for _, r := range line {
			sb.WriteRune(r)
			count++
			if count >= maxRunes {
				return sb.String()
			}
		}
		sb.WriteString("\n")
		count++
		if count >= maxRunes {
			return sb.String()
		}
	}

	return sb.String()
}

// ProcessRetry handles retry with idempotent rules per doc.
func (s *Service) ProcessRetry(ctx context.Context, sourceID string) error {
	src, err := s.store.GetByID(sourceID)
	if err != nil {
		return err
	}

	if err := s.store.UpdateStatus(sourceID, "processing", nil); err != nil {
		return err
	}

	// Check if normalized markdown already exists
	mdPath := filepath.Join(s.baseDir, src.MarkdownPath)
	info, err := os.Stat(mdPath)
	mdExists := err == nil && info.Size() > 0

	if !mdExists {
		// Full process from step 1
		if err := s.convertToMarkdown(ctx, src); err != nil {
			errMsg := err.Error()
			s.store.UpdateStatus(sourceID, "failed", &errMsg)
			return err
		}

		content, err := os.ReadFile(mdPath)
		if err != nil {
			errMsg := fmt.Sprintf("read markdown: %v", err)
			s.store.UpdateStatus(sourceID, "failed", &errMsg)
			return err
		}
		normalized := NormalizeMarkdown(string(content))
		if err := os.WriteFile(mdPath, []byte(normalized), 0644); err != nil {
			errMsg := fmt.Sprintf("write normalized: %v", err)
			s.store.UpdateStatus(sourceID, "failed", &errMsg)
			return err
		}
	}

	// Clear existing outlines before re-extracting
	if err := s.store.DeleteOutlines(sourceID); err != nil {
		errMsg := fmt.Sprintf("delete outlines: %v", err)
		s.store.UpdateStatus(sourceID, "failed", &errMsg)
		return err
	}

	// Continue with rest of processing (step 4+)
	content, _ := os.ReadFile(mdPath)
	normalized := string(content)
	wordCount := utf8.RuneCountInString(normalized)
	s.store.UpdateWordCount(sourceID, wordCount)

	structOutlines := ExtractStructuralOutlines(sourceID, normalized)

	if len(structOutlines) > 0 {
		mc := s.cfg.LLM.ModelForPurpose("extraction")
		GenerateOutlineSummaries(ctx, s.llmClient, structOutlines, normalized, mc)
	}

	trigger := CheckSemanticTrigger(structOutlines, normalized, src.Format, s.cfg.Source.SegmentMaxChars)

	var allOutlines []Outline
	outlineType := "structural"

	if trigger.Triggered {
		hasE := false
		onlyE := true
		for _, r := range trigger.Reasons {
			if strings.HasPrefix(r, "E:") || strings.HasPrefix(r, "F+E:") {
				hasE = true
			} else {
				onlyE = false
			}
		}

		if hasE && onlyE && len(structOutlines) > 0 {
			newNodes, err := RefineLeafNodes(ctx, s.llmClient, sourceID, normalized, structOutlines, s.cfg.LLM.ModelForPurpose("extraction"), s.cfg.Source.SegmentMaxChars)
			if err != nil {
				slog.Warn("leaf refinement failed", "error", err)
			}
			allOutlines = append(structOutlines, newNodes...)
			if len(newNodes) > 0 {
				outlineType = "mixed"
			}
		} else {
			semanticOutlines, err := GenerateSemanticOutlines(ctx, s.llmClient, sourceID, normalized, s.cfg.LLM.ModelForPurpose("extraction"), s.cfg.Source.SegmentMaxChars)
			if err != nil {
				slog.Warn("semantic outline generation failed", "error", err)
				allOutlines = structOutlines
			} else {
				allOutlines = semanticOutlines
				outlineType = "semantic"
			}
		}
	} else {
		allOutlines = structOutlines
	}

	if len(allOutlines) > 0 {
		mc := s.cfg.LLM.ModelForPurpose("extraction")
		GenerateOutlineSummaries(ctx, s.llmClient, allOutlines, normalized, mc)
	}

	s.store.InsertOutlines(allOutlines)
	s.store.UpdateOutlineType(sourceID, outlineType)
	s.generateSummary(ctx, sourceID, src.Title, normalized, allOutlines)
	s.matchDomain(ctx, sourceID)
	s.indexOutlines(allOutlines)

	if err := s.store.UpdateStatus(sourceID, "completed", nil); err != nil {
		return err
	}

	s.queue.Enqueue(queue.Task{
		Type:    queue.TaskTypeUnitExtract,
		Payload: queue.UnitTask{SourceID: sourceID},
	})

	return nil
}

// SetDB exposes the database for store operations that need direct access.
// Used by the retry endpoint.
func (s *Service) DB() *sql.DB {
	return s.store.db
}
