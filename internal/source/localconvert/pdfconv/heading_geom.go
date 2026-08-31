package pdfconv

import (
	"math"
	"unicode"
)

// This file covers the small slice of Part 2 (pdf-port/02-heading-style-render.md)
// that Part 1's extraction/merge algorithms call into directly: isHeading,
// and the chapter-heading-boundary helpers used by shouldMerge. Part 2's
// full style-clustering machinery (buildHeadingStyleProfile / StyleCluster /
// resolveHeadingLevel's multi-signal scoring) is NOT ported here — this is a
// scope reduction, flagged the same way pipeline.go already flags the DOCX
// MPP-stage reduction. What IS implemented: a geometric heading test
// (isHeading) combining font-size/bold/centering signals with the
// already-ported text-pattern heading tests (isHeadingByRegex,
// ClearlyFailsHeadingQuality) that the render layer (render.go) and
// mergeLines both depend on, plus the exact boundary helpers Part 1's
// shouldMerge spec names.

const centeredHeadingPageRatio = 0.15

// isHeading is the geometric heading test referenced throughout Part 1's
// shouldMerge (pdf-port/01 step 12/13/19) and Part 3's cross-page merge. A
// block reads as a heading when its text matches one of the structural
// heading regexes (isHeadingByRegex, chapter/section patterns) AND it does
// not clearly fail heading quality (ClearlyFailsHeadingQuality, e.g. a long
// sentence with terminal punctuation), OR it is visually styled like one
// (bigger than body font, or bold, and short).
func isHeading(block TextBlock, bodyFontMode float64, cfg Config) bool {
	text := block.Text
	if IsBlank(text) {
		return false
	}
	if ClearlyFailsHeadingQuality(text) {
		return false
	}
	if runeLen(text) > cfg.MaxHeadingLength {
		return false
	}
	if IsStructuralChapterHeading(text) {
		// "第X章/节/纲/目/条" is structurally unambiguous — it basically
		// never doubles as an informal in-prose enumeration marker, unlike
		// the numbered/CN-numeral patterns below, so a bare pattern match
		// is trusted on its own (matches the geometric shouldMerge spec's
		// treatment of these as always heading-strength boundaries).
		return true
	}
	if pattern := MatchFirst(text); pattern != titlePatternNone {
		// Every OTHER numbered pattern ("一、", "1.", "（1）", "1、", "A.",
		// "I."...) is routinely reused as an informal enumeration marker
		// inside ordinary flowing prose (a Chinese document listing several
		// points as "一、...二、...三、..." within one paragraph is
		// extremely common) — ClearlyFailsHeadingQuality's own length-based
		// gates only reject text over 80 runes, so a 30-50 char clause-dense
		// enumeration item like "一、查找的信息资源分散，孤岛数据多，获取
		// 渠道有限，...查不全、" slips through as "looks like a heading" at
		// this single-block, no-document-context geometric layer even
		// though it's clearly mid-paragraph body text. Real headings using
		// these patterns are short, title-like phrases; body enumeration
		// items are full clauses/sentences — looksLikeSectionTitleBody
		// (short_phrase_list_run.go, already used by IsListItem for the
		// same distinction) tells them apart via length + punctuation
		// density on the text AFTER the numbering prefix (IsStructuralCnSectionHeading,
		// the TitleCnNum check, is covered by this branch too — not handled
		// separately).
		if isShortTitlePatternBody(text, pattern) {
			return true
		}
		return isVisualOnlyHeading(block, bodyFontMode, cfg)
	}
	if isHeadingByRegex(text) {
		return true
	}
	return isVisualOnlyHeading(block, bodyFontMode, cfg)
}

// isShortTitlePatternBody reports whether text, which MatchFirst already
// matched to pattern, reads as a genuine short title rather than a long
// clause-dense body sentence merely reusing the same numbering marker
// (see isHeading's doc comment for the "一、查找的信息资源分散,...查不全、"
// example this distinguishes). Shared between isHeading (the geometric
// merge-suppression signal) and mpp_bodymerge.go's shouldBlockBodyMergePair
// (the text-only Part 6 pass) — both need the identical judgment call, just
// applied at different pipeline stages, and diverging between them
// reintroduces the same class of bug from two different code paths (as
// happened once already — see mpp_bodymerge.go's isHeadingLikeLineForMerge
// doc comment).
func isShortTitlePatternBody(text string, pattern TitlePattern) bool {
	def, ok := titlePatternDefs[pattern]
	if !ok {
		return false
	}
	loc := def.re.FindStringIndex(text)
	if loc == nil {
		return false
	}
	return looksLikeSectionTitleBody(text[loc[1]:])
}

// isShortTitlePatternLine is isShortTitlePatternBody with the MatchFirst
// call folded in, for callers (mpp_bodymerge.go) that only have raw text.
func isShortTitlePatternLine(text string) bool {
	pattern := MatchFirst(text)
	if pattern == titlePatternNone {
		return false
	}
	return isShortTitlePatternBody(text, pattern)
}

// isVisualOnlyHeading is the font-size-based fallback signal, split out
// from isHeading so render.go can tell the two apart: a text-pattern match
// (isHeadingByRegex/chapter/section) is left as a plain line for the
// existing, more thorough MarkdownHeadingStage (heading_stage.go,
// ApplyHeadingStage — a real Part 4/5 port, not this file's reduction) to
// discover and level on its own via its candidate/counter-evidence
// machinery — pre-stamping "#" on every pattern match short-circuits that
// machinery and was observed to over-promote things like a numbered
// notice-body line ("1、关于打卡时间") that ApplyHeadingStage's sequence/
// counter-evidence checks would otherwise correctly leave as body text. A
// visual-only match (bigger font, no numbering pattern at all — e.g. an
// unnumbered cover-page title) has no textual signal for
// MarkdownHeadingStage's pattern-based candidate detection to find, so
// those still need to be rendered as "#" headings directly here.
func isVisualOnlyHeading(block TextBlock, bodyFontMode float64, cfg Config) bool {
	text := block.Text
	if IsBlank(text) || ClearlyFailsHeadingQuality(text) || runeLen(text) > cfg.MaxHeadingLength {
		return false
	}
	if isHeadingByRegex(text) || IsStructuralChapterHeading(text) || IsStructuralCnSectionHeading(text) {
		// Any text with a recognizable numbering pattern must go through
		// ApplyHeadingStage's own pattern-based candidate/counter-evidence
		// machinery exclusively — never here. A real chapter title can
		// legitimately contain clause punctuation this function otherwise
		// screens out for the no-pattern case below (e.g.
		// "第三章出差期间发生的住宿费、交通费与伙食补贴" contains "、"),
		// so pre-stamping "#" here only when such punctuation happens to
		// be absent made a document's chapters flip between "pre-marked"
		// and "left for inference" depending on incidental commas in their
		// titles — and since inference for a whole pattern class gets
		// blacklisted document-wide the moment even ONE occurrence looks
		// "mixed" (heading-marked here, plain-text there,
		// detectMixedRecognitionPatternKeys), that inconsistency was
		// enough to silently drop every chapter using the same pattern.
		// Keeping this path always deferred to ApplyHeadingStage removes
		// the inconsistency at the source.
		return false
	}
	if !containsLetterOrHan(text) {
		// A line of pure punctuation/underscores (e.g. a fill-in-the-blank
		// "_____________" placeholder in a form template) can still be
		// bold/oversized in the source PDF but carries no title text at
		// all — never a heading regardless of its style.
		return false
	}
	// Free of clause-separating punctuation (a real title is a noun
	// phrase, not a comma-joined compound sentence — e.g.
	// "项目，目前已经具备开工条件，请予以安排" is form-letter body prose,
	// not a title, even though it may render in a bold/larger template
	// style), and short enough to plausibly be a heading rather than a
	// bold sentence.
	if bodyFontMode <= 0 || runeLen(text) > 40 || EndsWithTerminalPunctuation(text) || containsAnyRune(text, "：:，、,;；") {
		return false
	}
	sizeDelta := block.FontSizeMean - bodyFontMode

	if sizeDelta >= cfg.FontSizeDeltaPt*2 {
		return true
	}
	// A centered line only modestly larger than body — too small a bump
	// to trust font size alone (label：value fields at body size can drift
	// a fraction of a point from measurement noise), but centering plus
	// *any* real bump is a reliable combination for short document-title/
	// memo-number lines like "万户软件〔2024〕32号" (+0.95pt over body,
	// centered, not bold — the earlier bare size-only gate rejected it as
	// well below its 2x-delta threshold).
	if sizeDelta > cfg.FontSizeDeltaPt && isCenteredOnPage(block) {
		return true
	}
	return false
}

func containsLetterOrHan(s string) bool {
	for _, r := range s {
		if isChineseRune(r) || unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// visualHeadingLevel1Ratio: the font-size ratio (block / bodyFontMode)
// above which a visual-only heading (see isVisualOnlyHeading) is rendered
// at level 1 rather than level 2. Calibrated against real extracted font
// sizes across several test documents: a genuine cover-page document title
// runs ~2.9-3.0x body size (e.g. 35-36pt over a 12pt body), while a
// same-document *section* heading using the visually larger cover title
// again, or a "文件变更记录"-style section label, sits at ~1.3-1.8x body
// size and is leveled 2 in the reference output — there's a clear gap
// between the two clusters in the data, not just a rough guess. This is a
// blunt, single-threshold stand-in for the real Part 2 style-clustering
// algorithm (which would instead cluster all font sizes used across the
// whole document and rank clusters), not a re-derivation of it.
const visualHeadingLevel1Ratio = 2.5

func visualHeadingLevel(block TextBlock, bodyFontMode float64) int {
	if bodyFontMode > 0 && block.FontSizeMean >= bodyFontMode*visualHeadingLevel1Ratio {
		return 1
	}
	return 2
}

// isCenteredOnPage ports pdf-port/02's isCenteredOnPage: is this block's
// horizontal center within centeredHeadingPageRatio of the page's own
// center. Requires a real bbox (no metrics-based width estimate — the full
// Part 2 headingCenter/headingCenterFromMetrics fallback for bbox-less
// blocks isn't ported, see isVisualOnlyHeading's use of it).
func isCenteredOnPage(block TextBlock) bool {
	if block.Bbox == nil || block.PageWidth <= 0 {
		return false
	}
	center := (boxLLX(*block.Bbox) + boxURX(*block.Bbox)) / 2
	pageCenter := block.PageWidth / 2
	return math.Abs(center-pageCenter) <= block.PageWidth*centeredHeadingPageRatio
}

// isCenteredStructuralChapterHeading approximates Part 2's namesake: a
// structural chapter heading whose block is horizontally centered on the
// page (within centeredHeadingPageRatio of page width).
func isCenteredStructuralChapterHeading(block TextBlock) bool {
	if !IsStructuralChapterHeading(block.Text) {
		return false
	}
	return isCenteredOnPage(block)
}

// isChapterPrefixWithTitleNamePair ports pdf-port/01 step 4's
// isChapterPrefixWithTitleNamePair using the already-ported
// IsChapterPrefixOnlyLine/IsLikelyChapterTitleNameLine (chapter_toc.go).
func isChapterPrefixWithTitleNamePair(aText, bText string) bool {
	return IsChapterPrefixOnlyLine(aText) && IsLikelyChapterTitleNameLine(bText)
}

// shouldBlockMergeAtChapterHeadingBoundary approximates pdf-port/01 step 6:
// don't merge across a real chapter heading boundary. Deliberately narrower
// than its name's "chapter" scope might suggest expanding to — an earlier
// version of this function also blocked on IsStructuralCnSectionHeading
// (the "一、" pattern), but that pattern is routinely reused as an informal
// in-prose enumeration marker (see isHeading's same distinction), and
// unconditionally trusting it here reintroduced the same "long comma-dense
// enumeration item never merges with its own hard-wrap continuation" bug
// isHeading was fixed for, just via this separate call site. Only
// IsStructuralChapterHeading ("第X章/条/节/纲/目") is structurally
// unambiguous enough to gate a merge boundary without any quality check.
func shouldBlockMergeAtChapterHeadingBoundary(a, b TextBlock, cfg Config) bool {
	return IsStructuralChapterHeading(a.Text) || IsStructuralChapterHeading(b.Text)
}

// isNumberedClauseContinuation approximates ChapterReferenceHeuristics'
// namesake (pdf-port/01 step 9): conservatively returns false (no forced
// merge) since the Java source for this specific method was out of scope
// for the already-ported line-based ChapterReferenceHeuristics subset
// (chapter_reference.go only ports isBodyChapterReference). Returning false
// here just means this particular forced-merge shortcut doesn't fire; the
// rest of shouldMerge's chain still runs normally.
func isNumberedClauseContinuation(aText, bText string) bool {
	return false
}
