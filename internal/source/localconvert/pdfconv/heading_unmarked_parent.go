package pdfconv

// UnmarkedParentHeadingHeuristics port (pdf-port/05-mpp-heading-stack.md
// "UnmarkedParentHeadingHeuristics" section): after all headings are
// finalized and written as `#` lines, demote any child-level `#` heading
// (e.g. a "（一）" line) whose nearest heading ancestor is an *unmarked*
// plain-text parent-shaped line (e.g. a "三、" line with no `#`) rather than
// a real `#` heading. This is the compensation pass for the common
// Word/HTML-export artifact where only a subset of a document's heading
// levels got promoted.

import "strings"

var conventionalParents = map[string][]string{
	"TITLE_CN_PAREN":   {"TITLE_CN_NUM", "TITLE_CHAPTER_ONE", "TITLE_CHAPTER_TOW", "TITLE_CHAPTER_THREE", "TITLE_CHAPTER_FOUR", "TITLE_CHAPTER_FIVE"},
	"TITLE_NUM_TOW":    {"TITLE_NUM_DOT", "TITLE_CN_NUM", "TITLE_CN_PAREN"},
	"TITLE_NUM_THREE":  {"TITLE_NUM_TOW"},
	"TITLE_NUM_FOUR":   {"TITLE_NUM_THREE"},
	"TITLE_NUM_FIVE":   {"TITLE_NUM_FOUR"},
	"TITLE_NUM_DOT":    {"TITLE_CN_NUM", "TITLE_CN_PAREN"},
	"TITLE_NUM_DUNHAO": {"TITLE_CN_NUM", "TITLE_CN_PAREN"},
}

func isConventionalParentPrefix(parentKey, childKey string) bool {
	if parentKey == "" || childKey == "" {
		return false
	}
	allowed, ok := conventionalParents[childKey]
	if !ok {
		return false
	}
	for _, a := range allowed {
		if a == parentKey {
			return true
		}
	}
	return false
}

var demotableChildKeys = map[string]bool{
	"TITLE_CN_PAREN": true, "TITLE_NUM_TOW": true, "TITLE_NUM_THREE": true,
	"TITLE_NUM_FOUR": true, "TITLE_NUM_FIVE": true, "TITLE_NUM_DOT": true, "TITLE_NUM_DUNHAO": true,
}

var plainNumericSectionKeys = map[string]bool{
	"TITLE_NUM_DOT": true, "TITLE_NUM_TOW": true, "TITLE_NUM_THREE": true,
	"TITLE_NUM_FOUR": true, "TITLE_NUM_FIVE": true,
}

func isUnderChapterHeading(lines []string, lineID int) bool {
	inFence := false
	for i := lineID - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "```") {
			inFence = !inFence
			continue
		}
		if inFence || t == "" {
			continue
		}
		if isMarkdownHeadingLine(t) {
			title := headingTitleFromLine(t)
			return ClassifyPrefixKey(title) == "TITLE_CHAPTER_ONE"
		}
	}
	return false
}

func isValidSectionLabel(rawOrTitle string) bool {
	if IsBlank(rawOrTitle) {
		return false
	}
	norm := normalizeForHeadingPrefixMatch(StripHeadingHashes(rawOrTitle))
	if strings.HasSuffix(norm, "：") || strings.HasSuffix(norm, ":") {
		return false
	}
	if ClearlyFailsHeadingQuality(norm) {
		return false
	}
	return IsStandaloneHeadingLine(rawOrTitle)
}

func plainSectionLabelTitle(lines []string, lineID int) (string, bool) {
	if lineID < 0 || lineID >= len(lines) {
		return "", false
	}
	raw := lines[lineID]
	if IsBlank(raw) {
		return "", false
	}
	t := strings.TrimSpace(raw)
	if isMarkdownHeadingLine(t) {
		return "", false
	}
	if !isValidSectionLabel(raw) {
		return "", false
	}
	key := ClassifyPrefixKey(NormalizeLine(raw))
	if key == "TITLE_CN_NUM" || plainNumericSectionKeys[key] {
		return headingTitleFromLine(raw), true
	}
	return "", false
}

func hasCnParenSectionFollowing(lines []string, fromLineID int) bool {
	limit := fromLineID + 12
	if limit > len(lines) {
		limit = len(lines)
	}
	for i := fromLineID + 1; i < limit; i++ {
		title := headingTitleFromLine(lines[i])
		if ClassifyPrefixKey(title) == "TITLE_CN_PAREN" {
			return true
		}
	}
	return false
}

func isCnNumSectionDemotedUnderChapter(lines []string, lineID int, title string) bool {
	if ClassifyPrefixKey(title) != "TITLE_CN_NUM" {
		return false
	}
	return isUnderChapterHeading(lines, lineID) && isValidSectionLabel(title) && hasCnParenSectionFollowing(lines, lineID)
}

func shouldDemoteMisplacedChild(lines []string, childLineID int, childKey string, demotedCnNum map[int]bool) bool {
	if len(lines) == 0 || childKey == "" || childLineID <= 0 {
		return false
	}
	inFence := false
	for i := childLineID - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "```") {
			inFence = !inFence
			continue
		}
		if inFence || t == "" {
			continue
		}
		if isMarkdownHeadingLine(t) {
			title := headingTitleFromLine(t)
			pk := ClassifyPrefixKey(title)
			if pk == "TITLE_CHAPTER_ONE" || pk == "TITLE_CHAPTER_TOW" || pk == "TITLE_CHAPTER_THREE" || pk == "TITLE_CHAPTER_FOUR" || pk == "TITLE_CHAPTER_FIVE" {
				if !isConventionalParentPrefix(pk, childKey) {
					return false
				}
			}
			if isConventionalParentPrefix(pk, childKey) {
				if demotedCnNum[i] || isCnNumSectionDemotedUnderChapter(lines, i, title) {
					continue
				}
				return false
			}
			return false
		}
		if label, ok := plainSectionLabelTitle(lines, i); ok {
			parentKey := ClassifyPrefixKey(label)
			return isConventionalParentPrefix(parentKey, childKey)
		}
	}
	return false
}

// DemoteMisplacedSectionHeadings ports
// UnmarkedParentHeadingHeuristics.demoteMisplacedSectionHeadings. It mutates
// lines in place (demoting `#` to plain text) and returns the filtered hit
// list plus the set of demoted line indexes.
func DemoteMisplacedSectionHeadings(lines []string, hits []*HeadingHit) ([]*HeadingHit, map[int]bool) {
	demoted := map[int]bool{}
	demotedCnNum := map[int]bool{}
	toDemote := map[int]bool{}

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
		title := headingTitleFromLine(t)
		key := ClassifyPrefixKey(title)
		if key == "" {
			continue
		}
		if key == "TITLE_CN_NUM" && isCnNumSectionDemotedUnderChapter(lines, i, title) && hasCnParenSectionFollowing(lines, i) {
			toDemote[i] = true
			demotedCnNum[i] = true
		}
	}

	inFence = false
	for i, raw := range lines {
		t := strings.TrimSpace(raw)
		if strings.HasPrefix(t, "```") {
			inFence = !inFence
			continue
		}
		if inFence || toDemote[i] || !isMarkdownHeadingLine(t) {
			continue
		}
		title := headingTitleFromLine(t)
		childKey := ClassifyPrefixKey(title)
		if childKey == "" || !demotableChildKeys[childKey] {
			continue
		}
		if shouldDemoteMisplacedChild(lines, i, childKey, demotedCnNum) {
			toDemote[i] = true
		}
	}

	for i := range toDemote {
		lines[i] = headingTitleFromLine(lines[i])
		demoted[i] = true
	}

	var kept []*HeadingHit
	for _, h := range hits {
		if demoted[h.LineIndex] {
			continue
		}
		key := ClassifyPrefixKey(h.TitleRaw)
		if key == "TITLE_CN_NUM" && isCnNumSectionDemotedUnderChapter(lines, h.LineIndex, h.TitleRaw) {
			lines[h.LineIndex] = headingTitleFromLine(lines[h.LineIndex])
			demoted[h.LineIndex] = true
			continue
		}
		if key == "TITLE_CN_PAREN" || plainNumericSectionKeys[key] {
			if shouldDemoteMisplacedChild(lines, h.LineIndex, key, demotedCnNum) {
				lines[h.LineIndex] = headingTitleFromLine(lines[h.LineIndex])
				demoted[h.LineIndex] = true
				continue
			}
		}
		kept = append(kept, h)
	}
	return kept, demoted
}
