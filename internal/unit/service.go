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
	store       *Store
	sourceStore *source.Store
	llmClient   llm.LLMClient
	unitsIndex  bleve.Index
	pointsIndex bleve.Index
	queue       *queue.Queue
	cfg         *config.Config
	broadcaster *progress.Broadcaster
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

	s.emit(sourceID, progress.Event{Step: "done", Status: progress.StatusCompleted, Message: "处理完成"})

	return nil
}

type llmUnit struct {
	UnitID    string `json:"unit_id"`
	Center    string `json:"center"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
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

	schemaJSON := `{"units":[{"unit_id":"1","center":"知识单元主题","line_start":1,"line_end":8}],"points":[{"point_id":"1","unit_id":"1","content":"可激活摘要内容","type":"definition|rule|method|case|question"}]}`

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

	for _, u := range output.Units {
		if !s.validateUnit(u, output.Points) {
			s.handleFailedUnit(ctx, sourceID, seg, u, mdLines, localToUUID)
			continue
		}

		realUnitID := localToUUID[u.UnitID]

		ku := &KnowledgeUnit{
			UnitID:        realUnitID,
			SourceID:      sourceID,
			OutlineID:     seg.OutlineID,
			Center:        u.Center,
			LineStart:     u.LineStart,
			LineEnd:       u.LineEnd,
			Status:        "completed",
			PromptVersion: "v3",
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
	if u.LineStart > u.LineEnd {
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
	schemaJSON := `{"units":[{"unit_id":"1","center":"知识单元主题","line_start":1,"line_end":8}],"points":[{"point_id":"1","unit_id":"1","content":"可激活摘要内容","type":"definition|rule|method|case|question"}]}`

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
		if !s.validateUnit(ru, output.Points) {
			continue
		}
		realUnitID := uuid.New().String()

		ku := &KnowledgeUnit{
			UnitID:        realUnitID,
			SourceID:      sourceID,
			OutlineID:     seg.OutlineID,
			Center:        ru.Center,
			LineStart:     ru.LineStart,
			LineEnd:       ru.LineEnd,
			Status:        "completed",
			PromptVersion: "v3",
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
	lineStart := seg.LineStart
	lineEnd := seg.LineEnd
	if u.LineStart > 0 && u.LineEnd > 0 {
		lineStart = u.LineStart
		lineEnd = u.LineEnd
	}

	ku := &KnowledgeUnit{
		UnitID:        uuid.New().String(),
		SourceID:      sourceID,
		OutlineID:     seg.OutlineID,
		Center:        u.Center,
		LineStart:     lineStart,
		LineEnd:       lineEnd,
		Status:        "extraction_failed",
		ErrorMsg:      sql.NullString{String: errMsg, Valid: true},
		PromptVersion: "v3",
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
		}
		if err := s.store.InsertRelation(r); err != nil {
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
	doc := map[string]interface{}{
		"unit_id":    ku.UnitID,
		"source_id":  ku.SourceID,
		"center":     ku.Center,
		"line_start": ku.LineStart,
		"line_end":   ku.LineEnd,
		"content":    content,
	}
	if err := s.unitsIndex.Index(ku.UnitID, doc); err != nil {
		slog.Error("unit: bleve index unit failed", "error", err)
	}
}

func (s *Service) indexPoint(kp *KnowledgePoint) {
	if s.pointsIndex == nil {
		return
	}
	doc := map[string]interface{}{
		"point_id":   kp.PointID,
		"unit_id":    kp.UnitID,
		"source_id":  kp.SourceID,
		"content":    kp.Content,
		"point_type": kp.PointType,
	}
	if err := s.pointsIndex.Index(kp.PointID, doc); err != nil {
		slog.Error("unit: bleve index point failed", "error", err)
	}
}
