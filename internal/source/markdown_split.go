package source

import (
	"strings"
)

// breakPriority 标记每一行作为分割点的优先级（越高越适合分割）
// 3: heading 行之前（最佳分割点）
// 2: 空行（段落边界）
// 1: 普通行（可分割但不理想）
// 0: 不可分割（代码块/表格内部）
func markBreakPriorities(lines []string) []int {
	priorities := make([]int, len(lines))
	inCodeBlock := false

	// 标记代码块内部为不可分割
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if !inCodeBlock {
				inCodeBlock = true
				priorities[i] = 0 // 代码块开始行不可分割
			} else {
				inCodeBlock = false
				priorities[i] = 0 // 代码块结束行不可分割
			}
			continue
		}
		if inCodeBlock {
			priorities[i] = 0
			continue
		}
		priorities[i] = 1 // 默认：普通行
	}

	// 标记表格内部为不可分割
	inTable := false
	for i, line := range lines {
		if priorities[i] == 0 {
			continue // 已在代码块内
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "|") {
			if !inTable {
				inTable = true
			}
			priorities[i] = 0
		} else {
			inTable = false
		}
	}

	// 标记空行为段落边界（优先级 2）
	for i, line := range lines {
		if priorities[i] == 0 {
			continue
		}
		if strings.TrimSpace(line) == "" {
			priorities[i] = 2
		}
	}

	// 标记 heading 行之前为最佳分割点（优先级 3）
	for i, line := range lines {
		if priorities[i] == 0 {
			continue
		}
		if reHeading.MatchString(line) {
			priorities[i] = 3
		}
	}

	return priorities
}

// splitWindowsByMarkdown 按 Markdown 元素边界分窗口。
// 每个窗口内容 ≤ maxRunes，不在代码块/表格中间切割。
// 返回的 window 使用 1-based, inclusive 行号。
type mdWindow struct {
	StartLine int // 1-based
	EndLine   int // 1-based, inclusive
}

// splitWindowsByMarkdown splits lines into windows of ≤ maxRunes, breaking at
// the best available Markdown boundary (heading > blank line > plain line),
// never inside a code block or table.
//
// avoidCuttingIndivisible controls what happens when the only line to cut at
// still falls inside a code block/table (i.e. no acceptable break exists
// nearby): existing callers (extractLocalSketches, which reconciles its own
// windows afterward via starts_mid_section/ends_mid_section) pass false and
// keep today's behavior — cut at the size limit regardless. Callers that
// treat the split as final, uncorrected truth (splitOversizeLeaf) pass true:
// the boundary is pushed forward past the end of the indivisible run instead,
// letting that one window exceed maxRunes rather than ever slicing through a
// table/code block.
func splitWindowsByMarkdown(lines []string, maxRunes int, overlapPercent float64, avoidCuttingIndivisible bool) []mdWindow {
	totalLines := len(lines)
	if totalLines == 0 {
		return nil
	}

	priorities := markBreakPriorities(lines)

	// 预计算每行的累计 rune 数
	lineRunes := make([]int, totalLines)
	for i, l := range lines {
		lineRunes[i] = RuneCount(l) + 1 // +1 for newline
	}

	var windows []mdWindow
	start := 0 // 0-based

	for start < totalLines {
		// 贪心：尽量多取行，直到 rune 数超限
		runeSum := 0
		hardEnd := start
		for hardEnd < totalLines {
			if runeSum+lineRunes[hardEnd] > maxRunes && hardEnd > start {
				break
			}
			runeSum += lineRunes[hardEnd]
			hardEnd++
		}

		// 从 hardEnd 往回找最佳分割点
		bestBreak := hardEnd
		if hardEnd < totalLines {
			bestBreak = findBestBreak(priorities, start, hardEnd)
		}

		// bestBreak 必须大于 start，否则无法推进
		if bestBreak <= start {
			bestBreak = hardEnd
		}
		if bestBreak <= start {
			bestBreak = start + 1
		}

		// 切点本身仍落在不可分割元素内部（findBestBreak 找不到更好的点、
		// 只能退化到 hardEnd 的情况）：把切点推到该元素结束处，让当前窗口
		// 整体吞下这个元素、允许超过 maxRunes，而不是从中间切开。
		if avoidCuttingIndivisible {
			for bestBreak < totalLines && priorities[bestBreak] == 0 {
				bestBreak++
			}
		}

		windows = append(windows, mdWindow{
			StartLine: start + 1,
			EndLine:   bestBreak,
		})

		if bestBreak >= totalLines {
			break
		}

		// overlap — avoidCuttingIndivisible callers treat the split as final,
		// authoritative tiling (splitOversizeLeaf needs zero overlap to
		// guarantee non-overlapping full coverage), so they skip the forced
		// minimum-5-line overlap below entirely rather than just passing
		// overlapPercent=0 (which this minimum would otherwise override).
		nextStart := bestBreak
		if !avoidCuttingIndivisible {
			windowLines := bestBreak - start
			overlap := int(float64(windowLines) * overlapPercent)
			if overlap < 5 && windowLines > 10 {
				overlap = 5
			} else if windowLines <= 10 {
				overlap = 0
			}
			nextStart = bestBreak - overlap
			if nextStart <= start {
				nextStart = bestBreak // 确保推进
			}
		}
		start = nextStart
	}

	return windows
}

// findBestBreak 从 hardEnd 往回搜索最佳分割行（0-based）。
// 优先找 heading 边界 > 空行 > 普通行，不在不可分割元素内切。
func findBestBreak(priorities []int, start, hardEnd int) int {
	// 搜索范围：从 hardEnd 往回最多 20% 的窗口大小
	searchBack := (hardEnd - start) / 5
	if searchBack < 10 {
		searchBack = 10
	}
	searchFrom := hardEnd - searchBack
	if searchFrom < start+1 {
		searchFrom = start + 1
	}

	bestIdx := hardEnd
	bestPri := -1

	for i := hardEnd - 1; i >= searchFrom; i-- {
		if priorities[i] > bestPri {
			bestPri = priorities[i]
			bestIdx = i
			if bestPri == 3 {
				break // heading 边界是最佳，直接使用
			}
		}
	}

	// 如果找到的分割点优于"不可分割"，使用它
	if bestPri > 0 {
		return bestIdx
	}

	// 实在找不到好的分割点，允许在不可分割元素的边界切（尽量找元素结尾）
	return hardEnd
}
