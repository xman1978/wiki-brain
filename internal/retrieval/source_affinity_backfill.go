package retrieval

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// subjectTagCandidate is one source_tag_match.md candidate item — an
// existing subject_norm tag being tested against a single new/changed
// source's title+summary. Mirrors sourceFilterCandidate's shape (a small
// JSON-friendly struct rather than a hand-built text list) so it drops
// straight into the same generic runMatchBatches used by
// sourceSemanticFilter.
type subjectTagCandidate struct {
	CandidateID string `json:"candidate_id"`
	Subject     string `json:"subject"`
}

// BackfillSourceAffinityForSource matches sourceID's title/summary against
// its domain's existing subject_norm tag vocabulary and writes
// source_affinity rows for whatever tags it plausibly
// relates to (docs/design/retrieval.md 第 14 节 决策点 4 / docs/impl/v1/
// retrieval.md 步骤 5a). It is the inverse of trySourceAffinityShortcut's
// question-drives-lookup direction: here one source is matched against many
// existing tags, instead of one tag's bound sources being looked up for a
// question.
//
// Best-effort, by design: the caller (cmd/server/main.go's unit_extract
// handler) fires this via `go` after a source finishes processing (or a
// Shadow Source reupload swap completes). A failure here — LLM error,
// process crash mid-run, anything — only means this one source misses
// proactive tagging; it does not affect correctness, since the existing
// citation-driven path (RecordSourceAffinityOutcome) still tags it the
// moment a real question actually cites it, and until then queries simply
// fall through to the full domainPreFilter+sourceSemanticFilter pipeline
// unchanged. Never blocks or fails the ingestion pipeline.
func (s *Service) BackfillSourceAffinityForSource(ctx context.Context, sourceID string) error {
	if !s.sourceAffinityEnabled() || sourceID == "" {
		return nil
	}

	sources, err := s.store.GetSourcesByIDs([]string{sourceID})
	if err != nil {
		return fmt.Errorf("retrieval: backfill source lookup: %w", err)
	}
	if len(sources) == 0 {
		return nil
	}
	src := sources[0]
	if !src.DomainID.Valid || src.DomainID.String == "" {
		return nil
	}
	domainID := src.DomainID.String

	tags, err := s.store.ListAllSubjectNorms([]string{domainID})
	if err != nil {
		return fmt.Errorf("retrieval: backfill list subject norms: %w", err)
	}
	if len(tags) == 0 {
		return nil
	}

	items := make([]subjectTagCandidate, len(tags))
	bySubject := make(map[string]string, len(tags)) // norm_id -> canonical subject text
	for i, t := range tags {
		items[i] = subjectTagCandidate{CandidateID: t.NormID, Subject: t.Subject}
		bySubject[t.NormID] = t.Subject
	}

	summary := ""
	if src.Summary.Valid {
		summary = src.Summary.String
	}

	callBatch := func(ctx context.Context, batch []subjectTagCandidate) ([]string, error) {
		payload, err := json.Marshal(batch)
		if err != nil {
			return nil, fmt.Errorf("retrieval: source tag match payload: %w", err)
		}
		resp, err := s.llmClient.CompleteJSON(ctx, "source_tag_match.md", map[string]string{
			"title":      src.Title,
			"summary":    summary,
			"candidates": string(payload),
		}, "classification")
		if err != nil {
			return nil, fmt.Errorf("retrieval: source tag match llm: %w", err)
		}
		var result struct {
			RelevantIDs []string `json:"relevant_ids"`
		}
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, fmt.Errorf("retrieval: source tag match parse: %w", err)
		}
		return result.RelevantIDs, nil
	}

	matched, err := runMatchBatches(ctx, items, s.rerankJudgeBatchMaxChars(), s.rerankJudgeBatchMaxCandidates(), s.rerankJudgeConcurrency(), callBatch)
	if err != nil {
		return fmt.Errorf("retrieval: source tag match batches: %w", err)
	}

	for normID := range matched {
		subject, ok := bySubject[normID]
		if !ok {
			continue
		}
		if err := s.store.RecordSourceAffinitySuccess(domainID, subject, sourceID); err != nil {
			slog.Warn("retrieval: record source affinity backfill failed", "source_id", sourceID, "subject_norm", subject, "error", err)
		}
	}
	return nil
}
