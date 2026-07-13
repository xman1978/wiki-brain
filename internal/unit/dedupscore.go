package unit

import (
	"sort"
	"strings"
	"unicode"
)

// This file is the candidate-discovery half of duplicate handling: pure
// scoring functions with no LLM and no store access, shared by the in-
// pipeline document-level dedup pass and the offline cmd/dedup-report tool.
// It only ever proposes pairs worth judging — deciding whether a pair really
// is the same fact stated twice stays with unit_dedup.md (the model), same
// division of labor as dedupCandidates.

// Range relations between two units' line spans, ordered from strongest to
// weakest duplicate signal. exact/contains/overlap are structural candidates
// that always deserve a judgment; nearby reuses dedupMaxGapLines (the
// adjacency window the segment-local scan has used all along); distant pairs
// only become candidates through text similarity.
const (
	RangeExact    = "exact"
	RangeContains = "contains"
	RangeOverlap  = "overlap"
	RangeNearby   = "nearby"
	RangeDistant  = "distant"
)

// Candidate thresholds. Recall-oriented: these gate what gets *reported* (or
// judged), never what gets merged, so erring loose only costs a log line or
// an LLM call, while erring tight silently hides a real duplicate.
const (
	// centerSimMin flags two centers written as near-identical phrasings of
	// the same topic ("达梦数据库统计信息更新" vs "更新达梦数据库统计信息"
	// scores well above this under char bigrams regardless of word order).
	centerSimMin = 0.6
	// pointSimMin flags two units whose knowledge points say the same things
	// even when the centers were worded apart.
	pointSimMin = 0.5
	// sourceSimMin flags heavy vocabulary overlap between the two units' own
	// source lines, measured as Jaccard — NOT tokenOverlap's containment
	// coefficient. Containment is right for the adjacent heading-vs-content
	// gate (textsim.go's flagship case) but wrong for arbitrary-pair
	// discovery: a short总则/intro unit's generic vocabulary (公司/规定/员工…)
	// is trivially contained in any longer section of the same document
	// (observed 0.52 against an unrelated section in 考勤管理制度). Jaccard's
	// union denominator dilutes that while a true restatement still scores
	// high, since both sides' vocabularies nearly coincide.
	sourceSimMin = 0.4
	// centerSubstringMinRunes keeps the "one center is the core of the other"
	// rule from firing on trivially short fragments.
	centerSubstringMinRunes = 4
	// sourceOverlapMinTokens: the source_overlap signal uses tokenOverlap's
	// containment coefficient (shared/min), which over-fires when either side
	// is short — a 3-line总则 unit's vocabulary is trivially contained in any
	// longer section about the same document. Short units are still reachable
	// through the range/center/point signals; this guard only tames the one
	// signal whose math is unreliable for them (observed: a 3-line unit
	// scoring 0.52 against an unrelated section in 考勤管理制度).
	sourceOverlapMinTokens = 8
)

// DedupUnit is the minimal view of a knowledge unit that candidate discovery
// needs — satisfiable both from in-memory unitCandidates (pre-insert) and
// from store rows (offline report), which is why it holds plain values
// instead of either of those types.
type DedupUnit struct {
	UnitID    string
	Center    string
	LineStart int
	LineEnd   int
	// SegmentIndex is which extraction segment produced the unit, when known
	// (-1 offline): pairs from the same segment fall inside what the existing
	// adjacent-pair scan could already see, pairs across segments are exactly
	// the blind spot document-level discovery exists to cover.
	SegmentIndex int
	// PointsText is the unit's knowledge-point contents joined by newline.
	PointsText string
	// SourceText is the unit's own markdown lines [LineStart, LineEnd].
	SourceText string
}

// CandidatePair is one explainable "these two might be the same fact"
// nomination. Reasons lists every rule that fired, so a human reading the
// report (or a log line) can tell a structural hit from a text-similarity
// hit without re-deriving the scores.
type CandidatePair struct {
	A, B          DedupUnit
	RangeRelation string
	CenterSim     float64
	PointSim      float64
	SourceSim     float64
	Reasons       []string
}

// CrossSegment reports whether the pair spans two different extraction
// segments (unknowable offline, where SegmentIndex is -1 on both sides).
func (p CandidatePair) CrossSegment() bool {
	return p.A.SegmentIndex != p.B.SegmentIndex
}

// CandidatePairs runs multi-path recall over every pair of units in one
// document. Full O(n²) on purpose: a document yields 10–30 units, a few
// hundred cheap comparisons — far simpler and more reliable than standing up
// an FTS index for it (revisit only if single documents start exceeding ~200
// units). Pairs are returned ordered by (A.LineStart, B.LineStart) with
// A always the earlier unit.
func CandidatePairs(units []DedupUnit) []CandidatePair {
	sorted := make([]DedupUnit, len(units))
	copy(sorted, units)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].LineStart != sorted[j].LineStart {
			return sorted[i].LineStart < sorted[j].LineStart
		}
		return sorted[i].LineEnd < sorted[j].LineEnd
	})

	var pairs []CandidatePair
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if p, ok := scorePair(sorted[i], sorted[j]); ok {
				pairs = append(pairs, p)
			}
		}
	}
	return pairs
}

func scorePair(a, b DedupUnit) (CandidatePair, bool) {
	p := CandidatePair{
		A:             a,
		B:             b,
		RangeRelation: rangeRelation(a.LineStart, a.LineEnd, b.LineStart, b.LineEnd),
	}

	// Structural signals.
	switch p.RangeRelation {
	case RangeExact:
		p.Reasons = append(p.Reasons, "range_exact")
	case RangeContains:
		p.Reasons = append(p.Reasons, "range_contains")
	case RangeOverlap:
		p.Reasons = append(p.Reasons, "range_overlap")
	}

	normA, normB := centerNormalize(a.Center), centerNormalize(b.Center)
	if normA != "" && normA == normB {
		p.Reasons = append(p.Reasons, "center_identical")
	} else if centerSubstring(normA, normB) {
		p.Reasons = append(p.Reasons, "center_substring")
	}

	// Text-similarity signals.
	p.CenterSim = charNGramSim(normA, normB)
	if p.CenterSim >= centerSimMin {
		p.Reasons = append(p.Reasons, "center_similar")
	}
	p.PointSim = charNGramSim(normText(a.PointsText), normText(b.PointsText))
	if a.PointsText != "" && b.PointsText != "" && p.PointSim >= pointSimMin {
		p.Reasons = append(p.Reasons, "points_similar")
	}
	if a.SourceText != "" && b.SourceText != "" {
		p.SourceSim = jaccardOverlap(a.SourceText, b.SourceText)
		if p.SourceSim >= sourceSimMin &&
			tokenCount(a.SourceText) >= sourceOverlapMinTokens &&
			tokenCount(b.SourceText) >= sourceOverlapMinTokens {
			p.Reasons = append(p.Reasons, "source_overlap")
		}
	}

	return p, len(p.Reasons) > 0
}

// rangeRelation classifies how two line spans sit relative to each other.
// a must start no later than b (callers pass line-sorted pairs); it still
// handles either order defensively since the offline tool feeds it raw rows.
func rangeRelation(aStart, aEnd, bStart, bEnd int) string {
	if aStart == bStart && aEnd == bEnd {
		return RangeExact
	}
	if (aStart <= bStart && aEnd >= bEnd) || (bStart <= aStart && bEnd >= aEnd) {
		return RangeContains
	}
	if aStart <= bEnd && bStart <= aEnd {
		return RangeOverlap
	}
	gap := bStart - aEnd
	if bEnd < aStart {
		gap = aStart - bEnd
	}
	if gap <= 1+dedupMaxGapLines {
		return RangeNearby
	}
	return RangeDistant
}

// centerNormalize strips whitespace, punctuation/symbols, and parenthesized
// additions, lowercasing the rest — so "用户权限申请（含审批）" and
// "用户权限申请" compare equal, and word-order/punctuation noise doesn't
// defeat the identical/substring rules.
func centerNormalize(s string) string {
	s = stripParenthetical(s)
	var sb strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		sb.WriteRune(unicode.ToLower(r))
	}
	return sb.String()
}

// normText is centerNormalize without parenthetical stripping — for point/
// body text, parenthesized content is real content.
func normText(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		sb.WriteRune(unicode.ToLower(r))
	}
	return sb.String()
}

// stripParenthetical removes （…）/(…) spans, tolerating unclosed ones.
func stripParenthetical(s string) string {
	var sb strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '（', '(':
			depth++
		case '）', ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				sb.WriteRune(r)
			}
		}
	}
	return sb.String()
}

// centerSubstring reports whether the shorter normalized center is the main
// body of the longer one: contained in it, at least centerSubstringMinRunes
// long, and covering at least half of the longer center — "达梦统计信息更新"
// inside "达梦统计信息更新操作步骤" fires, a two-character fragment doesn't.
func centerSubstring(a, b string) bool {
	short, long := a, b
	if len([]rune(short)) > len([]rune(long)) {
		short, long = long, short
	}
	shortLen, longLen := len([]rune(short)), len([]rune(long))
	if shortLen < centerSubstringMinRunes || longLen == 0 {
		return false
	}
	if !strings.Contains(long, short) {
		return false
	}
	return float64(shortLen) >= 0.5*float64(longLen)
}

// charNGramSim is the Dice coefficient over character-bigram sets of the
// (already normalized) inputs. Character n-grams instead of gse tokens here:
// short titles, parameter names, SQL fragments, and mixed Chinese/English
// segment poorly and inconsistently, while bigrams degrade gracefully and
// are insensitive to word order. Single-rune inputs fall back to equality.
func charNGramSim(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 || len(rb) == 0 {
		return 0
	}
	if len(ra) == 1 || len(rb) == 1 {
		if a == b {
			return 1
		}
		return 0
	}

	setA := bigramSet(ra)
	setB := bigramSet(rb)
	shared := 0
	for g := range setA {
		if setB[g] {
			shared++
		}
	}
	return 2 * float64(shared) / float64(len(setA)+len(setB))
}

// jaccardOverlap is shared/union over gse token sets — see sourceSimMin for
// why discovery uses this instead of textsim.go's containment tokenOverlap.
func jaccardOverlap(a, b string) float64 {
	setA := tokenSet(a)
	setB := tokenSet(b)
	if len(setA) == 0 || len(setB) == 0 {
		return 0
	}
	shared := 0
	for tok := range setA {
		if setB[tok] {
			shared++
		}
	}
	return float64(shared) / float64(len(setA)+len(setB)-shared)
}

func bigramSet(runes []rune) map[string]bool {
	set := make(map[string]bool, len(runes))
	for i := 0; i+1 < len(runes); i++ {
		set[string(runes[i:i+2])] = true
	}
	return set
}
