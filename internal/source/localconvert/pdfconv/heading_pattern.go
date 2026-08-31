package pdfconv

// MarkdownTitlePattern port (pdf-port/05-mpp-heading-stack.md
// "MarkdownTitlePattern" section): the 17-entry numbering-scheme enum used by
// MarkdownHeadingStage to recognize plain-text lines as heading candidates
// purely from their numbering prefix (no font-size/bold/centering signal —
// that's docx_heading.go's job upstream). This is what lets a line like
// "一、K8S 架构" that failed docx_heading.go's bold+>=14pt gate still get
// promoted to a heading by the MPP pipeline, matching FileView's real
// behavior (ConvertWorker.java routes ALL WordToMarkdown output through this
// same MPP stage — see docx-port/01 §12).

import (
	"regexp"
	"strconv"
	"strings"
)

// TitlePattern mirrors the Java MarkdownTitlePattern enum. -1 (titlePatternNone)
// is the "no match" sentinel returned by MatchFirst.
type TitlePattern int

const (
	titlePatternNone TitlePattern = -1

	TitleChapterOne TitlePattern = iota - 1
	TitleChapterTow
	TitleChapterThree
	TitleChapterFour
	TitleChapterFive
	TitleCnParen
	TitleCnNum
	TitleNumFive
	TitleNumFour
	TitleNumThree
	TitleNumTow
	TitleNumDot
	TitleNumDunhao
	TitleNumSuffix
	TitleNumParen
	TitleRoman
	TitleAlpha
)

// titlePatternDef bundles the per-pattern regex (with a single capturing
// group over the numbering token, used by ParseIndex), the Go-regexp
// lookahead workaround flags (see pdf-port/05 §"Go regexp 兼容性预警" items
// 5/6), and the two per-pattern facts (depth, supportListLike) from the Java
// enum's instance methods.
type titlePatternDef struct {
	key             string
	re              *regexp.Regexp
	boundaryDot     bool // TITLE_NUM_TOW..FIVE: reject trailing digit/dot/dash
	boundaryDash    bool // TITLE_NUM_DOT: reject trailing digit/dash (dot OK)
	supportListLike bool
	depth           int
}

var titlePatternDefs = map[TitlePattern]titlePatternDef{
	TitleChapterOne:   {key: "TITLE_CHAPTER_ONE", re: regexp.MustCompile(`^第\s*([一二三四五六七八九十百千万零\d]+)\s*章`), depth: 1},
	TitleChapterTow:   {key: "TITLE_CHAPTER_TOW", re: regexp.MustCompile(`^第\s*([一二三四五六七八九十百千万零\d]+)\s*节`), depth: 1},
	TitleChapterThree: {key: "TITLE_CHAPTER_THREE", re: regexp.MustCompile(`^第\s*([一二三四五六七八九十百千万零\d]+)\s*纲`), depth: 1},
	TitleChapterFour:  {key: "TITLE_CHAPTER_FOUR", re: regexp.MustCompile(`^第\s*([一二三四五六七八九十百千万零\d]+)\s*目`), depth: 1},
	TitleChapterFive:  {key: "TITLE_CHAPTER_FIVE", re: regexp.MustCompile(`^第\s*([一二三四五六七八九十百千万零\d]+)\s*条`), depth: 1},
	TitleCnParen:      {key: "TITLE_CN_PAREN", re: regexp.MustCompile(`^[（(]\s*([一二三四五六七八九十百千万]+)\s*[)）]`), depth: 1},
	TitleCnNum:        {key: "TITLE_CN_NUM", re: regexp.MustCompile(`^([一二三四五六七八九十百千万]+)[、．.\s]`), depth: 1},
	TitleNumFive:      {key: "TITLE_NUM_FIVE", re: regexp.MustCompile(`^(\d+(?:\.\d+){4})(?:[.．])?`), boundaryDot: true, supportListLike: true, depth: 5},
	TitleNumFour:      {key: "TITLE_NUM_FOUR", re: regexp.MustCompile(`^(\d+(?:\.\d+){3})(?:[.．])?`), boundaryDot: true, supportListLike: true, depth: 4},
	TitleNumThree:     {key: "TITLE_NUM_THREE", re: regexp.MustCompile(`^(\d+(?:\.\d+){2})(?:[.．])?`), boundaryDot: true, supportListLike: true, depth: 3},
	TitleNumTow:       {key: "TITLE_NUM_TOW", re: regexp.MustCompile(`^(\d+(?:\.\d+){1})(?:[.．])?`), boundaryDot: true, supportListLike: true, depth: 2},
	TitleNumDot:       {key: "TITLE_NUM_DOT", re: regexp.MustCompile(`^(\d+)[.．]`), boundaryDash: true, supportListLike: true, depth: 1},
	TitleNumDunhao:    {key: "TITLE_NUM_DUNHAO", re: regexp.MustCompile(`^(\d+)、`), supportListLike: true, depth: 1},
	TitleNumSuffix:    {key: "TITLE_NUM_SUFFIX", re: regexp.MustCompile(`^(\d+)[)）]`), supportListLike: true, depth: 1},
	TitleNumParen:     {key: "TITLE_NUM_PAREN", re: regexp.MustCompile(`^[（(]\s*(\d+)\s*[)）]`), supportListLike: true, depth: 1},
	TitleRoman:        {key: "TITLE_ROMAN", re: regexp.MustCompile(`(?i)^([IVXLCDM]+)\.`), supportListLike: true, depth: 1},
	TitleAlpha:        {key: "TITLE_ALPHA", re: regexp.MustCompile(`^([A-Za-z])[.．]`), supportListLike: true, depth: 1},
}

// patternPriority mirrors MarkdownTitlePattern.PATTERN_PRIORITY exactly.
var patternPriority = []TitlePattern{
	TitleChapterOne, TitleChapterTow, TitleChapterThree, TitleChapterFour, TitleChapterFive,
	TitleCnParen, TitleCnNum,
	TitleNumFive, TitleNumFour, TitleNumThree, TitleNumTow,
	TitleNumDot, TitleNumDunhao, TitleNumSuffix, TitleNumParen,
	TitleRoman, TitleAlpha,
}

func (p TitlePattern) Key() string {
	if def, ok := titlePatternDefs[p]; ok {
		return def.key
	}
	return ""
}

func (p TitlePattern) Depth() int {
	if def, ok := titlePatternDefs[p]; ok {
		return def.depth
	}
	return 1
}

func (p TitlePattern) SupportListLike() bool {
	if def, ok := titlePatternDefs[p]; ok {
		return def.supportListLike
	}
	return false
}

// MatchFirst ports MarkdownTitlePattern.matchFirst / matchFirstOnNormalized.
func MatchFirst(line string) TitlePattern {
	norm := normalizeForHeadingPrefixMatch(line)
	return matchFirstOnNormalized(norm)
}

func matchFirstOnNormalized(norm string) TitlePattern {
	if IsBlank(norm) {
		return titlePatternNone
	}
	for _, p := range patternPriority {
		def := titlePatternDefs[p]
		loc := def.re.FindStringSubmatchIndex(norm)
		if loc == nil || loc[0] != 0 {
			continue
		}
		if def.boundaryDot || def.boundaryDash {
			if !numericBoundaryOK(norm, loc[1], def.boundaryDot) {
				continue
			}
		}
		return p
	}
	return titlePatternNone
}

// ParseIndex ports MarkdownTitlePattern.parseIndex.
func ParseIndex(text string, p TitlePattern) []int {
	def, ok := titlePatternDefs[p]
	if !ok {
		return nil
	}
	norm := normalizeForHeadingPrefixMatch(text)
	loc := def.re.FindStringSubmatchIndex(norm)
	if loc == nil || loc[0] != 0 || len(loc) < 4 || loc[2] < 0 {
		return nil
	}
	group1 := norm[loc[2]:loc[3]]

	switch p {
	case TitleChapterOne, TitleChapterTow, TitleChapterThree, TitleChapterFour, TitleChapterFive:
		n, ok := parseNum(group1)
		if !ok {
			return nil
		}
		return []int{n}
	case TitleCnParen, TitleCnNum:
		n, ok := ParseChineseNumber(group1)
		if !ok {
			return nil
		}
		return []int{n}
	case TitleNumTow, TitleNumThree, TitleNumFour, TitleNumFive:
		parts := strings.Split(group1, ".")
		out := make([]int, 0, len(parts))
		for _, seg := range parts {
			n, err := strconv.Atoi(seg)
			if err != nil {
				return nil
			}
			out = append(out, n)
		}
		return out
	case TitleNumDot, TitleNumDunhao, TitleNumSuffix, TitleNumParen:
		n, err := strconv.Atoi(group1)
		if err != nil {
			return nil
		}
		return []int{n}
	case TitleRoman:
		n, ok := ParseRoman(group1)
		if !ok {
			return nil
		}
		return []int{n}
	case TitleAlpha:
		r := []rune(strings.ToUpper(group1))
		if len(r) == 0 {
			return nil
		}
		return []int{int(r[0]-'A') + 1}
	}
	return nil
}

func parseNum(text string) (int, bool) {
	t := strings.TrimSpace(text)
	if t == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(t); err == nil {
		return n, true
	}
	return ParseChineseNumber(t)
}

// bodyEnumerationPatternSet mirrors MarkdownTitlePattern.isBodyEnumerationPattern.
var bodyEnumerationPatternSet = map[TitlePattern]bool{
	TitleNumDunhao: true,
	TitleNumDot:    true,
	TitleNumSuffix: true,
	TitleNumParen:  true,
	TitleCnParen:   true,
}

// IsBodyEnumerationPattern ports MarkdownTitlePattern.isBodyEnumerationPattern.
func IsBodyEnumerationPattern(p TitlePattern) bool {
	return bodyEnumerationPatternSet[p]
}

var (
	numTowThreeFourFiveBodyRe = regexp.MustCompile(`^(\d+(?:\.\d+)+)\.?\s*(.*)$`)
	numDunhaoPrefixRe         = regexp.MustCompile(`^\d+、\s*`)
	numDotPrefixRe            = regexp.MustCompile(`^\d+[.．]\s*`)
	numSuffixPrefixRe         = regexp.MustCompile(`^\d+[)）】]\s*`)
	numParenPrefixRe          = regexp.MustCompile(`^[（(]\s*\d+\s*[)）]\s*`)
	cnParenPrefixRe           = regexp.MustCompile(`^[（(]\s*[一二三四五六七八九十百千万]+\s*[)）]\s*`)
)

// StripBodyEnumerationPrefix ports MarkdownTitlePattern.stripBodyEnumerationPrefix.
func StripBodyEnumerationPrefix(text string, p TitlePattern) string {
	if text == "" {
		return ""
	}
	switch p {
	case TitleNumTow, TitleNumThree, TitleNumFour, TitleNumFive:
		if m := numTowThreeFourFiveBodyRe.FindStringSubmatch(text); m != nil {
			return strings.TrimSpace(m[2])
		}
		return strings.TrimSpace(text)
	case TitleNumDunhao:
		return strings.TrimSpace(numDunhaoPrefixRe.ReplaceAllString(text, ""))
	case TitleNumDot:
		return strings.TrimSpace(numDotPrefixRe.ReplaceAllString(text, ""))
	case TitleNumSuffix:
		return strings.TrimSpace(numSuffixPrefixRe.ReplaceAllString(text, ""))
	case TitleNumParen:
		return strings.TrimSpace(numParenPrefixRe.ReplaceAllString(text, ""))
	case TitleCnParen:
		return strings.TrimSpace(cnParenPrefixRe.ReplaceAllString(text, ""))
	default:
		return text
	}
}

// PatternKey pairs a TitlePattern with a depth, mirroring MarkdownPatternKey.
type PatternKey struct {
	Type  TitlePattern
	Depth int
}

// HeadingHit mirrors MarkdownHeadingHit.
type HeadingHit struct {
	LineIndex  int
	Level      int
	TitleRaw   string
	Slug       string
	PatternKey *PatternKey
}
