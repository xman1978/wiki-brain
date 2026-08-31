package localconvert

// Ports docs/impl/v1/docx-port/01-word-to-markdown.md §6
// expandSoftBreakPlainLines.

import (
	"strings"

	"github.com/jxman78/wiki-brain/internal/source/localconvert/pdfconv"
)

// renderPlainLineText ports docx-port/01 §6 renderPlainLineText.
func renderPlainLineText(listLabel, text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.TrimSpace(listLabel) == "" {
		return trimmed
	}
	return composeHeadingTextWithListLabel(listLabel, trimmed)
}

// expandSoftBreakPlainLines ports docx-port/01 §6.
func expandSoftBreakPlainLines(listLabel, text string) []string {
	if strings.TrimSpace(text) == "" {
		return []string{strings.TrimSpace(text)}
	}
	if !strings.ContainsRune(text, '\v') {
		return []string{renderPlainLineText(listLabel, text)}
	}

	rawSegments := strings.Split(text, "\v")
	var segments []string
	for _, s := range rawSegments {
		s = strings.TrimSpace(s)
		if s != "" {
			segments = append(segments, s)
		}
	}
	if len(segments) == 0 {
		return []string{renderPlainLineText(listLabel, text)}
	}

	firstLine := renderPlainLineText(listLabel, segments[0])
	prefixKey := pdfconv.ClassifyPrefixKey(firstLine)
	norm := pdfconv.NormalizeLine(firstLine)

	if prefixKey == "" || !pdfconv.LooksLikeSectionTitleNumberedLine(prefixKey, norm, nil) {
		if looksLikePreformattedSegments(segments) {
			out := []string{firstLine}
			out = append(out, segments[1:]...)
			return out
		}
		return []string{renderPlainLineText(listLabel, text)}
	}

	out := []string{firstLine}
	out = append(out, segments[1:]...)
	return out
}

func looksLikePreformattedSegments(segments []string) bool {
	return pdfconv.LooksLikePreformattedBlock(segments)
}
