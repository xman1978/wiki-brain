package pdfconv

import (
	"regexp"
	"strings"
)

func isMarkdownWeakMergeTableLikeLine(line string) bool {
	return classifyText(line, nil, nil, nil, nil) == kindTable
}

func isQuoteOrRuleLine(line string) bool {
	return classifyText(line, nil, nil, nil, nil) == kindQuoteOrRule
}

// ApplyHeadingStage ports the MarkdownHeadingStage.apply orchestration
// described in pdf-port/05-mpp-heading-stack.md "流水线执行顺序" — the
// subset that doesn't depend on ChapterTocCatalog / nonHeadingScopes (see
// heading_stage.go's file-header SCOPE NOTE for what's intentionally
// omitted: catalog cross-validation, HeadingLevelPrefixHeuristics.applyLevelPrefixConsistency,
// and the "mixed recognition" detection pass).
//
// Steps implemented, mapped to the spec's numbered list:
//
//	2/4  snapshot existing `#` heading line indexes/levels
//	3    extractExistingHeadings
//	6    (approximated) global disqualified-pattern-key blacklist via
//	     DetectDisqualifiedPatternKeys
//	7    filterExistingHeadingsByCounterEvidence
//	8    inferHeadings
//	9    mergeInferredAndExisting (existing wins)
//	10   normalizeCnStructuralHeadingLevels
//	13   ApplyReadingOrderNesting
//	14   applyHeadingSequenceConsistencyDemotion (reuses the already-ported
//	     HeadingSequenceConsistencyHeuristics.DetectMarkdownLinesToDemote)
//	19/22 pattern-quality-based final filtering
//	24   heading finalization 4-step (write `#`, UnmarkedParentHeadingHeuristics,
//	     demote non-hierarchy leftover `#` lines)
func ApplyHeadingStage(lines []string) (outLines []string, hits []*HeadingHit, hierarchyLineIndexes map[int]bool) {
	lines = append([]string(nil), lines...)

	sourceMarkdownHeadingLevels := collectSourceMarkdownHeadingLevels(lines)
	markdownHeadingLineIndexes := map[int]bool{}
	for i := range sourceMarkdownHeadingLevels {
		markdownHeadingLineIndexes[i] = true
	}

	existing := extractExistingHeadings(lines)

	// Step 6 (pdf-port/05): the "infer stage" blacklist is
	// buildInferDisqualifiedPatternKeys, not the bare detectDisqualifiedPatternKeys
	// — it additionally folds in detectMixedRecognitionPatternKeys computed
	// against the as-yet-unfiltered `existing` heading line set.
	inferDisqualifiedPatternKeys := buildInferDisqualifiedPatternKeys(lines, existing, nil)

	existing = filterExistingHeadingsByCounterEvidence(lines, existing)

	inferred := inferHeadings(lines, markdownHeadingLineIndexes, inferDisqualifiedPatternKeys)

	merged := mergeInferredAndExisting(inferred, existing)

	normalizeCnStructuralHeadingLevels(merged)

	merged = ApplyReadingOrderNesting(merged)

	merged = applyHeadingSequenceConsistencyDemotion(lines, merged)

	// Step 15/16 (pdf-port/05): prefix-consistency level correction can
	// change a hit's level to its "natural" level (e.g. demoting/promoting
	// "一、" out of a mixed same-level group), which can desync reading-order
	// nesting — re-apply nesting immediately after.
	merged = applyLevelPrefixConsistency(lines, merged, demoteMarkdownHeadingLineToPlain)
	merged = ApplyReadingOrderNesting(merged)

	// Steps 19-22 (pdf-port/05): the "final stage" blacklist is a second,
	// independent computation of detectDisqualifiedPatternKeys (state has
	// moved on since step 6 — lines have had existing-heading counter-evidence
	// demotions applied), unioned with detectMixedRecognitionPatternKeys
	// computed against the final hit set; filterHitsAndDemoteLines both
	// filters `merged` and demotes now-disqualified `#` lines as a
	// side effect.
	finalDisqualifiedPatternKeys := DetectDisqualifiedPatternKeys(lines)
	finalHitLines := map[int]bool{}
	for _, h := range merged {
		finalHitLines[h.LineIndex] = true
	}
	for pk := range detectMixedRecognitionPatternKeys(lines, finalHitLines, nil) {
		finalDisqualifiedPatternKeys[pk] = true
	}
	merged = filterHitsAndDemoteLines(lines, merged, finalDisqualifiedPatternKeys)

	// Step 24, "标题定稿四步":
	hierarchyLineIndexes = map[int]bool{}
	sort_HeadingHitsByLine(merged)
	for _, h := range merged {
		level := h.Level
		if srcLevel, ok := sourceMarkdownHeadingLevels[h.LineIndex]; ok && srcLevel > level {
			level = srcLevel
		}
		level = clampLevel(level)
		lines[h.LineIndex] = writeHeadingLine(level, h.TitleRaw)
		hierarchyLineIndexes[h.LineIndex] = true
	}

	kept, demoted := DemoteMisplacedSectionHeadings(lines, merged)
	for i := range demoted {
		delete(hierarchyLineIndexes, i)
	}
	merged = kept

	demoteNonHierarchyHashHeadings(lines, hierarchyLineIndexes)

	return lines, merged, hierarchyLineIndexes
}

func writeHeadingLine(level int, title string) string {
	hashes := ""
	for i := 0; i < level; i++ {
		hashes += "#"
	}
	return hashes + " " + ensureChapterHeadingSpacing(title)
}

// ensureChapterHeadingSpacing inserts a space between a "第X章/条/节/纲/目"
// numbering prefix and its title text when absent. This is PDF-specific
// compensation, not a DOCX behavior change: PDF extraction's normalizeText
// (geometry_textnorm.go) collapses whitespace between adjacent CJK
// characters as a faithfully-ported step of the original algorithm
// (CJK_SPACE), which incidentally erases the space FileView's reference
// output otherwise keeps between a chapter number and its title (e.g.
// "第一章 总则", not "第一章总则") — DOCX text never goes through that
// normalizer, so this is a no-op there in practice (a DOCX heading already
// either has its original spacing or none, and this only ever adds a
// missing space, never removes one).
var chapterHeadingSpacingRe = regexp.MustCompile(`^(第\s*(?:[一二三四五六七八九十百千万零廿卅]+|\d+)\s*(?:章|节|纲|目|条))([^\s].*)$`)

func ensureChapterHeadingSpacing(title string) string {
	m := chapterHeadingSpacingRe.FindStringSubmatch(title)
	if m == nil {
		return title
	}
	return m[1] + " " + m[2]
}

func sort_HeadingHitsByLine(hits []*HeadingHit) {
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j-1].LineIndex > hits[j].LineIndex; j-- {
			hits[j-1], hits[j] = hits[j], hits[j-1]
		}
	}
}

func collectSourceMarkdownHeadingLevels(lines []string) map[int]int {
	result := map[int]int{}
	inFence := false
	for i, raw := range lines {
		t := strings.TrimSpace(raw)
		if strings.HasPrefix(t, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if n := headingHashCount(t); n > 0 {
			result[i] = n
		}
	}
	return result
}

func extractExistingHeadings(lines []string) []*HeadingHit {
	var out []*HeadingHit
	inFence := false
	for i, raw := range lines {
		t := strings.TrimSpace(raw)
		if strings.HasPrefix(t, "```") {
			inFence = !inFence
			continue
		}
		if inFence || !isMarkdownHeadingLine(t) {
			continue
		}
		level := headingHashCount(t)
		if level > headingStageMaxLevel {
			level = headingStageMaxLevel
		}
		text := headingTitleFromLine(t)
		if text == "" {
			continue
		}
		pattern := MatchFirst(text)
		if pattern == titlePatternNone && looksLikeConfigCommentLineAmongPreformattedNeighbors(lines, i) {
			// A bare "# 注释" line (Word/PDF plain text that happens to
			// start with a literal "#", as pasted from a vmoptions/
			// properties-style config file comment) is textually
			// indistinguishable from a real ATX heading. Only treat it as
			// a config comment — not a heading candidate at all — when it
			// has no recognizable title pattern (a real chapter/numbered
			// title practically never has this shape) and its nearest
			// substantive neighbor looks like config/code, matching
			// MarkdownHeadingStage.extractExistingHeadings' namesake
			// guard in the Java source.
			continue
		}
		var pk *PatternKey
		if pattern != titlePatternNone {
			pk = &PatternKey{Type: pattern, Depth: pattern.Depth()}
		}
		out = append(out, &HeadingHit{LineIndex: i, Level: level, TitleRaw: text, PatternKey: pk})
	}
	return out
}

// filterExistingHeadingsByCounterEvidence ports
// MarkdownHeadingStage.filterExistingHeadingsByCounterEvidence /
// shouldDemoteExistingHeadingByCounterEvidence — the branches that depend on
// ChapterTocCatalog / nonHeadingScopes (never populated in this DOCX-scoped
// port, see file header SCOPE NOTE) are omitted; headingSequenceDemoteLineIds
// mirrors the Java call site, which always passes an empty set (see
// pdf-port/05 移植注意事项 #5) so it's modeled as always-nil here rather than
// threaded through as a parameter.
var shellOrJVMFlagLineRe = regexp.MustCompile(`^-{1,2}[A-Za-z][A-Za-z0-9_.-]*(?:[=:].*)?$`)
var xmlTagLineRe = regexp.MustCompile(`^</?[A-Za-z][^>]*>.*`)

const configCommentNeighborScanHops = 6

// looksLikeConfigCommentLineAmongPreformattedNeighbors ports
// MarkdownHeadingStage.looksLikeConfigCommentLineAmongPreformattedNeighbors
// (mpp/MarkdownHeadingStage.java): a bare "# 注释" line pasted from a
// vmoptions/properties-style config file comment is textually
// indistinguishable, in plain text, from a real ATX heading. It's only
// treated as a config comment — not a heading candidate — when its nearest
// substantive neighbor (skipping blank lines and further "#"-prefixed
// comment-continuation lines, up to configCommentNeighborScanHops hops)
// looks like a JVM/shell flag, an XML tag, or classifies as preformatted
// (config/code/log shape — classifyAt, markdown_line_classify.go).
func looksLikeConfigCommentLineAmongPreformattedNeighbors(lines []string, lineIndex int) bool {
	return isConfigDirectiveBeyondCommentRun(lines, lineIndex, -1) ||
		isConfigDirectiveBeyondCommentRun(lines, lineIndex, 1)
}

func isConfigDirectiveBeyondCommentRun(lines []string, lineIndex, direction int) bool {
	i := lineIndex
	for hop := 0; hop < configCommentNeighborScanHops; hop++ {
		i += direction
		if i < 0 || i >= len(lines) {
			return false
		}
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if shellOrJVMFlagLineRe.MatchString(t) || xmlTagLineRe.MatchString(t) {
			return true
		}
		return looksLikeConfigOrCodeLine(t)
	}
	return false
}

// looksLikeConfigOrCodeLine checks the same strong, file-format-specific
// signals classifyAt's generic PREFORMATTED classification treats as an
// automatic match (looksPreformatted's "return true" fast path plus
// configLineRe, which independently earns the same score) — but skips its
// weaker fallback (a generic "mostly ASCII, symbol-dense, few tokens" score
// that classifyAt also accepts on its own). That fallback is too easily
// tripped by short ASCII-heavy reference strings that are not config/code
// at all, e.g. a document number line "WHRJ-Y-YX-GL-2024-31 / V1.0" scores
// as PREFORMATTED purely on shape, which wrongly demoted the real cover
// titles above it (confirmed regression: f9fba5fb's document titles lost
// their "#" once this neighbor check used the full classifyAt).
// pathOrDeviceRe is deliberately excluded too, for the same reason with a
// narrower trigger: "WHRJ-CW-GL-2024-08 /V2.2" matches it purely because a
// version suffix "/V2.2" happens to have no space after the slash — a real
// file path (e.g. "/etc/nginx/nginx.conf") is a much stronger signal than
// this coincidental shape, and treating it as one wrongly demoted
// 4950596c's cover title the same way. A real config/code line always
// matches one of the signals kept below; requiring one here keeps the
// config-comment protection (da005b4b's "TOP_COST_SQL_LIST_LEN=0", a
// keyValueRe/configLineRe match) while dropping both false positives.
func looksLikeConfigOrCodeLine(t string) bool {
	return sqlStartRe.MatchString(t) || codeStartRe.MatchString(t) || configLineRe.MatchString(t) ||
		keyValueRe.MatchString(t) || permissionLineRe.MatchString(t) || logLineRe.MatchString(t) ||
		stackLineRe.MatchString(t) || shellFlagRe.MatchString(t)
}

func filterExistingHeadingsByCounterEvidence(lines []string, existing []*HeadingHit) []*HeadingHit {
	if len(existing) == 0 {
		return existing
	}
	shortPhraseLines := DetectExistingHeadingShortPhraseListRuns(lines)
	var kept []*HeadingHit
	for _, h := range existing {
		if shouldDemoteExistingHeadingByCounterEvidence(lines, h, shortPhraseLines) {
			demoteMarkdownHeadingLineToPlain(lines, h.LineIndex)
			continue
		}
		kept = append(kept, h)
	}
	return kept
}

var tocMdLinkLineRe = regexp.MustCompile(`^\s*[-*+]\s+\[[^\]]+\]\(#.+\)\s*$`)
var tocHeadingRe = regexp.MustCompile(`(?i)^(?:#{1,6}\s*)?(?:目\s*录|目录|CONTENTS|TABLE\s+OF\s+CONTENTS|图目录|表目录)\s*$`)

func shouldDemoteExistingHeadingByCounterEvidence(lines []string, hit *HeadingHit, shortPhraseLines map[int]bool) bool {
	if hit == nil || hit.LineIndex < 0 || hit.LineIndex >= len(lines) {
		return true
	}
	raw := lines[hit.LineIndex]
	if IsChapterTocLine(strings.TrimSpace(raw)) || IsChapterTocLine(hit.TitleRaw) {
		return true
	}
	if tocHeadingRe.MatchString(hit.TitleRaw) || tocMdLinkLineRe.MatchString(raw) {
		return true
	}
	if isBodyChapterReference(raw) || isBodyChapterReference(hit.TitleRaw) {
		return true
	}
	if isMarkdownWeakMergeTableLikeLine(raw) || isQuoteOrRuleLine(raw) {
		return true
	}
	if shortPhraseLines != nil && shortPhraseLines[hit.LineIndex] {
		return true
	}
	if looksLikeBodySentence(hit.TitleRaw) {
		return true
	}
	if isCnParenBodyEnumeration(raw) {
		return true
	}
	if runeLen(NormalizeLine(hit.TitleRaw)) > MaxHeadingLength*2 {
		return true
	}
	return false
}

func isCnParenBodyEnumeration(line string) bool {
	return looksLikeCnParenBodyEnumerationText(StripHeadingHashes(line))
}

func demoteMarkdownHeadingLineToPlain(lines []string, lineIndex int) {
	if lineIndex < 0 || lineIndex >= len(lines) {
		return
	}
	t := strings.TrimSpace(lines[lineIndex])
	if m := headingLineRe.FindStringSubmatch(t); m != nil {
		lines[lineIndex] = NormalizeLine(m[2])
	}
}

func demoteHashHeadingToBold(line string) string {
	if IsBlank(line) {
		return line
	}
	m := headingLineRe.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return line
	}
	text := NormalizeLine(m[2])
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "**") && strings.HasSuffix(text, "**") && runeLen(text) >= 4 {
		return text
	}
	return "**" + text + "**"
}

func demoteNonHierarchyHashHeadings(lines []string, hierarchyLineIndexes map[int]bool) {
	inFence := false
	for i, raw := range lines {
		t := strings.TrimSpace(raw)
		if strings.HasPrefix(t, "```") {
			inFence = !inFence
			continue
		}
		if inFence || !isMarkdownHeadingLine(t) {
			continue
		}
		if hierarchyLineIndexes[i] {
			continue
		}
		lines[i] = demoteHashHeadingToBold(raw)
	}
}

func mergeInferredAndExisting(inferred, existing []*HeadingHit) []*HeadingHit {
	byLine := map[int]*HeadingHit{}
	for _, h := range existing {
		byLine[h.LineIndex] = h
	}
	for _, h := range inferred {
		if _, ok := byLine[h.LineIndex]; !ok {
			byLine[h.LineIndex] = h
		}
	}
	out := make([]*HeadingHit, 0, len(byLine))
	for _, h := range byLine {
		out = append(out, h)
	}
	sort_HeadingHitsByLine(out)
	return out
}

var cnChapterHeadingRe = regexp.MustCompile(`第\s*[` + cnDigits + `\d]+\s*章`)
var cnArticleHeadingFindRe = regexp.MustCompile(`第\s*[` + cnDigits + `\d]+\s*条`)

func normalizeCnStructuralHeadingLevels(hits []*HeadingHit) {
	for _, h := range hits {
		if cnChapterHeadingRe.MatchString(h.TitleRaw) {
			h.Level = 1
		} else if cnArticleHeadingFindRe.MatchString(h.TitleRaw) {
			if h.Level < 2 {
				h.Level = 2
			}
		}
	}
}

func applyHeadingSequenceConsistencyDemotion(lines []string, hits []*HeadingHit) []*HeadingHit {
	if len(lines) == 0 || len(hits) == 0 {
		return hits
	}
	recognized := map[int]bool{}
	for _, h := range hits {
		recognized[h.LineIndex] = true
	}
	toDemote := DetectMarkdownLinesToDemote(lines, func(i int) bool { return recognized[i] })
	if len(toDemote) == 0 {
		return hits
	}
	var out []*HeadingHit
	for _, h := range hits {
		if toDemote[h.LineIndex] {
			demoteMarkdownHeadingLineToPlain(lines, h.LineIndex)
			continue
		}
		out = append(out, h)
	}
	return out
}
