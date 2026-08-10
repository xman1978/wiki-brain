package unit

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jxman78/wiki-brain/internal/rerank"
	"github.com/jxman78/wiki-brain/internal/source"
)

// RegenerateRerankSemanticsResult reports one source's backfill outcome.
type RegenerateRerankSemanticsResult struct {
	SourceID       string
	Updated        int
	Skipped        int // manually_edited rows, left untouched
	AlreadyCurrent int // already at rerank.ExtractPromptVersion, not re-sent to the LLM
	Ignored        int // extraction produced no result even after every fallback tier (see extractRerankSemanticBatch)
}

// RegenerateRerankSemantics re-runs unit_semantics_extract.md
// (rerank.ExtractPromptVersion) over every current unit of src that isn't
// already on that prompt version and hasn't been manually corrected,
// overwriting unit_rerank_semantics with the fresh result. Used to backfill
// existing sources after a semantics-extraction prompt change, without
// re-running the whole source's extraction pipeline (segmentation, dedup,
// KPN — none of that is touched here).
//
// Idempotent by design: a unit already stamped with the current prompt
// version is left alone rather than re-sent to the LLM, so re-running this
// over a source that's already been backfilled — e.g. to retry the handful
// of units a previous run's extraction had to discard (see Ignored) — only
// pays for the units that still need it.
//
// Units flagged manually_edited are skipped entirely, never sent to the LLM:
// a human correction must not be silently discarded by a backfill re-run.
func (s *Service) RegenerateRerankSemantics(ctx context.Context, src source.Source) (RegenerateRerankSemanticsResult, error) {
	res := RegenerateRerankSemanticsResult{SourceID: src.SourceID}

	units, err := s.store.GetUnitsBySourceIDFiltered(src.SourceID, LifecycleCurrent)
	if err != nil {
		return res, fmt.Errorf("unit: regenerate semantics: list units: %w", err)
	}
	if len(units) == 0 {
		return res, nil
	}

	if src.MarkdownPath == "" {
		return res, fmt.Errorf("unit: regenerate semantics: source %s has no markdown_path", src.SourceID)
	}
	data, err := os.ReadFile(src.MarkdownPath)
	if err != nil {
		return res, fmt.Errorf("unit: regenerate semantics: read markdown: %w", err)
	}
	mdLines := strings.Split(string(data), "\n")

	var pool []unitCandidate
	for _, ku := range units {
		existing, err := s.store.GetRerankSemanticsByUnitID(ku.UnitID)
		if err != nil {
			return res, fmt.Errorf("unit: regenerate semantics: lookup existing (%s): %w", ku.UnitID, err)
		}
		if existing != nil && existing.ManuallyEdited {
			res.Skipped++
			continue
		}
		if existing != nil && existing.PromptVersion == rerank.ExtractPromptVersion {
			res.AlreadyCurrent++
			continue
		}
		pool = append(pool, unitCandidate{id: ku.UnitID, lineStart: ku.LineStart, lineEnd: ku.LineEnd})
	}
	if len(pool) == 0 {
		return res, nil
	}

	semantics, err := s.extractRerankSemantics(ctx, src.Title, mdLines, pool)
	if err != nil {
		return res, fmt.Errorf("unit: regenerate semantics: extract: %w", err)
	}

	for _, candidate := range pool {
		sem, ok := semantics[candidate.id]
		if !ok {
			res.Ignored++
			continue
		}
		if err := s.store.UpsertRerankSemanticsFromExtraction(candidate.id, sem, rerank.ExtractPromptVersion); err != nil {
			return res, fmt.Errorf("unit: regenerate semantics: store (%s): %w", candidate.id, err)
		}
		res.Updated++
	}
	return res, nil
}
