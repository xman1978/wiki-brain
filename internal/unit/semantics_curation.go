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

// ErrNotManualPoint is returned when POST /points/:id/deprecate targets a
// non-manual (LLM-extracted) KP — only manually_edited rows may be revoked
// this way; extracted points are retired via re-extract / reupload.
var ErrNotManualPoint = errors.New("unit: only manually-added points can be deprecated via this endpoint")

var validPointTypes = map[string]bool{
	"definition": true,
	"rule":       true,
	"method":     true,
	"case":       true,
	"question":   true,
}

// PointSemanticsView is the full curation view of one KP: the read-only KP
// fields (content/type, plus its owning unit's outline context) and its
// editable rerank semantics (source_theme/content_theme/object/scope, which
// live directly on knowledge_points — docs/impl/v1/semantics-curation.md
// 2026-08-21 改判: 下沉自 KU 级 unit_rerank_semantics 到 KP 级).
type PointSemanticsView struct {
	Point KnowledgePoint
	// OutlinePath is the owning unit's outline chain root→leaf joined with
	// " / "; empty for standalone units (coverage-fix 产物没有 outline_id).
	OutlinePath     string
	OutlineNodeType string
}

func (s *Service) GetPointSemanticsView(pointID string) (*PointSemanticsView, error) {
	kp, err := s.store.GetPointByID(pointID)
	if err != nil {
		return nil, err
	}

	ku, err := s.store.GetUnitByID(kp.UnitID)
	if err != nil {
		return nil, err
	}

	view := &PointSemanticsView{Point: *kp}
	if ku.OutlineID.Valid {
		path, nodeType, err := s.store.GetOutlinePath(ku.OutlineID.String)
		if err != nil {
			// 目录只是展示辅助信息，查不到不阻塞语义编辑本身。
			slog.Warn("unit: semantics view: outline path lookup failed", "point_id", pointID, "outline_id", ku.OutlineID.String, "error", err)
		} else {
			view.OutlinePath = path
			view.OutlineNodeType = nodeType
		}
	}

	return view, nil
}

// UpdatePointSemantics persists a human-curated semantics row for a current
// KP. sem must already be shape-validated by the caller (source_theme/
// content_theme/scope non-empty, object may be blank) — the same shape
// kp_semantics_extract.md produces, so rerank's readers never need to care
// where a row came from.
func (s *Service) UpdatePointSemantics(pointID string, sem rerank.Semantics) error {
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
	return s.store.UpsertManualPointSemantics(pointID, sem, rerank.ExtractPromptVersion)
}

// readUnitContent slices the unit's verbatim content out of the markdown
// that belongs to this unit's lifecycle version (current path, or archived
// path for superseded — historical evidence backlink design). Line range is
// 1-based inclusive.
func (s *Service) readUnitContent(ku *KnowledgeUnit) (string, error) {
	relPath, err := s.sourceStore.ResolveMarkdownPathForUnit(ku.SourceID, ku.Lifecycle, ku.LifecycleChangedAt)
	if err != nil {
		return "", fmt.Errorf("unit: read content: resolve markdown: %w", err)
	}
	mdBytes, err := os.ReadFile(relPath)
	if err != nil {
		return "", fmt.Errorf("unit: read content: read markdown: %w", err)
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

// DeprecateManualPoint revokes a human-added KP (POST /points/:id/deprecate):
// sets lifecycle=deprecated so retrieval / qualifying stop seeing it, without
// hard-deleting the row (KPN / ActivationLink / Wiki citations may still
// reference point_id). Only manually_edited=1 points are eligible; already-
// deprecated is idempotent success.
func (s *Service) DeprecateManualPoint(pointID string) (*KnowledgePoint, error) {
	kp, err := s.store.GetPointByID(pointID)
	if err != nil {
		return nil, err
	}
	if !kp.ManuallyEdited {
		return nil, ErrNotManualPoint
	}
	if kp.Lifecycle == LifecycleDeprecated {
		return kp, nil
	}
	if err := s.store.UpdatePointLifecycle(pointID, LifecycleDeprecated); err != nil {
		return nil, err
	}
	kp.Lifecycle = LifecycleDeprecated
	s.indexPoint(kp)
	slog.Info("unit: deprecated manual point", "point_id", pointID, "unit_id", kp.UnitID)
	return kp, nil
}
