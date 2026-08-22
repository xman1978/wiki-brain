package unit

import (
	"context"
	"fmt"

	"github.com/jxman78/wiki-brain/internal/rerank"
	"github.com/jxman78/wiki-brain/internal/source"
)

// RegenerateRerankSemanticsResult reports one source's backfill outcome.
type RegenerateRerankSemanticsResult struct {
	SourceID       string
	Updated        int
	Skipped        int // manually_edited rows, left untouched
	AlreadyCurrent int // already at rerank.ExtractPromptVersion, not re-sent to the LLM
	Ignored        int // extraction produced no result even after every fallback tier (see extractKPSemanticBatch)
}

// RegenerateRerankSemantics re-runs kp_semantics_extract.md
// (rerank.ExtractPromptVersion) over every current knowledge point of src
// that isn't already on that prompt version and hasn't been manually
// corrected, overwriting its source_theme/content_theme/object/scope
// columns with the fresh result. Used to backfill points whose semantics
// were never populated at extraction time (gap/retry/coverage-fill paths,
// or a prompt version bump) without re-running the source's whole
// extraction pipeline.
//
// Idempotent by design: a point already stamped with the current prompt
// version is left alone rather than re-sent to the LLM.
//
// Points flagged manually_edited are skipped entirely, never sent to the LLM.
func (s *Service) RegenerateRerankSemantics(ctx context.Context, src source.Source) (RegenerateRerankSemanticsResult, error) {
	res := RegenerateRerankSemanticsResult{SourceID: src.SourceID}

	points, err := s.store.GetPointsBySourceID(src.SourceID)
	if err != nil {
		return res, fmt.Errorf("unit: regenerate semantics: list points: %w", err)
	}
	if len(points) == 0 {
		return res, nil
	}

	var pool []kpSemanticCandidate
	for _, kp := range points {
		if kp.ManuallyEdited {
			res.Skipped++
			continue
		}
		if kp.SemanticsPromptVersion == rerank.ExtractPromptVersion {
			res.AlreadyCurrent++
			continue
		}
		pool = append(pool, kpSemanticCandidate{id: kp.PointID, content: kp.Content})
	}
	if len(pool) == 0 {
		return res, nil
	}

	semantics, err := s.extractKPSemantics(ctx, src.Title, src.Summary.String, pool)
	if err != nil {
		return res, fmt.Errorf("unit: regenerate semantics: extract: %w", err)
	}

	for _, candidate := range pool {
		sem, ok := semantics[candidate.id]
		if !ok {
			res.Ignored++
			continue
		}
		if err := s.store.UpsertPointSemanticsFromExtraction(candidate.id, sem, rerank.ExtractPromptVersion); err != nil {
			return res, fmt.Errorf("unit: regenerate semantics: store (%s): %w", candidate.id, err)
		}
		res.Updated++
	}
	return res, nil
}
