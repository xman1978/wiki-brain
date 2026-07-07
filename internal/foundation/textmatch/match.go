// Package textmatch locates model-produced text anchors within original
// source content: exact match first, then a whitespace-collapsed fuzzy match
// that still resolves back to the verbatim original-text span. Shared by
// evidence mining (docs/impl/v1/evidence.md 步骤 3.1-3.2) and unit boundary
// resolution (internal/unit) — both need "the model gives text, the program
// finds where it really is" instead of trusting a model-reported offset.
package textmatch

import (
	"strings"
	"unicode/utf8"
)

// MatchFragment finds fragment within content, exact first, then with both
// sides' whitespace runs collapsed to single spaces (tolerating a model's
// reflow of newlines/indentation). The returned span and matched text always
// refer to the original content, never the model's (possibly collapsed)
// rendering.
func MatchFragment(content, fragment string) (startByte, endByte int, matched string, ok bool) {
	if idx := strings.Index(content, fragment); idx >= 0 {
		return idx, idx + len(fragment), fragment, true
	}

	collapsedContent, starts, ends := collapseWhitespace(content)
	collapsedFragment, _, _ := collapseWhitespace(fragment)
	if collapsedFragment == "" {
		return 0, 0, "", false
	}

	idxByte := strings.Index(collapsedContent, collapsedFragment)
	if idxByte < 0 {
		return 0, 0, "", false
	}
	idxRune := utf8.RuneCountInString(collapsedContent[:idxByte])
	fragRuneLen := utf8.RuneCountInString(collapsedFragment)
	if fragRuneLen == 0 || idxRune+fragRuneLen > len(starts) {
		return 0, 0, "", false
	}

	startByte = starts[idxRune]
	endByte = ends[idxRune+fragRuneLen-1]
	return startByte, endByte, content[startByte:endByte], true
}

// collapseWhitespace folds every run of space/tab/newline/CR into a single
// space, and returns, per rune of the collapsed string, the [start,end) byte
// range in the original string that rune's run came from — the mapping
// MatchFragment uses to translate a match in collapsed-space back to a
// verbatim original-text span.
func collapseWhitespace(s string) (collapsed string, starts, ends []int) {
	runes := []rune(s)
	byteOffset := make([]int, len(runes)+1)
	off := 0
	for i, r := range runes {
		byteOffset[i] = off
		off += utf8.RuneLen(r)
	}
	byteOffset[len(runes)] = off

	var b strings.Builder
	i := 0
	for i < len(runes) {
		if isSpaceRune(runes[i]) {
			j := i
			for j < len(runes) && isSpaceRune(runes[j]) {
				j++
			}
			b.WriteRune(' ')
			starts = append(starts, byteOffset[i])
			ends = append(ends, byteOffset[j])
			i = j
			continue
		}
		b.WriteRune(runes[i])
		starts = append(starts, byteOffset[i])
		ends = append(ends, byteOffset[i+1])
		i++
	}
	return b.String(), starts, ends
}

func isSpaceRune(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// ByteRangeToLines converts a [startByte,endByte) span within content into
// 1-based, inclusive line numbers relative to content itself (the caller adds
// its own offset to get absolute source-file line numbers).
func ByteRangeToLines(content string, startByte, endByte int) (lineStart, lineEnd int) {
	lineStart = 1 + strings.Count(content[:startByte], "\n")
	end := endByte
	if end > 0 && end <= len(content) && content[end-1] == '\n' {
		end--
	}
	lineEnd = 1 + strings.Count(content[:end], "\n")
	return lineStart, lineEnd
}
