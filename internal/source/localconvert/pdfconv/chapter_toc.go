package pdfconv

import "regexp"

// ChapterTocLineRemover port — only the four methods docx-port/01 §4 needs
// (isChapterTocLine / isLikelyChapterTitleNameLine / isChapterPrefixOnlyLine
// / isStructuralChapterHeading), plus isStructuralCnSectionHeading which
// isLikelyChapterTitleNameLine depends on
// (pdf-port/04-toplevel-heuristics.md "ChapterTocLineRemover" section).

const cnDigits = `一二三四五六七八九十百千万零`

var (
	chapterTocLineRe            = regexp.MustCompile(`^(?:#{1,6}\s*)?第\s*[` + cnDigits + `\d]+\s*章.*(?:\.{1,}|…{1,}|·{1,}|⋯{1,}|\s{2,}|\t).*\d{1,4}\s*$`)
	chapterTocDashPageRe        = regexp.MustCompile(`^(?:#{1,6}\s*)?第\s*[` + cnDigits + `\d]+\s*章.*\.{2,}.*-\d+-\s*$`)
	chapterTocDashPageTruncated = regexp.MustCompile(`^(?:#{1,6}\s*)?第\s*[` + cnDigits + `\d]+\s*章.*\.{2,}-\s*$`)
	chapterTocEmbeddedPageRe    = regexp.MustCompile(`^(?:#{1,6}\s*)?第\s*[` + cnDigits + `\d]+\s*章.*\.{2,}\d{1,4}\p{Han}.*`)

	chapterHeadingRe        = regexp.MustCompile(`^(?:#{1,6}\s*)?第\s*[` + cnDigits + `\d]+\s*章.*`)
	cnSectionHeadingRe      = regexp.MustCompile(`^(?:#{1,6}\s*)?([一二三四五六七八九十百千万]+)[、.．\s].*`)
	headingPrefixFragmentRe = regexp.MustCompile(`^\s*(?:第\s*[` + cnDigits + `\d]+\s*章|[一二三四五六七八九十百千万零]+[、.．])`)
	chapterPrefixOnlyRe     = regexp.MustCompile(`^(?:#{1,6}\s*)?第\s*[` + cnDigits + `\d]+\s*章\s*$`)
)

// IsChapterTocLine ports ChapterTocLineRemover.isChapterTocLine.
func IsChapterTocLine(line string) bool {
	if IsBlank(line) {
		return false
	}
	return chapterTocLineRe.MatchString(line) ||
		chapterTocDashPageRe.MatchString(line) ||
		chapterTocDashPageTruncated.MatchString(line) ||
		chapterTocEmbeddedPageRe.MatchString(line)
}

// IsChapterPrefixOnlyLine ports ChapterTocLineRemover.isChapterPrefixOnlyLine.
func IsChapterPrefixOnlyLine(line string) bool {
	if line == "" {
		return false
	}
	return chapterPrefixOnlyRe.MatchString(line)
}

// IsStructuralChapterHeading ports ChapterTocLineRemover.isStructuralChapterHeading.
func IsStructuralChapterHeading(line string) bool {
	if line == "" {
		return false
	}
	t := stripLeadingHashPrefix(line)
	return t != "" && chapterHeadingRe.MatchString(t) && !IsChapterTocLine(t)
}

// IsStructuralCnSectionHeading ports
// ChapterTocLineRemover.isStructuralCnSectionHeading.
func IsStructuralCnSectionHeading(line string) bool {
	if line == "" {
		return false
	}
	t := stripLeadingHashPrefix(line)
	return t != "" && cnSectionHeadingRe.MatchString(t) && !IsChapterTocLine(t)
}

var leadingHashPrefixRe = regexp.MustCompile(`^#+\s*`)

func stripLeadingHashPrefix(line string) string {
	t := line
	t = leadingHashPrefixRe.ReplaceAllString(t, "")
	return strings_TrimSpace(t)
}

// IsLikelyChapterTitleNameLine ports
// ChapterTocLineRemover.isLikelyChapterTitleNameLine.
func IsLikelyChapterTitleNameLine(line string) bool {
	if line == "" {
		return false
	}
	t := stripLeadingHashPrefix(line)
	n := runeLen(t)
	if n < 2 || n > 40 {
		return false
	}
	if IsChapterTocLine(t) || IsChapterPrefixOnlyLine(t) {
		return false
	}
	if IsStructuralChapterHeading(t) || IsStructuralCnSectionHeading(t) {
		return false
	}
	if headingPrefixFragmentRe.FindStringIndex(t) != nil {
		return false
	}
	if startsWithChar(t) {
		return false
	}
	if containsAnyRune(t, "。！？；：,.!?;:") {
		return false
	}
	if containsDigit(t) {
		return false
	}
	return likelyTitleNameCharsRe.MatchString(t)
}

var likelyTitleNameCharsRe = regexp.MustCompile(`^[\p{Han}\p{L}（）()、\s·]{2,40}$`)

func startsWithChar(t string) bool {
	// "^第\s*.*" — still starts with the literal char 第.
	for _, r := range t {
		return r == '第'
	}
	return false
}
