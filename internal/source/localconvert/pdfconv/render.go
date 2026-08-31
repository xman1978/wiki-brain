package pdfconv

import (
	"strings"
)

// render.go is a reduced-scope port of Part 2's rendering half
// (appendTextAsMarkdown / appendTextBodyAsMarkdown / renderTableMarkdown /
// appendSingleCellTableAsText). Full Part 2 style-clustering
// (buildHeadingStyleProfile / StyleCluster / canMergeHeadingPair /
// mergeWrappedHeadingLines) is not ported — see heading_geom.go's doc
// comment. What this does: walk the ordered, already-merged element list
// from convertDocument and emit Markdown, assigning heading levels via
// isHeading + the already-ported text-pattern level classifier
// (NaturalLevelForTitle, heading_level_prefix.go) rather than Java's
// font-size style clustering. The result is then handed to the shared
// RunMarkdownPipeline (pipeline.go) — the same MPP-derived heading/TOC
// cleanup stage already used for DOCX — for final structural cleanup.

// renderMarkdown walks ordered elements and produces raw (pre-MPP-pipeline)
// markdown text. Each TextBlock carries its own page's BodyFontMode
// (set in buildLineBlock) — heading detection must compare a block against
// *its own page's* body font, not a single document-wide value, since body
// font size can legitimately vary page to page (e.g. a cover page has
// little ordinary body text at all).
//
// Blank-line separation between consecutive blocks is not uniform: a run
// of consecutive list items (IsListItem, structure_rules.go) is rendered
// tightly with no blank line between entries, matching the reference
// output's convention for numbered/bulleted lists — every other block
// boundary (including into/out of a list run, tables, and headings) keeps
// a blank line.
func renderMarkdown(elements []GeometricElement, cfg Config) (string, map[string]bool) {
	var lines []string
	prevWasListItem := false
	first := true
	protectedLeftTexts := map[string]bool{}
	var prevTextBlock *TextBlock
	var prevBlockLastLine string
	for _, el := range elements {
		if t, ok := isTableBlock(el); ok {
			tableLines := renderTableMarkdown(t)
			if len(tableLines) == 0 {
				continue
			}
			if !first {
				lines = append(lines, "")
			}
			lines = append(lines, tableLines...)
			prevWasListItem = false
			first = false
			prevTextBlock = nil
			continue
		}
		t, ok := isTextBlock(el)
		if !ok {
			continue
		}
		text := strings.TrimSpace(t.Text)
		if text == "" {
			continue
		}
		blockLines, isHeadingBlock := renderTextBlock(t, cfg)
		isListItem := !isHeadingBlock && !t.MonoFont && IsListItem(text)
		if !first && !(prevWasListItem && isListItem) {
			lines = append(lines, "")
		}
		if prevTextBlock != nil && isVisualOnlyHeading(*prevTextBlock, prevTextBlock.BodyFontMode, cfg) &&
			styleDifferent(*prevTextBlock, t, cfg) {
			// Headings already get protected from stage-3 body-merge via
			// hierarchyLineIndexes (pipeline.go) once ApplyHeadingStage
			// confirms them — this is a second, independent line of
			// defense at render time, for the same font-boundary reason,
			// kept because it's a general property of the rendered output
			// rather than something that depends on ApplyHeadingStage's
			// text-only confirmation succeeding. See
			// RunMarkdownPipelineForPDF's doc comment.
			//
			// Gated on isVisualOnlyHeading, not a bare styleDifferent
			// check on every adjacent pair: an ordinary document-header
			// line ("WHRJ-CW-W-GL-2024-35/ V1.0") that happens to sit in a
			// slightly different font from the "编制：林燕云" line right
			// after it still needs to merge with it normally — protecting
			// every font-different boundary broke that real, wanted merge.
			// Only a block that would read as a heading candidate in its
			// own right (isVisualOnlyHeading's full shape+size checks) is
			// worth protecting from being swept into unrelated body text.
			protectedLeftTexts[prevBlockLastLine] = true
		}
		lines = append(lines, blockLines...)
		prevWasListItem = isListItem
		first = false
		if !t.MonoFont {
			prevTextBlockCopy := t
			prevTextBlock = &prevTextBlockCopy
			prevBlockLastLine = blockLines[len(blockLines)-1]
		} else {
			prevTextBlock = nil
		}
	}
	return strings.Join(lines, "\n"), protectedLeftTexts
}

// renderTextBlock returns the block's rendered lines (no leading/trailing
// blank-line separator — renderMarkdown owns spacing between blocks) plus
// whether it was rendered as a "#" heading.
func renderTextBlock(t TextBlock, cfg Config) ([]string, bool) {
	text := strings.TrimSpace(t.Text)
	if t.MonoFont {
		return []string{"```", text, "```"}, false
	}
	// Only visual-only (unnumbered) headings are pre-stamped with "#" — see
	// isVisualOnlyHeading's doc comment. Pattern-matched headings
	// (isHeadingByRegex/chapter/section — covered by isHeading but not
	// isVisualOnlyHeading) are left as plain lines for ApplyHeadingStage
	// (heading_stage.go, invoked via RunMarkdownPipeline) to discover and
	// level itself.
	if isVisualOnlyHeading(t, t.BodyFontMode, cfg) {
		level := visualHeadingLevel(t, t.BodyFontMode)
		return []string{strings.Repeat("#", level) + " " + text}, true
	}
	return []string{text}, false
}

func renderTableMarkdown(t TableBlock) []string {
	if t.RowCount == 1 && t.ColCount == 1 && len(t.SingleCellLines) > 0 {
		return appendSingleCellTableAsText(t)
	}
	grid := make([][]string, t.RowCount)
	for i := range grid {
		grid[i] = make([]string, t.ColCount)
	}
	for _, c := range t.Cells {
		if c.Row < 0 || c.Row >= t.RowCount || c.Col < 0 || c.Col >= t.ColCount {
			continue
		}
		grid[c.Row][c.Col] = strings.ReplaceAll(c.Text, "|", "\\|")
	}
	if t.RowCount == 0 || t.ColCount == 0 {
		return nil
	}
	var out []string
	out = append(out, "| "+strings.Join(grid[0], " | ")+" |")
	sep := make([]string, t.ColCount)
	for i := range sep {
		sep[i] = "---"
	}
	out = append(out, "| "+strings.Join(sep, " | ")+" |")
	for r := 1; r < t.RowCount; r++ {
		out = append(out, "| "+strings.Join(grid[r], " | ")+" |")
	}
	return out
}

func appendSingleCellTableAsText(t TableBlock) []string {
	if LooksLikePreformattedBlock(t.SingleCellLines) {
		return []string{WrapCodeFence(t.SingleCellLines)}
	}
	return append([]string(nil), t.SingleCellLines...)
}
