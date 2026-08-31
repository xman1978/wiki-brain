package pdfconv

// ChapterReferenceHeuristics port (pdf-port/04-toplevel-heuristics.md
// "ChapterReferenceHeuristics" section) — only isBodyChapterReference, which
// is a direct dependency of HeadingSuppressHeuristics.shouldSuppressHeading
// (step 1) and HeadingPatternQualityHeuristics.countsForMixedRecognitionCandidate
// (step 5). isNumberedClauseContinuation is PDF-block-merge-only (never
// called from the pure-line MarkdownHeadingStage path) and is not ported.

import (
	"regexp"
	"strings"
)

var chapterBookTitleRefRe = regexp.MustCompile(`第\s*[` + cnDigits + `\d]+\s*章《[^》]{1,40}》`)

// isBodyChapterReference ports ChapterReferenceHeuristics.isBodyChapterReference.
func isBodyChapterReference(line string) bool {
	if IsBlank(line) {
		return false
	}
	t := StripHeadingHashes(line)
	if t == "" || !strings.Contains(t, "章") {
		return false
	}
	if IsChapterTocLine(t) {
		return false
	}
	for _, sub := range []string{"否则投标无效", "具体见", "做出了响应", "偏差和例外", "说明所提供"} {
		if strings.Contains(t, sub) {
			return true
		}
	}
	if chapterBookTitleRefRe.FindStringIndex(t) != nil {
		if strings.ContainsAny(t, "）") || strings.Contains(t, "), ") || strings.ContainsAny(t, "，,") {
			return true
		}
	}
	if strings.HasPrefix(t, "第") && strings.Contains(t, "《") && runeLen(t) > 18 {
		if strings.ContainsAny(t, "；：。，") {
			return true
		}
	}
	return false
}
