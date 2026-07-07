package unit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/progress"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/source"
)

type Service struct {
	store              *Store
	sourceStore        *source.Store
	llmClient          llm.LLMClient
	unitsIndex         bleve.Index
	pointsIndex        bleve.Index
	queue              *queue.Queue
	cfg                *config.Config
	broadcaster        *progress.Broadcaster
	wikiNotifier       WikiNotifier
	activationNotifier ActivationNotifier
}

// WikiNotifier lets the (not yet implemented) Wiki module learn about
// lifecycle changes so it can mark dependent pages needs_recompile
// (docs/impl/v1/lifecycle.md 步骤 4). SetUnitLifecycle no-ops when unset.
type WikiNotifier interface {
	NotifyPointsLifecycleChanged(pointIDs []string) error
}

// ActivationNotifier lets the Activation module's Matcher learn about KP
// lifecycle changes, since its verified-link cache only holds links whose
// target KP is lifecycle=current (docs/impl/v1/activation.md 步骤 2 候选加载).
// SetUnitLifecycle no-ops when unset.
type ActivationNotifier interface {
	InvalidateCache() error
}

func (s *Service) SetWikiNotifier(n WikiNotifier) {
	s.wikiNotifier = n
}

func (s *Service) SetActivationNotifier(n ActivationNotifier) {
	s.activationNotifier = n
}

func NewService(store *Store, sourceStore *source.Store, llmClient llm.LLMClient, unitsIdx, pointsIdx bleve.Index, q *queue.Queue, cfg *config.Config) *Service {
	return &Service{
		store:       store,
		sourceStore: sourceStore,
		llmClient:   llmClient,
		unitsIndex:  unitsIdx,
		pointsIndex: pointsIdx,
		queue:       q,
		cfg:         cfg,
	}
}

func (s *Service) SetBroadcaster(b *progress.Broadcaster) {
	s.broadcaster = b
}

func (s *Service) emit(sourceID string, evt progress.Event) {
	if s.broadcaster != nil {
		s.broadcaster.Emit(sourceID, evt)
	}
}

func (s *Service) RegisterHandler() {
	s.queue.RegisterHandler(queue.TaskTypeUnitExtract, func(payload interface{}) {
		task := payload.(queue.UnitTask)
		if err := s.Extract(context.Background(), task.SourceID); err != nil {
			slog.Error("unit extract failed", "source_id", task.SourceID, "error", err)
		}
		if s.broadcaster != nil {
			s.broadcaster.Close(task.SourceID)
		}
	})
}

func (s *Service) TriggerExtract(sourceID string) error {
	src, err := s.sourceStore.GetByID(sourceID)
	if err != nil {
		return fmt.Errorf("unit: get source: %w", err)
	}
	if src.Status != "completed" {
		return fmt.Errorf("unit: source %s status is %q, need completed", sourceID, src.Status)
	}

	s.queue.Enqueue(queue.Task{
		Type:    queue.TaskTypeUnitExtract,
		Payload: queue.UnitTask{SourceID: sourceID},
	})
	return nil
}

// SourceCoverageReport recomputes the same segments Extract used
// (outlines + markdown → BuildSegments) and diffs them against the units
// already in SQLite — read-only, no LLM calls — to surface any segment
// lines that ended up in no completed unit at all (docs/impl/mvp/unit.md
// 完成标准 doesn't cover this yet; see ComputeCoverage for what counts).
func (s *Service) SourceCoverageReport(sourceID string) ([]SegmentCoverage, error) {
	src, err := s.sourceStore.GetByID(sourceID)
	if err != nil {
		return nil, fmt.Errorf("unit: get source: %w", err)
	}

	outlines, err := s.sourceStore.GetOutlines(sourceID)
	if err != nil {
		return nil, fmt.Errorf("unit: get outlines: %w", err)
	}

	mdBytes, err := os.ReadFile(src.MarkdownPath)
	if err != nil {
		return nil, fmt.Errorf("unit: read markdown: %w", err)
	}
	mdLines := strings.Split(string(mdBytes), "\n")

	segments := BuildSegments(outlines, mdLines, s.cfg.Source.SegmentMaxChars, s.cfg.Source.MinSegmentChars)

	// Only current-lifecycle units represent the document's present content;
	// a reuploaded source's superseded units must not count toward coverage.
	units, err := s.store.GetUnitsBySourceIDFiltered(sourceID, LifecycleCurrent)
	if err != nil {
		return nil, fmt.Errorf("unit: get units: %w", err)
	}

	return ComputeCoverage(segments, units, mdLines), nil
}

func (s *Service) Extract(ctx context.Context, sourceID string) error {
	src, err := s.sourceStore.GetByID(sourceID)
	if err != nil {
		return fmt.Errorf("unit: get source: %w", err)
	}

	outlines, err := s.sourceStore.GetOutlines(sourceID)
	if err != nil {
		return fmt.Errorf("unit: get outlines: %w", err)
	}

	mdBytes, err := os.ReadFile(src.MarkdownPath)
	if err != nil {
		return fmt.Errorf("unit: read markdown: %w", err)
	}
	mdLines := strings.Split(string(mdBytes), "\n")

	segments := BuildSegments(outlines, mdLines, s.cfg.Source.SegmentMaxChars, s.cfg.Source.MinSegmentChars)
	if len(segments) == 0 {
		slog.Warn("unit: no segments to extract", "source_id", sourceID)
		return nil
	}

	s.emit(sourceID, progress.Event{Step: progress.StepUnitSegment, Status: progress.StatusCompleted, Message: fmt.Sprintf("切分为 %d 段", len(segments)), Total: len(segments)})

	for i, seg := range segments {
		stepStart := time.Now()
		s.emit(sourceID, progress.Event{Step: progress.StepUnitExtract, Status: progress.StatusStarted, Message: fmt.Sprintf("提取知识单元 (%d/%d)", i+1, len(segments)), Current: i + 1, Total: len(segments)})
		s.processSegment(ctx, sourceID, src.MarkdownPath, seg, mdLines)
		s.emit(sourceID, progress.Event{Step: progress.StepUnitExtract, Status: progress.StatusCompleted, Current: i + 1, Total: len(segments), ElapsedMs: time.Since(stepStart).Milliseconds()})
	}

	stepStart := time.Now()
	s.emit(sourceID, progress.Event{Step: progress.StepKPNGenerate, Status: progress.StatusStarted, Message: "KPN 关系生成"})
	s.generateKPN(ctx, sourceID)
	s.emit(sourceID, progress.Event{Step: progress.StepKPNGenerate, Status: progress.StatusCompleted, ElapsedMs: time.Since(stepStart).Milliseconds()})

	stepStart = time.Now()
	s.emit(sourceID, progress.Event{Step: progress.StepConceptMatch, Status: progress.StatusStarted, Message: "概念匹配"})
	s.matchConcepts(ctx, sourceID, src.DomainID)
	s.emit(sourceID, progress.Event{Step: progress.StepConceptMatch, Status: progress.StatusCompleted, ElapsedMs: time.Since(stepStart).Milliseconds()})

	// Step 6 (docs/impl/v1/kpn.md): cross-Source KPN matching. Failure is
	// isolated — it must never fail the Source's own completion status.
	stepStart = time.Now()
	s.emit(sourceID, progress.Event{Step: progress.StepKPNCrossMatch, Status: progress.StatusStarted, Message: "跨源 KPN 匹配"})
	if _, err := s.CrossSourceKPN(ctx, sourceID); err != nil {
		slog.Warn("unit: cross source kpn failed", "source_id", sourceID, "error", err)
	}
	s.emit(sourceID, progress.Event{Step: progress.StepKPNCrossMatch, Status: progress.StatusCompleted, ElapsedMs: time.Since(stepStart).Milliseconds()})

	s.emit(sourceID, progress.Event{Step: "done", Status: progress.StatusCompleted, Message: "处理完成"})

	return nil
}

type llmUnit struct {
	UnitID          string `json:"unit_id"`
	Center          string `json:"center"`
	FirstLineAnchor string `json:"first_line_anchor"`
	LastLineAnchor  string `json:"last_line_anchor"`
}

type llmPoint struct {
	PointID string `json:"point_id"`
	UnitID  string `json:"unit_id"`
	Content string `json:"content"`
	Type    string `json:"type"`
}

type extractOutput struct {
	Units  []llmUnit  `json:"units"`
	Points []llmPoint `json:"points"`
}

func (s *Service) processSegment(ctx context.Context, sourceID, markdownPath string, seg Segment, mdLines []string) {
	textContent := sliceLinesWithLineNumbers(mdLines, seg.LineStart, seg.LineEnd)

	schemaJSON := `{"units":[{"unit_id":"1","center":"知识单元主题","first_line_anchor":"该单元第一行开头","last_line_anchor":"该单元最后一行结尾"}],"points":[{"point_id":"1","unit_id":"1","content":"可激活摘要内容","type":"definition|rule|method|case|question"}]}`

	vars := map[string]string{
		"outline_title":      seg.Title,
		"segment_line_start": strconv.Itoa(seg.LineStart),
		"segment_line_end":   strconv.Itoa(seg.LineEnd),
		"text_content":       textContent,
		"json_schema":        schemaJSON,
	}

	data, err := s.llmClient.CompleteJSON(ctx, "unit_extract.md", vars, "extraction")
	if err != nil {
		data, err = s.retrySegment(ctx, seg, textContent, schemaJSON)
		if err != nil {
			slog.Error("unit: segment extraction failed after retry", "source_id", sourceID,
				"line_start", seg.LineStart, "line_end", seg.LineEnd, "error", err)
			return
		}
	}

	var output extractOutput
	if err := json.Unmarshal(data, &output); err != nil {
		data, err = s.retrySegment(ctx, seg, textContent, schemaJSON)
		if err != nil {
			slog.Error("unit: segment JSON parse failed after retry", "source_id", sourceID, "error", err)
			return
		}
		if err := json.Unmarshal(data, &output); err != nil {
			slog.Error("unit: retry JSON parse still failed", "source_id", sourceID, "error", err)
			return
		}
	}

	localToUUID := make(map[string]string)
	for _, u := range output.Units {
		localToUUID[u.UnitID] = uuid.New().String()
	}

	cursor := seg.LineStart
	for _, u := range output.Units {
		lineStart, lineEnd, nextCursor, locateOK := LocateUnitBounds(mdLines, seg, u.FirstLineAnchor, u.LastLineAnchor, cursor)
		if locateOK {
			cursor = nextCursor
		}
		if !locateOK || !s.validateUnit(u, output.Points) {
			s.handleFailedUnit(ctx, sourceID, seg, u, mdLines, localToUUID)
			continue
		}

		realUnitID := localToUUID[u.UnitID]

		ku := &KnowledgeUnit{
			UnitID:        realUnitID,
			SourceID:      sourceID,
			OutlineID:     seg.OutlineID,
			Center:        u.Center,
			LineStart:     lineStart,
			LineEnd:       lineEnd,
			Status:        "completed",
			PromptVersion: "v4",
		}
		if err := s.store.InsertUnit(ku); err != nil {
			slog.Error("unit: insert unit failed", "error", err)
			continue
		}

		for _, p := range output.Points {
			if p.UnitID != u.UnitID {
				continue
			}
			kp := &KnowledgePoint{
				PointID:   uuid.New().String(),
				UnitID:    realUnitID,
				SourceID:  sourceID,
				Content:   p.Content,
				PointType: p.Type,
			}
			if err := s.store.InsertPoint(kp); err != nil {
				slog.Error("unit: insert point failed", "error", err)
			}

			s.indexPoint(kp)
		}

		s.indexUnit(ku, mdLines)
	}
}

func (s *Service) validateUnit(u llmUnit, points []llmPoint) bool {
	if u.Center == "" {
		return false
	}
	hasPoints := false
	for _, p := range points {
		if p.UnitID == u.UnitID {
			if p.Content == "" {
				return false
			}
			hasPoints = true
		}
	}
	return hasPoints
}

func (s *Service) handleFailedUnit(ctx context.Context, sourceID string, seg Segment, u llmUnit, mdLines []string, localToUUID map[string]string) {
	schemaJSON := `{"units":[{"unit_id":"1","center":"知识单元主题","first_line_anchor":"该单元第一行开头","last_line_anchor":"该单元最后一行结尾"}],"points":[{"point_id":"1","unit_id":"1","content":"可激活摘要内容","type":"definition|rule|method|case|question"}]}`

	vars := map[string]string{
		"segment_line_start": strconv.Itoa(seg.LineStart),
		"segment_line_end":   strconv.Itoa(seg.LineEnd),
		"segment_line_count": strconv.Itoa(seg.LineEnd - seg.LineStart + 1),
		"text_content":       sliceLinesWithLineNumbers(mdLines, seg.LineStart, seg.LineEnd),
		"json_schema":        schemaJSON,
	}

	data, err := s.llmClient.CompleteJSON(ctx, "unit_extract_retry.md", vars, "extraction")
	if err != nil {
		s.insertFailedUnit(sourceID, seg, u, "retry LLM call failed: "+err.Error())
		return
	}

	var output extractOutput
	if err := json.Unmarshal(data, &output); err != nil {
		s.insertFailedUnit(sourceID, seg, u, "retry JSON parse failed: "+err.Error())
		return
	}

	for _, ru := range output.Units {
		lineStart, lineEnd, _, locateOK := LocateUnitBounds(mdLines, seg, ru.FirstLineAnchor, ru.LastLineAnchor, seg.LineStart)
		if !locateOK || !s.validateUnit(ru, output.Points) {
			continue
		}
		realUnitID := uuid.New().String()

		ku := &KnowledgeUnit{
			UnitID:        realUnitID,
			SourceID:      sourceID,
			OutlineID:     seg.OutlineID,
			Center:        ru.Center,
			LineStart:     lineStart,
			LineEnd:       lineEnd,
			Status:        "completed",
			PromptVersion: "v4",
		}
		if err := s.store.InsertUnit(ku); err != nil {
			slog.Error("unit: retry insert unit failed", "error", err)
			continue
		}

		for _, p := range output.Points {
			if p.UnitID != ru.UnitID {
				continue
			}
			kp := &KnowledgePoint{
				PointID:   uuid.New().String(),
				UnitID:    realUnitID,
				SourceID:  sourceID,
				Content:   p.Content,
				PointType: p.Type,
			}
			s.store.InsertPoint(kp)
			s.indexPoint(kp)
		}
		s.indexUnit(ku, mdLines)
		return
	}

	s.insertFailedUnit(sourceID, seg, u, "retry validation still failed")
}

func (s *Service) insertFailedUnit(sourceID string, seg Segment, u llmUnit, errMsg string) {
	// The model's anchor text couldn't be located (or wasn't given), so there's
	// no derived line range to fall back to beyond the whole segment's bounds.
	ku := &KnowledgeUnit{
		UnitID:        uuid.New().String(),
		SourceID:      sourceID,
		OutlineID:     seg.OutlineID,
		Center:        u.Center,
		LineStart:     seg.LineStart,
		LineEnd:       seg.LineEnd,
		Status:        "extraction_failed",
		ErrorMsg:      sql.NullString{String: errMsg, Valid: true},
		PromptVersion: "v4",
	}
	if ku.Center == "" {
		ku.Center = "(extraction failed)"
	}
	if err := s.store.InsertUnit(ku); err != nil {
		slog.Error("unit: insert failed unit", "error", err)
	}
}

func (s *Service) retrySegment(ctx context.Context, seg Segment, textContent, schemaJSON string) ([]byte, error) {
	vars := map[string]string{
		"segment_line_start": strconv.Itoa(seg.LineStart),
		"segment_line_end":   strconv.Itoa(seg.LineEnd),
		"segment_line_count": strconv.Itoa(seg.LineEnd - seg.LineStart + 1),
		"text_content":       textContent,
		"json_schema":        schemaJSON,
	}
	return s.llmClient.CompleteJSON(ctx, "unit_extract_retry.md", vars, "extraction")
}

type kpnRelation struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
}

type kpnOutput struct {
	Relations []kpnRelation `json:"relations"`
}

func (s *Service) generateKPN(ctx context.Context, sourceID string) {
	points, err := s.store.GetPointsBySourceID(sourceID)
	if err != nil {
		slog.Error("unit: kpn get points failed", "source_id", sourceID, "error", err)
		return
	}
	if len(points) == 0 {
		return
	}

	units, _ := s.store.GetCompletedUnitsBySourceID(sourceID)
	unitCenterMap := make(map[string]string)
	for _, u := range units {
		unitCenterMap[u.UnitID] = u.Center
	}

	if len(points) <= 60 {
		s.kpnBatch(ctx, points, unitCenterMap)
	} else {
		outlines, _ := s.sourceStore.GetOutlines(sourceID)
		topLevel := findTopLevelOutlines(outlines)
		batches := groupPointsByTopOutline(points, units, topLevel)
		for _, batch := range batches {
			for i := 0; i < len(batch); i += 60 {
				end := i + 60
				if end > len(batch) {
					end = len(batch)
				}
				s.kpnBatch(ctx, batch[i:end], unitCenterMap)
			}
		}
	}
}

func findTopLevelOutlines(outlines []source.Outline) []source.Outline {
	var top []source.Outline
	for _, o := range outlines {
		if !o.ParentID.Valid {
			top = append(top, o)
		}
	}
	return top
}

func groupPointsByTopOutline(points []KnowledgePoint, units []KnowledgeUnit, topOutlines []source.Outline) [][]KnowledgePoint {
	unitOutlineMap := make(map[string]string)
	for _, u := range units {
		if u.OutlineID.Valid {
			unitOutlineMap[u.UnitID] = u.OutlineID.String
		}
	}

	groups := make(map[string][]KnowledgePoint)
	ungrouped := "ungrouped"

	for _, p := range points {
		outlineID := unitOutlineMap[p.UnitID]
		if outlineID == "" {
			groups[ungrouped] = append(groups[ungrouped], p)
			continue
		}
		grouped := false
		for _, top := range topOutlines {
			if outlineID == top.OutlineID {
				groups[top.OutlineID] = append(groups[top.OutlineID], p)
				grouped = true
				break
			}
		}
		if !grouped {
			groups[ungrouped] = append(groups[ungrouped], p)
		}
	}

	var result [][]KnowledgePoint
	for _, g := range groups {
		result = append(result, g)
	}
	return result
}

func (s *Service) kpnBatch(ctx context.Context, points []KnowledgePoint, unitCenterMap map[string]string) {
	var sb strings.Builder
	pointIDs := make(map[string]bool)
	for _, p := range points {
		center := unitCenterMap[p.UnitID]
		sb.WriteString(p.PointID)
		sb.WriteString("\t")
		sb.WriteString(center)
		sb.WriteString("\t")
		sb.WriteString(p.Content)
		sb.WriteString("\n")
		pointIDs[p.PointID] = true
	}

	schemaJSON := `{"relations":[{"from":"point_id","to":"point_id","type":"related|contradicts"}]}`

	vars := map[string]string{
		"knowledge_points": sb.String(),
		"json_schema":      schemaJSON,
	}

	data, err := s.llmClient.CompleteJSON(ctx, "kpn_extract.md", vars, "extraction")
	if err != nil {
		slog.Warn("unit: kpn extraction failed", "error", err)
		return
	}

	var output kpnOutput
	if err := json.Unmarshal(data, &output); err != nil {
		slog.Warn("unit: kpn JSON parse failed", "error", err)
		return
	}

	for _, rel := range output.Relations {
		if !pointIDs[rel.From] || !pointIDs[rel.To] {
			continue
		}
		if rel.From == rel.To {
			continue
		}

		if rel.Type != "related" && rel.Type != "contradicts" {
			continue
		}

		r := &KnowledgePointRelation{
			SourcePointID: rel.From,
			TargetPointID: rel.To,
			RelationType:  rel.Type,
			Direction:     "bidirectional",
			PromptVersion: "v3",
			Scope:         RelationScopeIntra,
		}
		if _, err := s.store.InsertRelation(r); err != nil {
			slog.Error("unit: insert kpn relation failed", "error", err)
		}
	}
}

type conceptMatch struct {
	UnitID    string `json:"unit_id"`
	ConceptID string `json:"concept_id"`
}

type conceptMatchOutput struct {
	Matches []conceptMatch `json:"matches"`
}

func (s *Service) matchConcepts(ctx context.Context, sourceID string, domainID sql.NullString) {
	units, err := s.store.GetCompletedUnitsBySourceID(sourceID)
	if err != nil || len(units) == 0 {
		return
	}

	did := ""
	if domainID.Valid {
		did = domainID.String
	}
	concepts, err := s.store.GetConceptsByDomainID(did)
	if err != nil {
		slog.Warn("unit: get concepts failed", "error", err)
		return
	}
	if len(concepts) == 0 {
		return
	}

	var conceptList strings.Builder
	for _, c := range concepts {
		conceptList.WriteString(fmt.Sprintf("[%s] %s：%s\n", c.ConceptID, c.Name, c.Description))
	}

	for i := 0; i < len(units); i += 50 {
		end := i + 50
		if end > len(units) {
			end = len(units)
		}
		s.matchConceptBatch(ctx, units[i:end], conceptList.String())
	}
}

func (s *Service) matchConceptBatch(ctx context.Context, units []KnowledgeUnit, conceptList string) {
	var unitsList strings.Builder
	unitIDSet := make(map[string]bool)
	for _, u := range units {
		unitsList.WriteString(fmt.Sprintf("[%s] %s\n", u.UnitID, u.Center))
		unitIDSet[u.UnitID] = true
	}

	schemaJSON := `{"matches":[{"unit_id":"unit_uuid_xxx","concept_id":"xxx"}]}`

	vars := map[string]string{
		"units_list":   unitsList.String(),
		"concept_list": conceptList,
		"json_schema":  schemaJSON,
	}

	data, err := s.llmClient.CompleteJSON(ctx, "unit_concept_match.md", vars, "extraction")
	if err != nil {
		slog.Warn("unit: concept match LLM failed", "error", err)
		return
	}

	var output conceptMatchOutput
	if err := json.Unmarshal(data, &output); err != nil {
		slog.Warn("unit: concept match JSON parse failed", "error", err)
		return
	}

	for _, m := range output.Matches {
		if !unitIDSet[m.UnitID] {
			continue
		}
		if m.ConceptID == "" {
			continue
		}
		if err := s.store.UpdateUnitConceptID(m.UnitID, &m.ConceptID); err != nil {
			slog.Warn("unit: update concept_id failed", "unit_id", m.UnitID, "error", err)
		}
	}
}

func (s *Service) indexUnit(ku *KnowledgeUnit, mdLines []string) {
	if s.unitsIndex == nil {
		return
	}
	content := sliceLines(mdLines, ku.LineStart, ku.LineEnd)
	lifecycle := ku.Lifecycle
	if lifecycle == "" {
		lifecycle = LifecycleCurrent
	}
	doc := map[string]interface{}{
		"unit_id":    ku.UnitID,
		"source_id":  ku.SourceID,
		"center":     ku.Center,
		"line_start": ku.LineStart,
		"line_end":   ku.LineEnd,
		"content":    content,
		"lifecycle":  lifecycle,
	}
	if err := s.unitsIndex.Index(ku.UnitID, doc); err != nil {
		slog.Error("unit: bleve index unit failed", "error", err)
	}
}

func (s *Service) indexPoint(kp *KnowledgePoint) {
	if s.pointsIndex == nil {
		return
	}
	lifecycle := kp.Lifecycle
	if lifecycle == "" {
		lifecycle = LifecycleCurrent
	}
	doc := map[string]interface{}{
		"point_id":   kp.PointID,
		"unit_id":    kp.UnitID,
		"source_id":  kp.SourceID,
		"content":    kp.Content,
		"point_type": kp.PointType,
		"lifecycle":  lifecycle,
	}
	if err := s.pointsIndex.Index(kp.PointID, doc); err != nil {
		slog.Error("unit: bleve index point failed", "error", err)
	}
}

// SetUnitLifecycle is the single entry point for changing KU lifecycle state
// (docs/impl/v1/lifecycle.md 步骤 1). It cascades to the units' KPs, keeps
// Bleve in sync, logs the reason, and notifies the Wiki module if wired in.
// Business code must never UPDATE the lifecycle column directly.
func (s *Service) SetUnitLifecycle(unitIDs []string, lifecycle, reason string) error {
	if len(unitIDs) == 0 {
		return nil
	}
	switch lifecycle {
	case LifecycleCurrent, LifecycleSuperseded, LifecycleDeprecated:
	default:
		return fmt.Errorf("unit: invalid lifecycle %q", lifecycle)
	}

	units, err := s.store.GetUnitsByIDs(unitIDs)
	if err != nil {
		return fmt.Errorf("unit: set lifecycle: get units: %w", err)
	}

	if err := s.store.UpdateUnitsLifecycle(unitIDs, lifecycle); err != nil {
		return fmt.Errorf("unit: set lifecycle: update units: %w", err)
	}
	if err := s.store.UpdatePointsLifecycleByUnitIDs(unitIDs, lifecycle); err != nil {
		return fmt.Errorf("unit: set lifecycle: update points: %w", err)
	}

	points, err := s.store.GetPointsByUnitIDs(unitIDs)
	if err != nil {
		slog.Warn("unit: set lifecycle: get points for reindex failed", "error", err)
	}

	s.reindexLifecycle(units, points, lifecycle)

	slog.Info("unit: lifecycle changed", "unit_ids", unitIDs, "lifecycle", lifecycle, "reason", reason)

	if s.wikiNotifier != nil {
		pointIDs := make([]string, len(points))
		for i, p := range points {
			pointIDs[i] = p.PointID
		}
		if err := s.wikiNotifier.NotifyPointsLifecycleChanged(pointIDs); err != nil {
			slog.Warn("unit: wiki notify failed", "error", err)
		}
	}

	if s.activationNotifier != nil {
		if err := s.activationNotifier.InvalidateCache(); err != nil {
			slog.Warn("unit: activation notify failed", "error", err)
		}
	}

	return nil
}

// SnapshotAndDeprecate implements the Source module's soft-delete lifecycle
// step: before marking every one of a Source's KUs deprecated, it snapshots
// each one's current lifecycle value so a later RestoreLifecycle call can
// reverse the change precisely — resetting only the ones that were current
// at delete time, not ones already superseded by an earlier reupload.
func (s *Service) SnapshotAndDeprecate(unitIDs []string, reason string) error {
	if len(unitIDs) == 0 {
		return nil
	}
	if err := s.store.SnapshotLifecycleBeforeDelete(unitIDs); err != nil {
		return fmt.Errorf("unit: snapshot and deprecate: %w", err)
	}
	return s.SetUnitLifecycle(unitIDs, LifecycleDeprecated, reason)
}

// RestoreLifecycle reverses a prior SnapshotAndDeprecate: each unit is set
// back to its snapshotted pre-delete lifecycle value (grouped, since a
// source's units may have held different lifecycle states at delete time),
// then the snapshot is cleared. Units with no snapshot (never soft-deleted)
// are left untouched.
func (s *Service) RestoreLifecycle(unitIDs []string, reason string) error {
	if len(unitIDs) == 0 {
		return nil
	}
	groups, err := s.store.GroupUnitIDsByLifecycleBeforeDelete(unitIDs)
	if err != nil {
		return fmt.Errorf("unit: restore lifecycle: %w", err)
	}
	for lifecycle, ids := range groups {
		if err := s.SetUnitLifecycle(ids, lifecycle, reason); err != nil {
			return fmt.Errorf("unit: restore lifecycle: %w", err)
		}
	}
	if err := s.store.ClearLifecycleBeforeDelete(unitIDs); err != nil {
		return fmt.Errorf("unit: restore lifecycle: clear snapshot: %w", err)
	}
	return nil
}

// reindexLifecycle rewrites the affected units/points into Bleve with their new
// lifecycle value. Bleve documents are replaced wholesale (no partial update),
// so unit content is re-sliced from the owning source's markdown file.
func (s *Service) reindexLifecycle(units []KnowledgeUnit, points []KnowledgePoint, lifecycle string) {
	mdCache := make(map[string][]string)
	pointsByUnit := make(map[string][]KnowledgePoint)
	for _, p := range points {
		pointsByUnit[p.UnitID] = append(pointsByUnit[p.UnitID], p)
	}

	for _, ku := range units {
		ku := ku
		ku.Lifecycle = lifecycle

		mdLines, cached := mdCache[ku.SourceID]
		if !cached {
			src, err := s.sourceStore.GetByID(ku.SourceID)
			if err != nil {
				slog.Warn("unit: reindex lifecycle: get source failed", "source_id", ku.SourceID, "error", err)
			} else if data, err := os.ReadFile(src.MarkdownPath); err != nil {
				slog.Warn("unit: reindex lifecycle: read markdown failed", "source_id", ku.SourceID, "error", err)
			} else {
				mdLines = strings.Split(string(data), "\n")
			}
			mdCache[ku.SourceID] = mdLines
		}
		s.indexUnit(&ku, mdLines)

		for _, kp := range pointsByUnit[ku.UnitID] {
			kp := kp
			kp.Lifecycle = lifecycle
			s.indexPoint(&kp)
		}
	}
}
