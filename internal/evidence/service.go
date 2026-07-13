package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/textmatch"
)

type Service struct {
	llmClient llm.LLMClient
	cfg       config.EvidenceConfig
}

func NewService(llmClient llm.LLMClient, cfg config.EvidenceConfig) *Service {
	return &Service{llmClient: llmClient, cfg: cfg}
}

type mineResult struct {
	CandidateID string   `json:"candidate_id"`
	Fragments   []string `json:"fragments"`
}

type mineResponse struct {
	Results []mineResult `json:"results"`
}

// Mine implements docs/impl/v1/evidence.md 步骤 1-4: batch candidates by KU
// text size, ask the LLM to pick verbatim supporting fragments per batch,
// verify every fragment actually occurs in its KU's text, and fall back to
// whole-segment evidence (mined=false) wherever mining can't be trusted.
// It never returns an error — every failure mode degrades to a usable
// candidate list, per "Mine 在任何失败下都返回可用的候选列表，不向上抛错".
func (s *Service) Mine(ctx context.Context, question, subject, intent string, candidates []EvidenceItem) []EvidenceItem {
	if !s.cfg.Enabled || len(candidates) == 0 {
		return candidates
	}

	start := time.Now()
	batches := batchCandidates(candidates, s.cfg.BatchMaxChars)

	var out []EvidenceItem
	fragmentsProduced, droppedFragments, wholeSegmentFallbacks := 0, 0, 0
	for _, batch := range batches {
		items, fp, df, wf := s.mineBatch(ctx, question, subject, intent, batch)
		out = append(out, items...)
		fragmentsProduced += fp
		droppedFragments += df
		wholeSegmentFallbacks += wf
	}

	slog.Info("evidence: mine complete",
		"candidates", len(candidates), "batches", len(batches),
		"fragments_produced", fragmentsProduced, "fragments_dropped", droppedFragments,
		"whole_segment_fallbacks", wholeSegmentFallbacks,
		"duration_ms", time.Since(start).Milliseconds())
	return out
}

// batchCandidates packs candidates (in caller order — direct first) so each
// batch's KU text totals at most maxChars runes; a single KU over the limit
// gets its own batch rather than being split (docs/impl/v1/evidence.md 步骤 1).
func batchCandidates(candidates []EvidenceItem, maxChars int) [][]EvidenceItem {
	if maxChars <= 0 {
		maxChars = 6000
	}

	var batches [][]EvidenceItem
	var current []EvidenceItem
	currentChars := 0

	flush := func() {
		if len(current) > 0 {
			batches = append(batches, current)
			current = nil
			currentChars = 0
		}
	}

	for _, c := range candidates {
		n := len([]rune(c.Content))
		if n > maxChars {
			flush()
			batches = append(batches, []EvidenceItem{c})
			continue
		}
		if currentChars+n > maxChars {
			flush()
		}
		current = append(current, c)
		currentChars += n
	}
	flush()
	return batches
}

// mineBatch runs one LLM call (with retries) for a single batch and applies
// the per-KU / batch-level fallback rules. Returns the resulting items plus
// observability counters (fragments produced, fragments dropped by
// validation, whole-segment fallbacks).
func (s *Service) mineBatch(ctx context.Context, question, subject, intent string, batch []EvidenceItem) ([]EvidenceItem, int, int, int) {
	ids := make([]string, len(batch))
	var candidatesText strings.Builder
	for i, c := range batch {
		ids[i] = fmt.Sprintf("c%d", i+1)
		fmt.Fprintf(&candidatesText, "【%s】\n%s\n\n", ids[i], c.Content)
	}

	vars := map[string]string{
		"question":      question,
		"subject":       placeholderIfEmpty(subject),
		"intent":        placeholderIfEmpty(intent),
		"candidates":    candidatesText.String(),
		"max_fragments": strconv.Itoa(s.cfg.MaxFragmentsPerKU),
	}

	attempts := s.cfg.Retry + 1
	var resp mineResponse
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		raw, err := s.llmClient.CompleteJSON(ctx, "evidence_mine.md", vars, "extraction")
		if err != nil {
			lastErr = fmt.Errorf("llm call: %w", err)
			slog.Warn("evidence: mine batch llm call failed", "attempt", attempt, "candidates", len(batch), "error", err)
			continue
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			lastErr = fmt.Errorf("parse: %w", err)
			slog.Warn("evidence: mine batch parse failed", "attempt", attempt, "candidates", len(batch), "error", err)
			continue
		}
		if err := validateCoverage(resp.Results, ids); err != nil {
			lastErr = err
			slog.Warn("evidence: mine batch schema validation failed", "attempt", attempt, "candidates", len(batch), "error", err)
			continue
		}
		lastErr = nil
		break
	}

	if lastErr != nil {
		slog.Error("evidence: mine batch failed after retries, whole-segment fallback",
			"candidates", len(batch), "attempts", attempts, "error", lastErr)
		return wholeSegmentFallback(batch), 0, 0, len(batch)
	}

	fragmentsByID := make(map[string][]string, len(resp.Results))
	for _, r := range resp.Results {
		fragmentsByID[r.CandidateID] = r.Fragments
	}

	var out []EvidenceItem
	fragmentsProduced, droppedFragments, wholeSegmentFallbacks := 0, 0, 0
	for i, c := range batch {
		mined, dropped := s.mineCandidate(c, fragmentsByID[ids[i]])
		droppedFragments += dropped
		if len(mined) == 0 {
			if c.Role == RoleDirect {
				slog.Warn("evidence: direct candidate mined nothing, whole-segment fallback", "unit_id", c.UnitID)
				out = append(out, wholeSegmentItem(c))
				wholeSegmentFallbacks++
			} else {
				slog.Debug("evidence: supporting candidate mined nothing, dropped", "unit_id", c.UnitID)
			}
			continue
		}
		out = append(out, mined...)
		fragmentsProduced += len(mined)
	}
	return out, fragmentsProduced, droppedFragments, wholeSegmentFallbacks
}

// validateCoverage is the "程序整合后的结果" check (docs/impl/v1/evidence.md
// 步骤 2): every candidate_id in results must belong to this batch, and every
// candidate_id in the batch must be covered by exactly one result (an empty
// fragments array is fine — that's a legitimate "nothing to mine" verdict).
func validateCoverage(results []mineResult, batchIDs []string) error {
	known := make(map[string]bool, len(batchIDs))
	for _, id := range batchIDs {
		known[id] = false
	}
	for _, r := range results {
		if _, ok := known[r.CandidateID]; !ok {
			return fmt.Errorf("evidence: unknown candidate_id %q in results", r.CandidateID)
		}
		known[r.CandidateID] = true
	}
	for id, covered := range known {
		if !covered {
			return fmt.Errorf("evidence: candidate_id %q missing from results", id)
		}
	}
	return nil
}

type fragmentSpan struct {
	content            string
	lineStart, lineEnd int
	byteStart          int
}

// mineCandidate implements docs/impl/v1/evidence.md 步骤 3: validate each
// fragment against c.Content (exact then whitespace-fuzzy match), drop
// fragments that don't survive, resolve line numbers, widen any fragment
// that lands inside a markdown table to the table's full contiguous range
// (see expandToTableBlock), sort by original appearance order, dedupe
// exact-overlapping ranges, and cap at max_fragments_per_ku.
func (s *Service) mineCandidate(c EvidenceItem, fragments []string) (items []EvidenceItem, dropped int) {
	minChars := s.cfg.MinFragmentChars
	contentLines := strings.Split(c.Content, "\n")

	var spans []fragmentSpan
	for _, frag := range fragments {
		if len([]rune(frag)) < minChars {
			dropped++
			continue
		}
		startByte, endByte, matched, ok := textmatch.MatchFragment(c.Content, frag)
		if !ok {
			dropped++
			preview := frag
			if r := []rune(preview); len(r) > 30 {
				preview = string(r[:30])
			}
			slog.Warn("evidence: fragment not found in KU content, dropped",
				"unit_id", c.UnitID, "fragment_preview", preview)
			continue
		}
		relStart, relEnd := textmatch.ByteRangeToLines(c.Content, startByte, endByte)

		content := matched
		widenedStart, widenedEnd := expandToTableBlock(contentLines, relStart, relEnd)
		if widenedStart != relStart || widenedEnd != relEnd {
			content = strings.Join(contentLines[widenedStart-1:widenedEnd], "\n")
			slog.Debug("evidence: fragment widened to cover its whole markdown table",
				"unit_id", c.UnitID, "original_lines", []int{relStart, relEnd}, "widened_lines", []int{widenedStart, widenedEnd})
		}

		spans = append(spans, fragmentSpan{
			content:   content,
			lineStart: c.LineStart - 1 + widenedStart,
			lineEnd:   c.LineStart - 1 + widenedEnd,
			byteStart: startByte,
		})
	}

	sort.SliceStable(spans, func(i, j int) bool { return spans[i].byteStart < spans[j].byteStart })

	var deduped []fragmentSpan
	for _, sp := range spans {
		dup := false
		for _, d := range deduped {
			if d.lineStart == sp.lineStart && d.lineEnd == sp.lineEnd {
				dup = true
				break
			}
		}
		if !dup {
			deduped = append(deduped, sp)
		}
	}

	if s.cfg.MaxFragmentsPerKU > 0 && len(deduped) > s.cfg.MaxFragmentsPerKU {
		deduped = deduped[:s.cfg.MaxFragmentsPerKU]
	}

	items = make([]EvidenceItem, len(deduped))
	for i, d := range deduped {
		items[i] = EvidenceItem{
			UnitID: c.UnitID, PointID: c.PointID, SourceID: c.SourceID,
			LineStart: d.lineStart, LineEnd: d.lineEnd,
			Content: d.content, Role: c.Role, Origin: c.Origin, Mined: true,
		}
	}
	return items, dropped
}

// isMarkdownTableRow matches a GFM table row (header, separator, or data
// row alike — they're syntactically identical: a line bounded by "|" on
// both ends). Distinguishing header/separator/data isn't needed here; only
// "is this line part of some table" is.
func isMarkdownTableRow(line string) bool {
	t := strings.TrimSpace(line)
	return len(t) > 1 && strings.HasPrefix(t, "|") && strings.HasSuffix(t, "|")
}

// expandToTableBlock widens [relStart, relEnd] (1-based line numbers within
// contentLines) to the full contiguous run of markdown table rows it
// touches, if any — a markdown table is only meaningful as a whole. This is
// the fix for the motivating bug: a mined fragment that was just the data
// row "全体员工 | 350元 | 280元 | 220元 | 200元" with no header row led an
// answer to guess the wrong column for a "福州属于C类城市" question, because
// a bare data row from a category-as-columns table doesn't say which number
// belongs to which category. Rather than only prepending the header row,
// the whole contiguous table (header, separator, and every data row it
// touches or borders) is folded into the fragment, so partial-row mining
// can never again lose the column/row labels that give the numbers meaning.
func expandToTableBlock(contentLines []string, relStart, relEnd int) (int, int) {
	inTable := false
	for i := relStart; i <= relEnd && i >= 1 && i <= len(contentLines); i++ {
		if isMarkdownTableRow(contentLines[i-1]) {
			inTable = true
			break
		}
	}
	if !inTable {
		return relStart, relEnd
	}

	start := relStart
	for start > 1 && isMarkdownTableRow(contentLines[start-2]) {
		start--
	}
	end := relEnd
	for end < len(contentLines) && isMarkdownTableRow(contentLines[end]) {
		end++
	}
	return start, end
}

func wholeSegmentFallback(batch []EvidenceItem) []EvidenceItem {
	out := make([]EvidenceItem, len(batch))
	for i, c := range batch {
		out[i] = wholeSegmentItem(c)
	}
	return out
}

func wholeSegmentItem(c EvidenceItem) EvidenceItem {
	c.Mined = false
	return c
}

func placeholderIfEmpty(s string) string {
	if s == "" {
		return "（未提取）"
	}
	return s
}
