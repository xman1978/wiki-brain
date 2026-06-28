package source

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func ExtractStructuralOutlines(sourceID, content string) []Outline {
	lines := strings.Split(content, "\n")
	type heading struct {
		level    int
		title    string
		lineIdx  int // 0-based
	}

	var headings []heading
	inCodeBlock := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			continue
		}
		if m := reHeading.FindStringSubmatch(line); m != nil {
			level := len(m[1])
			if level > 4 {
				level = 4
			}
			title := strings.TrimSpace(strings.TrimLeft(line, "#"))
			headings = append(headings, heading{level: level, title: title, lineIdx: i})
		}
	}

	if len(headings) == 0 {
		return nil
	}

	totalLines := len(lines)
	var outlines []Outline

	for i, h := range headings {
		lineStart := h.lineIdx + 1 // 1-based

		var lineEnd int
		if i+1 < len(headings) {
			lineEnd = headings[i+1].lineIdx // line before next heading (0-based), so 1-based = lineIdx
		} else {
			lineEnd = totalLines
		}

		outlines = append(outlines, Outline{
			OutlineID: uuid.New().String(),
			SourceID:  sourceID,
			Level:     h.level,
			Title:     h.title,
			LineStart: lineStart,
			LineEnd:   lineEnd,
			NodeType:  "structural",
		})
	}

	assignParentsAndPositions(outlines)
	return outlines
}

func assignParentsAndPositions(outlines []Outline) {
	type stackEntry struct {
		outlineID string
		level     int
	}
	var stack []stackEntry
	positionCounters := make(map[string]int) // parent_id -> next position

	for i := range outlines {
		o := &outlines[i]

		for len(stack) > 0 && stack[len(stack)-1].level >= o.Level {
			stack = stack[:len(stack)-1]
		}

		var parentKey string
		if len(stack) > 0 {
			parentID := stack[len(stack)-1].outlineID
			o.ParentID = sql.NullString{String: parentID, Valid: true}
			parentKey = parentID
		} else {
			parentKey = ""
		}

		o.Position = positionCounters[parentKey]
		positionCounters[parentKey]++

		stack = append(stack, stackEntry{outlineID: o.OutlineID, level: o.Level})
	}
}

type SemanticTrigger struct {
	Triggered bool
	Reasons   []string
}

func CheckSemanticTrigger(outlines []Outline, content string, format string, segmentMaxChars int) SemanticTrigger {
	result := SemanticTrigger{}
	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	totalRunes := RuneCount(content)

	nodeCount := len(outlines)

	// A: No structure
	if nodeCount == 0 {
		result.Triggered = true
		result.Reasons = append(result.Reasons, "A: no structural outlines")
		return result
	}

	// B: Too sparse
	if nodeCount < 3 && totalRunes > 2000 {
		result.Triggered = true
		result.Reasons = append(result.Reasons, "B: structure too sparse")
	}

	// C: Coverage < 50%
	coveredLines := 0
	for _, o := range outlines {
		coveredLines += o.LineEnd - o.LineStart + 1
	}
	if totalLines > 0 && float64(coveredLines)/float64(totalLines) < 0.5 {
		result.Triggered = true
		result.Reasons = append(result.Reasons, "C: coverage < 50%")
	}

	// D: Flat structure
	if nodeCount < 5 {
		allSameLevel := true
		firstLevel := outlines[0].Level
		for _, o := range outlines[1:] {
			if o.Level != firstLevel {
				allSameLevel = false
				break
			}
		}
		if allSameLevel {
			result.Triggered = true
			result.Reasons = append(result.Reasons, "D: flat structure")
		}
	}

	// E: Leaf nodes too long
	leafNodes := findLeafNodes(outlines)
	for _, leaf := range leafNodes {
		nodeContent := extractLineRange(lines, leaf.LineStart, leaf.LineEnd)
		if RuneCount(nodeContent) > segmentMaxChars {
			result.Triggered = true
			result.Reasons = append(result.Reasons, "E: leaf node too long")
			break
		}
	}

	// F: PDF or image format — force leaf node length check
	if IsPDFOrImage(format) {
		for _, leaf := range leafNodes {
			nodeContent := extractLineRange(lines, leaf.LineStart, leaf.LineEnd)
			if RuneCount(nodeContent) > segmentMaxChars {
				if !result.Triggered {
					result.Triggered = true
				}
				hasE := false
				for _, r := range result.Reasons {
					if strings.HasPrefix(r, "E:") {
						hasE = true
						break
					}
				}
				if !hasE {
					result.Reasons = append(result.Reasons, "F+E: PDF/image with long leaf nodes")
				}
				break
			}
		}
	}

	return result
}

func findLeafNodes(outlines []Outline) []Outline {
	parentIDs := make(map[string]bool)
	for _, o := range outlines {
		if o.ParentID.Valid {
			parentIDs[o.ParentID.String] = true
		}
	}

	var leaves []Outline
	for _, o := range outlines {
		if !parentIDs[o.OutlineID] {
			leaves = append(leaves, o)
		}
	}
	return leaves
}

// numberedLineRange returns content with line number prefixes like "76: content".
func numberedLineRange(lines []string, lineStart, lineEnd int) string {
	if lineStart < 1 {
		lineStart = 1
	}
	if lineEnd > len(lines) {
		lineEnd = len(lines)
	}
	var sb strings.Builder
	for i := lineStart - 1; i < lineEnd; i++ {
		fmt.Fprintf(&sb, "%d: %s\n", i+1, lines[i])
	}
	return sb.String()
}

func extractLineRange(lines []string, lineStart, lineEnd int) string {
	if lineStart < 1 {
		lineStart = 1
	}
	if lineEnd > len(lines) {
		lineEnd = len(lines)
	}
	return strings.Join(lines[lineStart-1:lineEnd], "\n")
}
