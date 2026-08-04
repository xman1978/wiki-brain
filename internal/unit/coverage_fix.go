package unit

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/source"
)

// FixCoverageGap manually recovers one gap surfaced by SourceCoverageReport
// (GET /sources/:id/coverage): a line range inside a completed Source's
// current markdown that ended up in no completed KnowledgeUnit. The most
// common cause is not a boundary-extraction miss but a unit that was
// correctly located and pointed, then discarded at publish time because
// rerank-semantics extraction couldn't produce a result for it even after
// every fallback tier (see rerank_semantics.go) — often just LLM
// non-determinism on content that is perfectly extractable in isolation.
//
// This treats the gap's own line range as a fixed, already-known boundary —
// no unit_boundary_extract.md call — and only re-runs point extraction
// (unit_point_extract.md) and rerank-semantics extraction
// (unit_semantics_extract.md, via the same multi-tier retry as normal
// extraction). On success the result is inserted as one new standalone
// current knowledge unit alongside whatever units already exist for the
// source (see Store.InsertStandaloneUnit) — it never touches or supersedes
// them — and then fed through the same downstream steps a normal extraction
// run would trigger, so it participates in cross-Source KPN matching,
// concept candidates, and Wiki/Activation invalidation like any other unit.
func (s *Service) FixCoverageGap(ctx context.Context, sourceID string, lineStart, lineEnd int) (*KnowledgeUnit, error) {
	if lineStart < 1 || lineEnd < lineStart {
		return nil, fmt.Errorf("unit: fix coverage gap: invalid range %d-%d", lineStart, lineEnd)
	}

	src, err := s.sourceStore.GetByID(sourceID)
	if err != nil {
		return nil, fmt.Errorf("unit: fix coverage gap: get source: %w", err)
	}
	if src.Status != "completed" {
		return nil, fmt.Errorf("unit: fix coverage gap: source %s status is %q, need completed", sourceID, src.Status)
	}

	mdBytes, err := os.ReadFile(src.MarkdownPath)
	if err != nil {
		return nil, fmt.Errorf("unit: fix coverage gap: read markdown: %w", err)
	}
	mdLines := strings.Split(string(mdBytes), "\n")
	if lineEnd > len(mdLines) {
		return nil, fmt.Errorf("unit: fix coverage gap: range %d-%d exceeds document length %d", lineStart, lineEnd, len(mdLines))
	}

	current, err := s.store.GetUnitsBySourceIDFiltered(sourceID, LifecycleCurrent)
	if err != nil {
		return nil, fmt.Errorf("unit: fix coverage gap: get current units: %w", err)
	}
	for _, u := range current {
		if u.Status != "completed" {
			continue
		}
		if lineStart <= u.LineEnd && lineEnd >= u.LineStart {
			return nil, fmt.Errorf("unit: fix coverage gap: range %d-%d overlaps existing unit %s (L%d-L%d) — refresh coverage and retry", lineStart, lineEnd, u.UnitID, u.LineStart, u.LineEnd)
		}
	}

	outlines, err := s.sourceStore.GetOutlines(sourceID)
	if err != nil {
		return nil, fmt.Errorf("unit: fix coverage gap: get outlines: %w", err)
	}
	var outlineID string
	bestSpan := -1
	for _, o := range outlines {
		if o.LineStart <= lineStart && o.LineEnd >= lineEnd {
			span := o.LineEnd - o.LineStart
			if bestSpan == -1 || span < bestSpan {
				bestSpan = span
				outlineID = o.OutlineID
			}
		}
	}

	unitID := uuid.New().String()
	u := llmUnit{
		UnitID:          unitID,
		LineStart:       lineStart,
		FirstLineAnchor: mdLines[lineStart-1],
		LineEnd:         lineEnd,
		LastLineAnchor:  mdLines[lineEnd-1],
	}
	unitContent := sliceLinesWithLineNumbers(mdLines, lineStart, lineEnd)

	center, points, ok, err := s.extractPointsForSplitUnit(ctx, u, unitContent)
	if err != nil {
		return nil, fmt.Errorf("unit: fix coverage gap: extract points: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("unit: fix coverage gap: no extractable knowledge points for range %d-%d", lineStart, lineEnd)
	}
	if strings.TrimSpace(center) != "" {
		u.Center = strings.TrimSpace(center)
	} else {
		u.Center = fallbackCenterFromLines(mdLines, lineStart, lineEnd)
	}

	candidate := rerankSemanticCandidate{
		id:      unitID,
		content: strings.Join(mdLines[lineStart-1:lineEnd], "\n"),
	}
	semantics, err := s.extractRerankSemanticBatch(ctx, src.Title, []rerankSemanticCandidate{candidate})
	if err != nil {
		return nil, fmt.Errorf("unit: fix coverage gap: extract semantics: %w", err)
	}
	sem, ok := semantics[unitID]
	if !ok {
		return nil, fmt.Errorf("unit: fix coverage gap: semantics extraction returned no result for range %d-%d, try again", lineStart, lineEnd)
	}

	ku := &KnowledgeUnit{
		UnitID:        unitID,
		SourceID:      sourceID,
		Center:        u.Center,
		LineStart:     lineStart,
		LineEnd:       lineEnd,
		Status:        "completed",
		PromptVersion: promptVersionSplitExtract,
		Lifecycle:     LifecycleCurrent,
	}
	if outlineID != "" {
		ku.OutlineID = sql.NullString{String: outlineID, Valid: true}
	}

	kps := make([]KnowledgePoint, 0, len(points))
	for _, p := range points {
		kps = append(kps, KnowledgePoint{
			SourceID:  sourceID,
			Content:   p.Content,
			PointType: p.Type,
			Lifecycle: LifecycleCurrent,
		})
	}

	if err := s.store.InsertStandaloneUnit(ku, kps, sem); err != nil {
		return nil, fmt.Errorf("unit: fix coverage gap: persist: %w", err)
	}

	if err := s.indexUnitWithError(ku, mdLines); err != nil {
		slog.Warn("unit: fix coverage gap: index unit failed", "unit_id", unitID, "error", err)
	}
	for i := range kps {
		if err := s.indexPointWithError(&kps[i]); err != nil {
			slog.Warn("unit: fix coverage gap: index point failed", "point_id", kps[i].PointID, "error", err)
		}
	}

	// Downstream integration, same as a normal extraction run (Service.
	// Extract) — these all re-scan the source's full current point/unit set
	// from the store, so the newly inserted unit is picked up automatically
	// without any special-casing here.
	s.generateKPN(ctx, sourceID)
	s.matchEntries(ctx, sourceID, src.DomainID)
	if _, err := s.CrossSourceKPN(ctx, sourceID); err != nil {
		slog.Warn("unit: fix coverage gap: cross source kpn failed", "source_id", sourceID, "error", err)
	}
	if s.wikiNotifier != nil {
		pointIDs := make([]string, len(kps))
		for i, p := range kps {
			pointIDs[i] = p.PointID
		}
		if err := s.wikiNotifier.NotifyPointsLifecycleChanged(pointIDs); err != nil {
			slog.Warn("unit: fix coverage gap: wiki notify failed", "error", err)
		}
	}
	if s.activationNotifier != nil {
		if err := s.activationNotifier.InvalidateCache(); err != nil {
			slog.Warn("unit: fix coverage gap: activation notify failed", "error", err)
		}
	}

	slog.Info("unit: coverage gap fixed", "source_id", sourceID, "unit_id", unitID, "line_start", lineStart, "line_end", lineEnd)

	return ku, nil
}

const (
	MergeDirectionPrev = "prev"
	MergeDirectionNext = "next"
)

// resolveMergeTarget loads a source's markdown and current completed units
// and finds the neighbor a gap would merge into for the given direction,
// without changing anything. Shared by PreviewMergeTarget (read-only, for
// the frontend's confirmation dialog) and MergeCoverageGap (which performs
// the actual widen after resolving the same target) so the two can never
// disagree about which unit "prev"/"next" means for a given gap.
func (s *Service) resolveMergeTarget(sourceID string, lineStart, lineEnd int, direction string) (target *KnowledgeUnit, completed []KnowledgeUnit, mdLines []string, src *source.Source, err error) {
	if lineStart < 1 || lineEnd < lineStart {
		return nil, nil, nil, nil, fmt.Errorf("invalid range %d-%d", lineStart, lineEnd)
	}
	if direction != MergeDirectionPrev && direction != MergeDirectionNext {
		return nil, nil, nil, nil, fmt.Errorf("invalid direction %q, want %q or %q", direction, MergeDirectionPrev, MergeDirectionNext)
	}

	src, err = s.sourceStore.GetByID(sourceID)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("get source: %w", err)
	}
	if src.Status != "completed" {
		return nil, nil, nil, nil, fmt.Errorf("source %s status is %q, need completed", sourceID, src.Status)
	}

	mdBytes, err := os.ReadFile(src.MarkdownPath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("read markdown: %w", err)
	}
	mdLines = strings.Split(string(mdBytes), "\n")
	if lineEnd > len(mdLines) {
		return nil, nil, nil, nil, fmt.Errorf("range %d-%d exceeds document length %d", lineStart, lineEnd, len(mdLines))
	}

	current, err := s.store.GetUnitsBySourceIDFiltered(sourceID, LifecycleCurrent)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("get current units: %w", err)
	}
	completed = make([]KnowledgeUnit, 0, len(current))
	for _, u := range current {
		if u.Status == "completed" {
			completed = append(completed, u)
		}
	}
	for _, u := range completed {
		if lineStart <= u.LineEnd && lineEnd >= u.LineStart {
			return nil, nil, nil, nil, fmt.Errorf("range %d-%d overlaps existing unit %s (L%d-L%d) — refresh coverage and retry", lineStart, lineEnd, u.UnitID, u.LineStart, u.LineEnd)
		}
	}

	for i := range completed {
		u := &completed[i]
		switch direction {
		case MergeDirectionPrev:
			if u.LineEnd < lineStart && (target == nil || u.LineEnd > target.LineEnd) {
				target = u
			}
		case MergeDirectionNext:
			if u.LineStart > lineEnd && (target == nil || u.LineStart < target.LineStart) {
				target = u
			}
		}
	}
	if target == nil {
		dirLabel := "上一个"
		if direction == MergeDirectionNext {
			dirLabel = "下一个"
		}
		return nil, nil, nil, nil, fmt.Errorf("no %s knowledge unit to merge range %d-%d into", dirLabel, lineStart, lineEnd)
	}

	return target, completed, mdLines, src, nil
}

// mergePreviewMaxRunes caps how much of the target unit's raw text
// PreviewMergeTarget returns — long enough for a human to judge whether the
// merge makes sense, short enough to stay readable in a confirmation dialog.
const mergePreviewMaxRunes = 300

// MergeTargetPreview is what the frontend shows in the confirmation dialog
// before actually merging — the neighbor's identity, line range, and a
// content preview, all read-only.
type MergeTargetPreview struct {
	UnitID    string `json:"unit_id"`
	Center    string `json:"center"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Content   string `json:"content"`
}

// PreviewMergeTarget resolves which neighbor MergeCoverageGap would merge
// into for the same (lineStart, lineEnd, direction) without changing
// anything, so the UI can show the target unit's content and ask for
// confirmation before the widen happens — MergeCoverageGap doesn't
// re-derive center/points afterward, so a wrong merge isn't self-correcting.
func (s *Service) PreviewMergeTarget(sourceID string, lineStart, lineEnd int, direction string) (*MergeTargetPreview, error) {
	target, _, mdLines, _, err := s.resolveMergeTarget(sourceID, lineStart, lineEnd, direction)
	if err != nil {
		return nil, fmt.Errorf("unit: preview merge target: %w", err)
	}
	return &MergeTargetPreview{
		UnitID:    target.UnitID,
		Center:    target.Center,
		LineStart: target.LineStart,
		LineEnd:   target.LineEnd,
		Content:   truncateRunes(sliceLines(mdLines, target.LineStart, target.LineEnd), mergePreviewMaxRunes),
	}, nil
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// MergeCoverageGap manually recovers one gap the same way the automatic
// extraction-time gap-fill already does for gaps it finds during its own
// pass (see mergeGapIntoNeighborInMemory in predup.go): absorb the gap's
// line range into a neighboring unit by widening that unit's bounds, with no
// new point and no LLM call — the gap's content wasn't judged to warrant its
// own summary, which is exactly the case for content like `/etc/hosts`-style
// single-line entries that FixCoverageGap's point extraction has nothing to
// say about. direction picks which neighbor: "prev" absorbs into the nearest
// completed current unit ending before the gap, "next" into the nearest one
// starting after it. Unlike FixCoverageGap, this never creates a new unit or
// point, so it skips the KPN/concept/Wiki/Activation downstream steps — the
// only externally visible effect is the widened unit's own Bleve document
// picking up the gap's text.
func (s *Service) MergeCoverageGap(ctx context.Context, sourceID string, lineStart, lineEnd int, direction string) (*KnowledgeUnit, error) {
	target, completed, mdLines, _, err := s.resolveMergeTarget(sourceID, lineStart, lineEnd, direction)
	if err != nil {
		return nil, fmt.Errorf("unit: merge coverage gap: %w", err)
	}

	newStart, newEnd := target.LineStart, target.LineEnd
	if lineStart < newStart {
		newStart = lineStart
	}
	if lineEnd > newEnd {
		newEnd = lineEnd
	}
	for _, u := range completed {
		if u.UnitID == target.UnitID {
			continue
		}
		if newStart <= u.LineEnd && newEnd >= u.LineStart {
			return nil, fmt.Errorf("unit: merge coverage gap: widening unit %s to L%d-L%d would overlap unit %s (L%d-L%d) — refresh coverage and retry", target.UnitID, newStart, newEnd, u.UnitID, u.LineStart, u.LineEnd)
		}
	}

	if err := s.store.UpdateUnitBounds(target.UnitID, newStart, newEnd); err != nil {
		return nil, fmt.Errorf("unit: merge coverage gap: %w", err)
	}

	updated := *target
	updated.LineStart = newStart
	updated.LineEnd = newEnd
	if err := s.indexUnitWithError(&updated, mdLines); err != nil {
		slog.Warn("unit: merge coverage gap: index unit failed", "unit_id", target.UnitID, "error", err)
	}

	slog.Info("unit: coverage gap merged", "source_id", sourceID, "unit_id", target.UnitID, "direction", direction,
		"gap_line_start", lineStart, "gap_line_end", lineEnd, "new_line_start", newStart, "new_line_end", newEnd)

	return &updated, nil
}
