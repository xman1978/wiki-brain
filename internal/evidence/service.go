package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/textmatch"
)

// defaultMineConcurrency mirrors defaultRerankJudgeConcurrency in
// internal/retrieval/service.go — same resolution pattern, same default.
const defaultMineConcurrency = 4

// configAssignmentLine matches bare KEY=value / KEY = value parameter lines
// (dm.ini / spfile-style), used to recognize unsplittable config blocks.
var configAssignmentLine = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*=\s*\S`)

// markdownHeadingLine matches ATX headings (# … ######); a fragment that is
// only headings is dropped (docs/impl/v1/evidence.md 步骤 3.1.0) — never
// widened into a whole section.
var markdownHeadingLine = regexp.MustCompile(`^#{1,6}\s+\S`)

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
//
// lastResort extends that same whole-segment fallback to role=supporting
// candidates that mine nothing (normally they're just dropped — see
// mineBatch). The caller sets it only on retrieval's final retry attempt,
// where the alternative to a noisier supporting fallback is an empty
// EvidenceSet and a "no evidence found" answer despite rerank having judged
// the candidate at least topically relevant.
func (s *Service) Mine(ctx context.Context, question, subject, intent string, candidates []EvidenceItem, lastResort bool) []EvidenceItem {
	if !s.cfg.Enabled || len(candidates) == 0 {
		return candidates
	}

	start := time.Now()
	batches := batchCandidates(candidates, s.cfg.BatchMaxChars)

	// Batches run concurrently, bounded by mineConcurrency() (docs/impl/v1/
	// evidence.md 步骤 1 explicitly allows this: "批次串行或并发执行均可，
	// 并发受 llm.max_concurrency 约束"). Results are collected into a
	// per-batch slot and flattened in original batch order afterward, so
	// output order/determinism is unchanged from the old sequential loop —
	// only wall-clock time changes when a question's candidates span more
	// than one batch.
	results := make([][]EvidenceItem, len(batches))
	counters := make([][3]int, len(batches)) // [fragmentsProduced, droppedFragments, wholeSegmentFallbacks]

	sem := make(chan struct{}, s.mineConcurrency())
	var wg sync.WaitGroup
	for i, batch := range batches {
		i, batch := i, batch
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			items, fp, df, wf := s.mineBatch(ctx, question, subject, intent, batch, lastResort)
			results[i] = items
			counters[i] = [3]int{fp, df, wf}
		}()
	}
	wg.Wait()

	var out []EvidenceItem
	fragmentsProduced, droppedFragments, wholeSegmentFallbacks := 0, 0, 0
	for i := range batches {
		out = append(out, results[i]...)
		fragmentsProduced += counters[i][0]
		droppedFragments += counters[i][1]
		wholeSegmentFallbacks += counters[i][2]
	}

	slog.Info("evidence: mine complete",
		"candidates", len(candidates), "batches", len(batches),
		"fragments_produced", fragmentsProduced, "fragments_dropped", droppedFragments,
		"whole_segment_fallbacks", wholeSegmentFallbacks,
		"duration_ms", time.Since(start).Milliseconds())
	return out
}

// mineConcurrency resolves config.EvidenceConfig.Concurrency, defaulting to
// defaultMineConcurrency when unset/invalid (same pattern as
// internal/retrieval/service.go rerankJudgeConcurrency()).
func (s *Service) mineConcurrency() int {
	if s.cfg.Concurrency > 0 {
		return s.cfg.Concurrency
	}
	return defaultMineConcurrency
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
func (s *Service) mineBatch(ctx context.Context, question, subject, intent string, batch []EvidenceItem, lastResort bool) ([]EvidenceItem, int, int, int) {
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
			switch {
			case c.Role == RoleDirect:
				slog.Warn("evidence: direct candidate mined nothing, whole-segment fallback", "unit_id", c.UnitID)
				out = append(out, wholeSegmentItem(c))
				wholeSegmentFallbacks++
			case lastResort:
				slog.Warn("evidence: supporting candidate mined nothing, last-resort whole-segment fallback", "unit_id", c.UnitID)
				out = append(out, wholeSegmentItem(c))
				wholeSegmentFallbacks++
			default:
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
// fragments that don't survive (including heading-only fragments), resolve
// line numbers, widen any fragment that lands inside an unsplittable block
// (fenced code / markdown table / command-config run — see expandToAtomicBlock),
// sort by original appearance order, dedupe exact-overlapping ranges, and
// cap at max_fragments_per_ku.
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

		if isHeadingOnlyFragment(contentLines, relStart, relEnd) {
			dropped++
			slog.Debug("evidence: heading-only fragment dropped",
				"unit_id", c.UnitID, "lines", []int{relStart, relEnd})
			continue
		}

		content := matched
		widenedStart, widenedEnd, kind := expandToAtomicBlock(contentLines, relStart, relEnd)
		if widenedStart != relStart || widenedEnd != relEnd {
			content = strings.Join(contentLines[widenedStart-1:widenedEnd], "\n")
			slog.Debug("evidence: fragment widened to cover unsplittable block",
				"unit_id", c.UnitID, "kind", kind,
				"original_lines", []int{relStart, relEnd}, "widened_lines", []int{widenedStart, widenedEnd})
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

// isHeadingOnlyFragment reports whether every non-empty line in [relStart,relEnd]
// is a markdown heading. Such fragments are dropped — never expanded into a
// whole section (docs/impl/v1/evidence.md 步骤 3.1.0).
func isHeadingOnlyFragment(contentLines []string, relStart, relEnd int) bool {
	saw := false
	for i := relStart; i <= relEnd && i >= 1 && i <= len(contentLines); i++ {
		line := strings.TrimSpace(contentLines[i-1])
		if line == "" {
			continue
		}
		if !markdownHeadingLine.MatchString(line) {
			return false
		}
		saw = true
	}
	return saw
}

// isMarkdownTableRow matches a GFM table row (header, separator, or data
// row alike — they're syntactically identical: a line bounded by "|" on
// both ends). Distinguishing header/separator/data isn't needed here; only
// "is this line part of some table" is.
func isMarkdownTableRow(line string) bool {
	t := strings.TrimSpace(line)
	return len(t) > 1 && strings.HasPrefix(t, "|") && strings.HasSuffix(t, "|")
}

// isFencedCodeFence reports a markdown fenced-code delimiter line.
func isFencedCodeFence(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "```")
}

// isCommandOrConfigLine recognizes bare (unfenced) SQL / Shell / parameter
// lines that form an unsplittable command-config block. Headings and table
// rows are excluded so those paths stay with their own expanders.
func isCommandOrConfigLine(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" || markdownHeadingLine.MatchString(t) || isMarkdownTableRow(t) || isFencedCodeFence(t) {
		return false
	}
	lower := strings.ToLower(t)

	switch {
	case strings.HasPrefix(lower, "sql>"),
		strings.HasPrefix(lower, "rman>"),
		strings.HasPrefix(t, "$ "):
		return true
	case strings.Contains(lower, "alter system "),
		strings.Contains(lower, "alter database "),
		strings.HasPrefix(lower, "chmod "),
		strings.HasPrefix(lower, "chown "),
		strings.HasPrefix(lower, "export "),
		strings.Contains(lower, "scope=spfile"):
		return true
	case configAssignmentLine.MatchString(t):
		return true
	}

	// "$ su - oracle" style without trailing args already covered by "$ ".
	// Also accept lines that are clearly shell invocations starting with a
	// known binary (no path) when the rest looks like argv.
	if fields := strings.Fields(t); len(fields) >= 1 {
		cmd := strings.ToLower(fields[0])
		switch cmd {
		case "chmod", "chown", "export", "sqlplus", "asmcmd", "srvctl", "docker", "kubectl", "kubeadm":
			return true
		}
	}
	return false
}

// expandToAtomicBlock widens [relStart,relEnd] to the unsplittable block it
// touches, if any (docs/impl/v1/evidence.md 步骤 3.1). Priority: fenced code
// → markdown table → bare command/config run. kind is for debug logs.
func expandToAtomicBlock(contentLines []string, relStart, relEnd int) (int, int, string) {
	if start, end, ok := expandToFencedCodeBlock(contentLines, relStart, relEnd); ok {
		return start, end, "fenced_code"
	}
	if start, end := expandToTableBlock(contentLines, relStart, relEnd); start != relStart || end != relEnd {
		return start, end, "table"
	}
	if start, end, ok := expandToCommandConfigBlock(contentLines, relStart, relEnd); ok {
		return start, end, "command_config"
	}
	return relStart, relEnd, ""
}

// expandToFencedCodeBlock widens to the enclosing ``` … ``` block when the
// fragment overlaps any line of that fence (including delimiter lines).
func expandToFencedCodeBlock(contentLines []string, relStart, relEnd int) (int, int, bool) {
	inFence := false
	openLine := 0
	for idx, line := range contentLines {
		lineNo := idx + 1
		if !isFencedCodeFence(line) {
			continue
		}
		if !inFence {
			openLine = lineNo
			inFence = true
			continue
		}
		closeLine := lineNo
		if relEnd >= openLine && relStart <= closeLine {
			return openLine, closeLine, true
		}
		inFence = false
	}
	return relStart, relEnd, false
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

// expandToCommandConfigBlock widens to a contiguous run of bare SQL / Shell /
// parameter lines when the fragment intersects at least one such line.
// Blank lines and non-command prose terminate the run (same spirit as table
// contiguity — don't swallow neighboring narrative).
func expandToCommandConfigBlock(contentLines []string, relStart, relEnd int) (int, int, bool) {
	touches := false
	for i := relStart; i <= relEnd && i >= 1 && i <= len(contentLines); i++ {
		if isCommandOrConfigLine(contentLines[i-1]) {
			touches = true
			break
		}
	}
	if !touches {
		return relStart, relEnd, false
	}

	start := relStart
	for start > 1 && isCommandOrConfigLine(contentLines[start-2]) {
		start--
	}
	end := relEnd
	for end < len(contentLines) && isCommandOrConfigLine(contentLines[end]) {
		end++
	}
	return start, end, true
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
