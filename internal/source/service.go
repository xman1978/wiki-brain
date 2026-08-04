package source

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	store           *Store
	fileView        FileViewClient
	llmClient       llm.LLMClient
	purposeModels   llm.PurposeModels
	outlineIdx      bleve.Index
	unitsIdx        bleve.Index
	pointsIdx       bleve.Index
	queue           *queue.Queue
	cfg             *config.Config
	baseDir         string
	broadcaster     *progress.Broadcaster
	lifecycleSetter LifecycleSetter
	conceptMatcher  EntryMatcher
}

// LifecycleSetter is implemented by the unit package's Service. Source depends
// on it only through this interface to avoid an import cycle (unit already
// imports source). It is the sole path by which Source marks KU/KP as
// superseded (reupload swap) or deprecated (soft delete) — see
// docs/impl/v1/lifecycle.md 步骤 1-2. SnapshotAndDeprecate/RestoreLifecycle
// back SoftDelete/Restore (文件管理 恢复按钮): a blind restore-to-current
// would incorrectly resurrect units that were already superseded before the
// delete, so the pre-delete lifecycle value is snapshotted and restored
// precisely instead.
type LifecycleSetter interface {
	SetUnitLifecycle(unitIDs []string, lifecycle, reason string) error
	SnapshotAndDeprecate(unitIDs []string, reason string) error
	RestoreLifecycle(unitIDs []string, reason string) error
	// ReindexSource rewrites a source's KU/KP Bleve documents from their
	// current DB rows — CompleteShadowSwap must call it after the 换血事务
	// reparents the shadow's rows, or the documents keep the (now-deleted)
	// shadow source_id and Retrieval's source filter drops every hit.
	ReindexSource(sourceID string) error
}

// EntryMatcher is implemented by the unit package's Service. SetDomain uses
// it to re-run concept matching for a source's current KUs after a manual
// domain reassignment — matchDomain/matchEntries normally run once, back to
// back, during unit_extract, so a KU's entry_id otherwise keeps pointing at
// a concept from whatever domain the source had at extraction time even after
// a human corrects the domain (docs/impl/v1/lifecycle.md's domain-fix flow,
// added per user feedback 2026-07-16). Async and best-effort: SetDomain does
// not wait on it, matching the existing "go func(){ TouchLastUsed }" pattern
// in tryFastPath.
type EntryMatcher interface {
	MatchEntries(ctx context.Context, sourceID, domainID string)
}

func NewService(store *Store, fv FileViewClient, lc llm.LLMClient, pm llm.PurposeModels, outlineIdx bleve.Index, q *queue.Queue, cfg *config.Config, baseDir string) *Service {
	return &Service{
		store:         store,
		fileView:      fv,
		llmClient:     lc,
		purposeModels: pm,
		outlineIdx:    outlineIdx,
		queue:         q,
		cfg:           cfg,
		baseDir:       baseDir,
	}
}

func (s *Service) extractionModel() (llm.ModelParams, error) {
	if s.purposeModels == nil {
		return llm.ModelParams{}, llm.ErrNotConfigured
	}
	return s.purposeModels.ModelForPurpose("extraction")
}

func (s *Service) Store() *Store {
	return s.store
}

func (s *Service) SetUnitIndexes(unitsIdx, pointsIdx bleve.Index) {
	s.unitsIdx = unitsIdx
	s.pointsIdx = pointsIdx
}

func (s *Service) SetLifecycleSetter(ls LifecycleSetter) {
	s.lifecycleSetter = ls
}

func (s *Service) SetEntryMatcher(cm EntryMatcher) {
	s.conceptMatcher = cm
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
	return s.importInternal(ctx, fileName, fileReader, "", SourceOriginUpload, "")
}

// ImportWithOrigin implements docs/impl/v1/wiki.md 步骤 10's reflow entry:
// POST /sources with an optional origin/origin_page_id, defaulting to
// SourceOriginUpload ("upload") when origin is empty — the existing call
// behavior. origin_page_id is only meaningful (and only stored) alongside
// origin=wiki_draft.
func (s *Service) ImportWithOrigin(ctx context.Context, fileName string, fileReader io.Reader, origin, originPageID string) (*Source, error) {
	if origin == "" {
		origin = SourceOriginUpload
	}
	return s.importInternal(ctx, fileName, fileReader, "", origin, originPageID)
}

// ImportShadow creates a hidden Shadow Source for POST /sources/:id/reupload
// (docs/impl/v1/lifecycle.md 步骤 2). It runs through the exact same import +
// source_process/unit_extract pipeline as a normal Import, only shadow_of is
// set and the file-name dedup check is relaxed against targetSourceID. Any
// stale shadow left over from an abandoned attempt is discarded first.
func (s *Service) ImportShadow(ctx context.Context, targetSourceID, fileName string, fileReader io.Reader) (*Source, error) {
	target, err := s.store.GetByID(targetSourceID)
	if err != nil {
		return nil, fmt.Errorf("source not found: %w", err)
	}
	if target.ShadowOf.Valid {
		return nil, fmt.Errorf("cannot reupload a shadow source")
	}

	if existing, err := s.store.GetShadowByTarget(targetSourceID); err != nil {
		slog.Warn("reupload: check existing shadow failed", "target_id", targetSourceID, "error", err)
	} else if existing != nil {
		if err := s.discardShadow(existing); err != nil {
			slog.Warn("reupload: discard stale shadow failed", "shadow_id", existing.SourceID, "error", err)
		}
	}

	return s.importInternal(ctx, fileName, fileReader, targetSourceID, SourceOriginUpload, "")
}

func (s *Service) importInternal(ctx context.Context, fileName string, fileReader io.Reader, shadowOf, origin, originPageID string) (*Source, error) {
	sourceID := uuid.New().String()
	ext := strings.ToLower(filepath.Ext(fileName))

	if !IsSupportedFormat(fileName) {
		return nil, fmt.Errorf("unsupported format: %s", ext)
	}

	var exists bool
	var err error
	if shadowOf != "" {
		exists, err = s.store.ExistsByFileNameExcept(fileName, shadowOf)
	} else {
		exists, err = s.store.ExistsByFileName(fileName)
	}
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, fmt.Errorf("duplicate file name: %s", fileName)
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
		Origin:       origin,
	}
	if origin == SourceOriginWikiDraft && originPageID != "" {
		src.OriginPageID = sql.NullString{String: originPageID, Valid: true}
	}
	if shadowOf != "" {
		src.ShadowOf = sql.NullString{String: shadowOf, Valid: true}
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
			mc, err := s.extractionModel()
			if err != nil {
				slog.Warn("leaf refinement skipped", "source_id", sourceID, "error", err)
			} else {
				newNodes, err := RefineLeafNodes(ctx, s.llmClient, sourceID, normalized, structOutlines, mc, s.cfg.Source.SegmentMaxChars)
				if err != nil {
					slog.Warn("leaf refinement failed", "source_id", sourceID, "error", err)
				}
				allOutlines = append(structOutlines, newNodes...)
				if len(newNodes) > 0 {
					outlineType = "mixed"
				}
			}
		} else {
			mc, err := s.extractionModel()
			if err != nil {
				slog.Warn("semantic outline skipped", "source_id", sourceID, "error", err)
				allOutlines = structOutlines
			} else {
				semanticOutlines, err := GenerateSemanticOutlines(ctx, s.llmClient, sourceID, normalized, mc, s.cfg.Source.SegmentMaxChars)
				if err != nil {
					slog.Warn("semantic outline generation failed, using structural", "source_id", sourceID, "error", err)
					allOutlines = structOutlines
				} else {
					allOutlines = semanticOutlines
					outlineType = "semantic"
				}
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
		if mc, err := s.extractionModel(); err == nil {
			GenerateOutlineSummaries(ctx, s.llmClient, allOutlines, normalized, mc)
		}
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
		if err := s.store.UpdateUnitsStatus(sourceID, "failed"); err != nil {
			return fmt.Errorf("enqueue unit extraction: queue full; mark units failed: %w", err)
		}
		return fmt.Errorf("enqueue unit extraction: queue full")
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

// SetDomain implements the file list's manual domain override (文件列表可修改
// 所属知识领域): matchDomain's LLM classification can misfile a source when the
// domain definitions don't clearly cover its topic, so a human correction has
// to be possible without waiting on a re-import. An empty domainID clears the
// assignment back to unclassified. When the domain actually changes, this
// also kicks off async concept re-matching (see EntryMatcher) so the
// source's KUs stop pointing at entries from their old domain.
func (s *Service) SetDomain(sourceID, domainID string) error {
	src, err := s.store.GetByID(sourceID)
	if err != nil {
		return fmt.Errorf("source not found")
	}

	if domainID != "" {
		exists, err := s.store.DomainExists(domainID)
		if err != nil {
			return fmt.Errorf("source: set domain: %w", err)
		}
		if !exists {
			return fmt.Errorf("unknown domain_id: %s", domainID)
		}
	}

	oldDomainID := ""
	if src.DomainID.Valid {
		oldDomainID = src.DomainID.String
	}

	if domainID == "" {
		err = s.store.UpdateDomainID(sourceID, nil)
	} else {
		err = s.store.UpdateDomainID(sourceID, &domainID)
	}
	if err != nil {
		return err
	}

	if domainID != oldDomainID && s.conceptMatcher != nil {
		go s.conceptMatcher.MatchEntries(context.Background(), sourceID, domainID)
	}
	return nil
}

// SetSummary lets a human correct the auto-generated summary (e.g. when it
// omits a product/role name that source_filter's title+summary pre-screen
// relies on to keep the source in the candidate pool for a question).
func (s *Service) SetSummary(sourceID, summary string) error {
	if _, err := s.store.GetByID(sourceID); err != nil {
		return fmt.Errorf("source not found")
	}
	return s.store.UpdateSummary(sourceID, summary)
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

// removeIndexedArtifacts deletes a source's outlines/units/points from Bleve
// and its original/markdown/html files from disk. Shared by Delete (hard
// delete of a failed source) and discardShadow (abandoning an incomplete
// or superseded-before-swap Shadow Source) — neither leaves orphaned index
// docs or files behind.
func (s *Service) removeIndexedArtifacts(src *Source) {
	sourceID := src.SourceID

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
}

// Delete 删除失败状态的 Source 及其关联资源（文件、DB 记录、Bleve 索引）。
// 仅对 status=failed 或 units_status=failed 的 Source 生效（后者是 status=
// completed 但知识单元抽取阶段失败的情形）；其他状态走 SoftDelete（软删除）。
func (s *Service) Delete(sourceID string) error {
	src, err := s.store.GetByID(sourceID)
	if err != nil {
		return fmt.Errorf("source not found: %w", err)
	}
	if src.Status != "failed" && src.UnitsStatus != "failed" {
		return fmt.Errorf("only failed sources can be deleted (current: status=%s units_status=%s)", src.Status, src.UnitsStatus)
	}

	s.removeIndexedArtifacts(src)

	if err := s.store.DeleteSource(sourceID); err != nil {
		return fmt.Errorf("delete source: %w", err)
	}

	slog.Info("source deleted", "source_id", sourceID, "title", src.Title)
	return nil
}

// SoftDelete implements DELETE /sources/:id for non-failed sources
// (docs/impl/v1/lifecycle.md 步骤 2): KU/KP (including already-superseded ones)
// are marked deprecated, outline index nodes are removed, but rows and files
// are kept for evidence_snapshot traceability. Returns the number of KUs
// marked deprecated.
func (s *Service) SoftDelete(sourceID string) (int, error) {
	src, err := s.store.GetByID(sourceID)
	if err != nil {
		return 0, fmt.Errorf("source not found: %w", err)
	}

	unitIDs, err := s.store.GetUnitIDs(sourceID)
	if err != nil {
		return 0, fmt.Errorf("get unit ids: %w", err)
	}
	if len(unitIDs) > 0 && s.lifecycleSetter != nil {
		if err := s.lifecycleSetter.SnapshotAndDeprecate(unitIDs, fmt.Sprintf("source %s deleted", sourceID)); err != nil {
			return 0, fmt.Errorf("mark deprecated: %w", err)
		}
	}

	if outlineIDs, err := s.store.GetOutlineIDs(sourceID); err != nil {
		slog.Warn("soft delete: get outline IDs failed", "error", err)
	} else if len(outlineIDs) > 0 {
		batch := s.outlineIdx.NewBatch()
		for _, id := range outlineIDs {
			batch.Delete(id)
		}
		if err := s.outlineIdx.Batch(batch); err != nil {
			slog.Warn("soft delete: remove outlines from index failed", "error", err)
		}
	}

	if err := s.store.MarkDeleted(sourceID); err != nil {
		return 0, fmt.Errorf("mark deleted: %w", err)
	}

	slog.Info("source soft-deleted", "source_id", sourceID, "title", src.Title, "deprecated_units", len(unitIDs))
	return len(unitIDs), nil
}

// Restore reverses SoftDelete (文件管理 恢复按钮): the Source flips back to
// completed, each KU/KP is set back to its pre-delete lifecycle value (not
// blindly to current — see LifecycleSetter.RestoreLifecycle, which skips
// units that were already superseded before the delete), and the Source's
// outline nodes are re-added to the outline search index. Only valid for
// soft-deleted sources — a hard-deleted (failed) source has no rows left to
// restore. Returns the number of KUs whose lifecycle was restored.
func (s *Service) Restore(sourceID string) (int, error) {
	src, err := s.store.GetByID(sourceID)
	if err != nil {
		return 0, fmt.Errorf("source not found: %w", err)
	}
	if src.Status != "deleted" {
		return 0, fmt.Errorf("source %s is not deleted (status=%s)", sourceID, src.Status)
	}

	unitIDs, err := s.store.GetUnitIDs(sourceID)
	if err != nil {
		return 0, fmt.Errorf("get unit ids: %w", err)
	}
	if len(unitIDs) > 0 && s.lifecycleSetter != nil {
		if err := s.lifecycleSetter.RestoreLifecycle(unitIDs, fmt.Sprintf("source %s restored", sourceID)); err != nil {
			return 0, fmt.Errorf("restore lifecycle: %w", err)
		}
	}

	if outlines, err := s.store.GetOutlines(sourceID); err != nil {
		slog.Warn("restore: get outlines failed", "error", err)
	} else if len(outlines) > 0 {
		s.indexOutlines(outlines)
	}

	if err := s.store.RestoreSource(sourceID); err != nil {
		return 0, fmt.Errorf("restore source: %w", err)
	}

	slog.Info("source restored", "source_id", sourceID, "title", src.Title, "restored_units", len(unitIDs))
	return len(unitIDs), nil
}

// discardShadow abandons an incomplete or stale Shadow Source: removes its
// indexed artifacts and hard-deletes its DB rows and files, regardless of
// status. Used when a new reupload attempt supersedes a failed shadow, or
// when POST /sources/:id/reupload/retry is not applicable.
func (s *Service) discardShadow(shadow *Source) error {
	s.removeIndexedArtifacts(shadow)
	if err := s.store.DeleteSource(shadow.SourceID); err != nil {
		return fmt.Errorf("delete shadow source: %w", err)
	}
	slog.Info("shadow source discarded", "shadow_id", shadow.SourceID, "shadow_of", shadow.ShadowOf.String)
	return nil
}

// ReuploadRetry implements POST /sources/:id/reupload/retry. Source-stage
// failures resume source processing; unit-stage failures keep the completed
// source artifacts and enqueue only unit extraction.
func (s *Service) ReuploadRetry(ctx context.Context, targetSourceID string) (*Source, error) {
	shadow, err := s.store.GetShadowByTarget(targetSourceID)
	if err != nil {
		return nil, fmt.Errorf("get shadow: %w", err)
	}
	if shadow == nil {
		return nil, fmt.Errorf("no failed shadow to retry for source %s", targetSourceID)
	}

	if shadow.Status == "completed" && shadow.UnitsStatus == "failed" {
		if err := s.store.UpdateUnitsStatus(shadow.SourceID, "pending"); err != nil {
			return nil, fmt.Errorf("retry shadow unit extraction: mark pending: %w", err)
		}
		if ok := s.queue.Enqueue(queue.Task{
			Type:    queue.TaskTypeUnitExtract,
			Payload: queue.UnitTask{SourceID: shadow.SourceID},
		}); !ok {
			if resetErr := s.store.UpdateUnitsStatus(shadow.SourceID, "failed"); resetErr != nil {
				return nil, fmt.Errorf("retry shadow unit extraction: enqueue failed; restore failed status: %w", resetErr)
			}
			return nil, fmt.Errorf("retry shadow unit extraction: enqueue failed")
		}
		return shadow, nil
	}

	if shadow.Status == "failed" {
		if err := s.Retry(ctx, shadow.SourceID); err != nil {
			return nil, fmt.Errorf("retry shadow: %w", err)
		}
		return shadow, nil
	}

	return nil, fmt.Errorf("no failed shadow to retry for source %s", targetSourceID)
}

// ErrShadowEmpty is returned by CompleteShadowSwap when the shadow's
// unit_extract produced zero knowledge units — swapping it in would silently
// wipe the target's existing (real) content, so the swap is skipped and the
// target is left untouched. Callers should treat this the same as any other
// unit_extract failure (mark the shadow's units_status failed) rather than
// log it as an unexpected swap error.
var ErrShadowEmpty = errors.New("shadow source produced zero knowledge units, swap skipped")

// CompleteShadowSwap performs the one-shot "换血" transaction once a Shadow
// Source's unit_extract has finished (docs/impl/v1/lifecycle.md 步骤 2, step 3):
// the target's pre-existing KUs are marked superseded (using their original
// markdown content, read before it gets overwritten), the shadow's KU/KP/
// outlines are re-parented onto the target, original/markdown files are
// swapped with the old ones archived, and the shadow row is dropped. No-op
// (returns nil) if shadowSourceID does not refer to a shadow.
func (s *Service) CompleteShadowSwap(ctx context.Context, shadowSourceID string) error {
	shadow, err := s.store.GetByID(shadowSourceID)
	if err != nil {
		return fmt.Errorf("get shadow source: %w", err)
	}
	if !shadow.ShadowOf.Valid {
		return nil
	}
	targetID := shadow.ShadowOf.String

	shadowUnitIDs, err := s.store.GetUnitIDs(shadowSourceID)
	if err != nil {
		return fmt.Errorf("get shadow unit ids: %w", err)
	}
	if len(shadowUnitIDs) == 0 {
		return ErrShadowEmpty
	}

	target, err := s.store.GetByID(targetID)
	if err != nil {
		return fmt.Errorf("get target source: %w", err)
	}

	oldUnitIDs, err := s.store.GetUnitIDs(targetID)
	if err != nil {
		return fmt.Errorf("get target unit ids: %w", err)
	}
	if len(oldUnitIDs) > 0 && s.lifecycleSetter != nil {
		if err := s.lifecycleSetter.SetUnitLifecycle(oldUnitIDs, "superseded", fmt.Sprintf("source %s reuploaded", targetID)); err != nil {
			return fmt.Errorf("mark superseded: %w", err)
		}
	}

	// SwapShadowIntoTarget deletes the target's own pre-reupload outline rows
	// from SQL (outlines have no lifecycle field, see the store method's
	// comment); grab their IDs first so the matching Bleve documents — indexed
	// under the target's old source_process run — can be removed too, the
	// same way SoftDelete does for a deleted source.
	oldOutlineIDs, err := s.store.GetOutlineIDs(targetID)
	if err != nil {
		return fmt.Errorf("get target outline ids: %w", err)
	}

	archived, newOriginalPath, newHTMLPath, err := s.archiveAndSwapFiles(target, shadow)
	if err != nil {
		return fmt.Errorf("archive files: %w", err)
	}

	// target.Version is still the pre-swap value here — SwapShadowIntoTarget
	// below increments it — so this records the version being superseded.
	if archived.MarkdownPath != "" {
		if err := s.store.InsertSourceVersion(&SourceVersion{
			SourceID:     targetID,
			Version:      target.Version,
			FileName:     target.FileName,
			OriginalPath: archived.OriginalPath,
			HTMLPath:     archived.HTMLPath,
			MarkdownPath: archived.MarkdownPath,
		}); err != nil {
			slog.Warn("shadow swap: record source version snapshot failed", "error", err)
		}
	}

	if err := s.store.SwapShadowIntoTarget(shadowSourceID, targetID, newOriginalPath, newHTMLPath); err != nil {
		return fmt.Errorf("swap shadow into target: %w", err)
	}

	if len(oldOutlineIDs) > 0 {
		batch := s.outlineIdx.NewBatch()
		for _, id := range oldOutlineIDs {
			batch.Delete(id)
		}
		if err := s.outlineIdx.Batch(batch); err != nil {
			slog.Warn("shadow swap: remove old outlines from index failed", "error", err)
		}
	}

	// The shadow's own pipeline indexed its units/points/outlines under the
	// shadow source_id; the swap above only rewrote SQLite. Rewrite the Bleve
	// documents under the target id too, or Retrieval's source filters drop
	// every hit from this source forever (the shadow row no longer exists).
	// The swap itself is committed, so index failures degrade search but must
	// not fail the reupload — cmd/reindex can repair them offline.
	if s.lifecycleSetter != nil {
		if err := s.lifecycleSetter.ReindexSource(targetID); err != nil {
			slog.Warn("shadow swap: reindex units/points failed", "target_id", targetID, "error", err)
		}
	}
	if err := s.ReindexOutlines(targetID); err != nil {
		slog.Warn("shadow swap: reindex outlines failed", "target_id", targetID, "error", err)
	}

	slog.Info("shadow swap completed", "target_id", targetID, "shadow_id", shadowSourceID, "superseded_units", len(oldUnitIDs), "replaced_outlines", len(oldOutlineIDs))
	return nil
}

// ReindexOutlines rewrites a source's outline Bleve documents from their
// current DB rows (document IDs are outline_ids, so stale documents — e.g.
// ones still carrying a swapped-away shadow source_id — are replaced in
// place). Used by CompleteShadowSwap and cmd/reindex.
func (s *Service) ReindexOutlines(sourceID string) error {
	outlines, err := s.store.GetOutlines(sourceID)
	if err != nil {
		return fmt.Errorf("reindex outlines: %w", err)
	}
	if len(outlines) == 0 {
		return nil
	}
	s.indexOutlines(outlines)
	return nil
}

// archivedFiles is target's pre-reupload files' new location under
// data/sources/archived/, for recording a source_versions snapshot row.
type archivedFiles struct {
	OriginalPath string
	MarkdownPath string
	HTMLPath     sql.NullString
}

// archiveAndSwapFiles moves target's current original/markdown files to
// data/sources/archived/<target_id>/<timestamp>/, then copies the shadow's
// files into target's paths so target's file paths keep pointing at valid
// content under target's own source_id. Returns the archived (old) file
// locations for the caller to record as a source_versions snapshot, and the
// (possibly new, since reupload may change format/extension) original_path
// and html_path target should be updated to.
func (s *Service) archiveAndSwapFiles(target, shadow *Source) (archived archivedFiles, originalPath string, htmlPath sql.NullString, err error) {
	archiveRelDir := filepath.Join("data", "sources", "archived", target.SourceID, time.Now().UTC().Format("20060102T150405Z"))
	archiveDir := filepath.Join(s.baseDir, archiveRelDir)
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		return archivedFiles{}, "", sql.NullString{}, fmt.Errorf("create archive dir: %w", err)
	}

	// archiveOne returns the new relative path the file was moved to, or ""
	// if there was nothing to archive (relPath empty or file already gone).
	archiveOne := func(relPath string) (string, error) {
		if relPath == "" {
			return "", nil
		}
		oldFull := filepath.Join(s.baseDir, relPath)
		if _, statErr := os.Stat(oldFull); statErr != nil {
			if os.IsNotExist(statErr) {
				return "", nil
			}
			return "", fmt.Errorf("stat old file %s: %w", oldFull, statErr)
		}
		newRel := filepath.Join(archiveRelDir, filepath.Base(relPath))
		if err := os.Rename(oldFull, filepath.Join(s.baseDir, newRel)); err != nil {
			return "", err
		}
		return newRel, nil
	}
	archivedOriginal, err := archiveOne(target.OriginalPath)
	if err != nil {
		return archivedFiles{}, "", sql.NullString{}, fmt.Errorf("archive original: %w", err)
	}
	archivedMarkdown, err := archiveOne(target.MarkdownPath)
	if err != nil {
		return archivedFiles{}, "", sql.NullString{}, fmt.Errorf("archive markdown: %w", err)
	}
	var archivedHTML sql.NullString
	if target.HTMLPath.Valid {
		p, err := archiveOne(target.HTMLPath.String)
		if err != nil {
			return archivedFiles{}, "", sql.NullString{}, fmt.Errorf("archive html: %w", err)
		}
		if p != "" {
			archivedHTML = sql.NullString{String: p, Valid: true}
		}
	}

	// Copy shadow's freshly processed content into target's identity.
	newOriginalPath := filepath.Join("data", "sources", "original", target.SourceID+filepath.Ext(shadow.OriginalPath))
	if copyErr := copyFile(filepath.Join(s.baseDir, shadow.OriginalPath), filepath.Join(s.baseDir, newOriginalPath)); copyErr != nil {
		return archivedFiles{}, "", sql.NullString{}, fmt.Errorf("copy shadow original: %w", copyErr)
	}
	os.Remove(filepath.Join(s.baseDir, shadow.OriginalPath))

	if copyErr := copyFile(filepath.Join(s.baseDir, shadow.MarkdownPath), filepath.Join(s.baseDir, target.MarkdownPath)); copyErr != nil {
		return archivedFiles{}, "", sql.NullString{}, fmt.Errorf("copy shadow markdown: %w", copyErr)
	}
	os.Remove(filepath.Join(s.baseDir, shadow.MarkdownPath))

	newHTMLPath := sql.NullString{}
	if shadow.HTMLPath.Valid {
		targetHTMLPath := filepath.Join("data", "sources", "html", target.SourceID+".html")
		if copyErr := copyFile(filepath.Join(s.baseDir, shadow.HTMLPath.String), filepath.Join(s.baseDir, targetHTMLPath)); copyErr == nil {
			os.Remove(filepath.Join(s.baseDir, shadow.HTMLPath.String))
			newHTMLPath = sql.NullString{String: targetHTMLPath, Valid: true}
		}
	}

	archived = archivedFiles{OriginalPath: archivedOriginal, MarkdownPath: archivedMarkdown, HTMLPath: archivedHTML}
	return archived, newOriginalPath, newHTMLPath, nil
}

func copyFile(srcPath, dstPath string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	return os.WriteFile(dstPath, data, 0644)
}

// Retry re-runs whichever stage failed. A source-level failure (status=
// failed, e.g. parse/outline) restarts the whole source_process pipeline
// from scratch. A source that parsed fine but whose knowledge-unit
// extraction failed (status=completed, units_status=failed) only re-enqueues
// unit_extract — nothing from the failed attempt was ever persisted (Extract
// publishes the whole generation in one call, only after extraction and
// semantics both succeed), so re-running it is safe to repeat as many times
// as needed without leaving duplicate or partial knowledge_units/points/
// semantics behind.
func (s *Service) Retry(ctx context.Context, sourceID string) error {
	src, err := s.store.GetByID(sourceID)
	if err != nil {
		return err
	}
	if src.Status != "failed" {
		if src.UnitsStatus != "failed" {
			return fmt.Errorf("source %s is not in failed state (current: status=%s units_status=%s)", sourceID, src.Status, src.UnitsStatus)
		}
		ok := s.queue.Enqueue(queue.Task{
			Type:    queue.TaskTypeUnitExtract,
			Payload: queue.UnitTask{SourceID: sourceID},
		})
		if !ok {
			return fmt.Errorf("failed to enqueue retry")
		}
		return nil
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

// GetMarkdownForUnit returns the markdown body that belongs to the given
// unit's lifecycle version (archived file when the unit is superseded).
func (s *Service) GetMarkdownForUnit(sourceID, unitID string) (string, error) {
	var unitSourceID string
	err := s.store.db.QueryRow(`SELECT source_id FROM knowledge_units WHERE unit_id = ?`, unitID).Scan(&unitSourceID)
	if err != nil {
		return "", fmt.Errorf("get unit source: %w", err)
	}
	if unitSourceID != sourceID {
		return "", fmt.Errorf("unit %s does not belong to source %s", unitID, sourceID)
	}
	relPath, err := s.store.ResolveMarkdownPathByUnitID(unitID)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(s.baseDir, relPath))
	if err != nil {
		return "", fmt.Errorf("read markdown: %w", err)
	}
	return string(data), nil
}

// GetMarkdownForVersion returns an archived source version's markdown.
func (s *Service) GetMarkdownForVersion(sourceID string, version int) (string, error) {
	v, err := s.store.GetSourceVersion(sourceID, version)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(s.baseDir, v.MarkdownPath))
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

// GetVersionOriginalPath resolves an archived version's original file for
// download (GET /sources/:id/versions/:version/download).
func (s *Service) GetVersionOriginalPath(sourceID string, version int) (fullPath, fileName string, err error) {
	v, err := s.store.GetSourceVersion(sourceID, version)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(s.baseDir, v.OriginalPath), v.FileName, nil
}

// GetVersionHTMLPreview mirrors GetHTMLPreview but reads an archived
// version's files instead of the source's current ones.
func (s *Service) GetVersionHTMLPreview(sourceID string, version int) (string, error) {
	v, err := s.store.GetSourceVersion(sourceID, version)
	if err != nil {
		return "", err
	}

	if v.HTMLPath.Valid {
		data, err := os.ReadFile(filepath.Join(s.baseDir, v.HTMLPath.String))
		if err == nil {
			return string(data), nil
		}
	}

	data, err := os.ReadFile(filepath.Join(s.baseDir, v.MarkdownPath))
	if err != nil {
		return "", fmt.Errorf("read archived markdown: %w", err)
	}
	escaped := strings.ReplaceAll(string(data), "<", "&lt;")
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
		if mc, err := s.extractionModel(); err == nil {
			GenerateOutlineSummaries(ctx, s.llmClient, structOutlines, normalized, mc)
		}
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
			mc, err := s.extractionModel()
			if err != nil {
				slog.Warn("leaf refinement skipped", "error", err)
			} else {
				newNodes, err := RefineLeafNodes(ctx, s.llmClient, sourceID, normalized, structOutlines, mc, s.cfg.Source.SegmentMaxChars)
				if err != nil {
					slog.Warn("leaf refinement failed", "error", err)
				}
				allOutlines = append(structOutlines, newNodes...)
				if len(newNodes) > 0 {
					outlineType = "mixed"
				}
			}
		} else {
			mc, err := s.extractionModel()
			if err != nil {
				slog.Warn("semantic outline skipped", "error", err)
				allOutlines = structOutlines
			} else {
				semanticOutlines, err := GenerateSemanticOutlines(ctx, s.llmClient, sourceID, normalized, mc, s.cfg.Source.SegmentMaxChars)
				if err != nil {
					slog.Warn("semantic outline generation failed", "error", err)
					allOutlines = structOutlines
				} else {
					allOutlines = semanticOutlines
					outlineType = "semantic"
				}
			}
		}
	} else {
		allOutlines = structOutlines
	}

	if len(allOutlines) > 0 {
		if mc, err := s.extractionModel(); err == nil {
			GenerateOutlineSummaries(ctx, s.llmClient, allOutlines, normalized, mc)
		}
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
