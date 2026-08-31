package pdfconv

import (
	"regexp"
	"strings"
	"unicode"
)

// MarkdownLineClassifier port (pdf-port/06-mpp-merge-cleanup.md
// "MarkdownLineClassifier" section) — only LooksLikePreformattedBlock is
// needed by docx-port/01 (§6, §8.1). classify()/classifyText() are ported in
// full per the spec; two of their dependencies
// (MarkdownWeakMergeHeuristics.isListLikeLine and
// MarkdownBodyMergeStage.isChineseDateLine) belong to files outside this
// port's read scope (only pdf-port/04, 05, 06 were read, and those two
// symbols are defined in sections not included in the read range) — they are
// reimplemented here as small best-effort approximations, flagged as an open
// item in the implementation report.

type lineKind int

const (
	kindBlank lineKind = iota
	kindFence
	kindHeading
	kindListItem
	kindDate
	kindTable
	kindQuoteOrRule
	kindPreformatted
	kindNaturalText
)

var (
	shellFlagRe        = regexp.MustCompile(`(^|\s)-{1,2}[A-Za-z0-9][A-Za-z0-9_-]*(?:[=\s]|$)`)
	pathOrDeviceRe     = regexp.MustCompile(`(^|\s)(?:/[^\s]+|[A-Za-z]:\\[^\s]+)`)
	keyValueRe         = regexp.MustCompile(`(^|\s)[A-Za-z_][A-Za-z0-9_{}.-]*(?:==|=|:=|=>).+`)
	ipOrPortRe         = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b`)
	versionPackageRe   = regexp.MustCompile(`\b[A-Za-z0-9_+.-]+-\d+(?:\.\d+){1,}[A-Za-z0-9_+.-]*\b`)
	hashOrUUIDRe       = regexp.MustCompile(`\b(?:[0-9a-fA-F]{16,}|[0-9a-fA-F]{8}-[0-9a-fA-F-]{13,})\b`)
	permissionLineRe   = regexp.MustCompile(`^[bcdlps-]?[rwx-]{9}[+.@]?\s+\d+\s+\S+\s+\S+\s+.*`)
	logLineRe          = regexp.MustCompile(`(?i)^(?:\d{4}[-/]\d{2}[-/]\d{2}|\d{2}:\d{2}:\d{2}|\[?(?:TRACE|DEBUG|INFO|WARN|ERROR|FATAL)\]?).*`)
	stackLineRe        = regexp.MustCompile(`^(?:at\s+[\w.$]+\(.+\)|Traceback \(most recent call last\):|File "[^"]+", line \d+.*|Caused by: .*)$`)
	sqlStartRe         = regexp.MustCompile(`(?i)^(?:select|with|insert|update|delete|create|alter|drop|merge|from|where|join|left\s+join|right\s+join|inner\s+join|group\s+by|order\s+by|having|values|set)\b.*`)
	codeStartRe        = regexp.MustCompile(`^(?:import|package|class|interface|enum|public|private|protected|static|final|def|function|func|if|else|for|while|switch|case|return|try|catch|throw|const|let|var)\b.*`)
	configLineRe       = regexp.MustCompile(`^(?:[A-Za-z_][A-Za-z0-9_.-]*\s*[:=]\s*.+|[A-Za-z_][A-Za-z0-9_.-]*\s*:\s*|[A-Z_{}][A-Z0-9_{}.-]*(?:==|=|\+=).+|<[/A-Za-z][^>]*>|[{}\[\]],?)$`)
	naturalPrefixRe    = regexp.MustCompile(`^(?:执行|通过|使用|可以|需要|例如|如果|当|为了|运行|输入|查看|配置|修改|将|把|在|然后|请|The\b|This\b|You\b|We\b|It\b).*`)
	commandLeadRe      = regexp.MustCompile(`^(?:sudo\s+)?[A-Za-z_][A-Za-z0-9_.+-]{1,40}(?:\s+.+)?$`)
	columnGapRe        = regexp.MustCompile(`\S {2,}\S`)
	sentenceFinalRe    = regexp.MustCompile(`.*[。！？!?]$`)
	proseSentenceEndRe = regexp.MustCompile(`.*[。！？，、.!?,]$`)
	tableSepRe         = regexp.MustCompile(`^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)+\|?$`)
	horizontalRuleRe   = regexp.MustCompile(`^\s*(?:-{3,}|\*{3,}|_{3,})\s*$`)
	headingLineFullRe  = regexp.MustCompile(`^(#{1,6})[\s\x{3000}]+(.+?)[\s\x{3000}]*$`)
)

var proseFunctionWordsCJK = []string{
	"的", "了", "是", "在", "和", "与", "但", "因为", "所以", "如果", "可以", "需要", "通过", "执行",
	"使用", "将", "把", "然后", "请", "当", "为了", "例如", "这个", "那个", "我们", "你们", "他们",
	"并且", "或者", "虽然", "不过", "由于", "根据", "对于", "关于", "建议", "应该", "可能", "一般",
	"通常", "主要", "还是",
}

var proseFunctionWordsENRe = regexp.MustCompile(`(?i)\b(?:the|is|are|was|were|of|and|or|to|in|on|at|for|with|this|that|these|those|it|be|as|by|an)\b`)

// weakMergeBulletMarkerRe is the same bullet-marker heuristic this file
// used before structure_rules.go's IsListItem port replaced the old
// placeholder bulletMarkerRe with a literal (broader) LIST_BULLET port;
// kept local here since isListLikeLineApprox targets a different,
// still-out-of-scope Java class (MarkdownWeakMergeHeuristics.isListLikeLine)
// and should not silently inherit LIST_BULLET's broadened match.
var weakMergeBulletMarkerRe = regexp.MustCompile(`^[-+*•●○■□►→]\s+\S`)

// isListLikeLineApprox approximates MarkdownWeakMergeHeuristics.isListLikeLine
// (out of this port's read scope — see file header note).
func isListLikeLineApprox(t string) bool {
	if t == "" {
		return false
	}
	if weakMergeBulletMarkerRe.MatchString(t) {
		return true
	}
	key := ClassifyPrefixKey(t)
	return key != "" && isListLikePatternKey(key)
}

var chineseDateLineRe = regexp.MustCompile(`^[0-9０-９]{4}\s*年\s*[0-9０-９]{1,2}\s*月\s*[0-9０-９]{1,2}\s*日\s*.*$`)

// isChineseDateLineApprox approximates MarkdownBodyMergeStage.isChineseDateLine
// (out of this port's read scope — see file header note).
func isChineseDateLineApprox(t string) bool {
	return chineseDateLineRe.MatchString(strings.TrimSpace(t))
}

func nonBlankAt(lines []string, index, direction, hop int) *string {
	found := 0
	i := index
	for {
		i += direction
		if i < 0 || i >= len(lines) {
			return nil
		}
		t := strings.TrimSpace(lines[i])
		if t != "" {
			found++
			if found == hop {
				return &t
			}
		}
	}
}

func classifyAt(lines []string, index int) lineKind {
	if lines == nil || index < 0 || index >= len(lines) {
		return kindBlank
	}
	prev := nonBlankAt(lines, index, -1, 1)
	prev2 := nonBlankAt(lines, index, -1, 2)
	next := nonBlankAt(lines, index, 1, 1)
	next2 := nonBlankAt(lines, index, 1, 2)
	return classifyText(lines[index], prev, prev2, next, next2)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func classifyText(line string, prev, prev2, next, next2 *string) lineKind {
	t := strings.TrimSpace(line)
	if t == "" {
		return kindBlank
	}
	if strings.HasPrefix(t, "```") {
		return kindFence
	}
	if headingLineFullRe.MatchString(t) || strings.HasPrefix(t, "#") {
		return kindHeading
	}
	if isListLikeLineApprox(t) || isNumericOutlineBoundaryLine(t) {
		return kindListItem
	}
	if isChineseDateLineApprox(t) {
		return kindDate
	}
	if strings.HasPrefix(t, "|") || tableSepRe.MatchString(t) {
		return kindTable
	}
	if strings.HasPrefix(t, ">") || horizontalRuleRe.MatchString(t) {
		return kindQuoteOrRule
	}
	if looksPreformatted(t, deref(prev), deref(prev2), deref(next), deref(next2)) {
		return kindPreformatted
	}
	return kindNaturalText
}

var numericOutlineBoundaryRe = regexp.MustCompile(`^\d+(?:[.．]\d+)+(?:[.．])?`)
var ipv4HostPortPrefixRe = regexp.MustCompile(`^((?:25[0-5]|2[0-4]\d|1?\d{1,2})(?:\.(?:25[0-5]|2[0-4]\d|1?\d{1,2})){3}):\d+.*`)

func isNumericOutlineBoundaryLine(t string) bool {
	if t == "" {
		return false
	}
	s := strings.TrimSpace(t)
	if ipv4HostPortPrefixRe.MatchString(s) {
		return false
	}
	loc := numericOutlineBoundaryRe.FindStringIndex(s)
	if loc == nil || loc[0] != 0 {
		return false
	}
	return numericBoundaryOK(s, loc[1], true)
}

func isCjkRuneNarrow(r rune) bool {
	return unicode.Is(unicode.Han, r)
}

func cjkRatio(s string) float64 {
	total, cjk := 0, 0
	for _, r := range s {
		if isWhitespaceRune(r) {
			continue
		}
		total++
		if isCjkRuneNarrow(r) {
			cjk++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(cjk) / float64(total)
}

func tokenize(s string) []string {
	return strings.Fields(strings.TrimSpace(s))
}

func tokenCount(s string) int { return len(tokenize(s)) }

var asciiTokenRe = regexp.MustCompile(`^[\x00-\x7F]+$`)

func asciiTokenRatio(s string) float64 {
	toks := tokenize(s)
	if len(toks) == 0 {
		return 0
	}
	n := 0
	for _, tok := range toks {
		if asciiTokenRe.MatchString(tok) {
			n++
		}
	}
	return float64(n) / float64(len(toks))
}

const symbolCharSet = "-_/:=\"'.,|<>[]{}()$*+@#;&"

func symbolRatio(s string) float64 {
	total, sym := 0, 0
	for _, r := range s {
		if isWhitespaceRune(r) {
			continue
		}
		total++
		if strings.ContainsRune(symbolCharSet, r) {
			sym++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(sym) / float64(total)
}

func indexOfWhitespacePrecededMarker(s, marker string) int {
	idx := strings.Index(s, marker)
	for idx > 0 {
		r, size := utf8DecodeLastRuneInPrefix(s, idx)
		_ = size
		if isWhitespaceRune(r) {
			return idx
		}
		next := strings.Index(s[idx+1:], marker)
		if next < 0 {
			return -1
		}
		idx = idx + 1 + next
	}
	return -1
}

func utf8DecodeLastRuneInPrefix(s string, idx int) (rune, int) {
	// decode the rune immediately preceding byte offset idx
	r := []rune(s[:idx])
	if len(r) == 0 {
		return 0, 0
	}
	last := r[len(r)-1]
	return last, len(string(last))
}

func commentStrippedPrefix(s string) string {
	idx := indexOfWhitespacePrecededMarker(s, "#")
	dashIdx := indexOfWhitespacePrecededMarker(s, "--")
	if dashIdx >= 0 && (idx < 0 || dashIdx < idx) {
		idx = dashIdx
	}
	if idx > 0 {
		return strings.TrimSpace(s[:idx])
	}
	return s
}

func columnGapCount(s string) int {
	count := 0
	from := 0
	for from < len(s) {
		loc := columnGapRe.FindStringIndex(s[from:])
		if loc == nil {
			break
		}
		count++
		matchEndAbs := from + loc[1]
		// step back one rune before continuing, to allow overlap detection.
		_, size := utf8DecodeLastRuneInPrefix(s, matchEndAbs)
		if size == 0 {
			size = 1
		}
		from = matchEndAbs - size
		if from <= 0 {
			from = from + 1
		}
	}
	return count
}

func shapeScore(t string) int {
	if t == "" {
		return 0
	}
	s := strings.TrimSpace(t)
	core := commentStrippedPrefix(s)
	score := 0
	if sqlStartRe.MatchString(s) {
		score += 4
	}
	if codeStartRe.MatchString(s) {
		score += 4
	}
	if configLineRe.MatchString(s) {
		score += 4
	}
	if permissionLineRe.MatchString(s) {
		score += 5
	}
	if logLineRe.MatchString(s) {
		score += 4
	}
	if stackLineRe.MatchString(s) {
		score += 5
	}
	if commandLeadRe.MatchString(core) && asciiTokenRatio(core) >= 0.65 {
		score += 2
	}
	if ratio := asciiTokenRatio(core); ratio >= 0.95 {
		if n := tokenCount(core); n >= 2 && n <= 10 {
			score += 2
		}
	}
	if shellFlagRe.MatchString(s) {
		score += 3
	}
	if pathOrDeviceRe.MatchString(s) {
		score += 3
	}
	if keyValueRe.MatchString(s) {
		score += 3
	}
	if ipOrPortRe.MatchString(s) {
		score += 2
	}
	if versionPackageRe.MatchString(s) {
		score += 2
	}
	if hashOrUUIDRe.MatchString(s) {
		score += 2
	}
	if strings.ContainsAny(s, "|><") || strings.Contains(s, "&&") || strings.Contains(s, "||") {
		score += 2
	}
	if strings.Contains(s, "::") || strings.Contains(s, "->") || strings.Contains(s, "=>") || strings.Contains(s, "==") || strings.Contains(s, "!=") {
		score += 2
	}
	if strings.HasSuffix(core, "{") || strings.HasSuffix(core, ";") || strings.HasSuffix(core, "\\") || strings.HasSuffix(core, ",") {
		score += 2
	}
	if symbolRatio(core) >= 0.18 && asciiTokenRatio(core) >= 0.70 {
		score += 2
	}
	if columnGapCount(s) >= 2 {
		score += 4
	}
	return score
}

func naturalLanguageScore(t string) int {
	score := 0
	if naturalPrefixRe.MatchString(t) {
		score += 3
	}
	core := commentStrippedPrefix(t)
	if cjkRatio(core) >= 0.30 {
		score += 2
	}
	if strings.ContainsAny(core, "。！？；，") && symbolRatio(core) < 0.12 {
		score += 2
	}
	return score
}

func endsWithTerminalPunctuationRune(x string) bool {
	return EndsWithTerminalPunctuation(x)
}

func isWeakPreformattedBridgeLine(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	x := strings.TrimSpace(s)
	if runeLen(x) > 24 {
		return false
	}
	if cjkRatio(x) > 0 {
		return false
	}
	return !endsWithTerminalPunctuationRune(x)
}

func contextIntroducesPreformatted(prev string) bool {
	if strings.TrimSpace(prev) == "" {
		return false
	}
	p := strings.ToLower(NormalizeLine(prev))
	if strings.HasSuffix(p, "如下") || strings.HasSuffix(p, "如下：") || strings.HasSuffix(p, "如下:") {
		return true
	}
	for _, kw := range []string{"以下命令", "下列命令", "执行命令", "启动", "安装", "服务", "目标盘", "配置文件", "sql", "代码", "日志", "输出", "示例"} {
		if strings.Contains(p, kw) {
			return true
		}
	}
	return false
}

func samePreformattedFamily(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	left, right := strings.TrimSpace(a), strings.TrimSpace(b)
	if sqlStartRe.MatchString(left) && sqlStartRe.MatchString(right) {
		return true
	}
	if permissionLineRe.MatchString(left) && permissionLineRe.MatchString(right) {
		return true
	}
	if configLineRe.MatchString(left) && configLineRe.MatchString(right) {
		return true
	}
	if ipOrPortRe.MatchString(left) && ipOrPortRe.MatchString(right) {
		return true
	}
	if asciiTokenRatio(left) >= 0.75 && asciiTokenRatio(right) >= 0.75 {
		if shellFlagRe.MatchString(left) || shellFlagRe.MatchString(right) || pathOrDeviceRe.MatchString(left) || pathOrDeviceRe.MatchString(right) {
			return true
		}
	}
	return false
}

func looksPreformatted(t, prev, prev2, next, next2 string) bool {
	score := shapeScore(t)
	if score <= 0 {
		return false
	}
	if naturalLanguageScore(t) >= 3 && score < 6 {
		return false
	}
	if sqlStartRe.MatchString(t) || codeStartRe.MatchString(t) || permissionLineRe.MatchString(t) || stackLineRe.MatchString(t) || logLineRe.MatchString(t) {
		return true
	}
	contextual := contextIntroducesPreformatted(prev) ||
		shapeScore(next) >= 4 ||
		samePreformattedFamily(t, next) ||
		samePreformattedFamily(prev, t) ||
		(isWeakPreformattedBridgeLine(next) && shapeScore(next2) >= 4) ||
		(isWeakPreformattedBridgeLine(prev) && shapeScore(prev2) >= 4)
	return score >= 6 || (score >= 2 && contextual)
}

const structuralBridgeMinPreformatted = 3
const proseRatioCeiling = 0.15
const structuredRatioFloor = 0.5
const proseRatioNearZero = 0.05
const proseRatioNearZeroMinLines = 3

func containsProseFunctionWord(t string) bool {
	for _, w := range proseFunctionWordsCJK {
		if strings.Contains(t, w) {
			return true
		}
	}
	return proseFunctionWordsENRe.MatchString(t)
}

func looksLikeProseLine(t string) bool {
	if strings.TrimSpace(t) == "" {
		return false
	}
	if sentenceFinalRe.MatchString(t) {
		return true
	}
	return containsProseFunctionWord(t) && symbolRatio(t) < 0.12
}

func isBridgeCandidate(kind lineKind, t string, allowStructuralKinds bool) bool {
	if kind == kindNaturalText {
		return true
	}
	if !allowStructuralKinds {
		return false
	}
	if kind != kindListItem && kind != kindQuoteOrRule {
		return false
	}
	if strings.HasPrefix(t, ">") {
		return false
	}
	return !proseSentenceEndRe.MatchString(t)
}

func isBridgeableRun(lines []string, nonBlankIdx []int, fromInclusive, toExclusive int, allowStructuralKinds bool) bool {
	for k := fromInclusive; k < toExclusive; k++ {
		i := nonBlankIdx[k]
		t := strings.TrimSpace(lines[i])
		if !isBridgeCandidate(classifyAt(lines, i), t, allowStructuralKinds) {
			return false
		}
		if ClassifyPrefixKey(t) != "" {
			return false
		}
	}
	return true
}

func bridgeWeakGaps(lines []string, nonBlankIdx []int, accepted []bool, allowStructuralKinds bool) {
	n := len(accepted)
	i := 0
	for i < n {
		if accepted[i] {
			i++
			continue
		}
		start := i
		for i < n && !accepted[i] {
			i++
		}
		end := i
		anchored := start > 0 || end < n
		if anchored && isBridgeableRun(lines, nonBlankIdx, start, end, allowStructuralKinds) {
			for k := start; k < end; k++ {
				accepted[k] = true
			}
		}
	}
}

func looksStructuredByProseExclusion(lines []string, nonBlankIdx []int) bool {
	total := len(nonBlankIdx)
	if total == 0 {
		return false
	}
	proseLines, structuredLines := 0, 0
	for _, i := range nonBlankIdx {
		t := strings.TrimSpace(lines[i])
		kind := classifyAt(lines, i)
		hashPrefixed := kind == kindHeading && strings.HasPrefix(t, "#")
		if !hashPrefixed && looksLikeProseLine(t) {
			proseLines++
		}
		if kind == kindPreformatted || hashPrefixed || isBridgeCandidate(kind, t, true) {
			structuredLines++
		}
	}
	proseRatio := float64(proseLines) / float64(total)
	if proseRatio <= proseRatioNearZero && total >= proseRatioNearZeroMinLines {
		return true
	}
	structuredRatio := float64(structuredLines) / float64(total)
	return proseRatio <= proseRatioCeiling && structuredRatio >= structuredRatioFloor
}

// LooksLikePreformattedBlock ports MarkdownLineClassifier.looksLikePreformattedBlock.
func LooksLikePreformattedBlock(lines []string) bool {
	if lines == nil {
		return false
	}
	var nonBlankIdx []int
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			nonBlankIdx = append(nonBlankIdx, i)
		}
	}
	if len(nonBlankIdx) < 2 {
		return false
	}
	accepted := make([]bool, len(nonBlankIdx))
	preformattedCount := 0
	for k, i := range nonBlankIdx {
		t := strings.TrimSpace(lines[i])
		kind := classifyAt(lines, i)
		if kind == kindPreformatted {
			accepted[k] = true
			preformattedCount++
		} else if kind == kindHeading && strings.HasPrefix(t, "#") {
			accepted[k] = true
		}
	}
	bridgeWeakGaps(lines, nonBlankIdx, accepted, preformattedCount >= structuralBridgeMinPreformatted)
	allAccepted := true
	for _, a := range accepted {
		if !a {
			allAccepted = false
			break
		}
	}
	if allAccepted && preformattedCount >= 2 {
		return true
	}
	return looksStructuredByProseExclusion(lines, nonBlankIdx)
}
