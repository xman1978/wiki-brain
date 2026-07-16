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

	// rerankContentHeadMinRunes is how many runes of a unit's own content
	// the model must copy verbatim into "content_head" for a result to be
	// trusted (see contentHeadMatches) — long enough that a wrong pairing
	// almost never accidentally matches, short enough to be a trivial copy
	// for the model. Content shorter than this only has to be matched in
	// full (see contentHeadMatches), so short fixtures in tests still work.
	rerankContentHeadMinRunes = 6
)

type rerankSemanticCandidate struct {
	id      string
	content string
}

type rerankSemanticExtractionResult struct {
	Results []rerankSemanticExtraction `json:"results"`
}

// rerankSemanticExtraction identifies its unit two ways: a small 1-based
// sequence number (matching the "N. content" numbering formatRerankSemanticUnits
// put in the prompt) instead of an echoed opaque UUID — a UUID was fragile to
// reproduce exactly, but copying back a small integer that's printed right
// next to the content is not — plus ContentHead, a verbatim copy of that
// unit's own opening text, which lets extractRerankSemanticBatchOnce catch a
// wrong index (model claims index 3 but the content it describes is
// actually unit 5's) instead of silently storing a misaligned result.
// Together these let the caller tell exactly which units a response omitted
// or mismatched, instead of only knowing the count was short, so a retry
// only needs to cover the units that still need it.
type rerankSemanticExtraction struct {
	Index        int      `json:"index"`
	ContentHead  string   `json:"content_head"`
	SourceTheme  string   `json:"source_theme"`
	ContentTheme string   `json:"content_theme"`
	Intent       string   `json:"intent"`
	Object       string   `json:"object"`
	Scope        string   `json:"scope"`
	KeyFacts     []string `json:"key_facts"`
}

// extractRerankSemantics extracts rerank semantics for every candidate in
// pool, batched and run concurrently (see splitRerankSemanticBatches). A
// candidate that still has no semantics after extractRerankSemanticBatch's
// three fallback tiers is not a fatal error — it's simply absent from the
// returned map, so the caller (publishCandidates → PublishGeneration)
// discards that one unit entirely (never inserted into knowledge_units, no
// unit_rerank_semantics row to be missing) rather than failing the whole
// source over one unresolvable unit.
func (s *Service) extractRerankSemantics(
	ctx context.Context,
	sourceTitle string,
	mdLines []string,
	pool []unitCandidate,
) (map[string]rerank.Semantics, error) {
	if len(pool) == 0 {
		return map[string]rerank.Semantics{}, nil
	}

	candidates := make([]rerankSemanticCandidate, 0, len(pool))
	seenIDs := make(map[string]bool, len(pool))
	for _, candidate := range pool {
		if candidate.id == "" {
			return nil, fmt.Errorf("unit: rerank semantics candidate has empty unit_id")
		}
		if seenIDs[candidate.id] {
			return nil, fmt.Errorf("unit: rerank semantics duplicate unit_id: %s", candidate.id)
		}
		if candidate.lineStart < 1 || candidate.lineEnd < candidate.lineStart || candidate.lineEnd > len(mdLines) {
			return nil, fmt.Errorf("unit: rerank semantics invalid range for unit_id %s: %d-%d", candidate.id, candidate.lineStart, candidate.lineEnd)
		}
		seenIDs[candidate.id] = true
		candidates = append(candidates, rerankSemanticCandidate{
			id:      candidate.id,
			content: strings.Join(mdLines[candidate.lineStart-1:candidate.lineEnd], "\n"),
		})
	}

	batches := splitRerankSemanticBatches(candidates, s.rerankExtractBatchMaxChars(), s.rerankExtractBatchMaxUnits())
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, s.rerankExtractConcurrency())
	semantics := make(map[string]rerank.Semantics, len(pool))
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

			batchSemantics, err := s.extractRerankSemanticBatch(ctx, sourceTitle, batch)
			if err != nil {
				errOnce.Do(func() {
					firstErr = err
					cancel()
				})
				return
			}

			mu.Lock()
			for unitID, semantic := range batchSemantics {
				semantics[unitID] = semantic
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
	if len(semantics) != len(pool) {
		var ignored []string
		for _, candidate := range candidates {
			if _, ok := semantics[candidate.id]; !ok {
				ignored = append(ignored, candidate.id)
			}
		}
		slog.Warn("unit: rerank semantics could not be extracted for some units even after every fallback tier, discarding them",
			"ignored_count", len(ignored), "unit_ids", ignored)
	}
	return semantics, nil
}

// extractRerankSemanticBatch has three tiers, each covering only what the
// previous one left missing: the initial batch call, then one retry scoped
// to just the omitted units, then — if that retry still comes up short —
// one call per still-missing unit (a batch of 1 can't have an index
// collision or an omission that isn't total failure). A unit that fails
// even alone is not retried further — retrying again would just mask
// something wrong with that specific unit's content or the LLM's handling
// of it, not the batch size — it's simply left out of the returned map;
// the caller (extractRerankSemantics → PublishGeneration) discards that one
// unit and publishes every other unit normally, rather than blocking the
// whole source over one unit's content (e.g. a boundary-extraction edge
// case: a heading-only unit with no body for the model to describe).
// An actual LLM/transport/parse error, as opposed to a unit-level omission,
// still fails the whole batch — it says nothing about any specific unit's
// content, and retrying it here (across all three tiers) already gives it
// every reasonable chance to recover.
func (s *Service) extractRerankSemanticBatch(ctx context.Context, sourceTitle string, batch []rerankSemanticCandidate) (map[string]rerank.Semantics, error) {
	semantics, missing, err := s.extractRerankSemanticBatchOnce(ctx, sourceTitle, batch)
	if err != nil {
		return nil, err
	}
	if len(missing) == 0 {
		return semantics, nil
	}

	slog.Warn("unit: rerank semantics batch omitted/mismatched units, retrying only those",
		"missing_count", len(missing), "batch_count", len(batch))
	retried, stillMissing, err := s.extractRerankSemanticBatchOnce(ctx, sourceTitle, missing)
	if err != nil {
		return nil, fmt.Errorf("unit: rerank semantics retry omitted units: %w", err)
	}
	for id, sem := range retried {
		semantics[id] = sem
	}
	if len(stillMissing) == 0 {
		return semantics, nil
	}

	slog.Warn("unit: rerank semantics still omitted/mismatched after retry, falling back to one call per unit",
		"still_missing_count", len(stillMissing))
	for _, candidate := range stillMissing {
		single, singleMissing, err := s.extractRerankSemanticBatchOnce(ctx, sourceTitle, []rerankSemanticCandidate{candidate})
		if err != nil {
			return nil, fmt.Errorf("unit: rerank semantics single-unit fallback for unit_id %s: %w", candidate.id, err)
		}
		if len(singleMissing) > 0 {
			slog.Warn("unit: rerank semantics unit still unmatched in single-unit fallback, discarding it",
				"unit_id", candidate.id, "content_actual", runePrefixDebug(candidate.content, 200))
			continue
		}
		for id, sem := range single {
			semantics[id] = sem
		}
	}
	return semantics, nil
}

// extractRerankSemanticBatchOnce is a single (non-retrying) call. With more
// than one candidate in the batch, matching results back to candidates needs
// index+content_head (see below) since the model could plausibly mean any of
// them; malformed or unverifiable per-result problems — an out-of-range or
// duplicate index, a content_head that doesn't match its claimed index —
// only cost that one result, not the whole call: the entry is discarded and
// its candidate falls into missing for the caller to retry, while every
// other, valid entry in the same response is still used. A batch of exactly
// one candidate has no such ambiguity to resolve, so index/content_head
// aren't required at all — whatever the model returns is assumed to be
// about that one candidate (see matchSingleCandidateResult).
func (s *Service) extractRerankSemanticBatchOnce(ctx context.Context, sourceTitle string, batch []rerankSemanticCandidate) (map[string]rerank.Semantics, []rerankSemanticCandidate, error) {
	resp, err := s.llmClient.CompleteJSON(ctx, "unit_semantics_extract.md", map[string]string{
		"source_title": sourceTitle,
		"units":        formatRerankSemanticUnits(batch),
	}, "classification")
	if err != nil {
		return nil, nil, fmt.Errorf("unit: rerank semantics llm: %w", err)
	}

	var result rerankSemanticExtractionResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, nil, fmt.Errorf("unit: rerank semantics parse: %w", err)
	}

	if len(batch) == 1 {
		if len(result.Results) == 0 {
			return map[string]rerank.Semantics{}, []rerankSemanticCandidate{batch[0]}, nil
		}
		return matchSingleCandidateResult(batch[0], result.Results[0]), nil, nil
	}

	indexCount := make(map[int]int, len(result.Results))
	for _, extracted := range result.Results {
		indexCount[extracted.Index]++
	}

	semantics := make(map[string]rerank.Semantics, len(batch))
	matchedIndex := make(map[int]bool, len(result.Results))
	for _, extracted := range result.Results {
		if extracted.Index < 1 || extracted.Index > len(batch) {
			slog.Warn("unit: rerank semantics returned an out-of-range index, ignoring that result",
				"index", extracted.Index, "batch_count", len(batch))
			continue
		}
		if indexCount[extracted.Index] > 1 {
			slog.Warn("unit: rerank semantics returned the same index more than once, ignoring all of them for it",
				"index", extracted.Index)
			continue
		}

		candidate := batch[extracted.Index-1]
		if !contentHeadMatches(candidate.content, extracted.ContentHead) {
			slog.Warn("unit: rerank semantics content_head did not match its claimed index, treating as unmatched",
				"unit_id", candidate.id, "index", extracted.Index,
				"content_head_got", extracted.ContentHead,
				"content_actual", runePrefixDebug(candidate.content, 40))
			continue
		}
		matchedIndex[extracted.Index] = true
		semantics[candidate.id] = rerank.Semantics{
			UnitID:        candidate.id,
			SourceTheme:   extracted.SourceTheme,
			ContentTheme:  extracted.ContentTheme,
			Intent:        extracted.Intent,
			Object:        extracted.Object,
			Scope:         extracted.Scope,
			KeyFacts:      extracted.KeyFacts,
			PromptVersion: rerank.ExtractPromptVersion,
		}
	}

	var missing []rerankSemanticCandidate
	for i, candidate := range batch {
		if !matchedIndex[i+1] {
			missing = append(missing, candidate)
		}
	}
	return semantics, missing, nil
}

// matchSingleCandidateResult assigns extracted unconditionally to candidate
// — a batch of one has nothing for index or content_head to disambiguate,
// and requiring them anyway only reproduces the same alignment failures a
// single candidate can't actually have (e.g. the model quoting the body
// text after a unit's own leading markdown heading as content_head,
// correctly, without that phrase being a byte-prefix of the full content it
// was asked about).
func matchSingleCandidateResult(candidate rerankSemanticCandidate, extracted rerankSemanticExtraction) map[string]rerank.Semantics {
	return map[string]rerank.Semantics{
		candidate.id: {
			UnitID:        candidate.id,
			SourceTheme:   extracted.SourceTheme,
			ContentTheme:  extracted.ContentTheme,
			Intent:        extracted.Intent,
			Object:        extracted.Object,
			Scope:         extracted.Scope,
			KeyFacts:      extracted.KeyFacts,
			PromptVersion: rerank.ExtractPromptVersion,
		},
	}
}

// runePrefixDebug returns the first n runes of s, for diagnostic logging
// only — not used in any matching/validation logic.
func runePrefixDebug(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// contentHeadMatches reports whether head is a genuine verbatim prefix of
// content — or of content with its own leading markdown heading line(s)
// stripped. Real content_head mismatches observed in practice were the
// model correctly quoting the first line of body text right after a unit's
// "# heading" line (treating the heading as a label, not "content"), which
// is legitimate and shouldn't be rejected just because it isn't a literal
// prefix of the heading-inclusive block. Either match requires at least
// rerankContentHeadMinRunes runes of overlap (or the whole of content, if
// content itself is shorter than that) — long enough that a wrong pairing
// essentially never matches by accident, while staying trivial for the
// model to copy correctly when it isn't wrong.
func contentHeadMatches(content, head string) bool {
	head = strings.TrimSpace(head)
	if head == "" {
		return false
	}
	if runeCountPrefixMatch(content, head) {
		return true
	}
	if body := stripLeadingHeadingLines(content); body != content {
		return runeCountPrefixMatch(body, head)
	}
	return false
}

func runeCountPrefixMatch(content, head string) bool {
	content = strings.TrimSpace(content)
	minRunes := rerankContentHeadMinRunes
	if n := utf8.RuneCountInString(content); n < minRunes {
		minRunes = n
	}
	if utf8.RuneCountInString(head) < minRunes {
		return false
	}
	return strings.HasPrefix(content, head)
}

// stripLeadingHeadingLines drops content's leading run of blank lines and
// markdown heading lines ("# ..."), returning what's left (the first line
// of actual body text onward). Returns content unchanged if it doesn't
// start with a heading line.
func stripLeadingHeadingLines(content string) string {
	lines := strings.Split(content, "\n")
	i := 0
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			i++
			continue
		}
		break
	}
	if i == 0 {
		return content
	}
	return strings.Join(lines[i:], "\n")
}

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

// splitRerankSemanticBatches caps each batch by both a character budget and
// a unit count (whichever is hit first) — the unit count cap exists
// separately from the character budget because a batch's failure/omission
// risk scales with how many similarly-shaped JSON objects the model has to
// produce in one response, not just how much text it has to read.
func splitRerankSemanticBatches(candidates []rerankSemanticCandidate, maxChars, maxUnits int) [][]rerankSemanticCandidate {
	if len(candidates) == 0 {
		return nil
	}
	if maxChars <= 0 {
		maxChars = defaultRerankExtractBatchMaxChars
	}
	if maxUnits <= 0 {
		maxUnits = defaultRerankExtractBatchMaxUnits
	}

	var batches [][]rerankSemanticCandidate
	var current []rerankSemanticCandidate
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

// formatRerankSemanticUnits numbers each unit 1..N — the model echoes this
// number back as each result's "index" field (see rerankSemanticExtraction),
// which is how extractRerankSemanticBatchOnce matches results back to
// candidates.
func formatRerankSemanticUnits(candidates []rerankSemanticCandidate) string {
	var out strings.Builder
	for i, candidate := range candidates {
		fmt.Fprintf(&out, "%d. %s\n\n", i+1, candidate.content)
	}
	return out.String()
}
