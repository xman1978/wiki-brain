package unit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/rerank"
)

// preInsertDedupMinOverlapDefault mirrors BuildSegments' pattern for
// MinSegmentChars: used whenever cfg.Source.PreInsertDedupMinOverlap is left
// at its zero value.
const preInsertDedupMinOverlapDefault = 0.15

// preInsertDedupConcurrencyDefault is used whenever
// cfg.Source.PreInsertDedupConcurrency is left at its zero value.
const preInsertDedupConcurrencyDefault = 2

// preInsertDedupShortTokenMaxDefault is used whenever
// cfg.Source.PreInsertDedupShortTokenMax is left at its zero value. 4 was
// picked empirically: both real short-lead-in misses found so far
// ("(二)年度积分基准线" and "--杀掉数据库会话", punctuation excluded by
// tokenSet) have exactly 4 meaningful tokens on their short side.
const preInsertDedupShortTokenMaxDefault = 4

// unitCandidate is a located-but-not-yet-inserted unit — the shape
// fillGapsInMemory and dedupCandidates work on before any store write
// happens. id is the unit's final UUID, assigned once at creation (from the
// original extraction batch or from a gap-fill pass) so a duplicate merge or
// a later insert never needs to look it back up.
type unitCandidate struct {
	id        string
	llm       llmUnit
	points    []llmPoint
	lineStart int
	lineEnd   int
	// seg is the segment that produced this candidate — carried so that
	// document-level dedup can tell same-segment pairs (the range the old
	// per-segment scan already covered) from cross-segment ones, and so the
	// final insert knows each candidate's outline_id without a lookup.
	seg Segment
	// segIndex is seg's position in the document's segment list.
	segIndex int
	// promptVersion records which prompt generation produced the candidate
	// (main extract / retry / gap), stamped onto the row at insert.
	promptVersion string
}

type segmentExtraction struct {
	seg           Segment
	segIndex      int
	output        extractOutput
	promptVersion string
	ok            bool
}

// extractSegmentsPreInsertDedup is the unit-extraction path. Up to
// PreInsertDedupConcurrency segments have their extraction call in flight at
// once — the only part of a segment's work with no dependency on any other
// segment or the store — while everything that touches the database
// (locate/validate, gap-fill, dedup, insert) runs on a single goroutine, in
// whatever order extraction completes. Nothing but that one goroutine ever
// calls the store or the bleve indexes, so this needs no SQLite busy-timeout
// tuning or concurrent-write handling.
//
// Gap-filling and deduplication both run against the in-memory candidate
// batch before any of a segment's units reach the store, instead of
// inserting first and cleaning up afterward — most real duplicates
// originate from one extraction call splitting the same fact into two units
// (a heading and its own content, docs/impl/mvp/unit.md 3.3), and this skips
// the insert-then-update/delete churn that costs today.
//
// onSegmentDone is called once per segment as it finishes, for progress
// reporting — segments complete in extraction order, not document order, so
// callers must not assume it matches segment index.
func (s *Service) extractSegmentsPreInsertDedup(ctx context.Context, sourceTitle, sourceID string, segments []Segment, mdLines []string, onSegmentDone func()) error {
	concurrency := s.cfg.Source.PreInsertDedupConcurrency
	if concurrency <= 0 {
		concurrency = preInsertDedupConcurrencyDefault
	}

	type indexedSegment struct {
		seg   Segment
		index int
	}
	work := make(chan indexedSegment)
	results := make(chan segmentExtraction)

	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for is := range work {
				output, ok := s.extractSegmentOutputSplit(ctx, sourceID, is.seg, mdLines)
				results <- segmentExtraction{seg: is.seg, segIndex: is.index, output: output, promptVersion: promptVersionSplitExtract, ok: ok}
			}
		}()
	}

	go func() {
		for i, seg := range segments {
			work <- indexedSegment{seg: seg, index: i}
		}
		close(work)
		wg.Wait()
		close(results)
	}()

	// Candidates from every segment pool up here; nothing reaches the store
	// until the whole document has been extracted and deduplicated, so
	// cross-segment and non-adjacent duplicates are at least visible
	// (logDocumentCandidates) before insertion — the per-segment adjacent
	// scan alone was structurally blind to them.
	var pool []unitCandidate
	var extractionErr error
	for r := range results {
		if r.ok {
			pool = append(pool, s.collectSegmentCandidates(ctx, sourceID, r.seg, r.segIndex, mdLines, r.output, r.promptVersion)...)
		} else if extractionErr == nil {
			extractionErr = fmt.Errorf("segment extraction failed: source_id %s segment %q lines %d-%d", sourceID, r.seg.Title, r.seg.LineStart, r.seg.LineEnd)
		}
		if onSegmentDone != nil {
			onSegmentDone()
		}
	}
	if extractionErr != nil {
		return extractionErr
	}

	s.logDocumentCandidates(sourceID, mdLines, pool)

	semantics, err := s.extractRerankSemantics(ctx, sourceTitle, mdLines, pool)
	if err != nil {
		return fmt.Errorf("extract rerank semantics: %w", err)
	}
	if err := s.publishCandidates(sourceID, mdLines, pool, semantics); err != nil {
		return err
	}
	return nil
}

// collectSegmentCandidates is one segment's store-free half of the bypass
// pipeline: locate and validate each unit, then gap-fill and deduplicate the
// segment's own candidates entirely in memory.
// The per-segment dedupCandidates pass here preserves the established
// adjacent-pair merge behavior exactly; document-level discovery across
// segments happens later over the pooled result (logDocumentCandidates).
// Units that fail locate/validate are retried via retryFailedUnit and the
// result joins this same candidate pool — unlike the sequential path's
// handleFailedUnit, nothing on this path inserts around dedup anymore.
func (s *Service) collectSegmentCandidates(ctx context.Context, sourceID string, seg Segment, segIndex int, mdLines []string, output extractOutput, promptVersion string) []unitCandidate {
	localToUUID := make(map[string]string)
	for _, u := range output.Units {
		localToUUID[u.UnitID] = uuid.New().String()
	}

	var candidates []unitCandidate
	cursor := seg.LineStart
	for _, u := range output.Units {
		lineStart, lineEnd, nextCursor, locateOK := LocateUnitBounds(mdLines, seg, u.LineStart, u.FirstLineAnchor, u.LineEnd, u.LastLineAnchor, cursor)
		if locateOK {
			cursor = nextCursor
		}
		if !locateOK || !s.validateUnit(u, output.Points) {
			// Unlike the sequential path's handleFailedUnit, the retry result
			// joins the candidate pool like any other unit — it gets gap-fill
			// and dedup coverage instead of being inserted around them (the
			// old direct insert here is how v5-retry twins of v6 units ended
			// up stored next to each other).
			if c, ok := s.retryFailedUnit(ctx, sourceID, seg, segIndex, u, mdLines); ok {
				candidates = append(candidates, c)
			}
			continue
		}

		var unitPoints []llmPoint
		for _, p := range output.Points {
			if p.UnitID == u.UnitID {
				unitPoints = append(unitPoints, p)
			}
		}
		lineStart, lineEnd = WidenBoundsFromPoints(mdLines, seg, lineStart, lineEnd, unitPoints)

		candidates = append(candidates, unitCandidate{id: localToUUID[u.UnitID], llm: u, points: unitPoints, lineStart: lineStart, lineEnd: lineEnd, seg: seg, segIndex: segIndex, promptVersion: promptVersion})
	}

	candidates = s.fillGapsInMemory(ctx, sourceID, seg, segIndex, mdLines, candidates)
	return s.dedupCandidates(ctx, sourceID, mdLines, candidates)
}

// logDocumentCandidates runs document-level multi-path recall over the whole
// pooled candidate set and logs, without merging, every nominated pair the
// per-segment adjacent scan could not have judged: pairs spanning two
// segments, and same-segment pairs too far apart for the adjacency window.
// Diagnostic-only by design (first rollout stage): the log output is for
// verifying recall quality against human-annotated duplicates before any
// automatic cross-segment merging is enabled. The logged pairs are also
// returned so tests can assert recall without scraping log output.
func (s *Service) logDocumentCandidates(sourceID string, mdLines []string, pool []unitCandidate) []CandidatePair {
	if len(pool) < 2 {
		return nil
	}

	dedupUnits := make([]DedupUnit, len(pool))
	for i, c := range pool {
		var pointTexts []string
		for _, p := range c.points {
			pointTexts = append(pointTexts, p.Content)
		}
		dedupUnits[i] = DedupUnit{
			UnitID:       c.id,
			Center:       c.llm.Center,
			LineStart:    c.lineStart,
			LineEnd:      c.lineEnd,
			SegmentIndex: c.segIndex,
			PointsText:   strings.Join(pointTexts, "\n"),
			SourceText:   sliceLines(mdLines, c.lineStart, c.lineEnd),
		}
	}

	var diagnostic []CandidatePair
	for _, p := range CandidatePairs(dedupUnits) {
		// Same-segment pairs within the adjacency window were already judged
		// (and possibly merged) by dedupCandidates — re-reporting them would
		// only echo a decision that's already been made.
		if !p.CrossSegment() && p.RangeRelation != RangeDistant {
			continue
		}
		diagnostic = append(diagnostic, p)
		slog.Info("unit: dedup candidate (diagnostic only)",
			"source_id", sourceID,
			"unit_a", p.A.UnitID, "center_a", p.A.Center, "range_a", []int{p.A.LineStart, p.A.LineEnd},
			"unit_b", p.B.UnitID, "center_b", p.B.Center, "range_b", []int{p.B.LineStart, p.B.LineEnd},
			"cross_segment", p.CrossSegment(), "range_relation", p.RangeRelation,
			"center_sim", p.CenterSim, "point_sim", p.PointSim, "source_sim", p.SourceSim,
			"reasons", p.Reasons)
	}
	return diagnostic
}

// publishCandidates commits the full document generation before rewriting
// any Bleve documents. points contains both cascaded old points and new
// points, matching the affected unit set returned by PublishGeneration.
func (s *Service) publishCandidates(sourceID string, mdLines []string, pool []unitCandidate, semantics map[string]rerank.Semantics) error {
	superseded, inserted, points, err := s.store.PublishGeneration(sourceID, pool, semantics)
	if err != nil {
		return fmt.Errorf("publish generation: %w", err)
	}
	units := make([]KnowledgeUnit, 0, len(superseded)+len(inserted))
	units = append(units, superseded...)
	units = append(units, inserted...)
	for i := range units {
		if err := s.indexUnitWithError(&units[i], mdLines); err != nil {
			return fmt.Errorf("publish generation indexes: %w", err)
		}
	}
	for i := range points {
		if err := s.indexPointWithError(&points[i]); err != nil {
			return fmt.Errorf("publish generation indexes: %w", err)
		}
	}
	return nil
}

// fillGapsInMemory mirrors fillGaps but resolves gaps against the in-memory
// candidate batch, so gap-filling — like dedupCandidates — runs before any
// of the segment's units reach the store. gapRanges, isTrivialGap and
// gapContextText are pure (no store dependency) and are reused as-is from
// gapfill.go.
func (s *Service) fillGapsInMemory(ctx context.Context, sourceID string, seg Segment, segIndex int, mdLines []string, candidates []unitCandidate) []unitCandidate {
	if len(candidates) == 0 {
		return candidates
	}

	ranges := make([][2]int, len(candidates))
	for i, c := range candidates {
		ranges[i] = [2]int{c.lineStart, c.lineEnd}
	}

	for _, gap := range gapRanges(seg.LineStart, seg.LineEnd, ranges) {
		gapStart, gapEnd := gap[0], gap[1]

		action := ""
		if isTrivialGap(mdLines, gapStart, gapEnd) {
			slog.Debug("unit: gap is trivial, merging without an LLM call", "source_id", sourceID, "line_start", gapStart, "line_end", gapEnd)
		} else {
			var extra []unitCandidate
			extra, action = s.extractGapCandidates(ctx, sourceID, seg, segIndex, mdLines, gapStart, gapEnd)
			if len(extra) > 0 {
				slog.Debug("unit: gap resolved by its own extraction", "source_id", sourceID, "line_start", gapStart, "line_end", gapEnd, "units_created", len(extra))
				candidates = append(candidates, extra...)
				continue
			}
			slog.Info("unit: gap produced no standalone unit, merging into a neighbor", "source_id", sourceID, "line_start", gapStart, "line_end", gapEnd, "action", action)
		}
		candidates = mergeGapIntoNeighborInMemory(sourceID, candidates, gapStart, gapEnd, action)
	}
	return candidates
}

// extractGapCandidates mirrors extractGap (same unit_gap_extract.md
// placement judgment) but returns in-memory candidates instead of inserting
// — nothing in this pipeline has reached the store yet. The returned action
// guides fillGapsInMemory's merge when no candidates are produced.
func (s *Service) extractGapCandidates(ctx context.Context, sourceID string, seg Segment, segIndex int, mdLines []string, gapStart, gapEnd int) ([]unitCandidate, string) {
	textContent := gapContextText(mdLines, seg, gapStart, gapEnd)

	vars := map[string]string{
		"outline_title":  seg.Title,
		"gap_line_start": strconv.Itoa(gapStart),
		"gap_line_end":   strconv.Itoa(gapEnd),
		"text_content":   textContent,
	}

	data, err := s.llmClient.CompleteJSON(ctx, "unit_gap_extract.md", vars, "extraction")
	if err != nil {
		slog.Warn("unit: gap extraction call failed", "source_id", sourceID, "line_start", gapStart, "line_end", gapEnd, "error", err)
		return nil, ""
	}

	var output gapExtractOutput
	if err := json.Unmarshal(data, &output); err != nil {
		slog.Warn("unit: gap extraction JSON parse failed", "source_id", sourceID, "line_start", gapStart, "line_end", gapEnd, "error", err)
		return nil, ""
	}
	if output.Action != gapActionStandalone || len(output.Units) == 0 {
		slog.Debug("unit: gap judged not worth its own unit", "source_id", sourceID, "line_start", gapStart, "line_end", gapEnd, "action", output.Action)
		return nil, output.Action
	}

	gapSeg := Segment{OutlineID: seg.OutlineID, Title: seg.Title, LineStart: gapStart, LineEnd: gapEnd}

	var out []unitCandidate
	cursor := gapSeg.LineStart
	for _, u := range output.Units {
		lineStart, lineEnd, nextCursor, locateOK := LocateUnitBounds(mdLines, gapSeg, u.LineStart, u.FirstLineAnchor, u.LineEnd, u.LastLineAnchor, cursor)
		if locateOK {
			cursor = nextCursor
		}
		if !locateOK {
			slog.Info("unit: gap extraction proposed a unit that could not be located within the gap — likely anchored in the surrounding context instead of the target range",
				"source_id", sourceID, "gap_start", gapStart, "gap_end", gapEnd,
				"center", u.Center, "reported_line_start", u.LineStart, "first_line_anchor", u.FirstLineAnchor,
				"reported_line_end", u.LineEnd, "last_line_anchor", u.LastLineAnchor)
			continue
		}
		if !s.validateUnit(u, output.Points) {
			slog.Info("unit: gap extraction proposed a unit that failed validation (empty center or no points)",
				"source_id", sourceID, "gap_start", gapStart, "gap_end", gapEnd, "center", u.Center, "located_range", []int{lineStart, lineEnd})
			continue
		}

		var unitPoints []llmPoint
		for _, p := range output.Points {
			if p.UnitID == u.UnitID {
				unitPoints = append(unitPoints, p)
			}
		}
		// Widened against gapSeg, not the parent seg — same reasoning as
		// extractGap: an anchor landing in the context zone must fail to
		// locate here too.
		lineStart, lineEnd = WidenBoundsFromPoints(mdLines, gapSeg, lineStart, lineEnd, unitPoints)

		out = append(out, unitCandidate{id: uuid.New().String(), llm: u, points: unitPoints, lineStart: lineStart, lineEnd: lineEnd, seg: seg, segIndex: segIndex, promptVersion: promptVersionGapExtract})
	}

	return out, gapActionStandalone
}

// mergeGapIntoNeighborInMemory mirrors mergeGapIntoNeighbor: absorbs
// [gapStart, gapEnd] into a neighboring candidate, widening its bounds in
// place. action (from unit_gap_extract.md) may direct the choice —
// absorb_left restricts to candidates before the gap, absorb_right to ones
// after it, with a nearest-neighbor fallback when the directed side is empty.
// There's no store UPDATE to issue and nothing to reindex — the candidate
// hasn't been inserted yet.
func mergeGapIntoNeighborInMemory(sourceID string, candidates []unitCandidate, gapStart, gapEnd int, action string) []unitCandidate {
	best := -1
	bestDist := 0
	for i, c := range candidates {
		var dist int
		switch {
		case gapStart > c.lineEnd: // c sits before the gap
			if action == gapActionAbsorbRight {
				continue
			}
			dist = gapStart - c.lineEnd
		case gapEnd < c.lineStart: // c sits after the gap
			if action == gapActionAbsorbLeft {
				continue
			}
			dist = c.lineStart - gapEnd
		default:
			continue // already overlaps — shouldn't happen, skip
		}
		if best == -1 || dist < bestDist {
			best = i
			bestDist = dist
		}
	}
	if best == -1 && (action == gapActionAbsorbLeft || action == gapActionAbsorbRight) {
		// The directed side has no candidate (gap at the segment's edge) —
		// retry undirected rather than leaving the gap uncovered.
		return mergeGapIntoNeighborInMemory(sourceID, candidates, gapStart, gapEnd, "")
	}
	if best == -1 {
		slog.Warn("unit: gap has no eligible neighbor to merge into — every candidate reports overlapping it, so the gap will remain uncovered",
			"source_id", sourceID, "line_start", gapStart, "line_end", gapEnd, "candidate_count", len(candidates))
		return candidates
	}

	if gapStart < candidates[best].lineStart {
		candidates[best].lineStart = gapStart
	}
	if gapEnd > candidates[best].lineEnd {
		candidates[best].lineEnd = gapEnd
	}
	return candidates
}

// dedupCandidates scans adjacent candidates (dedupMaxGapLines window,
// restart-from-top-after-a-merge loop) and adds a text-overlap gate in front
// of the LLM call: a pair whose underlying lines share essentially no
// vocabulary is skipped without ever asking the model. This only ever skips
// a call — it never auto-confirms a merge — so a real duplicate with
// unusually little lexical overlap still gets its chance to be judged (the
// LLM is still the one deciding "duplicate or not").
//
// The gate itself is skipped — always asking the LLM — whenever either
// side's text is at or below PreInsertDedupShortTokenMax tokens: a short
// lead-in (a heading or a code comment) can legitimately share zero literal
// vocabulary with the content it introduces (a Chinese comment above an
// English/SQL command has no token overlap with it under any set-similarity
// formula, found via test/markdown/神通数据库优化.md's "--杀掉数据库会话" /
// "kill session sid abort;" pair), so the overlap score isn't trustworthy
// exactly where short-vs-long pairs matter most.
func (s *Service) dedupCandidates(ctx context.Context, sourceID string, mdLines []string, candidates []unitCandidate) []unitCandidate {
	if len(candidates) < 2 {
		return candidates
	}

	minOverlap := s.cfg.Source.PreInsertDedupMinOverlap
	if minOverlap <= 0 {
		minOverlap = preInsertDedupMinOverlapDefault
	}
	shortTokenMax := s.cfg.Source.PreInsertDedupShortTokenMax
	if shortTokenMax <= 0 {
		shortTokenMax = preInsertDedupShortTokenMaxDefault
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].lineStart != candidates[j].lineStart {
			return candidates[i].lineStart < candidates[j].lineStart
		}
		return candidates[i].lineEnd < candidates[j].lineEnd
	})

	for {
		mergedAny := false
		for i := 0; i+1 < len(candidates); i++ {
			a, b := candidates[i], candidates[i+1]
			if b.lineStart > a.lineEnd+1+dedupMaxGapLines {
				continue
			}

			aText := sliceLines(mdLines, a.lineStart, a.lineEnd)
			bText := sliceLines(mdLines, b.lineStart, b.lineEnd)
			isShort := tokenCount(aText) <= shortTokenMax || tokenCount(bText) <= shortTokenMax
			if !isShort && tokenOverlap(aText, bText) < minOverlap {
				continue
			}

			merged, ok := s.resolveCandidateDuplicate(ctx, sourceID, mdLines, a, b)
			if !ok {
				continue
			}

			next := make([]unitCandidate, 0, len(candidates)-1)
			next = append(next, candidates[:i]...)
			next = append(next, merged)
			next = append(next, candidates[i+2:]...)
			candidates = next
			mergedAny = true
			break
		}
		if !mergedAny {
			return candidates
		}
	}
}

// resolveCandidateDuplicate runs the judgePair ladder (deterministic merge →
// unit_dedup_classify.md → unit_dedup_merge.md), reading a/b from the
// candidate batch and returning the merged candidate instead of writing it —
// nothing here has been inserted yet, so there's nothing to update or
// delete. The survivor keeps a's id.
func (s *Service) resolveCandidateDuplicate(ctx context.Context, sourceID string, mdLines []string, a, b unitCandidate) (unitCandidate, bool) {
	vars := map[string]string{
		"a_line_start": strconv.Itoa(a.lineStart),
		"a_line_end":   strconv.Itoa(a.lineEnd),
		"a_center":     a.llm.Center,
		"a_points":     formatLLMPointsForPrompt(a.points),
		"a_text":       sliceLines(mdLines, a.lineStart, a.lineEnd),
		"b_line_start": strconv.Itoa(b.lineStart),
		"b_line_end":   strconv.Itoa(b.lineEnd),
		"b_center":     b.llm.Center,
		"b_points":     formatLLMPointsForPrompt(b.points),
		"b_text":       sliceLines(mdLines, b.lineStart, b.lineEnd),
	}

	merged := s.judgePair(ctx, sourceID, vars,
		a.llm.Center, b.llm.Center,
		a.lineStart, a.lineEnd, b.lineStart, b.lineEnd,
		llmPointsToDedupPoints(a.points), llmPointsToDedupPoints(b.points))
	if merged == nil {
		return unitCandidate{}, false
	}

	newStart, newEnd := a.lineStart, a.lineEnd
	if b.lineStart < newStart {
		newStart = b.lineStart
	}
	if b.lineEnd > newEnd {
		newEnd = b.lineEnd
	}

	mergedPoints := make([]llmPoint, len(merged.Points))
	for i, p := range merged.Points {
		mergedPoints[i] = llmPoint{UnitID: a.llm.UnitID, Content: p.Content, Type: p.Type}
	}

	return unitCandidate{
		id:            a.id,
		llm:           llmUnit{UnitID: a.llm.UnitID, Center: merged.Center},
		points:        mergedPoints,
		lineStart:     newStart,
		lineEnd:       newEnd,
		seg:           a.seg,
		segIndex:      a.segIndex,
		promptVersion: a.promptVersion,
	}, true
}

// llmPointsToDedupPoints adapts a candidate's extraction-shaped points to
// the judgment/merge shape.
func llmPointsToDedupPoints(points []llmPoint) []llmDedupPoint {
	out := make([]llmDedupPoint, len(points))
	for i, p := range points {
		out[i] = llmDedupPoint{Content: p.Content, Type: p.Type}
	}
	return out
}

func formatLLMPointsForPrompt(points []llmPoint) string {
	var sb strings.Builder
	for _, p := range points {
		sb.WriteString("- [" + p.Type + "] " + p.Content + "\n")
	}
	return sb.String()
}
