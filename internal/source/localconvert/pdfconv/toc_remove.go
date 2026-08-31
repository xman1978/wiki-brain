package pdfconv

import (
	"regexp"
	"strings"
)

// removeTocFromMarkdown ports PdfToMarkdown.removeTocFromMarkdown
// (docs/impl/v1/pdf-port/03-toc-cleanup-sequence.md "算法：removeTocFromMarkdown"):
// strip the "目录/CONTENTS/图目录/表目录" heading block and every following
// block that is entirely TOC entries, then run stripChapterTocLines
// (ChapterTocLineRemover.stripFromMarkdown) once more to mop up any bare
// TOC lines the block-level pass didn't cover.
func removeTocFromMarkdown(markdown string) string {
	normalized := strings.ReplaceAll(markdown, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	normalized = strings.TrimSpace(normalized)
	if normalized == "" {
		return ""
	}

	blocks := splitMarkdownBlocks(normalized)
	var out []string
	inToc := false
	for _, block := range blocks {
		if strings.TrimSpace(block) == "" {
			if !inToc {
				out = append(out, block)
			}
			continue
		}

		parts := splitTracePrefix(block)
		body := strings.TrimSpace(parts.body)
		visible := strings.TrimSpace(leadingHashPrefixRe.ReplaceAllString(body, ""))

		if tocBlockMarkerRe.MatchString(visible) {
			inToc = true
			continue
		}

		if inToc {
			if isEntireBlockTocEntries(body) {
				continue
			}
			inToc = false
		}

		if isChapterTocOnlyBlock(body) {
			continue
		}
		if isChapterTocOrphanPageOnlyBlock(body) {
			continue
		}

		stripped := stripChapterTocLinesFromBlock(body)
		if strings.TrimSpace(stripped) == "" {
			continue
		}
		merged := stripped
		if parts.prefix != "" {
			merged = parts.prefix + "\n" + stripped
		}
		out = append(out, strings.TrimSpace(merged))
	}

	result := strings.TrimSpace(strings.Join(out, "\n\n"))
	return stripChapterTocLines(result)
}

// tocBlockMarkerRe matches a block that, once trimmed and stripped of any
// leading heading hashes, is nothing but a TOC section title.
var tocBlockMarkerRe = regexp.MustCompile(`(?i)^(目\s*录|CONTENTS|图目录|表目录)$`)

// blockParts mirrors PdfToMarkdown's private BlockParts.
type blockParts struct {
	prefix string
	body   string
}

var traceCommentPrefixRe = regexp.MustCompile(`^<!--\s*TRACE`)

// splitTracePrefix ports PdfToMarkdown.splitTracePrefix.
func splitTracePrefix(block string) blockParts {
	trimmed := strings.TrimSpace(block)
	if trimmed == "" {
		return blockParts{}
	}
	lines := strings.Split(trimmed, "\n")
	var prefixLines []string
	idx := 0
	for idx < len(lines) {
		l := strings.TrimSpace(lines[idx])
		if !traceCommentPrefixRe.MatchString(l) {
			break
		}
		prefixLines = append(prefixLines, l)
		idx++
	}
	body := strings.TrimSpace(strings.Join(lines[idx:], "\n"))
	return blockParts{prefix: strings.Join(prefixLines, "\n"), body: body}
}

var blankRunSplitRe = regexp.MustCompile(`\n{3,}`)
var blockSplitRe = regexp.MustCompile(`\n\n+`)

// splitMarkdownBlocks ports PdfToMarkdown.splitMarkdownBlocks.
func splitMarkdownBlocks(markdown string) []string {
	cleaned := strings.TrimSpace(blankRunSplitRe.ReplaceAllString(markdown, "\n\n"))
	if cleaned == "" {
		return nil
	}
	return blockSplitRe.Split(cleaned, -1)
}

// isTocPagedLine ports ChapterTocLineRemover-adjacent PdfToMarkdown.isTocPagedLine.
var (
	endsWithPageNoRe    = regexp.MustCompile(`.*\d+$`)
	hasLeaderDotsRe     = regexp.MustCompile(`.*(\.{2,}|…{2,}|·{2,}|⋯{2,}).*\d+$`)
	hasAlignedSpacesRe  = regexp.MustCompile(`(?s).*(\t|\s{2,}).*\d+$`)
)

func isTocPagedLine(visible string) bool {
	v := strings.TrimSpace(visible)
	if v == "" {
		return false
	}
	if !endsWithPageNoRe.MatchString(v) {
		return false
	}
	hasLeaderDots := hasLeaderDotsRe.MatchString(v)
	hasAlignedSpaces := hasAlignedSpacesRe.MatchString(v)
	return hasLeaderDots || hasAlignedSpaces || isChapterTocPagedEntry(v)
}

// isChapterTocPagedEntry ports PdfToMarkdown.isChapterTocPagedEntry.
func isChapterTocPagedEntry(visible string) bool {
	return IsChapterTocLine(strings.TrimSpace(visible))
}

// isEntireBlockTocEntries ports PdfToMarkdown.isEntireBlockTocEntries.
func isEntireBlockTocEntries(body string) bool {
	lines := strings.Split(body, "\n")
	n := 0
	for _, line := range lines {
		v := strings.TrimSpace(line)
		if v == "" {
			continue
		}
		if !isTocPagedLine(v) {
			return false
		}
		n++
	}
	return n > 0
}

const minChapterTocLinesInBlock = 1

// isChapterTocOnlyBlock ports ChapterTocLineRemover.isChapterTocOnlyBlock.
func isChapterTocOnlyBlock(body string) bool {
	lines := strings.Split(body, "\n")
	n := 0
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if !IsChapterTocLine(t) {
			return false
		}
		n++
	}
	return n >= minChapterTocLinesInBlock
}

// isChapterTocOrphanPageOnlyBlock ports PdfToMarkdown.isChapterTocOrphanPageOnlyBlock.
func isChapterTocOrphanPageOnlyBlock(body string) bool {
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		t = strings.TrimSpace(leadingHashPrefixRe.ReplaceAllString(t, ""))
		return isChapterTocOrphanPageSuffixLine(t)
	}
	return false
}

// stripChapterTocLinesFromBlock ports PdfToMarkdown.stripChapterTocLinesFromBlock.
func stripChapterTocLinesFromBlock(body string) string {
	lines := strings.Split(body, "\n")
	var kept []string
	for _, line := range lines {
		v := strings.TrimSpace(leadingHashPrefixRe.ReplaceAllString(strings.TrimSpace(line), ""))
		if v != "" && (isChapterTocPagedEntry(v) || isChapterTocOrphanPageSuffixLine(v)) {
			continue
		}
		kept = append(kept, line)
	}
	// Trim leading/trailing blank lines.
	start := 0
	for start < len(kept) && strings.TrimSpace(kept[start]) == "" {
		start++
	}
	end := len(kept)
	for end > start && strings.TrimSpace(kept[end-1]) == "" {
		end--
	}
	kept = kept[start:end]
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
