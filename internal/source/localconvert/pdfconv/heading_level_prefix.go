package pdfconv

import (
	"regexp"
	"strings"
)

// HeadingLevelPrefixHeuristics port (pdf-port/04-toplevel-heuristics.md
// "HeadingLevelPrefixHeuristics" section). Only the pieces docx-port/01 §3.1
// and §4 need: ClassifyPrefixKey, NaturalLevelForTitle,
// IsLeadingPriorityMarkerHierarchyHeading.

const HeuristicHeadingLevel = 2

type prefixDef struct {
	key          string
	re           *regexp.Regexp
	boundaryDot  bool // true: apply numericBoundaryOK(disallowDotOrDash=true)
	boundaryDash bool // true: apply numericBoundaryOK(disallowDotOrDash=false) (DOT-only case)
}

// PREFIX_DEFS, in matching-order (pdf-port/04 table under
// "HeadingLevelPrefixHeuristics"). The five negative-lookahead patterns are
// pre-truncated at the boundary-check point per the Go regexp workaround.
var prefixDefs = []prefixDef{
	{key: "TITLE_CHAPTER_ONE", re: regexp.MustCompile(`^第\s*[` + cnDigits + `\d]+\s*章.*`)},
	{key: "TITLE_CHAPTER_TOW", re: regexp.MustCompile(`^第\s*[` + cnDigits + `\d]+\s*节.*`)},
	{key: "TITLE_CHAPTER_THREE", re: regexp.MustCompile(`^第\s*[` + cnDigits + `\d]+\s*纲.*`)},
	{key: "TITLE_CHAPTER_FOUR", re: regexp.MustCompile(`^第\s*[` + cnDigits + `\d]+\s*目.*`)},
	{key: "TITLE_CHAPTER_FIVE", re: regexp.MustCompile(`^第\s*[` + cnDigits + `\d]+\s*条.*`)},
	{key: "TITLE_CN_PAREN", re: regexp.MustCompile(`^[（(]\s*[一二三四五六七八九十百千万]+\s*[)）].*`)},
	{key: "TITLE_CN_NUM", re: regexp.MustCompile(`^([一二三四五六七八九十百千万]+)[、．.\s].*`)},
	{key: "TITLE_NUM_FIVE", re: regexp.MustCompile(`^(\d+(?:\.\d+){4})(?:[.．])?`), boundaryDot: true},
	{key: "TITLE_NUM_FOUR", re: regexp.MustCompile(`^(\d+(?:\.\d+){3})(?:[.．])?`), boundaryDot: true},
	{key: "TITLE_NUM_THREE", re: regexp.MustCompile(`^(\d+(?:\.\d+){2})(?:[.．])?`), boundaryDot: true},
	{key: "TITLE_NUM_TOW", re: regexp.MustCompile(`^(\d+(?:\.\d+){1})(?:[.．])?`), boundaryDot: true},
	{key: "TITLE_NUM_DOT", re: regexp.MustCompile(`^(\d+)[.．]`), boundaryDash: true},
	{key: "TITLE_NUM_DUNHAO", re: regexp.MustCompile(`^(\d+)、\s*.*`)},
	{key: "TITLE_NUM_SUFFIX", re: regexp.MustCompile(`^(\d+)[)）]\s*.*`)},
	{key: "TITLE_NUM_PAREN", re: regexp.MustCompile(`^[（(]\s*(\d+)\s*[)）]\s*.*`)},
	{key: "TITLE_ROMAN", re: regexp.MustCompile(`(?i)^([IVXLCDM]+)\.\s*.*`)},
	{key: "TITLE_ALPHA", re: regexp.MustCompile(`^([A-Za-z])[.．]\s*.*`)},
}

// classifyPrefixKeyOnNormalized ports
// HeadingLevelPrefixHeuristics.classifyPrefixKeyOnNormalized.
func classifyPrefixKeyOnNormalized(norm string) string {
	if IsBlank(norm) {
		return ""
	}
	for _, def := range prefixDefs {
		if def.boundaryDot || def.boundaryDash {
			loc := def.re.FindStringIndex(norm)
			if loc == nil || loc[0] != 0 {
				continue
			}
			if !numericBoundaryOK(norm, loc[1], def.boundaryDot) {
				continue
			}
			return def.key
		}
		if def.re.MatchString(norm) {
			return def.key
		}
	}
	return ""
}

// ClassifyPrefixKey ports HeadingLevelPrefixHeuristics.classifyPrefixKey.
func ClassifyPrefixKey(title string) string {
	if IsBlank(title) {
		return ""
	}
	return classifyPrefixKeyOnNormalized(normalizeForHeadingPrefixMatch(title))
}

var leadingPriorityMarkersRe = regexp.MustCompile(`^[★☆●○■□►▶◆◇※▪▸√✓✔]+`)

func stripLeadingPriorityMarkers(normalized string) string {
	t := normalized
	for {
		prev := t
		t = leadingPriorityMarkersRe.ReplaceAllString(t, "")
		t = stripEdgeWhitespaceOnly(t)
		if t == prev {
			return t
		}
	}
}

func stripEdgeWhitespaceOnly(s string) string {
	return strings.TrimSpace(s)
}

func isListLikePatternKey(key string) bool {
	switch key {
	case "TITLE_NUM_DOT", "TITLE_NUM_DUNHAO", "TITLE_NUM_SUFFIX", "TITLE_NUM_PAREN",
		"TITLE_ROMAN", "TITLE_ALPHA", "TITLE_NUM_TOW", "TITLE_NUM_THREE",
		"TITLE_NUM_FOUR", "TITLE_NUM_FIVE":
		return true
	}
	return false
}

func isHierarchyTitlePrefix(norm string) bool {
	key := classifyPrefixKeyOnNormalized(norm)
	return key != "" && !isListLikePatternKey(key)
}

// normalizeForHeadingPrefixMatch ports
// HeadingLevelPrefixHeuristics.normalizeForHeadingPrefixMatch.
func normalizeForHeadingPrefixMatch(title string) string {
	normalized := NormalizeLine(title)
	stripped := stripLeadingPriorityMarkers(normalized)
	if stripped != normalized && isHierarchyTitlePrefix(stripped) {
		return stripped
	}
	return normalized
}

// IsLeadingPriorityMarkerHierarchyHeading ports
// HeadingLevelPrefixHeuristics.isLeadingPriorityMarkerHierarchyHeading.
func IsLeadingPriorityMarkerHierarchyHeading(line string) bool {
	normalized := NormalizeLine(line)
	stripped := stripLeadingPriorityMarkers(normalized)
	return stripped != normalized && isHierarchyTitlePrefix(stripped)
}

// NaturalLevelForPatternKey ports
// HeadingLevelPrefixHeuristics.naturalLevelForPatternKey.
func NaturalLevelForPatternKey(patternKey string) int {
	switch patternKey {
	case "":
		return 0
	case "TITLE_CHAPTER_ONE":
		return 1
	case "TITLE_CHAPTER_TOW", "TITLE_CHAPTER_THREE", "TITLE_CHAPTER_FOUR", "TITLE_CHAPTER_FIVE":
		return 2
	case "TITLE_CN_NUM", "TITLE_CN_PAREN":
		return 3
	case "TITLE_NUM_TOW":
		return 3
	case "TITLE_NUM_DOT", "TITLE_NUM_DUNHAO", "TITLE_NUM_SUFFIX", "TITLE_NUM_PAREN",
		"TITLE_ROMAN", "TITLE_ALPHA", "TITLE_NUM_THREE", "TITLE_NUM_FOUR", "TITLE_NUM_FIVE":
		return 4
	default:
		return 0
	}
}

// NaturalLevelForTitle ports HeadingLevelPrefixHeuristics.naturalLevelForTitle.
func NaturalLevelForTitle(title string) int {
	key := ClassifyPrefixKey(title)
	if key == "" {
		return 0
	}
	return NaturalLevelForPatternKey(key)
}

// patternCanonicalPriority mirrors HeadingLevelPrefixHeuristics.PATTERN_CANONICAL_PRIORITY
// (lower value = higher priority; unlisted keys default to 100).
var patternCanonicalPriority = map[string]int{
	"TITLE_CHAPTER_ONE":   1,
	"TITLE_CHAPTER_TOW":   2,
	"TITLE_CHAPTER_THREE": 3,
	"TITLE_CHAPTER_FOUR":  4,
	"TITLE_CHAPTER_FIVE":  5,
	"TITLE_CN_PAREN":      10,
	"TITLE_CN_NUM":        11,
	"TITLE_NUM_TOW":       20,
	"TITLE_NUM_THREE":     21,
	"TITLE_NUM_FOUR":      22,
	"TITLE_NUM_FIVE":      23,
	"TITLE_NUM_DOT":       30,
	"TITLE_NUM_DUNHAO":    31,
	"TITLE_NUM_SUFFIX":    32,
	"TITLE_NUM_PAREN":     33,
	"TITLE_ROMAN":         40,
	"TITLE_ALPHA":         41,
}

func priorityForPatternKey(pk string) int {
	if v, ok := patternCanonicalPriority[pk]; ok {
		return v
	}
	return 100
}

// pickCanonicalPatternForLevel ports
// HeadingLevelPrefixHeuristics.pickCanonicalPatternForLevel.
func pickCanonicalPatternForLevel(atLevel []*HeadingHit, markdownLevel int) (string, bool) {
	counts := map[string]int{}
	firstLine := map[string]int{}
	for _, h := range atLevel {
		pk := ClassifyPrefixKey(h.TitleRaw)
		if pk == "" {
			continue
		}
		counts[pk]++
		if fl, ok := firstLine[pk]; !ok || h.LineIndex < fl {
			firstLine[pk] = h.LineIndex
		}
	}
	if len(counts) == 0 {
		return "", false
	}
	if markdownLevel == 1 {
		_, hasChapterOne := counts["TITLE_CHAPTER_ONE"]
		_, hasCnNum := counts["TITLE_CN_NUM"]
		_, hasCnParen := counts["TITLE_CN_PAREN"]
		if hasChapterOne && (hasCnNum || hasCnParen) {
			return "TITLE_CHAPTER_ONE", true
		}
	}
	var best string
	bestSet := false
	for pk := range counts {
		if !bestSet {
			best = pk
			bestSet = true
			continue
		}
		if counts[pk] > counts[best] {
			best = pk
			continue
		}
		if counts[pk] < counts[best] {
			continue
		}
		pp, bp := priorityForPatternKey(pk), priorityForPatternKey(best)
		if pp != bp {
			if pp < bp {
				best = pk
			}
			continue
		}
		if firstLine[pk] > firstLine[best] {
			best = pk
		}
	}
	return best, bestSet
}

// keepCanonicalMismatchAtLevel ports
// HeadingLevelPrefixHeuristics.keepCanonicalMismatchAtLevel.
func keepCanonicalMismatchAtLevel(pk, required string, level int) bool {
	if pk != "TITLE_CHAPTER_ONE" || level != 1 || required == "" {
		return false
	}
	switch {
	case strings.HasPrefix(required, "TITLE_NUM_"):
		return true
	case required == "TITLE_NUM_DOT":
		return true
	case required == "TITLE_CN_NUM":
		return true
	case required == "TITLE_CN_PAREN":
		return true
	}
	return false
}

// applyLevelPrefixConsistency ports
// HeadingLevelPrefixHeuristics.applyLevelPrefixConsistency. demoteFn mirrors
// the Java private demoteHeadingLine(lines, hit) — the caller passes
// demoteMarkdownHeadingLineToPlain bound to lines.
func applyLevelPrefixConsistency(lines []string, hits []*HeadingHit, demoteFn func(lines []string, lineIndex int)) []*HeadingHit {
	if lines == nil || len(hits) == 0 {
		return hits
	}
	working := append([]*HeadingHit(nil), hits...)
	changed := true
	for changed {
		changed = false
		for lv := 1; lv <= 6; lv++ {
			var atLevel []*HeadingHit
			for _, h := range working {
				if h.Level == lv {
					atLevel = append(atLevel, h)
				}
			}
			if len(atLevel) < 2 {
				continue
			}
			required, ok := pickCanonicalPatternForLevel(atLevel, lv)
			if !ok {
				continue
			}
			distinct := map[string]bool{}
			for _, h := range atLevel {
				if pk := ClassifyPrefixKey(h.TitleRaw); pk != "" {
					distinct[pk] = true
				}
			}
			if len(distinct) <= 1 {
				continue
			}
			var next []*HeadingHit
			for _, h := range working {
				if h.Level != lv {
					next = append(next, h)
					continue
				}
				pk := ClassifyPrefixKey(h.TitleRaw)
				if pk == required {
					next = append(next, h)
					continue
				}
				if pk == "" {
					// A heading with no recognized numbering pattern at all
					// (e.g. an unnumbered cover-page title or section label
					// promoted to this level purely by visual style — see
					// pdfconv/heading_geom.go's isVisualOnlyHeading) has no
					// pattern to reconcile against `required` in the first
					// place. This loop's purpose is bringing headings that
					// share a level into agreement on ONE canonical
					// numbering pattern (e.g. don't mix "第一章" with "一、"
					// at the same level) — a heading outside that
					// disagreement entirely must not be swept into the
					// `default: demote` branch below just for sharing a
					// level with headings that do have a pattern.
					next = append(next, h)
					continue
				}
				natural := NaturalLevelForPatternKey(pk)
				switch {
				case pk == "TITLE_CHAPTER_ONE":
					next = append(next, h)
				case natural > 0 && natural != lv:
					h.Level = natural
					next = append(next, h)
					changed = true
				case natural > 0 && natural == lv && keepCanonicalMismatchAtLevel(pk, required, lv):
					next = append(next, h)
				default:
					demoteFn(lines, h.LineIndex)
					changed = true
				}
			}
			working = next
		}
	}
	sort_HeadingHitsByLine(working)
	return working
}
