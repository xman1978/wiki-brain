package pptx

import (
	"fmt"
	"strings"
)

// ToMarkdown renders a Deck as agent-readable Markdown.
func ToMarkdown(deck Deck) string {
	var b strings.Builder
	title := strings.TrimSpace(deck.Title)
	if title == "" {
		title = "Presentation"
	}
	b.WriteString("# " + title + "\n\n")

	for _, s := range deck.Slides {
		heading := strings.TrimSpace(s.Title)
		if heading == "" {
			heading = fmt.Sprintf("Slide %d", s.Number)
		}
		fmt.Fprintf(&b, "## Slide %d: %s\n\n", s.Number, heading)

		for _, block := range s.Blocks {
			switch block.Type {
			case "bullet":
				b.WriteString(strings.Repeat("  ", block.Level) + "- " + block.Text + "\n")
			case "image":
				alt := strings.TrimSpace(block.Alt)
				if alt == "" {
					alt = "no description"
				}
				b.WriteString("[IMAGE: " + alt + "]\n\n")
			case "table":
				if t := renderTable(block.Rows); t != "" {
					b.WriteString(t + "\n\n")
				}
			default:
				b.WriteString(block.Text + "\n\n")
			}
		}

		if notes := strings.TrimSpace(s.Notes); notes != "" {
			b.WriteString("> **Notes:** " + notes + "\n")
		}
		b.WriteString("\n---\n\n")
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func renderTable(rows [][]string) string {
	if len(rows) == 0 || !tableHasContent(rows) {
		return ""
	}
	cols := len(rows[0])
	esc := func(s string) string { return strings.ReplaceAll(s, "|", "\\|") }
	cells := func(row []string) string {
		c := make([]string, cols)
		for i := 0; i < cols; i++ {
			if i < len(row) {
				c[i] = esc(row[i])
			}
		}
		return "| " + strings.Join(c, " | ") + " |"
	}

	var b strings.Builder
	b.WriteString(cells(rows[0]) + "\n")
	sep := make([]string, cols)
	for i := range sep {
		sep[i] = "---"
	}
	b.WriteString("| " + strings.Join(sep, " | ") + " |")
	for _, row := range rows[1:] {
		b.WriteString("\n" + cells(row))
	}
	return b.String()
}

// tableHasContent reports whether any cell has non-whitespace text. An
// all-empty table is noise (PowerPoint placeholder grids) and is dropped.
func tableHasContent(rows [][]string) bool {
	for _, row := range rows {
		for _, cell := range row {
			if strings.TrimSpace(cell) != "" {
				return true
			}
		}
	}
	return false
}
