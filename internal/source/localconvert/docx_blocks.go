package localconvert

// Ports docs/impl/v1/docx-port/01-word-to-markdown.md §2 (collectBodyBlocks)
// and §5 (renderBodyBlocks / heading demotion re-check).

import (
	"strings"

	"github.com/jxman78/wiki-brain/internal/source/localconvert/pdfconv"
)

// wordBodyBlock ports docx-port/01 §2's BodyBlock record.
type wordBodyBlock struct {
	Line    string
	Heading bool
	Level   int
}

type pendingHeading struct {
	text  string
	level int
}

// flushPendingHeadingBlock ports docx-port/01 §2 flushPendingHeadingBlock.
func flushPendingHeadingBlock(pending *pendingHeading, out *[]wordBodyBlock) {
	if pending == nil || strings.TrimSpace(pending.text) == "" {
		return
	}
	*out = append(*out, wordBodyBlock{Line: strings.TrimSpace(pending.text), Heading: true, Level: pending.level})
}

// collectBodyBlocks ports docx-port/01 §2.
func collectBodyBlocks(doc *docxDoc) []wordBodyBlock {
	var out []wordBodyBlock
	var pending *pendingHeading

	flush := func() {
		flushPendingHeadingBlock(pending, &out)
		pending = nil
	}

	for _, block := range doc.Blocks {
		if block.Table != nil {
			flush()
			md := renderTable(block.Table)
			out = append(out, wordBodyBlock{Line: md + "\n", Heading: false})
			continue
		}
		p := block.Para
		if p == nil || p.InCell {
			continue
		}
		text := strings.TrimSpace(p.Text)
		if text == "" || text == "目录" {
			flush()
			continue
		}
		if isWordHeadingFragment(p, text) {
			headingText := headingTextWithListLabel(p, text, doc.numbering)
			level := resolveWordHeadingLevel(p, headingText)
			switch {
			case pending == nil:
				pending = &pendingHeading{text: headingText, level: level}
			case shouldMergeWordHeadingFragments(pending.text, pending.level, headingText, level):
				pending.text = joinHeadingFragments(pending.text, headingText)
				if level < pending.level {
					pending.level = level
				}
			default:
				flush()
				pending = &pendingHeading{text: headingText, level: level}
			}
			continue
		}
		flush()
		listLabel := ""
		if p.HasNum && doc.numbering != nil {
			listLabel = doc.numbering.LabelForNext(p.NumID, p.ILvl)
		}
		for _, line := range expandSoftBreakPlainLines(listLabel, text) {
			out = append(out, wordBodyBlock{Line: line, Heading: false})
		}
	}
	flush()
	return out
}

// toMarkdownHeadingLine clamps level to [1,6] and renders "# text".
func toMarkdownHeadingLine(level int, text string) string {
	if level < 1 {
		level = 1
	}
	if level > 6 {
		level = 6
	}
	return strings.Repeat("#", level) + " " + strings.TrimSpace(text)
}

// renderBodyBlocks ports docx-port/01 §5.
func renderBodyBlocks(blocks []wordBodyBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	lines := make([]string, len(blocks))
	for i, b := range blocks {
		lines[i] = b.Line
	}
	isHeadingAt := func(i int) bool { return blocks[i].Heading }

	demote := pdfconv.DetectMarkdownLinesToDemote(lines, isHeadingAt)
	for i := range demote {
		demote[i] = true
	}
	demote2 := pdfconv.DetectLineIndexesToDemoteAsNonHeading(lines, isHeadingAt)
	for i, v := range demote2 {
		if v {
			demote[i] = true
		}
	}

	var sb strings.Builder
	for i, b := range blocks {
		if b.Heading && !demote[i] {
			sb.WriteString(toMarkdownHeadingLine(b.Level, b.Line))
			sb.WriteString("\n\n")
		} else {
			sb.WriteString(b.Line)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}
