package pdfconv

import (
	"regexp"
	"sort"
	"strings"
)

// HeadingSequenceConsistencyHeuristics port
// (pdf-port/04-toplevel-heuristics.md "HeadingSequenceConsistencyHeuristics"
// section) — Markdown-line entry point only (docx-port/01 §5 uses
// DetectMarkdownLinesToDemote; the PDF-block entry point is out of scope).

const maxLineGap = 20
const minSegmentSize = 2

type seqPatternDef struct {
	key    string
	re     *regexp.Regexp
	parser func(groups []string) []int
}

func splitDotIntsParser(idx int) func([]string) []int {
	return func(groups []string) []int { return SplitDotInts(groups[idx]) }
}

func chineseNumberParser(idx int) func([]string) []int {
	return func(groups []string) []int {
		n, ok := ParseChineseNumber(groups[idx])
		if !ok {
			return nil
		}
		return []int{n}
	}
}

func romanParser(idx int) func([]string) []int {
	return func(groups []string) []int {
		n, ok := ParseRoman(groups[idx])
		if !ok {
			return nil
		}
		return []int{n}
	}
}

func plainIntParser(idx int) func([]string) []int {
	return func(groups []string) []int {
		n := 0
		for _, r := range groups[idx] {
			if r < '0' || r > '9' {
				return nil
			}
			n = n*10 + int(r-'0')
		}
		return []int{n}
	}
}

func alphaParser(idx int) func([]string) []int {
	return func(groups []string) []int {
		if len(groups[idx]) != 1 {
			return nil
		}
		c := groups[idx][0]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		return []int{int(c-'A') + 1}
	}
}

// seqPatternDefs — no negative-lookahead (deliberately looser than
// HeadingLevelPrefixHeuristics/ShortPhraseListRunHeuristics; see pdf-port/04
// table row "HeadingSequenceConsistencyHeuristics: PATTERN_DEFS").
var seqPatternDefs = []seqPatternDef{
	{"TITLE_NUM_FIVE", regexp.MustCompile(`^(\d+(?:\.\d+){4})\.?\s*.*`), splitDotIntsParser(1)},
	{"TITLE_NUM_FOUR", regexp.MustCompile(`^(\d+(?:\.\d+){3})\.?\s*.*`), splitDotIntsParser(1)},
	{"TITLE_NUM_THREE", regexp.MustCompile(`^(\d+(?:\.\d+){2})\.?\s*.*`), splitDotIntsParser(1)},
	{"TITLE_NUM_TOW", regexp.MustCompile(`^(\d+(?:\.\d+){1})\.?\s*.*`), splitDotIntsParser(1)},
	{"TITLE_CN_PAREN", regexp.MustCompile(`^[（(]\s*([一二三四五六七八九十百千万]+)\s*[)）].*`), chineseNumberParser(1)},
	{"TITLE_CN_NUM", regexp.MustCompile(`^([一二三四五六七八九十百千万]+)[、．.\s].*`), chineseNumberParser(1)},
	{"TITLE_NUM_DOT", regexp.MustCompile(`^(\d+)\.\s*.*`), plainIntParser(1)},
	{"TITLE_NUM_DUNHAO", regexp.MustCompile(`^(\d+)、\s*.*`), plainIntParser(1)},
	{"TITLE_NUM_SUFFIX", regexp.MustCompile(`^(\d+)[)）]\s*.*`), plainIntParser(1)},
	{"TITLE_NUM_PAREN", regexp.MustCompile(`^[（(]\s*(\d+)\s*[)）]\s*.*`), plainIntParser(1)},
	{"TITLE_ROMAN", regexp.MustCompile(`(?i)^([IVXLCDM]+)\.\s*.*`), romanParser(1)},
	{"TITLE_ALPHA", regexp.MustCompile(`^([A-Za-z])[.．]\s*.*`), alphaParser(1)},
}

// parseSequenceLine ports HeadingSequenceConsistencyHeuristics.parseSequenceLine.
func parseSequenceLine(normalizedLine string) (string, []int) {
	if IsBlank(normalizedLine) {
		return "", nil
	}
	for _, def := range seqPatternDefs {
		m := def.re.FindStringSubmatch(normalizedLine)
		if m == nil {
			continue
		}
		idx := def.parser(m)
		if len(idx) > 0 {
			return def.key, idx
		}
	}
	return "", nil
}

// IsSectionTitleNumberedLine ports
// HeadingSequenceConsistencyHeuristics.isSectionTitleNumberedLine (single-arg
// overload; the lines-aware overload is used internally where needed).
func IsSectionTitleNumberedLine(normalizedLine string) bool {
	return isSectionTitleNumberedLineWithLines(normalizedLine, nil)
}

func isSectionTitleNumberedLineWithLines(normalizedLine string, lines []string) bool {
	key, _ := parseSequenceLine(normalizedLine)
	if key == "" {
		return false
	}
	return LooksLikeSectionTitleNumberedLine(key, normalizedLine, lines)
}

func collectMarkdownSequenceEntries(lines []string, isRecognizedAsHeading func(int) bool) []numberedEntry {
	var entries []numberedEntry
	inFence := false
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		norm := strings.TrimSpace(leadingHashPrefixRe.ReplaceAllString(trimmed, ""))
		key, idx := parseSequenceLine(norm)
		if key == "" {
			continue
		}
		entries = append(entries, numberedEntry{
			LineID:     i,
			NormText:   norm,
			PatternKey: key,
			Index:      idx,
		})
	}
	return entries
}

func continuesSegment(prev, next numberedEntry) bool {
	if prev.PatternKey != next.PatternKey {
		return false
	}
	if !IsSequentialIndex(prev.Index, next.Index) {
		return false
	}
	return next.LineID-prev.LineID < maxLineGap
}

func isParallelEnumerationSibling(lines []string, lineID int) bool {
	if lines == nil {
		return true
	}
	if lineID < 0 || lineID >= len(lines) {
		return true
	}
	norm := strings.TrimSpace(leadingHashPrefixRe.ReplaceAllString(strings.TrimSpace(lines[lineID]), ""))
	if norm == "" {
		return true
	}
	if isColonTerminatedSectionFieldLabel(norm) {
		return false
	}
	if ClearlyFailsHeadingQuality(norm) {
		return false
	}
	key, _ := parseSequenceLine(norm)
	if key != "" && LooksLikeSectionTitleNumberedLine(key, norm, lines) {
		return false
	}
	return true
}

// recognizedEntry threads Java's Entry.recognizedAsHeading boolean (captured
// at collection time) alongside the shared numberedEntry.
type recognizedEntry struct {
	numberedEntry
	Recognized bool
}

func shouldDemoteMixedSegment(seg []recognizedEntry, lines []string) bool {
	if len(seg) < minSegmentSize {
		return false
	}
	anyHeading, anyNonHeading := false, false
	for _, e := range seg {
		if e.Recognized {
			anyHeading = true
		} else {
			anyNonHeading = true
		}
	}
	if !(anyHeading && anyNonHeading) {
		return false
	}
	for _, e := range seg {
		if !e.Recognized {
			if isParallelEnumerationSibling(lines, e.LineID) {
				return true
			}
		}
	}
	return colonLabelSiblingsDominateSegmentR(seg, lines)
}

func colonLabelSiblingsDominateSegmentR(seg []recognizedEntry, lines []string) bool {
	if lines == nil {
		return false
	}
	headingCount, colonLabelCount := 0, 0
	for _, e := range seg {
		if e.Recognized {
			headingCount++
			continue
		}
		norm := e.NormText
		if e.LineID >= 0 && e.LineID < len(lines) {
			norm = strings.TrimSpace(leadingHashPrefixRe.ReplaceAllString(strings.TrimSpace(lines[e.LineID]), ""))
		}
		if !isColonTerminatedSectionFieldLabel(norm) {
			return false
		}
		colonLabelCount++
	}
	return headingCount > 0 && colonLabelCount > headingCount
}

func findMixedSequenceBodyLineIds(entries []recognizedEntry, lines []string) map[int]bool {
	result := map[int]bool{}
	if len(entries) < minSegmentSize {
		return result
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].LineID < entries[j].LineID })
	i := 0
	for i < len(entries) {
		j := i + 1
		for j < len(entries) && continuesSegment(entries[j-1].numberedEntry, entries[j].numberedEntry) {
			j++
		}
		seg := entries[i:j]
		if shouldDemoteMixedSegment(seg, lines) {
			for _, e := range seg {
				result[e.LineID] = true
			}
		}
		i = j
	}
	return result
}

// DetectMarkdownLinesToDemote ports
// HeadingSequenceConsistencyHeuristics.detectMarkdownLinesToDemote.
func DetectMarkdownLinesToDemote(lines []string, isRecognizedAsHeading func(int) bool) map[int]bool {
	if lines == nil || isRecognizedAsHeading == nil {
		return map[int]bool{}
	}
	raw := collectMarkdownSequenceEntries(lines, isRecognizedAsHeading)
	entries := make([]recognizedEntry, 0, len(raw))
	for _, e := range raw {
		entries = append(entries, recognizedEntry{numberedEntry: e, Recognized: isRecognizedAsHeading(e.LineID)})
	}
	return findMixedSequenceBodyLineIds(entries, lines)
}
