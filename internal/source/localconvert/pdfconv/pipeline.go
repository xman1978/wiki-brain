package pdfconv

import (
	"regexp"
	"strings"
)

// RunMarkdownPipeline is the shared entry point into the MPP post-processing
// pipeline described in docs/impl/v1/docx-port/01-word-to-markdown.md §12
// ("Word 输出是否复用 PDF 的 MPP 后处理管线") and
// docs/impl/v1/pdf-port/05-mpp-heading-stack.md /
// docs/impl/v1/pdf-port/06-mpp-merge-cleanup.md, used by both the DOCX path
// (docx.go's renderBodyBlocks output) and the PDF path (pdf.go's
// renderMarkdown output).
//
// Stages, in order:
//  1. TOC block removal (removeTocFromMarkdown, toc_remove.go, pdf-port/03
//     "算法：removeTocFromMarkdown") — strips the "目录/CONTENTS/图目录/表目录"
//     heading block plus every following all-TOC-entries block, then runs
//     ChapterTocLineRemover.stripFromMarkdown + isGenericDotLeaderTocLine
//     once more (stripChapterTocLines) to mop up any bare TOC lines the
//     block-level pass didn't cover.
//  2. ApplyHeadingStage (heading_stage.go/heading_stage_apply.go) — Part
//     4/5's title recognition/leveling/consistency machinery.
//  3. mergeWrappedBodyLines (mpp_bodymerge.go) — a reduced port of Part 6's
//     MarkdownBodyMergeStage, run after titles are finalized (matching the
//     real pipeline's order) so hierarchy lines are protected from being
//     re-absorbed into body text. See mpp_bodymerge.go's doc comment for
//     exactly what's ported vs the further scope reductions (cross-blank-
//     line merging, OCR-specific heuristics, the weak 2-/3-line pass) still
//     left for a follow-up.
//  4. Line-ending / control-character cleanup mirroring
//     WordToMarkdown.cleanOutput (docx-port/01 §9): CRLF normalization,
//     stripping form-feed/vertical-tab/bell control characters, collapsing
//     runs of 3+ blank lines to at most 2, trimming leading blank lines.
func RunMarkdownPipeline(markdown string) string {
	return runMarkdownPipeline(markdown, true, nil)
}

// RunMarkdownPipelineForPDF is RunMarkdownPipeline plus protectedLeftTexts:
// the exact trimmed text of rendered heading-candidate lines (render.go's
// renderMarkdown, gated on isVisualOnlyHeading) whose font size/weight
// genuinely differs from the block immediately following them — e.g. a
// cover title in a much larger font than its subtitle. mergeWrappedBodyLines
// (stage 3) never merges one of these lines forward into its successor:
// headings/titles only merge into one line when font size and style
// actually match (2026-08-31 user decision), matching the rule real
// confirmed "#" headings already get via hierarchyLineIndexes — this
// extends the same protection to a heading-shaped line for the rare case
// where ApplyHeadingStage's later text-only pass doesn't independently
// confirm it as one.
func RunMarkdownPipelineForPDF(markdown string, protectedLeftTexts map[string]bool) string {
	return runMarkdownPipeline(markdown, true, protectedLeftTexts)
}

// RunMarkdownPipelineNoBodyMerge is the DOCX-path entry point: identical to
// RunMarkdownPipeline but skips stage 3 (mergeWrappedBodyLines). That stage
// exists to re-glue paragraphs that PDF page-width reflow incorrectly split
// across lines — an artifact class that doesn't exist for DOCX, since
// docx_blocks.go already reads real `<w:p>` paragraph boundaries from the
// OOXML tree, and those boundaries are authoritative and must not be
// merged away regardless of how the resulting line reads (2026-08-31
// user decision, made after finding a DOCX test fixture that wanted two
// structurally-identical multi-`<w:p>` cases merged in one case and left
// split in the other — i.e. no content-based heuristic can be made to
// agree with both, so `<w:p>` boundaries win unconditionally).
func RunMarkdownPipelineNoBodyMerge(markdown string) string {
	return runMarkdownPipeline(markdown, false, nil)
}

func runMarkdownPipeline(markdown string, mergeBodyLines bool, protectedLeftTexts map[string]bool) string {
	if strings.TrimSpace(markdown) == "" {
		return ""
	}
	out := removeTocFromMarkdown(markdown)

	normalized := strings.ReplaceAll(out, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	lines, _, hierarchyLineIndexes := ApplyHeadingStage(lines)
	if mergeBodyLines {
		lines = mergeWrappedBodyLines(lines, hierarchyLineIndexes, protectedLeftTexts)
	}
	out = strings.Join(lines, "\n")

	if mergeBodyLines {
		// pdf-port/03 "算法：cleanOutput" step 3 (splitConcatenatedOrderedListLines):
		// PDF-only — re-splits a line where PDF hard-wrap/geometry line
		// merging glued multiple "（1）……（2）……" ordered-list markers
		// together, one physical line at a time. DOCX paragraphs come from
		// real <w:p> boundaries and never suffer this artifact, so this
		// step is gated the same way as mergeWrappedBodyLines above.
		out = splitConcatenatedOrderedListLines(out)
	}

	out = cleanOutputControlChars(out)
	return out
}

// stripChapterTocLines ports ChapterTocLineRemover.stripFromMarkdown /
// stripLines / stripLinesInternal (the subset needed here: removing
// standalone TOC entry lines and their trailing orphan page-number lines,
// outside of fenced code blocks). The "裸目录" (stripBareTocChapterRuns)
// OCR-only branch is PDF/OCR-specific and out of scope for DOCX input.
func stripChapterTocLines(markdown string) string {
	normalized := strings.ReplaceAll(markdown, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")

	var out []string
	inFence := false
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			out = append(out, line)
			i++
			continue
		}
		if !inFence && (IsChapterTocLine(trimmed) || isGenericDotLeaderTocLine(trimmed)) {
			j := i + 1
			for j < len(lines) {
				if strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
					break
				}
				if strings.TrimSpace(lines[j]) == "" {
					j++
					continue
				}
				if IsChapterTocLine(strings.TrimSpace(lines[j])) || isGenericDotLeaderTocLine(strings.TrimSpace(lines[j])) || isChapterTocOrphanPageSuffixLine(lines[j]) {
					j++
					continue
				}
				break
			}
			i = j
			continue
		}
		if !inFence && isChapterTocOrphanPageSuffixLine(line) {
			i++
			continue
		}
		out = append(out, line)
		i++
	}
	return strings.Join(out, "\n")
}

// isGenericDotLeaderTocLine catches TOC entries that ChapterTocLineRemover's
// ported subset (chapter_toc.go's IsChapterTocLine) doesn't cover — it only
// recognizes "第X章"-prefixed TOC lines, but a document's TOC just as often
// lists sub-sections ("1.1政策背景....2"), appendices ("附件二：《...》....10")
// or other non-chapter entries in the same dot-leader-plus-page-number
// shape. This one isn't just cosmetic: an un-stripped TOC line reusing the
// same numbering pattern as a real body heading (e.g. both are
// "1.1"-style) feeds HeadingPatternQualityHeuristics'
// detectMixedRecognitionPatternKeys (heading_pattern_quality.go) a
// body-like-looking occurrence of that pattern key, which can blacklist
// the pattern document-wide and suppress the real heading too — so leaving
// generic TOC lines in place doesn't just clutter output, it can silently
// eat legitimate headings elsewhere in the document.
var genericDotLeaderTocLineRe = regexp.MustCompile(`^(?:#{1,6}\s*)?.{1,80}(?:\.{3,}|…{1,}|·{3,})\s*[-—]?\s*\d{1,4}\s*[-—]?\s*$`)

func isGenericDotLeaderTocLine(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	return genericDotLeaderTocLineRe.MatchString(t)
}

var chapterTocOrphanDashPageRe = regexp.MustCompile(`^(?:#{1,6}\s*)?\d+-\s*$`)

func isChapterTocOrphanPageSuffixLine(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	return chapterTocOrphanDashPageRe.MatchString(t)
}

var (
	bellRunRe      = regexp.MustCompile("\x07+")
	controlCharsRe = regexp.MustCompile(`[\x00-\x08\x0E-\x1F]`)
	blankRunRe     = regexp.MustCompile(`\n{3,}`)
)

// cleanOutputControlChars ports WordToMarkdown.cleanOutput (docx-port/01 §9).
func cleanOutputControlChars(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	out := strings.ReplaceAll(text, "\r\n", "\n")
	out = strings.ReplaceAll(out, "\r", "\n")
	out = strings.ReplaceAll(out, "\f", "")
	out = strings.ReplaceAll(out, "\v", "")
	out = bellRunRe.ReplaceAllString(out, "\n")
	out = controlCharsRe.ReplaceAllString(out, "")
	out = blankRunRe.ReplaceAllString(out, "\n\n")
	out = strings.TrimLeft(out, "\n")
	return strings.TrimSpace(out)
}
