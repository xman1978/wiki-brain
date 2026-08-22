package unit

import (
	"context"
	"encoding/json"
	"log/slog"
)

type llmDedupPoint struct {
	Content string `json:"content"`
	Type    string `json:"type"`
	// SourceTheme/ContentTheme/Object/Scope/SemanticsPromptVersion carry a
	// point's own rerank semantics through the merge ladder. unit_dedup_merge.md
	// only ever regenerates Content/Type, so mergePairViaLLM's result must
	// have these fields re-attached by matching the merged content back to
	// whichever original a/b point it came from (see inheritMergedSemantics)
	// — an approximation, same caliber as Center/Points merging already is.
	SourceTheme            string `json:"-"`
	ContentTheme           string `json:"-"`
	Object                 string `json:"-"`
	Scope                  string `json:"-"`
	SemanticsPromptVersion string `json:"-"`
}

type llmDedupMerged struct {
	Center string          `json:"center"`
	Points []llmDedupPoint `json:"points"`
}

// llmClassifyOutput is unit_dedup_classify.md's response: judgment only,
// separated from merge generation (unit_dedup_merge.md) so the model isn't
// deciding "duplicate or not" while already committed to producing a merged
// result — the combined task measurably biased small models toward
// confirming duplicates just to have something to merge.
type llmClassifyOutput struct {
	Relation string `json:"relation"`
	Reason   string `json:"reason"`
}

// Relations unit_dedup_classify.md can return. Only duplicate leads to a
// merge; parent_child (overview vs independent detail), parallel (different
// members of the same parent topic), and distinct all keep both units.
const (
	relationDuplicate = "duplicate"
)

// classifyPair asks unit_dedup_classify.md how a and b relate. Returns the
// relation, or "" (treat as keep-both) on any call/parse failure — dedup is
// an optimization, never worth failing extraction over.
func (s *Service) classifyPair(ctx context.Context, sourceID string, vars map[string]string) string {
	data, err := s.llmClient.CompleteJSON(ctx, "unit_dedup_classify.md", vars, "extraction")
	if err != nil {
		slog.Warn("unit: dedup classify call failed", "source_id", sourceID, "error", err)
		return ""
	}
	var out llmClassifyOutput
	if err := json.Unmarshal(data, &out); err != nil {
		slog.Warn("unit: dedup classify JSON parse failed", "source_id", sourceID, "error", err)
		return ""
	}
	slog.Debug("unit: dedup classify", "source_id", sourceID, "relation", out.Relation, "reason", out.Reason)
	return out.Relation
}

// mergePairViaLLM asks unit_dedup_merge.md for the merged center/points of a
// pair already classified duplicate. nil on failure (caller keeps both).
func (s *Service) mergePairViaLLM(ctx context.Context, sourceID string, vars map[string]string) *llmDedupMerged {
	data, err := s.llmClient.CompleteJSON(ctx, "unit_dedup_merge.md", vars, "extraction")
	if err != nil {
		slog.Warn("unit: dedup merge call failed", "source_id", sourceID, "error", err)
		return nil
	}
	var merged llmDedupMerged
	if err := json.Unmarshal(data, &merged); err != nil {
		slog.Warn("unit: dedup merge JSON parse failed", "source_id", sourceID, "error", err)
		return nil
	}
	if merged.Center == "" || len(merged.Points) == 0 {
		return nil
	}
	return &merged
}

// deterministicMerge handles the one shape that needs no model judgment at
// all: identical line ranges AND identical normalized centers — the same
// span extracted twice under trivially-reworded titles (e.g. an extraction
// retry twin). Points are unioned with normalized-text dedup, preferring the
// longer (more complete) phrasing of each duplicate pair. Returns nil when
// the pair doesn't qualify.
func deterministicMerge(aCenter, bCenter string, aStart, aEnd, bStart, bEnd int, aPoints, bPoints []llmDedupPoint) *llmDedupMerged {
	if aStart != bStart || aEnd != bEnd {
		return nil
	}
	na := centerNormalize(aCenter)
	if na == "" || na != centerNormalize(bCenter) {
		return nil
	}

	center := aCenter
	if len([]rune(bCenter)) > len([]rune(aCenter)) {
		center = bCenter
	}

	merged := &llmDedupMerged{Center: center}
	seen := map[string]int{} // normalized content -> index in merged.Points
	for _, p := range append(append([]llmDedupPoint{}, aPoints...), bPoints...) {
		key := normText(p.Content)
		if key == "" {
			continue
		}
		if i, ok := seen[key]; ok {
			if len([]rune(p.Content)) > len([]rune(merged.Points[i].Content)) {
				merged.Points[i] = p
			}
			continue
		}
		seen[key] = len(merged.Points)
		merged.Points = append(merged.Points, p)
	}
	if len(merged.Points) == 0 {
		return nil
	}
	return merged
}

// judgePair runs the full pair-resolution ladder shared by both dedup paths:
// deterministic merge first (no LLM), then classify, then — only for
// duplicates — the merge call. nil means keep both units.
func (s *Service) judgePair(ctx context.Context, sourceID string, vars map[string]string, aCenter, bCenter string, aStart, aEnd, bStart, bEnd int, aPoints, bPoints []llmDedupPoint) *llmDedupMerged {
	if m := deterministicMerge(aCenter, bCenter, aStart, aEnd, bStart, bEnd, aPoints, bPoints); m != nil {
		slog.Debug("unit: dedup deterministic merge (same range, same normalized center)", "source_id", sourceID, "center", m.Center)
		return m
	}
	if s.classifyPair(ctx, sourceID, vars) != relationDuplicate {
		return nil
	}
	merged := s.mergePairViaLLM(ctx, sourceID, vars)
	if merged != nil {
		inheritMergedSemantics(merged, aPoints, bPoints)
	}
	return merged
}

// inheritMergedSemantics fills in each merged point's rerank semantics
// (never produced by unit_dedup_merge.md itself) by matching its content
// back to whichever original a/b point it echoes, via exact normalized-text
// equality. A merged point whose wording changed enough that no original
// matches is left with blank semantics — the same approximation the merge
// ladder already accepts for Center/Points, closeable later by the KP-level
// backfill (see kp_semantics.go) rather than by a bespoke mechanism here.
func inheritMergedSemantics(merged *llmDedupMerged, aPoints, bPoints []llmDedupPoint) {
	byText := make(map[string]llmDedupPoint, len(aPoints)+len(bPoints))
	for _, p := range append(append([]llmDedupPoint{}, aPoints...), bPoints...) {
		byText[normText(p.Content)] = p
	}
	for i, p := range merged.Points {
		if orig, ok := byText[normText(p.Content)]; ok {
			merged.Points[i].SourceTheme = orig.SourceTheme
			merged.Points[i].ContentTheme = orig.ContentTheme
			merged.Points[i].Object = orig.Object
			merged.Points[i].Scope = orig.Scope
			merged.Points[i].SemanticsPromptVersion = orig.SemanticsPromptVersion
		}
	}
}

// dedupMaxGapLines is how far apart two units' line ranges can sit and still
// be checked as a possible duplicate pair. Real duplicates found in practice
// (docs/impl/mvp/unit.md 3.3) are rarely perfectly zero-gap-adjacent — a
// blank line, a trivial heading, or a table separator that fillGaps merged
// into one side or the other often sits between them (e.g. a heading and its
// own content separated by a blank line, or the same parameter explained
// once near a table and again a few lines later past an intervening row).
// Zero-gap-only missed exactly these cases, so this allows a small amount of
// slack instead of requiring the ranges to literally touch or overlap.
const dedupMaxGapLines = 3
