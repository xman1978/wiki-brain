package unit

import "github.com/jxman78/wiki-brain/internal/foundation/textmatch"

// LocateUnitBounds resolves a unit's absolute line_start/line_end by finding
// firstAnchor/lastAnchor — verbatim text the model copied from the unit's
// first and last line — within mdLines[seg.LineStart-1 : seg.LineEnd].
//
// This replaces trusting a model-reported line number (which requires the
// model to correctly count/copy a bracketed index over a potentially long
// segment, the likely cause of a KU boundary silently spanning far beyond its
// actual content): the model only has to give back text, and the program
// finds where it really is via textmatch.MatchFragment (exact, falling back
// to whitespace-collapsed fuzzy match), the same "摘选不是改写，结果程序可
// 校验" principle already used by evidence mining (docs/impl/v1/evidence.md).
//
// cursor hints where to start searching for firstAnchor (pass seg.LineStart
// for the first unit in a segment, then each call's returned nextCursor for
// the following one) to disambiguate repeated line content (e.g. "# su –
// oracle" appearing many times in a segment) — it is only a search-order
// hint, not an enforced non-overlap constraint, since some segments
// legitimately have a broader "overview" unit whose span contains narrower
// sibling units. If nothing matches from cursor onward, the search restarts
// from seg.LineStart once, to tolerate the model not listing units in strict
// document order.
//
// Both anchors, and every candidate line, are matched independently
// line-by-line (never against the segment as one blob) so the resolved
// boundary can only ever land on an actual line within the segment — the
// exact failure mode that let one bad unit swallow a dozen unrelated ones is
// structurally impossible here.
func LocateUnitBounds(mdLines []string, seg Segment, firstAnchor, lastAnchor string, cursor int) (lineStart, lineEnd, nextCursor int, ok bool) {
	lineStart, ok = findAnchorLine(mdLines, cursor, seg.LineEnd, firstAnchor)
	if !ok && cursor != seg.LineStart {
		lineStart, ok = findAnchorLine(mdLines, seg.LineStart, seg.LineEnd, firstAnchor)
	}
	if !ok {
		return 0, 0, cursor, false
	}

	lineEnd, ok = findAnchorLine(mdLines, lineStart, seg.LineEnd, lastAnchor)
	if !ok {
		return 0, 0, cursor, false
	}

	return lineStart, lineEnd, lineEnd + 1, true
}

// findAnchorLine scans mdLines[from-1 : to] (1-based, inclusive) and returns
// the absolute line number of the first line containing anchor.
func findAnchorLine(mdLines []string, from, to int, anchor string) (int, bool) {
	if from < 1 {
		from = 1
	}
	if to > len(mdLines) {
		to = len(mdLines)
	}
	for i := from; i <= to; i++ {
		if _, _, _, ok := textmatch.MatchFragment(mdLines[i-1], anchor); ok {
			return i, true
		}
	}
	return 0, false
}
