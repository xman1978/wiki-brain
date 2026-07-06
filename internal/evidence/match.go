package evidence

import (
	"strings"
	"unicode/utf8"
)

// matchFragment implements docs/impl/v1/evidence.md 步骤 3.1-3.2: exact match
// first, then a whitespace-collapsed fuzzy match that still resolves back to
// the verbatim original-text span (content is always taken from the
// original, never from the model's own whitespace/indentation).
func matchFragment(content, fragment string) (startByte, endByte int, matched string, ok bool) {
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
// matchFragment uses to translate a match in collapsed-space back to a
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

// byteRangeToLines converts a [startByte,endByte) span within content into
// 1-based, inclusive line numbers relative to content itself (the "KU 内相对
// 行号" of docs/impl/v1/evidence.md 步骤 3.5 — the caller adds
// KU.LineStart-1 to get absolute source-file line numbers).
func byteRangeToLines(content string, startByte, endByte int) (lineStart, lineEnd int) {
	lineStart = 1 + strings.Count(content[:startByte], "\n")
	end := endByte
	if end > 0 && end <= len(content) && content[end-1] == '\n' {
		end--
	}
	lineEnd = 1 + strings.Count(content[:end], "\n")
	return lineStart, lineEnd
}
