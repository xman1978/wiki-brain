package unit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/jxman78/wiki-brain/internal/rerank"
)

const (
	defaultRerankExtractBatchMaxChars = 4000
	defaultRerankExtractBatchMaxUnits = 8
	defaultRerankExtractConcurrency   = 2
)

func (s *Service) rerankExtractBatchMaxChars() int {
	if s.cfg != nil && s.cfg.Retrieval.RerankExtractBatchMaxChars > 0 {
		return s.cfg.Retrieval.RerankExtractBatchMaxChars
	}
	return defaultRerankExtractBatchMaxChars
}

func (s *Service) rerankExtractBatchMaxUnits() int {
	if s.cfg != nil && s.cfg.Retrieval.RerankExtractBatchMaxUnits > 0 {
		return s.cfg.Retrieval.RerankExtractBatchMaxUnits
	}
	return defaultRerankExtractBatchMaxUnits
}

func (s *Service) rerankExtractConcurrency() int {
	if s.cfg != nil && s.cfg.Retrieval.RerankExtractConcurrency > 0 {
		return s.cfg.Retrieval.RerankExtractConcurrency
	}
	return defaultRerankExtractConcurrency
}

// kpSemanticCandidate is one knowledge point awaiting rerank-semantics
// extraction — the KP-level analogue of the old rerank_semantics.go's
// rerankSemanticCandidate, now keyed by point_id with content = the point's
// own extracted content (not a whole knowledge unit's raw text range). Used
// by two callers that both need a standalone semantics extraction call
// because their points didn't come from unit_point_extract.md's inline
// content_theme/object/scope fields: Service.RegenerateRerankSemantics
// (backfill after a schema/prompt change) and Service.FixCoverageGap (its
// points come from unit_point_extract.md too, actually — but kept on this
// path for symmetry with historical gap-fill/retry-produced points that
// don't populate these fields; see split_extract.go's SemanticsPromptVersion
// comment).
type kpSemanticCandidate struct {
	id      string // point_id
	content string
}

type kpSemanticExtractionResult struct {
	Results []kpSemanticExtraction `json:"results"`
}

// kpSemanticExtraction mirrors the old rerankSemanticExtraction shape
// (index + content_head for alignment, see contentHeadMatches) but per KP.
type kpSemanticExtraction struct {
	Index        int    `json:"index"`
	ContentHead  string `json:"content_head"`
	SourceTheme  string `json:"source_theme"`
	ContentTheme string `json:"content_theme"`
	Object       string `json:"object"`
	Scope        string `json:"scope"`
}

// extractKPSemantics extracts rerank semantics for every candidate,
// batched and run concurrently — same three-tier fallback ladder as the
// retired KU-level engine (batch → retry-missing-only → per-point), just
// operating on a knowledge point's own content instead of a whole unit's
// line range. A candidate still missing semantics after every tier is
// simply absent from the returned map; callers treat that as "still
// unbackfilled, try again later" rather than a fatal error.
func (s *Service) extractKPSemantics(ctx context.Context, sourceTitle, sourceSummary string, candidates []kpSemanticCandidate) (map[string]rerank.Semantics, error) {
	if len(candidates) == 0 {
		return map[string]rerank.Semantics{}, nil
	}

	batches := splitKPSemanticBatches(candidates, s.rerankExtractBatchMaxChars(), s.rerankExtractBatchMaxUnits())
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, s.rerankExtractConcurrency())
	semantics := make(map[string]rerank.Semantics, len(candidates))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	var errOnce sync.Once

scheduling:
	for _, batch := range batches {
		batch := batch
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break scheduling
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			batchSemantics, err := s.extractKPSemanticBatch(ctx, sourceTitle, sourceSummary, batch)
			if err != nil {
				errOnce.Do(func() {
					firstErr = err
					cancel()
				})
				return
			}

			mu.Lock()
			for pointID, semantic := range batchSemantics {
				semantics[pointID] = semantic
			}
			mu.Unlock()
		}()
	}

	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(semantics) != len(candidates) {
		var ignored []string
		for _, candidate := range candidates {
			if _, ok := semantics[candidate.id]; !ok {
				ignored = append(ignored, candidate.id)
			}
		}
		slog.Warn("unit: kp semantics could not be extracted for some points even after every fallback tier, leaving them unbackfilled",
			"ignored_count", len(ignored), "point_ids", ignored)
	}
	return semantics, nil
}

func (s *Service) extractKPSemanticBatch(ctx context.Context, sourceTitle, sourceSummary string, batch []kpSemanticCandidate) (map[string]rerank.Semantics, error) {
	semantics, missing, err := s.extractKPSemanticBatchOnce(ctx, sourceTitle, sourceSummary, batch)
	if err != nil {
		return nil, err
	}
	if len(missing) == 0 {
		return semantics, nil
	}

	slog.Warn("unit: kp semantics batch omitted/mismatched points, retrying only those",
		"missing_count", len(missing), "batch_count", len(batch))
	retried, stillMissing, err := s.extractKPSemanticBatchOnce(ctx, sourceTitle, sourceSummary, missing)
	if err != nil {
		return nil, fmt.Errorf("unit: kp semantics retry omitted points: %w", err)
	}
	for id, sem := range retried {
		semantics[id] = sem
	}
	if len(stillMissing) == 0 {
		return semantics, nil
	}

	slog.Warn("unit: kp semantics still omitted/mismatched after retry, falling back to one call per point",
		"still_missing_count", len(stillMissing))
	for _, candidate := range stillMissing {
		single, singleMissing, err := s.extractKPSemanticBatchOnce(ctx, sourceTitle, sourceSummary, []kpSemanticCandidate{candidate})
		if err != nil {
			return nil, fmt.Errorf("unit: kp semantics single-point fallback for point_id %s: %w", candidate.id, err)
		}
		if len(singleMissing) > 0 {
			slog.Warn("unit: kp semantics point still unmatched in single-point fallback, leaving it unbackfilled",
				"point_id", candidate.id, "content_actual", runePrefixDebug(candidate.content, 200))
			continue
		}
		for id, sem := range single {
			semantics[id] = sem
		}
	}
	return semantics, nil
}

func (s *Service) extractKPSemanticBatchOnce(ctx context.Context, sourceTitle, sourceSummary string, batch []kpSemanticCandidate) (map[string]rerank.Semantics, []kpSemanticCandidate, error) {
	resp, err := s.llmClient.CompleteJSON(ctx, "kp_semantics_extract.md", map[string]string{
		"source_title":   sourceTitle,
		"source_summary": emptyOr(sourceSummary, "（无摘要）"),
		"points":         formatKPSemanticPoints(batch),
	}, "classification")
	if err != nil {
		return nil, nil, fmt.Errorf("unit: kp semantics llm: %w", err)
	}

	var result kpSemanticExtractionResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, nil, fmt.Errorf("unit: kp semantics parse: %w", err)
	}

	if len(batch) == 1 {
		if len(result.Results) == 0 {
			return map[string]rerank.Semantics{}, []kpSemanticCandidate{batch[0]}, nil
		}
		semantics, ok := matchSingleKPCandidateResult(batch[0], result.Results[0])
		if !ok {
			slog.Warn("unit: kp semantics single-candidate result missing a required field, treating as unmatched", "point_id", batch[0].id)
			return map[string]rerank.Semantics{}, []kpSemanticCandidate{batch[0]}, nil
		}
		return semantics, nil, nil
	}

	indexCount := make(map[int]int, len(result.Results))
	for _, extracted := range result.Results {
		indexCount[extracted.Index]++
	}

	semantics := make(map[string]rerank.Semantics, len(batch))
	matchedIndex := make(map[int]bool, len(result.Results))
	for _, extracted := range result.Results {
		if extracted.Index < 1 || extracted.Index > len(batch) {
			slog.Warn("unit: kp semantics returned an out-of-range index, ignoring that result", "index", extracted.Index, "batch_count", len(batch))
			continue
		}
		if indexCount[extracted.Index] > 1 {
			slog.Warn("unit: kp semantics returned the same index more than once, ignoring all of them for it", "index", extracted.Index)
			continue
		}

		candidate := batch[extracted.Index-1]
		if !contentHeadMatches(candidate.content, extracted.ContentHead) {
			slog.Warn("unit: kp semantics content_head did not match its claimed index, treating as unmatched",
				"point_id", candidate.id, "index", extracted.Index,
				"content_head_got", extracted.ContentHead, "content_actual", runePrefixDebug(candidate.content, 40))
			continue
		}
		semantic := rerank.Semantics{
			PointID:       candidate.id,
			SourceTheme:   extracted.SourceTheme,
			ContentTheme:  extracted.ContentTheme,
			Object:        extracted.Object,
			Scope:         extracted.Scope,
			PromptVersion: rerank.ExtractPromptVersion,
		}
		if field := kpSemanticMissingField(semantic); field != "" {
			slog.Warn("unit: kp semantics result missing a required field, treating as unmatched",
				"point_id", candidate.id, "index", extracted.Index, "missing_field", field)
			continue
		}
		matchedIndex[extracted.Index] = true
		semantics[candidate.id] = semantic
	}

	var missing []kpSemanticCandidate
	for i, candidate := range batch {
		if !matchedIndex[i+1] {
			missing = append(missing, candidate)
		}
	}
	return semantics, missing, nil
}

func matchSingleKPCandidateResult(candidate kpSemanticCandidate, extracted kpSemanticExtraction) (map[string]rerank.Semantics, bool) {
	semantic := rerank.Semantics{
		PointID:       candidate.id,
		SourceTheme:   extracted.SourceTheme,
		ContentTheme:  extracted.ContentTheme,
		Object:        extracted.Object,
		Scope:         extracted.Scope,
		PromptVersion: rerank.ExtractPromptVersion,
	}
	if kpSemanticMissingField(semantic) != "" {
		return nil, false
	}
	return map[string]rerank.Semantics{candidate.id: semantic}, true
}

// kpSemanticMissingField mirrors the retired semanticMissingField: object may
// be empty (the point's text may never state who it applies to), but
// source_theme/content_theme/scope must not be.
func kpSemanticMissingField(semantic rerank.Semantics) string {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "source_theme", value: semantic.SourceTheme},
		{name: "content_theme", value: semantic.ContentTheme},
		{name: "scope", value: semantic.Scope},
	} {
		if strings.TrimSpace(field.value) == "" {
			return field.name
		}
	}
	return ""
}

func splitKPSemanticBatches(candidates []kpSemanticCandidate, maxChars, maxUnits int) [][]kpSemanticCandidate {
	if len(candidates) == 0 {
		return nil
	}
	if maxChars <= 0 {
		maxChars = defaultRerankExtractBatchMaxChars
	}
	if maxUnits <= 0 {
		maxUnits = defaultRerankExtractBatchMaxUnits
	}

	var batches [][]kpSemanticCandidate
	var current []kpSemanticCandidate
	currentChars := 0
	for _, candidate := range candidates {
		itemChars := utf8.RuneCountInString(candidate.content)
		if len(current) > 0 && (currentChars+itemChars > maxChars || len(current) >= maxUnits) {
			batches = append(batches, current)
			current = nil
			currentChars = 0
		}
		current = append(current, candidate)
		currentChars += itemChars
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

func formatKPSemanticPoints(candidates []kpSemanticCandidate) string {
	var out strings.Builder
	for i, candidate := range candidates {
		fmt.Fprintf(&out, "%d. %s\n\n", i+1, candidate.content)
	}
	return out.String()
}

// kpContentHeadMinRunes is how many runes of a point's own content the model
// must copy verbatim into "content_head" for a result to be trusted — long
// enough that a wrong pairing almost never accidentally matches, short
// enough to be a trivial copy for the model.
const kpContentHeadMinRunes = 6

// runePrefixDebug returns the first n runes of s, for diagnostic logging only.
func runePrefixDebug(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// contentHeadMatches reports whether head is a genuine verbatim prefix of
// content, requiring at least kpContentHeadMinRunes runes of overlap (or the
// whole of content, if content itself is shorter than that).
func contentHeadMatches(content, head string) bool {
	head = strings.TrimSpace(head)
	if head == "" {
		return false
	}
	content = strings.TrimSpace(content)
	minRunes := kpContentHeadMinRunes
	if n := utf8.RuneCountInString(content); n < minRunes {
		minRunes = n
	}
	if utf8.RuneCountInString(head) < minRunes {
		return false
	}
	return strings.HasPrefix(content, head)
}
