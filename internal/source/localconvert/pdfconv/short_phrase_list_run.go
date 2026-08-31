package pdfconv

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// ShortPhraseListRunHeuristics port
// (pdf-port/04-toplevel-heuristics.md "ShortPhraseListRunHeuristics"
// section) — docx-port/01 §6 only needs LooksLikeSectionTitleNumberedLine
// (used both directly and transitively via HeadingSequenceConsistency's
// isParallelEnumerationSibling).

// PATTERN_DEFS with the negative-lookahead boundary checks (identical
// regex text to HeadingLevelPrefixHeuristics' numeric subset; see pdf-port/04
// "Go 包组织建议" §2 on sharing this table — kept separate here per-file for
// direct traceability against the spec, using the shared prefixDef type).
var shortPhrasePatternDefs = []prefixDef{
	{key: "TITLE_NUM_FIVE", re: regexp.MustCompile(`^(\d+(?:\.\d+){4})(?:[.．])?`), boundaryDot: true},
	{key: "TITLE_NUM_FOUR", re: regexp.MustCompile(`^(\d+(?:\.\d+){3})(?:[.．])?`), boundaryDot: true},
	{key: "TITLE_NUM_THREE", re: regexp.MustCompile(`^(\d+(?:\.\d+){2})(?:[.．])?`), boundaryDot: true},
	{key: "TITLE_NUM_TOW", re: regexp.MustCompile(`^(\d+(?:\.\d+){1})(?:[.．])?`), boundaryDot: true},
	{key: "TITLE_NUM_DOT", re: regexp.MustCompile(`^(\d+)[.．]`), boundaryDash: true},
	{key: "TITLE_NUM_DUNHAO", re: regexp.MustCompile(`^(\d+)、\s*.*`)},
	{key: "TITLE_NUM_SUFFIX", re: regexp.MustCompile(`^(\d+)[)）]\s*.*`)},
	{key: "TITLE_NUM_PAREN", re: regexp.MustCompile(`^[（(]\s*(\d+)\s*[)）]\s*.*`)},
	{key: "TITLE_ROMAN", re: regexp.MustCompile(`(?i)^([IVXLCDM]+)\.\s*.*`)},
	{key: "TITLE_ALPHA", re: regexp.MustCompile(`^([A-Za-z])[.．]\s*.*`)},
}

func supportsPatternKey(key string) bool {
	switch key {
	case "TITLE_CHAPTER_ONE", "TITLE_CHAPTER_TOW", "TITLE_CHAPTER_THREE",
		"TITLE_CHAPTER_FOUR", "TITLE_CHAPTER_FIVE", "TITLE_CN_PAREN", "TITLE_CN_NUM":
		return false
	case "":
		return false
	}
	return true
}

func isMultiLevelNumericSectionKey(key string) bool {
	switch key {
	case "TITLE_NUM_TOW", "TITLE_NUM_THREE", "TITLE_NUM_FOUR", "TITLE_NUM_FIVE":
		return true
	}
	return false
}

var firstNumericSegmentRe = regexp.MustCompile(`^(\d+)`)

func firstNumericSegment(norm string) int {
	m := firstNumericSegmentRe.FindStringSubmatch(strings.TrimSpace(norm))
	if m == nil {
		return 0
	}
	n := 0
	for _, r := range m[1] {
		n = n*10 + int(r-'0')
	}
	return n
}

var (
	sectionBodyDotRe   = regexp.MustCompile(`^\d+\.\s*`)
	sectionBodyMultiRe = regexp.MustCompile(`^(\d+(?:\.\d+)+)\.?\s*(.*)$`)
)

func sectionTitleBodyAfterNumericPrefix(patternKey, norm string) string {
	if patternKey == "" || norm == "" {
		return ""
	}
	t := strings.TrimSpace(norm)
	switch patternKey {
	case "TITLE_NUM_DOT":
		return strings.TrimSpace(sectionBodyDotRe.ReplaceAllString(t, ""))
	case "TITLE_NUM_TOW", "TITLE_NUM_THREE", "TITLE_NUM_FOUR", "TITLE_NUM_FIVE":
		m := sectionBodyMultiRe.FindStringSubmatch(t)
		if m == nil {
			return ""
		}
		return strings.TrimSpace(m[2])
	}
	return ""
}

// sectionTitleBodyPunct is the character class from
// PdfToMarkdown.looksLikeSectionTitleBody's regex
// ".*[，。；：,.!?;:].*" — note this differs from the terminal-punctuation
// set (EndsWithTerminalPunctuation): it has no "、" and is checked for
// presence ANYWHERE in the string, not just at the end.
const sectionTitleBodyPunct = "，。；：,.!?;:"

// looksLikeSectionTitleBody ports PdfToMarkdown.looksLikeSectionTitleBody
// literally: a short (<=18 rune) remainder containing none of the
// sentence-punctuation characters above (anywhere in the string, not just
// trailing) "usually behaves like a heading title" per the Java doc comment.
func looksLikeSectionTitleBody(body string) bool {
	s := strings.TrimSpace(body)
	if s == "" {
		return false
	}
	if runeLen(s) <= 18 && !strings.ContainsAny(s, sectionTitleBodyPunct) {
		return true
	}
	return false
}

func documentUsesChapterHeading(lines []string) bool {
	if len(lines) == 0 {
		return false
	}
	inFence := false
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		norm := strings.TrimSpace(leadingHashPrefixRe.ReplaceAllString(trimmed, ""))
		if ClassifyPrefixKey(norm) == "TITLE_CHAPTER_ONE" {
			return true
		}
	}
	return false
}

var chapterNumeralRe = regexp.MustCompile(`^第\s*([` + cnDigits + `\d]+)\s*章.*`)

func documentUsesChapterNumber(lines []string, chapterNumber int) bool {
	if len(lines) == 0 || chapterNumber <= 0 {
		return false
	}
	inFence := false
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		norm := strings.TrimSpace(leadingHashPrefixRe.ReplaceAllString(trimmed, ""))
		if ClassifyPrefixKey(norm) != "TITLE_CHAPTER_ONE" {
			continue
		}
		m := chapterNumeralRe.FindStringSubmatch(norm)
		if m == nil {
			continue
		}
		if n, ok := parseChapterNumeral(m[1]); ok && n == chapterNumber {
			return true
		}
	}
	return false
}

func parseChapterNumeral(token string) (int, bool) {
	allDigits := true
	for _, r := range token {
		if r < '0' || r > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		n := 0
		for _, r := range token {
			n = n*10 + int(r-'0')
		}
		return n, true
	}
	return ParseChineseNumber(token)
}

// Config defaults (pdf-port/04-toplevel-heuristics.md "常量与正则" table /
// pdf-port/02-heading-style-render.md line 138): shortPhraseNumberedRunMin=3,
// shortPhraseNumberedRunMaxGap=3, shortPhraseNumberedRunMaxBodyLines=1,
// shortPhraseNumberedRunSeqQualityMin=0.8, shortPhraseNumberedBodyMaxLen=18.
// wiki-brain has no config.properties equivalent yet (same rationale as
// MaxHeadingLength in heading_pattern_quality.go), so these stay
// package-level defaults.
const (
	shortPhraseNumberedRunMin           = 3
	shortPhraseNumberedRunMaxGap        = 3.0
	shortPhraseNumberedRunMaxBodyLines  = 1
	shortPhraseNumberedRunSeqQualityMin = 0.8
	shortPhraseNumberedBodyMaxLen       = 18
	shortPhraseEPS                      = 1e-9
)

// structuralSectionKeywords mirrors ShortPhraseListRunHeuristics.STRUCTURAL_SECTION_KEYWORDS.
var structuralSectionKeywords = []string{
	"总则", "范围", "附则", "概述", "项目背景", "建设内容", "实施范围",
	"总体要求", "工作目标", "基本原则", "组织实施", "保障措施", "术语", "定义",
}

type shortPhraseEntry struct {
	lineID     int
	patternKey string
	normText   string
	index      []int
}

// parseNumberedLine parses a normalized line against the numeric-only
// PATTERN_DEFS subset (shortPhrasePatternDefs) used by
// ShortPhraseListRunHeuristics, independent of MarkdownTitlePattern's
// broader CN/chapter patterns (which supportsPatternKey excludes anyway).
func parseNumberedLine(norm string) (string, []int) {
	for _, def := range shortPhrasePatternDefs {
		loc := def.re.FindStringSubmatchIndex(norm)
		if loc == nil || loc[0] != 0 {
			continue
		}
		if def.boundaryDot || def.boundaryDash {
			if !numericBoundaryOK(norm, loc[1], def.boundaryDot) {
				continue
			}
		}
		if len(loc) < 4 || loc[2] < 0 {
			continue
		}
		group1 := norm[loc[2]:loc[3]]
		idx := parseNumberedLineIndex(def.key, group1)
		if idx == nil {
			continue
		}
		return def.key, idx
	}
	return "", nil
}

func parseNumberedLineIndex(key, group1 string) []int {
	switch key {
	case "TITLE_NUM_TOW", "TITLE_NUM_THREE", "TITLE_NUM_FOUR", "TITLE_NUM_FIVE":
		parts := strings.Split(group1, ".")
		out := make([]int, 0, len(parts))
		for _, seg := range parts {
			n := 0
			for _, r := range seg {
				if r < '0' || r > '9' {
					return nil
				}
				n = n*10 + int(r-'0')
			}
			out = append(out, n)
		}
		return out
	case "TITLE_NUM_DOT", "TITLE_NUM_DUNHAO", "TITLE_NUM_SUFFIX", "TITLE_NUM_PAREN":
		n := 0
		for _, r := range group1 {
			if r < '0' || r > '9' {
				return nil
			}
			n = n*10 + int(r-'0')
		}
		return []int{n}
	case "TITLE_ROMAN":
		n, ok := ParseRoman(group1)
		if !ok {
			return nil
		}
		return []int{n}
	case "TITLE_ALPHA":
		r := []rune(strings.ToUpper(group1))
		if len(r) == 0 {
			return nil
		}
		return []int{int(r[0]-'A') + 1}
	}
	return nil
}

func isSequentialShortPhrase(a, b *shortPhraseEntry) bool {
	return IsSequentialIndex(a.index, b.index)
}

var (
	stripPrefixDunhaoRe = regexp.MustCompile(`^\d+、\s*`)
	stripPrefixDotRe    = regexp.MustCompile(`^\d+[.．]\s*`)
	stripPrefixSuffixRe = regexp.MustCompile(`^\d+[)）】]\s*`)
	stripPrefixParenRe  = regexp.MustCompile(`^[（(]\s*\d+\s*[)）]\s*`)
	stripPrefixTowRe    = regexp.MustCompile(`^\d+(?:\.\d+){1}\.?\s*`)
	stripPrefixThreeRe  = regexp.MustCompile(`^\d+(?:\.\d+){2}\.?\s*`)
	stripPrefixFourRe   = regexp.MustCompile(`^\d+(?:\.\d+){3}\.?\s*`)
	stripPrefixFiveRe   = regexp.MustCompile(`^\d+(?:\.\d+){4}\.?\s*`)
	stripPrefixRomanRe  = regexp.MustCompile(`(?i)^[ivxlcdm]+\.\s*`)
	stripPrefixAlphaRe  = regexp.MustCompile(`^[A-Za-z][.．]\s*`)
)

// shortPhraseStripPrefix ports ShortPhraseListRunHeuristics.stripPrefix.
func shortPhraseStripPrefix(text, patternKey string) string {
	if text == "" {
		return ""
	}
	switch patternKey {
	case "TITLE_NUM_DUNHAO":
		return stripPrefixDunhaoRe.ReplaceAllString(text, "")
	case "TITLE_NUM_DOT":
		return stripPrefixDotRe.ReplaceAllString(text, "")
	case "TITLE_NUM_SUFFIX":
		return stripPrefixSuffixRe.ReplaceAllString(text, "")
	case "TITLE_NUM_PAREN":
		return stripPrefixParenRe.ReplaceAllString(text, "")
	case "TITLE_NUM_TOW":
		return stripPrefixTowRe.ReplaceAllString(text, "")
	case "TITLE_NUM_THREE":
		return stripPrefixThreeRe.ReplaceAllString(text, "")
	case "TITLE_NUM_FOUR":
		return stripPrefixFourRe.ReplaceAllString(text, "")
	case "TITLE_NUM_FIVE":
		return stripPrefixFiveRe.ReplaceAllString(text, "")
	case "TITLE_ROMAN":
		return stripPrefixRomanRe.ReplaceAllString(text, "")
	case "TITLE_ALPHA":
		return stripPrefixAlphaRe.ReplaceAllString(text, "")
	}
	return text
}

// isShortPhraseBody ports ShortPhraseListRunHeuristics.isShortPhraseBody.
func isShortPhraseBody(body string) bool {
	s := strings.TrimSpace(body)
	if s == "" || runeLen(s) > shortPhraseNumberedBodyMaxLen {
		return false
	}
	if countSentencePunctuation(s) > 0 {
		return false
	}
	return true
}

// isSubstantiveBodyLine ports ShortPhraseListRunHeuristics.isSubstantiveBodyLine.
func isSubstantiveBodyLine(norm string) bool {
	if strings.HasPrefix(norm, "#") {
		return false
	}
	if runeLen(norm) > shortPhraseNumberedBodyMaxLen {
		return true
	}
	if countSentencePunctuation(norm) > 0 {
		return true
	}
	if pk, _ := parseNumberedLine(norm); pk != "" {
		return false
	}
	return runeLen(norm) >= 8
}

// shortPhraseNormalizeLine ports ShortPhraseListRunHeuristics's private
// normalizeLine (simplified: strip + `#` prefix removal, no whitespace
// folding).
func shortPhraseNormalizeLine(line string) string {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "#") {
		t = strings.TrimSpace(leadingHashPrefixRe.ReplaceAllString(t, ""))
	}
	return t
}

// countExpansionLinesBetween ports ShortPhraseListRunHeuristics.countExpansionLinesBetween.
func countExpansionLinesBetween(lines []string, fromLineID, toLineID int) int {
	if lines == nil || toLineID <= fromLineID+1 {
		return 0
	}
	count := 0
	for i := fromLineID + 1; i < toLineID; i++ {
		if i < 0 || i >= len(lines) {
			continue
		}
		norm := shortPhraseNormalizeLine(lines[i])
		if norm == "" {
			continue
		}
		if strings.HasPrefix(norm, "|") || tableSeparatorRe.MatchString(norm) {
			count++
			continue
		}
		if isSubstantiveBodyLine(norm) {
			count++
		}
	}
	return count
}

// segmentHasExpansionBetweenItems ports
// ShortPhraseListRunHeuristics.segmentHasExpansionBetweenItems (PLAIN mode:
// rejects if any adjacent pair exceeds shortPhraseNumberedRunMaxBodyLines).
func segmentHasExpansionBetweenItems(seg []*shortPhraseEntry, lines []string) bool {
	for i := 0; i+1 < len(seg); i++ {
		if countExpansionLinesBetween(lines, seg[i].lineID, seg[i+1].lineID) > shortPhraseNumberedRunMaxBodyLines {
			return true
		}
	}
	return false
}

// eachItemHasTrailingExpansion ports ShortPhraseListRunHeuristics.eachItemHasTrailingExpansion.
func eachItemHasTrailingExpansion(seg []*shortPhraseEntry, lines []string) bool {
	if lines == nil || len(seg) < 2 {
		return false
	}
	for i := 0; i+1 < len(seg); i++ {
		if countExpansionLinesBetween(lines, seg[i].lineID, seg[i+1].lineID) < 1 {
			return false
		}
	}
	return true
}

// segmentLooksLikeStableHeadingRun ports
// ShortPhraseListRunHeuristics.segmentLooksLikeStableHeadingRun (identical
// algorithm to segmentHasUniformMarkdownHeadingLevel, see spec note).
func segmentLooksLikeStableHeadingRun(seg []*shortPhraseEntry, lines []string) bool {
	if lines == nil || len(seg) == 0 {
		return false
	}
	level := -1
	for _, e := range seg {
		if e.lineID < 0 || e.lineID >= len(lines) {
			return false
		}
		t := strings.TrimSpace(lines[e.lineID])
		m := headingLineRe.FindStringSubmatch(t)
		if m == nil {
			return false
		}
		lv := len(m[1])
		if level == -1 {
			level = lv
		} else if lv != level {
			return false
		}
	}
	return true
}

func segmentHasUniformMarkdownHeadingLevel(seg []*shortPhraseEntry, lines []string) bool {
	return segmentLooksLikeStableHeadingRun(seg, lines)
}

// segmentHasAnyExpansionBetweenItems ports
// ShortPhraseListRunHeuristics.segmentHasAnyExpansionBetweenItems.
func segmentHasAnyExpansionBetweenItems(seg []*shortPhraseEntry, lines []string) bool {
	if lines == nil || len(seg) < 2 {
		return false
	}
	for i := 0; i+1 < len(seg); i++ {
		if countExpansionLinesBetween(lines, seg[i].lineID, seg[i+1].lineID) > 0 {
			return true
		}
	}
	return false
}

var (
	chineseStructuralChapterRe = regexp.MustCompile(`^第\s*[` + cnDigits + `\d]+\s*章.*`)
	chineseStructuralCnNumRe   = regexp.MustCompile(`^[一二三四五六七八九十百千万零]+[、.．].*`)
	chineseStructuralCnParenRe = regexp.MustCompile(`^[（(][一二三四五六七八九十百千万零]+[)）].*`)
)

// isChineseStructuralSectionTitle ports
// ShortPhraseListRunHeuristics.isChineseStructuralSectionTitle.
func isChineseStructuralSectionTitle(norm string) bool {
	if IsBlank(norm) {
		return false
	}
	t := strings.TrimSpace(norm)
	return chineseStructuralChapterRe.MatchString(t) || chineseStructuralCnNumRe.MatchString(t) || chineseStructuralCnParenRe.MatchString(t)
}

// bodyContainsStructuralSectionKeyword ports
// ShortPhraseListRunHeuristics.bodyContainsStructuralSectionKeyword.
func bodyContainsStructuralSectionKeyword(body string) bool {
	if IsBlank(body) {
		return false
	}
	for _, kw := range structuralSectionKeywords {
		if strings.Contains(body, kw) {
			return true
		}
	}
	return false
}

// segmentProtectedByStructuralSectionKeywords ports
// ShortPhraseListRunHeuristics.segmentProtectedByStructuralSectionKeywords.
func segmentProtectedByStructuralSectionKeywords(seg []*shortPhraseEntry) bool {
	if len(seg) == 0 {
		return false
	}
	for _, e := range seg {
		body := shortPhraseStripPrefix(e.normText, e.patternKey)
		if !bodyContainsStructuralSectionKeyword(body) {
			return false
		}
	}
	return true
}

func seqQualityAdjacentShortPhrase(seg []*shortPhraseEntry) float64 {
	if len(seg) < 2 {
		return 1.0
	}
	hits := 0
	for i := 1; i < len(seg); i++ {
		if isSequentialShortPhrase(seg[i-1], seg[i]) {
			hits++
		}
	}
	return float64(hits) / float64(len(seg)-1)
}

func meanLineGapShortPhrase(seg []*shortPhraseEntry) float64 {
	if len(seg) < 2 {
		return math.Inf(1)
	}
	sum := 0
	for i := 1; i < len(seg); i++ {
		sum += seg[i].lineID - seg[i-1].lineID
	}
	return float64(sum) / float64(len(seg)-1)
}

// shortPhraseMode mirrors ShortPhraseListRunHeuristics.ListRunDetectionMode.
type shortPhraseMode int

const (
	shortPhrasePlainMarkdown shortPhraseMode = iota
	shortPhraseExistingHeading
)

// qualifiesAsShortPhraseListRun ports
// ShortPhraseListRunHeuristics.qualifiesAsShortPhraseListRun (core overload
// 2; the PDF-only orderedBlocks/profile branches are never reached from the
// pure-line DOCX/PDF-Markdown path, see heading_stage.go SCOPE NOTE — this
// covers the PLAIN_MARKDOWN and EXISTING_HEADING branches only).
func qualifiesAsShortPhraseListRun(seg []*shortPhraseEntry, lines []string, mode shortPhraseMode) bool {
	minRun := shortPhraseNumberedRunMin
	if mode == shortPhraseExistingHeading && minRun < 3 {
		minRun = 3
	}
	if len(seg) < minRun {
		return false
	}
	if seqQualityAdjacentShortPhrase(seg)+shortPhraseEPS < shortPhraseNumberedRunSeqQualityMin {
		return false
	}
	if meanLineGapShortPhrase(seg) >= shortPhraseNumberedRunMaxGap {
		return false
	}
	for _, e := range seg {
		if !isShortPhraseBody(shortPhraseStripPrefix(e.normText, e.patternKey)) {
			return false
		}
	}
	if segmentHasExpansionBetweenItems(seg, lines) {
		return false
	}
	switch mode {
	case shortPhrasePlainMarkdown:
		if eachItemHasTrailingExpansion(seg, lines) {
			return false
		}
		if segmentLooksLikeStableHeadingRun(seg, lines) {
			return false
		}
		if segmentProtectedByStructuralSectionKeywords(seg) {
			return false
		}
		return true
	case shortPhraseExistingHeading:
		if !segmentHasUniformMarkdownHeadingLevel(seg, lines) {
			return false
		}
		if segmentHasAnyExpansionBetweenItems(seg, lines) {
			return false
		}
		for _, e := range seg {
			if isChineseStructuralSectionTitle(e.normText) {
				return false
			}
			if bodyContainsStructuralSectionKeyword(shortPhraseStripPrefix(e.normText, e.patternKey)) {
				return false
			}
		}
		return true
	}
	return false
}

// markRunsInPatternGroup ports ShortPhraseListRunHeuristics.markRunsInPatternGroup.
func markRunsInPatternGroup(group []*shortPhraseEntry, lines []string, mode shortPhraseMode, marked map[int]bool) {
	i := 0
	for i < len(group) {
		j := i + 1
		for j < len(group) && isSequentialShortPhrase(group[j-1], group[j]) && float64(group[j].lineID-group[j-1].lineID) < shortPhraseNumberedRunMaxGap {
			j++
		}
		seg := group[i:j]
		if qualifiesAsShortPhraseListRun(seg, lines, mode) {
			for _, e := range seg {
				marked[e.lineID] = true
			}
		}
		if j > i+1 {
			i = j
		} else {
			i++
		}
	}
}

// detectMarkedLineIdsInternal ports
// ShortPhraseListRunHeuristics.detectMarkedLineIdsInternal (core grouper,
// text-line entries only — the PDF orderedBlocks/profile parameters are
// always nil from this port's callers).
func detectMarkedLineIdsInternal(entries []*shortPhraseEntry, lines []string, mode shortPhraseMode) map[int]bool {
	marked := map[int]bool{}
	var filtered []*shortPhraseEntry
	for _, e := range entries {
		if supportsPatternKey(e.patternKey) {
			filtered = append(filtered, e)
		}
	}
	if len(filtered) == 0 {
		return marked
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].lineID < filtered[j].lineID })
	i := 0
	for i < len(filtered) {
		j := i + 1
		for j < len(filtered) && filtered[j].patternKey == filtered[i].patternKey {
			j++
		}
		markRunsInPatternGroup(filtered[i:j], lines, mode, marked)
		i = j
	}
	return marked
}

// detectMarkedLineIdsFromTextLinesInternal ports
// ShortPhraseListRunHeuristics.detectMarkedLineIdsFromTextLinesInternal.
func detectMarkedLineIdsFromTextLinesInternal(lines []string, mode shortPhraseMode) map[int]bool {
	if len(lines) == 0 {
		return map[int]bool{}
	}
	var entries []*shortPhraseEntry
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		isMarkdownHeading := isMarkdownHeadingLine(trimmed)
		if (mode == shortPhrasePlainMarkdown) && isMarkdownHeading {
			continue
		}
		if mode == shortPhraseExistingHeading && !isMarkdownHeading {
			continue
		}
		norm := shortPhraseNormalizeLine(raw)
		if norm == "" {
			continue
		}
		pk, idx := parseNumberedLine(norm)
		if pk == "" || !supportsPatternKey(pk) {
			continue
		}
		if pk != "TITLE_NUM_DOT" && LooksLikeSectionTitleNumberedLine(pk, norm, lines) {
			continue
		}
		entries = append(entries, &shortPhraseEntry{lineID: i, patternKey: pk, normText: norm, index: idx})
	}
	return detectMarkedLineIdsInternal(entries, lines, mode)
}

// DetectPlainShortPhraseListRuns ports
// ShortPhraseListRunHeuristics.detectPlainShortPhraseListRuns (text-line
// overload, PLAIN_MARKDOWN mode) — a dense run (>= shortPhraseNumberedRunMin)
// of sequentially-numbered, punctuation-free short-body lines that reads
// like a plain enumeration ("1.总则 2.范围 3.附则") rather than a heading
// outline.
func DetectPlainShortPhraseListRuns(lines []string) map[int]bool {
	return detectMarkedLineIdsFromTextLinesInternal(lines, shortPhrasePlainMarkdown)
}

// DetectExistingHeadingShortPhraseListRuns ports
// ShortPhraseListRunHeuristics.detectExistingHeadingShortPhraseListRuns —
// the stricter counter-evidence variant used to demote already-`#`-marked
// headings that turn out to be a short-phrase list run in disguise.
func DetectExistingHeadingShortPhraseListRuns(lines []string) map[int]bool {
	return detectMarkedLineIdsFromTextLinesInternal(lines, shortPhraseExistingHeading)
}

// LooksLikeSectionTitleNumberedLine ports
// ShortPhraseListRunHeuristics.looksLikeSectionTitleNumberedLine (two-arg and
// three-arg overloads collapsed; lines==nil means "no context").
func LooksLikeSectionTitleNumberedLine(patternKey, norm string, lines []string) bool {
	if patternKey == "" || norm == "" {
		return false
	}
	body := sectionTitleBodyAfterNumericPrefix(patternKey, norm)
	if !looksLikeSectionTitleBody(body) {
		return false
	}
	if patternKey == "TITLE_NUM_DOT" {
		return true
	}
	if !isMultiLevelNumericSectionKey(patternKey) {
		return false
	}
	first := firstNumericSegment(norm)
	if first <= 0 {
		return false
	}
	if lines == nil {
		return true
	}
	if !documentUsesChapterHeading(lines) {
		return true
	}
	if first == 1 {
		return true
	}
	return documentUsesChapterNumber(lines, first)
}
