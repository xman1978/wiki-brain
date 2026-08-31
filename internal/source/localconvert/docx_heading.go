package localconvert

// Ports docs/impl/v1/docx-port/01-word-to-markdown.md §3 (isWordHeadingFragment
// / isWordHeadingCandidate / resolveWordHeadingLevel), §4
// (shouldMergeWordHeadingFragments) and §7 (list-label composition).

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jxman78/wiki-brain/internal/source/localconvert/pdfconv"
)

const maxHeadingTextRunes = 64
const boldLargeMinPt = 14.0

// isWordHeadingCandidate ports docx-port/01 §3 isWordHeadingCandidate.
func isWordHeadingCandidate(text string, listItem bool, styleLevel int, centered, boldAndLarge bool) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	if utf8.RuneCountInString(text) > maxHeadingTextRunes {
		return false
	}
	if pdfconv.EndsWithTerminalPunctuation(text) {
		return false
	}
	if styleLevel > 0 {
		return true
	}
	if listItem {
		return false
	}
	return centered || boldAndLarge
}

var headingStyleNameRe = regexp.MustCompile(`(?i)(?:heading|标题|标题样式)\s*(\d)`)

// resolveWordHeadingStyleLevelFromName ports docx-port/01 §3.1.
func resolveWordHeadingStyleLevelFromName(styleName string) int {
	if strings.TrimSpace(styleName) == "" {
		return 0
	}
	lower := strings.ToLower(strings.TrimSpace(styleName))
	if m := headingStyleNameRe.FindStringSubmatch(lower); m != nil {
		lv := int(m[1][0] - '0')
		if lv < 1 {
			lv = 1
		}
		if lv > 6 {
			lv = 6
		}
		return lv
	}
	if strings.Contains(lower, "heading") || strings.Contains(styleName, "标题") {
		return pdfconv.HeuristicHeadingLevel
	}
	return 0
}

func resolveWordHeadingStyleLevel(p *docParagraph) int {
	if p == nil {
		return 0
	}
	return resolveWordHeadingStyleLevelFromName(p.StyleName)
}

// isWordHeadingFragment ports docx-port/01 §3 isWordHeadingFragment.
func isWordHeadingFragment(p *docParagraph, text string) bool {
	if p == nil || strings.TrimSpace(text) == "" {
		return false
	}
	styleLevel := resolveWordHeadingStyleLevel(p)
	centered := p.Alignment == "center"
	boldAndLarge := p.BoldLarge
	return isWordHeadingCandidate(text, p.HasNum, styleLevel, centered, boldAndLarge)
}

// resolveWordHeadingLevel ports docx-port/01 §3.1 resolveWordHeadingLevel.
func resolveWordHeadingLevel(p *docParagraph, headingText string) int {
	fromStyle := resolveWordHeadingStyleLevel(p)
	if fromStyle > 0 {
		return fromStyle
	}
	fromPrefix := pdfconv.NaturalLevelForTitle(headingText)
	if fromPrefix > 0 {
		return fromPrefix
	}
	return pdfconv.HeuristicHeadingLevel
}

// startsWithListLabel ports docx-port/01 §7 startsWithListLabel.
func startsWithListLabel(text, label string) bool {
	t := strings.TrimSpace(text)
	l := strings.TrimSpace(label)
	if l == "" || utf8.RuneCountInString(t) < utf8.RuneCountInString(l) {
		return false
	}
	tr := []rune(t)
	lr := []rune(l)
	if string(tr[:len(lr)]) != l {
		return false
	}
	if len(tr) == len(lr) {
		return true
	}
	next := tr[len(lr)]
	if unicode.IsSpace(next) {
		return true
	}
	return strings.HasSuffix(l, ".") && !unicode.IsDigit(next)
}

// composeHeadingTextWithListLabel ports docx-port/01 §7.
func composeHeadingTextWithListLabel(listLabel, text string) string {
	if strings.TrimSpace(text) == "" {
		return strings.TrimSpace(text)
	}
	trimmed := strings.TrimSpace(text)
	if strings.TrimSpace(listLabel) == "" {
		return trimmed
	}
	label := strings.TrimSpace(listLabel)
	if startsWithListLabel(trimmed, label) {
		return trimmed
	}
	return label + " " + trimmed
}

// headingTextWithListLabel ports docx-port/01 §7 headingTextWithListLabel.
// numModel is nil-safe (paragraphs without numbering never call it with a
// non-empty label).
func headingTextWithListLabel(p *docParagraph, text string, numModel *numberingModel) string {
	if text == "" {
		return ""
	}
	trimmed := strings.TrimSpace(text)
	if p == nil || !p.HasNum {
		return trimmed
	}
	label := ""
	if numModel != nil {
		label = numModel.LabelForNext(p.NumID, p.ILvl)
	}
	return composeHeadingTextWithListLabel(label, trimmed)
}

// --- §4: continuous heading fragment merging --------------------------------

// isWordHeadingTitleContinuationPair ports docx-port/01 §4.
func isWordHeadingTitleContinuationPair(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	if pdfconv.IsChapterTocLine(left) || pdfconv.IsChapterTocLine(right) {
		return false
	}
	if !pdfconv.IsLikelyChapterTitleNameLine(right) {
		return false
	}
	if pdfconv.IsChapterPrefixOnlyLine(left) {
		return true
	}
	if pdfconv.IsStructuralChapterHeading(left) {
		afterChapter := strings.TrimSpace(chapterPrefixStripRe.ReplaceAllString(left, ""))
		if afterChapter == "" {
			return true
		}
	}
	t := strings.TrimSpace(left)
	return cnNumOnlyLineRe.MatchString(t) || cnParenOnlyLineRe.MatchString(t)
}

var (
	chapterPrefixStripRe = regexp.MustCompile(`^第\s*[一二三四五六七八九十百千万零\d]+\s*章\s*`)
	cnNumOnlyLineRe       = regexp.MustCompile(`^[一二三四五六七八九十百千万]+[、．.]\s*$`)
	cnParenOnlyLineRe     = regexp.MustCompile(`^[（(][一二三四五六七八九十百千万]+[)）]\s*$`)
)

// shouldMergeWordHeadingFragments ports docx-port/01 §4.
func shouldMergeWordHeadingFragments(left string, leftLevel int, right string, rightLevel int) bool {
	if strings.TrimSpace(right) == "" {
		return false
	}
	if strings.TrimSpace(left) == "" {
		return true
	}
	if isWordHeadingTitleContinuationPair(left, right) {
		return true
	}
	if leftLevel != rightLevel {
		return false
	}
	if pdfconv.ClassifyPrefixKey(right) != "" {
		return false
	}
	if pdfconv.ClassifyPrefixKey(left) != "" {
		return true
	}
	return leftLevel == pdfconv.HeuristicHeadingLevel
}

// joinHeadingFragments ports docx-port/01 §4.
func joinHeadingFragments(a, b string) string {
	left := strings.TrimSpace(a)
	right := strings.TrimSpace(b)
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	leftLast := []rune(left)[len([]rune(left))-1]
	rightFirst := []rune(right)[0]
	if isLetterOrDigit(leftLast) && isLetterOrDigit(rightFirst) {
		return left + " " + right
	}
	return left + right
}

func isLetterOrDigit(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
