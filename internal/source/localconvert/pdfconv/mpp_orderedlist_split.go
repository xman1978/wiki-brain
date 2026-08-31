package pdfconv

// Ports docs/impl/v1/pdf-port/03-toc-cleanup-sequence.md's
// splitConcatenatedOrderedListLines / splitSingleLineByOrderedMarkersAndTail
// / splitTailOrgDateInListLine / isOrderedMarkerInsideNumericHierarchyPrefix
// / isMarkdownTableRowLine — the cleanOutput step (§ "算法：cleanOutput"
// step 3) that re-splits a line where PDF hard-wrap/geometry merging glued
// multiple "（1）……（2）……（3）……" ordered-list markers into one physical
// line. mergeLines (geometry_merge.go) and splitTextBlockByEmbeddedOrderedMarkers
// (geometry_text.go) only see one page-row at a time and can't detect a
// marker that only becomes "the second one in this line" after wrapped
// rows have already been merged into a paragraph — this stage runs later,
// on the fully reconstructed markdown text, as the documented safety net
// for exactly that case.
//
// Scope reduction: splitStructuralHeadingSegments (=
// ChapterTocLineRemover.splitEmbeddedCnSectionHeadings, pdf-port/04) and
// splitEmbeddedPipeTableLines / mergeWrappedListContinuationLines (the
// other two cleanOutput steps) are not ported here — no test fixture
// currently exercises them. Each segment below is returned as-is instead
// of being fed through splitStructuralHeadingSegments; if a fixture shows
// glued "N.……二、……" content, this is the place to add it.

import (
	"regexp"
	"strings"
)

// isMarkdownTableRowLine ports isMarkdownTableRowLine (pdf-port/03,
// "算法：splitSingleLineByOrderedMarkersAndTail" step 2).
func isMarkdownTableRowLine(t string) bool {
	if len(t) < 2 {
		return false
	}
	return t[0] == '|' && t[len(t)-1] == '|'
}

// numericHierarchyPrefixTailRe matches a numeric-hierarchy prefix ending
// right at the candidate marker start, e.g. "2.5." before "1" in "2.5.1.".
var numericHierarchyPrefixTailRe = regexp.MustCompile(`\d+(?:\.\d+)*\.$`)

// isOrderedMarkerInsideNumericHierarchyPrefix ports the namesake
// (pdf-port/03 "潜在遗漏" note L527): a marker match is not an independent
// list item start if everything before it (prefix) ends in a
// numeric-hierarchy prefix like "2.5.1." — that "1." is a hierarchy level,
// not a new list item.
func isOrderedMarkerInsideNumericHierarchyPrefix(prefix string) bool {
	return numericHierarchyPrefixTailRe.MatchString(prefix)
}

// splitListBodyAndOrgTail ports splitTailOrgDateInListLine's step 4 pairing
// helper, reusing the same org-suffix regex as splitTailOrgDateIfNeeded
// (geometry_text.go) — kept as a separate string-based helper since that
// one operates on TextBlock, not raw strings.
func splitListBodyAndOrgTail(beforeDate string) (listBody, org string, ok bool) {
	orgLoc := orgSuffixAtEndRe.FindStringSubmatchIndex(beforeDate)
	if orgLoc == nil {
		return "", "", false
	}
	org = beforeDate[orgLoc[2]:orgLoc[3]]
	listBody = strings.TrimSpace(beforeDate[:orgLoc[2]])
	if listBody == "" || org == "" {
		return "", "", false
	}
	return listBody, org, true
}

// splitTailOrgDateInListLine ports the namesake (pdf-port/03): splits a
// "N.现场勘察地点……某某局 2024年5月1日"-shaped line into
// [listBody, org, date], or returns [line] unchanged when it doesn't fit
// that shape.
func splitTailOrgDateInListLine(line string) []string {
	t := strings.TrimSpace(line)
	if !IsListItem(t) {
		return []string{line}
	}
	loc := dateAtLineEndRe.FindStringSubmatchIndex(t)
	if loc == nil {
		return []string{t}
	}
	date := strings.TrimSpace(t[loc[2]:loc[3]])
	beforeDate := strings.TrimSpace(t[:loc[2]])
	if beforeDate == "" || date == "" {
		return []string{t}
	}
	listBody, org, ok := splitListBodyAndOrgTail(beforeDate)
	if !ok {
		return []string{t}
	}
	return []string{listBody, org, date}
}

// splitSingleLineByOrderedMarkersAndTail ports the namesake.
func splitSingleLineByOrderedMarkersAndTail(line string) []string {
	raw := line
	t := strings.TrimSpace(raw)
	if t == "" {
		return []string{raw}
	}
	if isMarkdownTableRowLine(t) {
		return []string{raw}
	}

	runes := []rune(raw)

	// embeddedOrderedMarkerStarts (geometry_text.go) already implements the
	// EMBEDDED_ORDERED_LIST_MARKER matching (both alternatives, digit-form
	// deduped against paren-form) and returns rune-offset starts; this
	// step only adds the numeric-hierarchy-prefix exclusion
	// ("2.5.1." — that "1." is a level, not a new list item) on top of it.
	var starts []int
	for _, r := range embeddedOrderedMarkerStarts(raw) {
		if isOrderedMarkerInsideNumericHierarchyPrefix(string(runes[:r])) {
			continue
		}
		starts = append(starts, r)
	}

	if len(starts) < 2 {
		var out []string
		out = append(out, splitTailOrgDateInListLine(raw)...)
		if len(out) == 0 {
			return []string{raw}
		}
		return out
	}

	var out []string
	if starts[0] > 0 {
		head := strings.TrimSpace(string(runes[:starts[0]]))
		if head != "" {
			out = append(out, head)
		}
	}
	for i, s := range starts {
		e := len(runes)
		if i+1 < len(starts) {
			e = starts[i+1]
		}
		chunk := strings.TrimSpace(string(runes[s:e]))
		if chunk == "" {
			continue
		}
		out = append(out, splitTailOrgDateInListLine(chunk)...)
	}
	if len(out) == 0 {
		return []string{raw}
	}
	return out
}

// splitConcatenatedOrderedListLines ports the namesake (pdf-port/03
// "算法：cleanOutput" step 3): re-splits any line where PDF hard-wrap or
// geometric line-merging glued multiple ordered-list markers together
// (e.g. "1.xxx2.yyy3.zzz") into separate lines, one per marker.
func splitConcatenatedOrderedListLines(markdown string) string {
	lines := strings.Split(markdown, "\n")
	var out []string
	for _, line := range lines {
		out = append(out, splitSingleLineByOrderedMarkersAndTail(line)...)
	}
	return strings.Join(out, "\n")
}
