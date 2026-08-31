package pdfconv

// MarkdownHeadingStage port (pdf-port/05-mpp-heading-stack.md
// "MarkdownHeadingStage" + "流水线执行顺序" sections). This is the engine that
// promotes plain-text numbered/CN-prefixed lines ("一、K8S 架构", "3.1.1 xxx")
// to `#` headings and repairs heading nesting, independent of any
// font-size/bold/centering signal (that signal is docx_heading.go's job
// upstream of this stage, or PDF geometry rendering in the not-yet-ported
// PDF path).
//
// SCOPE NOTE (flagged, not silently decided): the full Java class also
// cross-validates against a "ChapterTocCatalog" (PDF/Word table-of-contents
// page detection) via ChapterTocHeadingValidator, and several Part-4-only
// helpers referenced by the spec (HeadingSuppressHeuristics.shouldSuppressHeadingLine,
// MarkdownStructureRules.isTitleExtractCandidateLine,
// HeadingPatternQualityHeuristics.buildInferDisqualifiedPatternKeys /
// detectMixedRecognitionPatternKeys / filterHitsAndDemoteLines,
// HeadingLevelPrefixHeuristics.applyLevelPrefixConsistency,
// ShortPhraseListRunHeuristics.detectExistingHeadingShortPhraseListRuns /
// detectPlainShortPhraseListRuns, ChapterReferenceHeuristics.isBodyChapterReference)
// were never ported in pdf-port/04's DOCX-scoped subset and are NOT
// implemented here — porting them would mean porting most of the remaining
// PDF-only Part 4 surface, which is out of this task's scope (DOCX has no
// PDF-style TOC *pages*, only a "目录" Word section, and no page-break
// induced line splitting). This file implements the self-contained core:
// candidate extraction/scoring/filtering, the scope-tree builder
// (resolveScope/pickCurrentPattern/patternConfidence), reading-order nesting
// repair, heading-sequence-consistency demotion (reusing the already-ported
// HeadingSequenceConsistencyHeuristics.DetectMarkdownLinesToDemote), pattern
// quality demotion (reusing DetectDisqualifiedPatternKeys/ClearlyFailsHeadingQuality),
// and UnmarkedParentHeadingHeuristics (misplaced-child-section demotion).
// See the implementation report for the exact list of skipped branches.

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

const (
	headingStageMaxLevel        = 4 // scope-tree recursion depth cap (not final output cap — that's 6)
	headingStageMaxHeadingLevel = 6
	confidenceThreshold         = 0.45
	siblingProtectGap           = 20
	listLikeMinRun              = 5
	bodyEnumListMinRun          = 3
	bodyEnumMinCharsAfterPrefix = 15
	listLikeMaxGap              = 3.0
	listLikeSeqQualityProtect   = 0.8
	scoreKeepThreshold          = 2
	shortPhraseListScorePenalty = 4
	holeFixMinH3                = 2
	holeFixSeqQualityMin        = 0.75
	headingStageEPS             = 1e-9
)

var headingLineRe = regexp.MustCompile(`^(#{1,6})[\s\x{3000}]+(.+?)[\s\x{3000}]*$`)

func isMarkdownHeadingLine(trimmed string) bool {
	return headingLineRe.MatchString(trimmed)
}

func headingHashCount(trimmed string) int {
	m := headingLineRe.FindStringSubmatch(trimmed)
	if m == nil {
		return 0
	}
	return len(m[1])
}

func headingTitleFromLine(raw string) string {
	t := strings.TrimSpace(raw)
	if m := headingLineRe.FindStringSubmatch(t); m != nil {
		return NormalizeLine(m[2])
	}
	return NormalizeLine(t)
}

// ---- candidate model --------------------------------------------------

type headingCandidate struct {
	id                 int
	lineID             int
	normText           string
	pattern            TitlePattern
	index              []int
	listLike           bool
	shortPhraseListRun bool
	score              int
}

func candidateLess(a, b *headingCandidate) bool {
	if a.lineID != b.lineID {
		return a.lineID < b.lineID
	}
	return a.id < b.id
}

func sortCandidates(cands []*headingCandidate) {
	sort.SliceStable(cands, func(i, j int) bool { return candidateLess(cands[i], cands[j]) })
}

type titleNode struct {
	id       int
	lineID   int
	level    int
	parentID int // -1 = no parent
	titleRaw string
	pattern  TitlePattern
	index    []int
}

type lineScope struct {
	startLine int
	endLine   int
}

func (s lineScope) contains(lineID int) bool {
	return lineID >= s.startLine && lineID < s.endLine
}

func inScope(cands []*headingCandidate, sc lineScope) []*headingCandidate {
	out := make([]*headingCandidate, 0, len(cands))
	for _, c := range cands {
		if sc.contains(c.lineID) {
			out = append(out, c)
		}
	}
	return out
}

// ---- extraction ---------------------------------------------------------

var bareIPv4Re = regexp.MustCompile(`^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$`)

func isBareIPv4AddressLine(t string) bool {
	m := bareIPv4Re.FindStringSubmatch(t)
	if m == nil {
		return false
	}
	for _, g := range m[1:] {
		n := 0
		for _, r := range g {
			n = n*10 + int(r-'0')
		}
		if n > 255 {
			return false
		}
	}
	return true
}

func isTitleExtractCandidateLine(norm string) bool {
	// Approximation of MarkdownStructureRules.isTitleExtractCandidateLine
	// (never ported — not in the pdf-port/04 DOCX-scoped subset). Basic
	// sanity gate only: MatchFirst already requires a recognizable numbering
	// prefix, so this mainly excludes degenerate/oversized lines.
	if IsBlank(norm) {
		return false
	}
	if runeLen(norm) > MaxHeadingLength*3 {
		return false
	}
	return true
}

// extractCandidates ports MarkdownHeadingStage.extractCandidates (four-param
// version). skipLineIds are lines already recognized as `#` headings;
// disqualifiedPatternKeys is the global inference blacklist.
func extractCandidates(lines []string, skipLineIds map[int]bool, disqualifiedPatternKeys map[string]bool) []*headingCandidate {
	var out []*headingCandidate
	nextID := 0
	inFence := false
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if skipLineIds != nil && skipLineIds[i] {
			continue
		}
		if isMarkdownHeadingLine(trimmed) {
			continue
		}
		if strings.HasPrefix(trimmed, "|") || tableSeparatorRe.MatchString(trimmed) {
			continue
		}
		norm := NormalizeLine(raw)
		// extractCandidates step 7 (pdf-port/05): a line otherwise
		// suppressed by HeadingSuppressHeuristics.shouldSuppressHeadingLine
		// is exempted from suppression when it independently looks like a
		// section-title-numbered line or a structural chapter heading.
		isSectionTitle := isSectionTitleNumberedLineWithLines(norm, lines)
		if !isSectionTitle && !IsStructuralChapterHeading(norm) && shouldSuppressHeadingLine(lines, i) {
			continue
		}
		if !isTitleExtractCandidateLine(norm) {
			continue
		}
		pattern := MatchFirst(norm)
		if pattern == titlePatternNone {
			continue
		}
		if pattern == TitleCnParen && looksLikeCnParenBodyEnumerationText(norm) {
			continue
		}
		if pattern == TitleChapterFive && LooksLikeCnArticleBodyParagraphLead(norm) {
			continue
		}
		if isBareIPv4AddressLine(norm) {
			continue
		}
		pk := ClassifyPrefixKey(norm)
		if pk != "" && disqualifiedPatternKeys != nil && disqualifiedPatternKeys[pk] {
			continue
		}
		idx := ParseIndex(norm, pattern)
		if len(idx) == 0 {
			continue
		}
		out = append(out, &headingCandidate{id: nextID, lineID: i, normText: norm, pattern: pattern, index: idx})
		nextID++
	}
	return out
}

var tableSeparatorRe = regexp.MustCompile(`^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)+\|?$`)

var cnParenColonAfterPrefixRe = regexp.MustCompile(`^[^：:]{0,40}[：:].*`)

// looksLikeCnParenBodyEnumerationText ports
// MarkdownHeadingStage.looksLikeCnParenBodyEnumerationText.
func looksLikeCnParenBodyEnumerationText(norm string) bool {
	if IsBlank(norm) {
		return false
	}
	if MatchFirst(norm) != TitleCnParen {
		return false
	}
	after := StripBodyEnumerationPrefix(norm, TitleCnParen)
	if cnParenColonAfterPrefixRe.MatchString(after) {
		return true
	}
	return ClearlyFailsHeadingQuality(norm)
}

// ---- list-like / body-enumeration / short-phrase marking ---------------

func meanLineGap(seg []*headingCandidate) float64 {
	if len(seg) < 2 {
		return math.Inf(1)
	}
	sum := 0
	for i := 1; i < len(seg); i++ {
		sum += seg[i].lineID - seg[i-1].lineID
	}
	return float64(sum) / float64(len(seg)-1)
}

func seqQualityAdjacent(sorted []*headingCandidate) float64 {
	if len(sorted) < 2 {
		return 1.0
	}
	hits := 0
	for i := 1; i < len(sorted); i++ {
		if IsSequentialIndex(sorted[i-1].index, sorted[i].index) {
			hits++
		}
	}
	return float64(hits) / float64(len(sorted)-1)
}

func isSequentialCand(a, b *headingCandidate) bool {
	return IsSequentialIndex(a.index, b.index)
}

func markListLikeCandidates(cands []*headingCandidate) {
	sortCandidates(cands)
	i := 0
	for i < len(cands) {
		if !cands[i].pattern.SupportListLike() {
			i++
			continue
		}
		j := i + 1
		for j < len(cands) && cands[j].pattern == cands[i].pattern {
			j++
		}
		seg := cands[i:j]
		if len(seg) >= listLikeMinRun {
			avgGap := meanLineGap(seg)
			quality := seqQualityAdjacent(seg)
			if avgGap < listLikeMaxGap && quality < listLikeSeqQualityProtect {
				for _, c := range seg {
					c.listLike = true
				}
			}
		}
		i = j
	}
	protectStructuralSiblings(cands)
}

func markBodyEnumerationLists(cands []*headingCandidate) {
	sortCandidates(cands)
	i := 0
	for i < len(cands) {
		if !IsBodyEnumerationPattern(cands[i].pattern) {
			i++
			continue
		}
		j := i + 1
		for j < len(cands) && cands[j].pattern == cands[i].pattern && cands[j].lineID-cands[j-1].lineID < siblingProtectGap {
			j++
		}
		seg := cands[i:j]
		if len(seg) >= bodyEnumListMinRun && bodyEnumRunLooksLikeBodyList(seg) {
			for _, c := range seg {
				c.listLike = true
			}
		}
		i = j
	}
}

func bodyEnumRunLooksLikeBodyList(seg []*headingCandidate) bool {
	for _, c := range seg {
		body := StripBodyEnumerationPrefix(c.normText, c.pattern)
		if runeLen(body) < bodyEnumMinCharsAfterPrefix {
			return false
		}
		if looksLikeNumericSectionTitleBody(c.pattern, body) {
			return false
		}
	}
	return true
}

func looksLikeNumericSectionTitleBody(p TitlePattern, body string) bool {
	switch p {
	case TitleNumDot, TitleNumTow, TitleNumThree, TitleNumFour, TitleNumFive:
		return looksLikeSectionTitleBody(body)
	}
	return false
}

// markShortPhraseListRuns ports MarkdownHeadingStage.markShortPhraseListRuns:
// builds an Entry list from the *already-extracted heading candidates*
// (pdf-port/05 step 1-2: filter by supportsPatternKey + the
// looksLikeSectionTitleNumberedLine exemption, TITLE_NUM_DOT excluded from
// that exemption), then delegates run-detection to the Entry-list overload
// of the full ShortPhraseListRunHeuristics port (short_phrase_list_run.go's
// detectMarkedLineIdsInternal, PLAIN_MARKDOWN mode) — NOT the
// text-lines overload (that one independently re-scans all `lines` and is
// used only by detectExistingHeadingShortPhraseListRunLineIds for the
// existing-heading counter-evidence path).
func markShortPhraseListRuns(cands []*headingCandidate, lines []string) {
	var entries []*shortPhraseEntry
	for _, c := range cands {
		if !supportsPatternKey(c.pattern.Key()) {
			continue
		}
		if c.pattern != TitleNumDot && LooksLikeSectionTitleNumberedLine(c.pattern.Key(), c.normText, lines) {
			continue
		}
		entries = append(entries, &shortPhraseEntry{lineID: c.lineID, patternKey: c.pattern.Key(), normText: c.normText, index: c.index})
	}
	marked := detectMarkedLineIdsInternal(entries, lines, shortPhrasePlainMarkdown)
	for _, c := range cands {
		if marked[c.lineID] {
			c.shortPhraseListRun = true
		}
	}
}

func protectStructuralSiblings(sorted []*headingCandidate) {
	for i := 0; i < len(sorted); i++ {
		a := sorted[i]
		if !a.listLike {
			continue
		}
		for j := i + 1; j < len(sorted); j++ {
			b := sorted[j]
			if !b.listLike || b.pattern != a.pattern {
				continue
			}
			if IsBodyEnumerationPattern(a.pattern) && IsBodyEnumerationPattern(b.pattern) {
				continue
			}
			gap := b.lineID - a.lineID
			if gap < 0 || gap >= siblingProtectGap {
				continue
			}
			if isSequentialCand(a, b) {
				a.listLike = false
				b.listLike = false
			}
		}
	}
}

// ---- scoring / filtering -------------------------------------------------

func scoreCandidates(cands []*headingCandidate, lines []string) {
	for _, c := range cands {
		c.score = scoreOne(c, lines)
	}
}

func scoreOne(c *headingCandidate, lines []string) int {
	score := 0
	if runeLen(c.normText) <= MaxHeadingLength {
		score += 2
	}
	if c.lineID-1 < 0 || strings.TrimSpace(safeLine(lines, c.lineID-1)) == "" {
		score += 2
	}
	if strings.TrimSpace(safeLine(lines, c.lineID+1)) == "" {
		score += 2
	}
	if !c.shortPhraseListRun && !containsAnyRune(c.normText, "的了是将") {
		score += 3
	}
	if !c.listLike && !c.shortPhraseListRun && !endsWithVerbLike(c.normText) {
		score += 2
	}
	if hasLongTailAfterColon(c.normText, 30) {
		score -= 3
	}
	if countCommasFull(c.normText) >= 2 {
		score -= 2
	}
	if c.listLike {
		score -= 2
	}
	if c.shortPhraseListRun {
		score -= shortPhraseListScorePenalty
	}
	return score
}

func safeLine(lines []string, i int) string {
	if i < 0 || i >= len(lines) {
		return ""
	}
	return lines[i]
}

var endParticles = "了着过去中其于及并"

var verbSuffixes = []string{
	"完成", "实现", "达成", "结束", "到位", "进行", "开展", "实施", "执行", "处理",
	"通过", "增加", "减少", "提高", "降低", "提升", "下降", "达到", "讨论", "商议", "建议", "要求", "请求", "告知",
}

var verbSingleEndRe = regexp.MustCompile(`[做写看听说读]$`)

func endsWithVerbLike(text string) bool {
	t := NormalizeLine(text)
	if t == "" {
		return false
	}
	r := []rune(t)
	last := r[len(r)-1]
	if strings.ContainsRune(endParticles, last) {
		return true
	}
	for _, suf := range verbSuffixes {
		if strings.HasSuffix(t, suf) {
			return true
		}
	}
	if verbSingleEndRe.MatchString(t) && runeLen(t) > 30 {
		return true
	}
	for _, suf := range []string{"予以奖励", "予以处罚", "予以说明", "进行分析", "进行探讨", "进行部署"} {
		if strings.HasSuffix(t, suf) {
			return true
		}
	}
	return false
}

var colonSplitRe = regexp.MustCompile(`[:：]`)

func hasLongTailAfterColon(text string, limit int) bool {
	loc := colonSplitRe.FindStringIndex(text)
	if loc == nil {
		return false
	}
	tail := NormalizeLine(text[loc[1]:])
	return runeLen(tail) > limit
}

func countCommasFull(text string) int {
	n := 0
	for _, r := range text {
		if r == ',' || r == '，' {
			n++
		}
	}
	return n
}

// filterCandidates ports MarkdownHeadingStage.filterCandidates.
func filterCandidates(lines []string, cands []*headingCandidate) []*headingCandidate {
	var out []*headingCandidate
	for _, c := range cands {
		if c.normText == "" {
			continue
		}
		if c.shortPhraseListRun {
			continue
		}
		if IsStructuralChapterHeading(c.normText) || LooksLikeSectionTitleNumberedLine(c.pattern.Key(), c.normText, lines) {
			out = append(out, c)
			continue
		}
		if runeLen(c.normText) > MaxHeadingLength {
			continue
		}
		if looksLikeBodySentence(c.normText) {
			continue
		}
		if c.pattern == TitleChapterFive && LooksLikeCnArticleBodyParagraphLead(c.normText) {
			continue
		}
		if IsBodyEnumerationPattern(c.pattern) && c.listLike {
			body := StripBodyEnumerationPrefix(c.normText, c.pattern)
			if !looksLikeNumericSectionTitleBody(c.pattern, body) {
				continue
			}
		}
		if c.pattern == TitleCnParen && looksLikeCnParenBodyEnumerationText(c.normText) {
			continue
		}
		if c.score < scoreKeepThreshold {
			continue
		}
		out = append(out, c)
	}
	return out
}

// looksLikeBodySentence is the shared implementation behind
// isPrefixHeadingButBodyLikeSentence and isExistingHeadingBodyLikeSentence
// (identical algorithms in the Java source — see pdf-port/05 移植注意事项 #6).
func looksLikeBodySentence(text string) bool {
	nonSpace := countNonSpaceChars(text)
	if nonSpace > 80 {
		return true
	}
	if nonSpace < 35 {
		return false
	}
	punct := countSentencePunctuation(text)
	if punct < 2 {
		return false
	}
	density := float64(punct) / float64(nonSpace)
	return density >= 0.015
}

// ---- scope-tree builder ---------------------------------------------------

func patternConfidence(same []*headingCandidate, sc lineScope) float64 {
	if len(same) == 0 {
		return 0
	}
	sorted := append([]*headingCandidate(nil), same...)
	sortCandidates(sorted)
	seq := seqQualityAdjacent(sorted)
	pers := persistenceMetric(sorted)
	local := localConsistencyAdjacent(sorted)
	lScope := sc.endLine - sc.startLine
	if lScope < 1 {
		lScope = 1
	}
	densityNorm := math.Min(1.0, float64(len(sorted))/float64(lScope))
	return 0.35*seq + 0.25*pers + 0.20*local + 0.20*(1-densityNorm)
}

func persistenceMetric(sorted []*headingCandidate) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return 1.0
	}
	var uniq []int
	seen := map[int]bool{}
	for _, c := range sorted {
		if !seen[c.lineID] {
			seen[c.lineID] = true
			uniq = append(uniq, c.lineID)
		}
	}
	u := len(uniq)
	if u == 1 {
		return 1.0
	}
	r := 1
	for i := 1; i < u; i++ {
		if uniq[i]-uniq[i-1] > 1 {
			r++
		}
	}
	return float64(r) / float64(u)
}

func localConsistencyAdjacent(sorted []*headingCandidate) float64 {
	if len(sorted) < 2 {
		return 1.0
	}
	hits := 0
	for i := 1; i < len(sorted); i++ {
		if compareIntSlices(sorted[i-1].index, sorted[i].index) < 0 {
			hits++
		}
	}
	return float64(hits) / float64(len(sorted)-1)
}

func compareIntSlices(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	if len(a) == len(b) {
		return 0
	}
	if len(a) < len(b) {
		return -1
	}
	return 1
}

type lineIDPair struct {
	lineID, id int
}

func compareFirstLine(a, b *lineIDPair) int {
	if a == nil {
		if b == nil {
			return 0
		}
		return 1
	}
	if b == nil {
		return -1
	}
	if a.lineID != b.lineID {
		if a.lineID < b.lineID {
			return -1
		}
		return 1
	}
	if a.id == b.id {
		return 0
	}
	if a.id < b.id {
		return -1
	}
	return 1
}

func hasEarlierDifferentPatternCandidate(candidatesInScope []*headingCandidate, dot *headingCandidate) bool {
	for _, c := range candidatesInScope {
		if c.pattern != dot.pattern && c.lineID < dot.lineID {
			return true
		}
	}
	return false
}

func preferDotSectionLayerWhenPresent(candidatesInScope []*headingCandidate) *TitlePattern {
	for _, c := range candidatesInScope {
		if c.pattern == TitleNumDot && len(c.index) == 1 && !hasEarlierDifferentPatternCandidate(candidatesInScope, c) {
			p := TitleNumDot
			return &p
		}
	}
	return nil
}

func preferTowSectionLayerOverThree(candidatesInScope []*headingCandidate) *TitlePattern {
	var tows, threes []*headingCandidate
	hasChapterOne := false
	for _, c := range candidatesInScope {
		switch c.pattern {
		case TitleNumTow:
			tows = append(tows, c)
		case TitleNumThree:
			threes = append(threes, c)
		case TitleChapterOne:
			hasChapterOne = true
		}
	}
	if len(tows) == 0 || len(threes) == 0 {
		return nil
	}
	if hasChapterOne {
		return nil
	}
	for _, c := range threes {
		if len(c.index) != 3 {
			continue
		}
		found := false
		for _, t := range tows {
			if len(t.index) == 2 && t.index[0] == c.index[0] && t.index[1] == c.index[1] {
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	p := TitleNumTow
	return &p
}

func pickCurrentPattern(cSorted, candidatesInScope []*headingCandidate, sc lineScope) TitlePattern {
	if p := preferDotSectionLayerWhenPresent(candidatesInScope); p != nil {
		return *p
	}
	if p := preferTowSectionLayerOverThree(candidatesInScope); p != nil {
		return *p
	}
	anchor := cSorted[0].pattern
	confAnchor := patternConfidence(sameLine(candidatesInScope, anchor), sc)
	if confAnchor+headingStageEPS >= confidenceThreshold {
		return anchor
	}
	var pAll []TitlePattern
	firstOccur := map[TitlePattern]*lineIDPair{}
	for _, c := range cSorted {
		if _, ok := firstOccur[c.pattern]; !ok {
			pAll = append(pAll, c.pattern)
			firstOccur[c.pattern] = &lineIDPair{lineID: c.lineID, id: c.id}
		}
	}
	best := anchor
	bestConf := confAnchor
	bestKey := firstOccur[anchor]
	for _, p := range pAll {
		var conf float64
		if p == anchor {
			conf = confAnchor
		} else {
			conf = patternConfidence(sameLine(candidatesInScope, p), sc)
		}
		fl := firstOccur[p]
		if conf > bestConf+headingStageEPS || (math.Abs(conf-bestConf) <= headingStageEPS && compareFirstLine(fl, bestKey) < 0) {
			best = p
			bestConf = conf
			bestKey = fl
		}
	}
	return best
}

func sameLine(cands []*headingCandidate, p TitlePattern) []*headingCandidate {
	var out []*headingCandidate
	for _, c := range cands {
		if c.pattern == p {
			out = append(out, c)
		}
	}
	return out
}

func resolveScope(sc lineScope, candidatesInScope, allFiltered []*headingCandidate, level int, nextNodeID *int) []*titleNode {
	if level > headingStageMaxLevel || len(candidatesInScope) == 0 {
		return nil
	}
	cSorted := append([]*headingCandidate(nil), candidatesInScope...)
	sortCandidates(cSorted)
	pk := pickCurrentPattern(cSorted, candidatesInScope, sc)
	layer := sameLine(cSorted, pk)
	if len(layer) == 0 {
		return nil
	}
	var nodes []*titleNode
	layerNodes := make([]*titleNode, len(layer))
	for i, c := range layer {
		id := *nextNodeID
		*nextNodeID++
		layerNodes[i] = &titleNode{id: id, lineID: c.lineID, level: level, parentID: -1, titleRaw: c.normText, pattern: c.pattern, index: c.index}
		nodes = append(nodes, layerNodes[i])
	}
	if level >= headingStageMaxLevel {
		return nodes
	}
	for i, t := range layerNodes {
		endLine := sc.endLine
		if i+1 < len(layerNodes) {
			endLine = layerNodes[i+1].lineID
		}
		childStart := t.lineID + 1
		if childStart >= endLine {
			continue
		}
		child := lineScope{startLine: childStart, endLine: endLine}
		sub := inScope(allFiltered, child)
		childNodes := resolveScope(child, sub, allFiltered, level+1, nextNodeID)
		nodes = append(nodes, childNodes...)
	}
	return nodes
}

func injectMissingTowSectionHeadings(nodes []*titleNode, pool []*headingCandidate, nextNodeID *int) []*titleNode {
	haveLine := map[int]bool{}
	for _, n := range nodes {
		haveLine[n.lineID] = true
	}
	hasChapterOne := false
	chapterOneLevel := 0
	for _, n := range nodes {
		if n.pattern == TitleChapterOne {
			hasChapterOne = true
			chapterOneLevel = n.level
			break
		}
	}
	out := append([]*titleNode(nil), nodes...)
	for _, c := range pool {
		if c.pattern != TitleNumTow || len(c.index) != 2 || haveLine[c.lineID] {
			continue
		}
		var sibling *titleNode
		for _, n := range nodes {
			if n.pattern == TitleNumThree && len(n.index) == 3 && n.index[0] == c.index[0] && n.index[1] == c.index[1] {
				sibling = n
				break
			}
		}
		if sibling == nil {
			continue
		}
		anchorLevel := 2
		if hasChapterOne && chapterOneLevel+1 > anchorLevel {
			anchorLevel = chapterOneLevel + 1
		}
		id := *nextNodeID
		*nextNodeID++
		out = append(out, &titleNode{id: id, lineID: c.lineID, level: anchorLevel, parentID: -1, titleRaw: c.normText, pattern: c.pattern, index: c.index})
		haveLine[c.lineID] = true
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].lineID != out[j].lineID {
			return out[i].lineID < out[j].lineID
		}
		return out[i].id < out[j].id
	})
	return out
}

func fallbackTitles(cands []*headingCandidate, nextNodeID *int) []*titleNode {
	if len(cands) == 0 {
		return nil
	}
	var distinct []TitlePattern
	seen := map[TitlePattern]bool{}
	sorted := append([]*headingCandidate(nil), cands...)
	sortCandidates(sorted)
	for _, c := range sorted {
		if !seen[c.pattern] {
			seen[c.pattern] = true
			distinct = append(distinct, c.pattern)
		}
	}
	allLevelOne := func() []*titleNode {
		var nodes []*titleNode
		for _, c := range sorted {
			id := *nextNodeID
			*nextNodeID++
			nodes = append(nodes, &titleNode{id: id, lineID: c.lineID, level: 1, parentID: -1, titleRaw: c.normText, pattern: c.pattern, index: c.index})
		}
		return nodes
	}
	if len(distinct) == 1 || len(distinct) >= 3 {
		return allLevelOne()
	}
	// len(distinct) == 2
	p0, p1 := distinct[0], distinct[1]
	root := lineScope{startLine: 0, endLine: math_MaxInt}
	c0 := patternConfidence(sameLine(sorted, p0), root)
	c1 := patternConfidence(sameLine(sorted, p1), root)
	top := p0
	if !(c0+headingStageEPS >= c1) {
		top = p1
	}
	var nodes []*titleNode
	for _, c := range sorted {
		lv := 2
		if c.pattern == top {
			lv = 1
		}
		id := *nextNodeID
		*nextNodeID++
		nodes = append(nodes, &titleNode{id: id, lineID: c.lineID, level: lv, parentID: -1, titleRaw: c.normText, pattern: c.pattern, index: c.index})
	}
	return nodes
}

const math_MaxInt = int(^uint(0) >> 1)

func rebuildTree(nodes []*titleNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].lineID != nodes[j].lineID {
			return nodes[i].lineID < nodes[j].lineID
		}
		return nodes[i].id < nodes[j].id
	})
	var stack []*titleNode
	for _, n := range nodes {
		for len(stack) > 0 && stack[len(stack)-1].level >= n.level {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			n.parentID = -1
		} else {
			n.parentID = stack[len(stack)-1].id
		}
		stack = append(stack, n)
	}
}

func scopeLevelFix(nodes []*titleNode) {
	if len(nodes) == 0 {
		return
	}
	byID := map[int]*titleNode{}
	children := map[int][]*titleNode{}
	var roots []*titleNode
	for _, n := range nodes {
		byID[n.id] = n
	}
	for _, n := range nodes {
		if n.parentID == -1 {
			roots = append(roots, n)
		} else {
			children[n.parentID] = append(children[n.parentID], n)
		}
	}
	for _, root := range roots {
		sub := collectSubtree(root.id, byID, children)
		applyRule1MinLevelShift(sub)
	}
	for _, n := range nodes {
		if n.level < 1 {
			n.level = 1
		}
		if n.level > headingStageMaxLevel {
			n.level = headingStageMaxLevel
		}
	}
	for _, root := range roots {
		sub := collectSubtree(root.id, byID, children)
		applyRule3HoleFix(sub)
	}
	for iter := 0; iter < 12; iter++ {
		changed := false
		for _, n := range nodes {
			if n.parentID == -1 {
				continue
			}
			parent := byID[n.parentID]
			if parent == nil {
				continue
			}
			if n.level > parent.level+1 {
				n.level = parent.level + 1
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	for _, root := range roots {
		sub := collectSubtree(root.id, byID, children)
		applyRule5SingleFlatRoot(sub)
	}
}

func collectSubtree(rootID int, byID map[int]*titleNode, children map[int][]*titleNode) []*titleNode {
	var out []*titleNode
	queue := []int{rootID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		n := byID[id]
		if n == nil {
			continue
		}
		out = append(out, n)
		for _, c := range children[id] {
			queue = append(queue, c.id)
		}
	}
	return out
}

func applyRule1MinLevelShift(sub []*titleNode) {
	if len(sub) == 0 {
		return
	}
	m := sub[0].level
	for _, n := range sub {
		if n.level < m {
			m = n.level
		}
	}
	if m > 1 {
		for _, n := range sub {
			n.level -= m - 1
		}
	}
}

func applyRule3HoleFix(sub []*titleNode) {
	var h1, h2, h3 []*titleNode
	for _, n := range sub {
		switch n.level {
		case 1:
			h1 = append(h1, n)
		case 2:
			h2 = append(h2, n)
		case 3:
			h3 = append(h3, n)
		}
	}
	if len(h1) == 0 || len(h2) != 0 || len(h3) < holeFixMinH3 {
		return
	}
	sort.SliceStable(h3, func(i, j int) bool { return h3[i].lineID < h3[j].lineID })
	pseudo := make([]*headingCandidate, len(h3))
	for i, n := range h3 {
		pseudo[i] = &headingCandidate{id: n.id, lineID: n.lineID, normText: n.titleRaw, pattern: n.pattern, index: n.index}
	}
	if seqQualityAdjacent(pseudo) >= holeFixSeqQualityMin {
		for _, n := range h3 {
			n.level = 2
		}
	}
}

func applyRule5SingleFlatRoot(sub []*titleNode) {
	if len(sub) == 0 {
		return
	}
	root := sub[0]
	if root.parentID != -1 {
		return
	}
	lvl := sub[0].level
	same := true
	for _, n := range sub {
		if n.level != lvl {
			same = false
			break
		}
	}
	if same && lvl > 1 && len(sub) == 1 {
		root.level = 1
	}
}

func removeEmptyTitleNodes(nodes []*titleNode) []*titleNode {
	var out []*titleNode
	for _, n := range nodes {
		if NormalizeLine(n.titleRaw) != "" {
			out = append(out, n)
		}
	}
	return out
}

// inferHeadings ports MarkdownHeadingStage.inferHeadings (four-param
// version). listGuideScopes are excluded outright (nonHeadingScopes — not
// modeled in this port, always empty from the caller).
func inferHeadings(lines []string, skipLineIds map[int]bool, disqualifiedPatternKeys map[string]bool) []*HeadingHit {
	cands := extractCandidates(lines, skipLineIds, disqualifiedPatternKeys)
	if len(cands) == 0 {
		return nil
	}
	markListLikeCandidates(cands)
	markBodyEnumerationLists(cands)
	markShortPhraseListRuns(cands, lines)
	scoreCandidates(cands, lines)
	filtered := filterCandidates(lines, cands)
	if len(filtered) == 0 {
		return nil
	}
	root := lineScope{startLine: 0, endLine: len(lines)}
	inRoot := inScope(filtered, root)
	nextNodeID := 0
	nodes := resolveScope(root, inRoot, filtered, 1, &nextNodeID)
	if len(nodes) == 0 {
		nodes = fallbackTitles(filtered, &nextNodeID)
	}
	if len(nodes) == 0 {
		return nil
	}
	nodes = injectMissingTowSectionHeadings(nodes, filtered, &nextNodeID)
	rebuildTree(nodes)
	scopeLevelFix(nodes)
	rebuildTree(nodes)
	nodes = removeEmptyTitleNodes(nodes)
	if len(nodes) == 0 {
		return nil
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].lineID != nodes[j].lineID {
			return nodes[i].lineID < nodes[j].lineID
		}
		return nodes[i].id < nodes[j].id
	})
	hits := make([]*HeadingHit, 0, len(nodes))
	for _, n := range nodes {
		pk := &PatternKey{Type: n.pattern, Depth: n.pattern.Depth()}
		hits = append(hits, &HeadingHit{LineIndex: n.lineID, Level: n.level, TitleRaw: n.titleRaw, PatternKey: pk})
	}
	return hits
}

// ---- HeadingReadingOrderValidator -----------------------------------------

type readingOrderStackEntry struct {
	hit            *HeadingHit
	referenceLevel int
}

func clampLevel(l int) int {
	if l < 1 {
		return 1
	}
	if l > headingStageMaxHeadingLevel {
		return headingStageMaxHeadingLevel
	}
	return l
}

// ApplyReadingOrderNesting ports HeadingReadingOrderValidator.applyReadingOrderNesting.
func ApplyReadingOrderNesting(hits []*HeadingHit) []*HeadingHit {
	if len(hits) == 0 {
		return hits
	}
	sorted := append([]*HeadingHit(nil), hits...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].LineIndex < sorted[j].LineIndex })
	var stack []readingOrderStackEntry
	for _, hit := range sorted {
		reference := clampLevel(hit.Level)
		for len(stack) > 0 && stack[len(stack)-1].referenceLevel >= reference {
			stack = stack[:len(stack)-1]
		}
		var actual int
		if len(stack) == 0 {
			actual = 1
		} else {
			parentLevel := stack[len(stack)-1].hit.Level
			switch {
			case reference <= parentLevel:
				actual = parentLevel
			case reference > parentLevel+1:
				actual = parentLevel + 1
			default:
				actual = reference
			}
		}
		hit.Level = clampLevel(actual)
		stack = append(stack, readingOrderStackEntry{hit: hit, referenceLevel: reference})
	}
	return sorted
}
