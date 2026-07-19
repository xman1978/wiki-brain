package unit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/rerank"
)

// 人工修正（docs/impl/v1/semantics-curation.md）：KU 本体（center、行号、正文）
// 是提取产物，只读；rerank 语义（source_theme/content_theme/intent/object/
// scope）与 KP（knowledge_points）是人工修正权落地的两个对象——KP 取代了旧版
// key_facts，成为 rerank judge 事实来源与人工补漏的统一载体。

// ErrUnitNotCurrent is returned when curation targets a superseded or
// deprecated KU — those never participate in retrieval, so editing their
// semantics/points is meaningless and almost certainly a mistake (e.g. a
// stale page still showing pre-reupload units).
var ErrUnitNotCurrent = errors.New("unit: semantics curation requires a current-lifecycle unit")

// ErrInvalidPointType is returned when a manually supplied point_type isn't
// one of the five values the extraction prompts (unit_point_extract.md 等)
// already constrain the LLM to.
var ErrInvalidPointType = errors.New("unit: point_type must be one of definition/rule/method/case/question")

// ErrEmptyPointContent is returned when a manual KP's content is blank.
var ErrEmptyPointContent = errors.New("unit: point content must not be empty")

var validPointTypes = map[string]bool{
	"definition": true,
	"rule":       true,
	"method":     true,
	"case":       true,
	"question":   true,
}

// SemanticsView is the full curation view of one KU: the read-only unit
// fields (including the verbatim content slice, so the curator can see what
// the summaries missed) plus the editable semantics row (nil when missing).
type SemanticsView struct {
	Unit      SemanticsViewUnit
	Semantics *RerankSemanticsRow
}

type SemanticsViewUnit struct {
	UnitID    string
	SourceID  string
	Center    string
	LineStart int
	LineEnd   int
	Lifecycle string
	Content   string
	// OutlinePath is the unit's owning outline chain root→leaf joined with
	// " / "（如"第三章 积分结果公布及应用"）; empty for standalone units
	// (coverage-fix 产物没有 outline_id). OutlineNodeType is the leaf node's
	// node_type（structural / semantic）.
	OutlinePath     string
	OutlineNodeType string
}

func (s *Service) GetUnitSemanticsView(unitID string) (*SemanticsView, error) {
	ku, err := s.store.GetUnitByID(unitID)
	if err != nil {
		return nil, err
	}

	content, err := s.readUnitContent(ku)
	if err != nil {
		return nil, err
	}

	sem, err := s.store.GetRerankSemanticsByUnitID(unitID)
	if err != nil {
		return nil, err
	}

	unitView := SemanticsViewUnit{
		UnitID:    ku.UnitID,
		SourceID:  ku.SourceID,
		Center:    ku.Center,
		LineStart: ku.LineStart,
		LineEnd:   ku.LineEnd,
		Lifecycle: ku.Lifecycle,
		Content:   content,
	}
	if ku.OutlineID.Valid {
		path, nodeType, err := s.store.GetOutlinePath(ku.OutlineID.String)
		if err != nil {
			// 目录只是展示辅助信息，查不到不阻塞语义编辑本身。
			slog.Warn("unit: semantics view: outline path lookup failed", "unit_id", unitID, "outline_id", ku.OutlineID.String, "error", err)
		} else {
			unitView.OutlinePath = path
			unitView.OutlineNodeType = nodeType
		}
	}

	return &SemanticsView{Unit: unitView, Semantics: sem}, nil
}

// UpdateUnitSemantics persists a human-curated semantics row for a current
// KU. sem must already be shape-validated by the caller (five non-empty
// fields) — the same shape unit_semantics_extract.md produces, so rerank's
// readers never need to care where a row came from.
func (s *Service) UpdateUnitSemantics(unitID string, sem rerank.Semantics) error {
	ku, err := s.store.GetUnitByID(unitID)
	if err != nil {
		return err
	}
	if ku.Lifecycle != LifecycleCurrent {
		return fmt.Errorf("%w: unit %s is %s", ErrUnitNotCurrent, unitID, ku.Lifecycle)
	}
	return s.store.UpsertManualRerankSemantics(unitID, sem, rerank.ExtractPromptVersion)
}

// readUnitContent slices the unit's verbatim content out of its source's
// markdown by line range (1-based, inclusive — the project-wide position
// convention).
func (s *Service) readUnitContent(ku *KnowledgeUnit) (string, error) {
	src, err := s.sourceStore.GetByID(ku.SourceID)
	if err != nil {
		return "", fmt.Errorf("unit: semantics view: get source: %w", err)
	}
	mdBytes, err := os.ReadFile(src.MarkdownPath)
	if err != nil {
		return "", fmt.Errorf("unit: semantics view: read markdown: %w", err)
	}
	lines := strings.Split(string(mdBytes), "\n")

	from, to := ku.LineStart, ku.LineEnd
	if from < 1 {
		from = 1
	}
	if to > len(lines) {
		to = len(lines)
	}
	if from > to {
		return "", nil
	}
	return strings.Join(lines[from-1:to], "\n"), nil
}

// ManualPointResult is what POST /units/:id/points reports back: the created
// KP plus how many KPN relations the follow-up incremental analysis found.
type ManualPointResult struct {
	Point            KnowledgePoint
	RelationsCreated int
}

// AddManualPoint inserts a human-added KP under unitID (docs/impl/v1/
// semantics-curation.md "KP 人工修正") — the mechanism that replaces manual
// key_facts editing: KP is the same "fact extracted from this KU" data
// rerank judge reads, so a fact an LLM extraction pass missed is added here
// directly instead of into a separate summary field. Requires the KU to be
// current; the new KP then runs an incremental KPN pass against the rest of
// its Source (see incrementalKPNForPoint) so it doesn't sit outside the
// knowledge graph other KPs participate in.
func (s *Service) AddManualPoint(ctx context.Context, unitID, content, pointType string) (*ManualPointResult, error) {
	ku, err := s.store.GetUnitByID(unitID)
	if err != nil {
		return nil, err
	}
	if ku.Lifecycle != LifecycleCurrent {
		return nil, fmt.Errorf("%w: unit %s is %s", ErrUnitNotCurrent, unitID, ku.Lifecycle)
	}
	if strings.TrimSpace(content) == "" {
		return nil, ErrEmptyPointContent
	}
	if !validPointTypes[pointType] {
		return nil, fmt.Errorf("%w: got %q", ErrInvalidPointType, pointType)
	}

	point := KnowledgePoint{
		PointID:   uuid.New().String(),
		UnitID:    unitID,
		SourceID:  ku.SourceID,
		Content:   content,
		PointType: pointType,
		Lifecycle: LifecycleCurrent,
	}
	if err := s.store.InsertManualPoint(&point); err != nil {
		return nil, err
	}

	relationsCreated, err := s.incrementalKPNForPoint(ctx, point)
	if err != nil {
		// KPN 是知识图谱补全，不是 KP 本身是否写入成功的判据——已经插入的 KP 仍然
		// 有效、立即参与检索，关系补全失败只记录、不回滚整个新增操作。
		slog.Warn("unit: incremental kpn after manual point add failed", "point_id", point.PointID, "unit_id", unitID, "error", err)
		relationsCreated = 0
	}

	return &ManualPointResult{Point: point, RelationsCreated: relationsCreated}, nil
}

// UpdateManualPoint edits an existing KP's content/point_type. Unlike
// AddManualPoint it does not re-run KPN — the point's existing relations
// aren't invalidated by a wording tweak, and re-analyzing on every edit would
// pay an LLM call for something that's usually a small correction (see
// docs/impl/v1/semantics-curation.md).
func (s *Service) UpdateManualPoint(pointID, content, pointType string) error {
	kp, err := s.store.GetPointByID(pointID)
	if err != nil {
		return err
	}
	ku, err := s.store.GetUnitByID(kp.UnitID)
	if err != nil {
		return err
	}
	if ku.Lifecycle != LifecycleCurrent {
		return fmt.Errorf("%w: unit %s is %s", ErrUnitNotCurrent, ku.UnitID, ku.Lifecycle)
	}
	if strings.TrimSpace(content) == "" {
		return ErrEmptyPointContent
	}
	if !validPointTypes[pointType] {
		return fmt.Errorf("%w: got %q", ErrInvalidPointType, pointType)
	}
	return s.store.UpdateManualPoint(pointID, content, pointType)
}
