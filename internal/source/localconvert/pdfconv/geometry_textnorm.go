package pdfconv

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/ivanvanderbyl/docmill/v2/pkg/geom"
)

// Regexes from pdf-port/01 §正则表达式. RE2 doesn't support the Java
// lookahead in TITLE_NUM_SIMPLE/NUM_MULTI/EMBEDDED_ORDERED_LIST_MARKER etc,
// so those are matched with the assertion stripped, then boundary-checked
// manually via numericBoundaryOK/firstRuneAfter (textutil.go).
var (
	geomZeroWidthRe      = regexp.MustCompile(`[\x{200B}-\x{200D}\x{FEFF}]`)
	geomCJKSpaceRe       = regexp.MustCompile(`([\x{4E00}-\x{9FFF}])\s+([\x{4E00}-\x{9FFF}])`)
	geomDigitToCJKSpace  = regexp.MustCompile(`(\d)\s+([\x{4E00}-\x{9FFF}])`)
	geomCJKToDigitSpace  = regexp.MustCompile(`([\x{4E00}-\x{9FFF}])\s+(\d)`)
	geomCJKToASCII       = regexp.MustCompile(`([\x{4E00}-\x{9FFF}])([A-Za-z])`)
	geomASCIIToCJK       = regexp.MustCompile(`([A-Za-z])([\x{4E00}-\x{9FFF}])`)
	geomNumUnit          = regexp.MustCompile(`(\d)([A-Za-z])`)
	geomMultiSpace       = regexp.MustCompile(`[ \t]{2,}`)
	geomAlphaTokenRe     = regexp.MustCompile(`^[A-Za-z]+$`)
	geomSingleDigitFragRe = regexp.MustCompile(`^(\D*)(\d)(\D*)$`)

	pageNumberBlockRe = regexp.MustCompile(`^\s*(?:第\s*\d{1,5}\s*(?:页)?(?:\s*[-/|]\s*(?:共)?\s*\d{1,5}\s*(?:页)?)?|(?:(?:共)?\s*\d{1,5}\s*(?:页)?)(?:\s*[-/|]\s*(?:共)?\s*\d{1,5}\s*(?:页)?)?|—\s*\d{1,5}\s*—)\s*$`)
)

// normalizeText mirrors PdfToMarkdown.normalizeText (pdf-port/01), used for
// body/heading text. Order is significant.
func normalizeText(text string, cfg Config) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return ""
	}
	t = geomZeroWidthRe.ReplaceAllString(t, "")
	t = removeCharacterDoubling(t)
	t = geomCJKSpaceRe.ReplaceAllString(t, "$1$2")
	t = mergeBrokenEnglishWords(t, cfg)
	t = mergeSingleDigitRuns(t)
	t = geomDigitToCJKSpace.ReplaceAllString(t, "$1$2")
	t = geomCJKToDigitSpace.ReplaceAllString(t, "$1$2")
	t = geomCJKToASCII.ReplaceAllString(t, "$1 $2")
	t = geomASCIIToCJK.ReplaceAllString(t, "$1 $2")
	t = geomNumUnit.ReplaceAllString(t, "$1 $2")
	t = geomMultiSpace.ReplaceAllString(t, " ")
	return strings.TrimSpace(t)
}

// normalizeTableCellText mirrors normalizeTableCellText — same as
// normalizeText but without the CJK<->ASCII boundary spacing (steps 9/10).
func normalizeTableCellText(text string, cfg Config) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return ""
	}
	t = geomZeroWidthRe.ReplaceAllString(t, "")
	t = removeCharacterDoubling(t)
	t = geomCJKSpaceRe.ReplaceAllString(t, "$1$2")
	t = mergeBrokenEnglishWords(t, cfg)
	t = mergeSingleDigitRuns(t)
	t = geomDigitToCJKSpace.ReplaceAllString(t, "$1$2")
	t = geomCJKToDigitSpace.ReplaceAllString(t, "$1$2")
	t = geomNumUnit.ReplaceAllString(t, "$1 $2")
	t = geomMultiSpace.ReplaceAllString(t, " ")
	return strings.TrimSpace(t)
}

// removeCharacterDoubling ports the asymmetric detect/replace scan
// described in pdf-port/01: detection only looks at even-stride pairs, but
// replacement scans every adjacent position.
func removeCharacterDoubling(s string) string {
	r := []rune(s)
	if len(r) < 4 {
		return s
	}
	pairCount := 0
	for i := 0; i+1 < len(r); i += 2 {
		if r[i] == r[i+1] {
			pairCount++
		}
	}
	if pairCount < 2 {
		return s
	}
	var out []rune
	for i := 0; i < len(r); {
		if i+1 < len(r) && r[i] == r[i+1] {
			out = append(out, r[i])
			i += 2
			continue
		}
		out = append(out, r[i])
		i++
	}
	return string(out)
}

// mergeBrokenEnglishWords merges adjacent short all-alpha tokens not in the
// stopword list (pdf-port/01).
func mergeBrokenEnglishWords(text string, cfg Config) string {
	tokens := strings.Fields(text)
	if len(tokens) < 2 {
		return text
	}
	var out []string
	i := 0
	for i < len(tokens) {
		if i+1 < len(tokens) {
			a, b := tokens[i], tokens[i+1]
			if geomAlphaTokenRe.MatchString(a) && geomAlphaTokenRe.MatchString(b) &&
				len([]rune(a)) <= 2 && len([]rune(b)) <= 2 {
				_, aStop := cfg.ShortStopwords[strings.ToLower(a)]
				_, bStop := cfg.ShortStopwords[strings.ToLower(b)]
				if !aStop && !bStop {
					out = append(out, a+b)
					i += 2
					continue
				}
			}
		}
		out = append(out, tokens[i])
		i++
	}
	return strings.Join(out, " ")
}

type digitFragment struct {
	prefix, digit, suffix string
	ok                     bool
}

func parseDigitFragment(tok string) digitFragment {
	m := geomSingleDigitFragRe.FindStringSubmatch(tok)
	if m == nil {
		return digitFragment{}
	}
	return digitFragment{prefix: m[1], digit: m[2], suffix: m[3], ok: true}
}

// mergeSingleDigitRuns re-joins multi-digit numbers that got split into
// single-digit tokens (pdf-port/01).
func mergeSingleDigitRuns(text string) string {
	tokens := strings.Fields(text)
	if len(tokens) == 0 {
		return text
	}
	var out []string
	i := 0
	for i < len(tokens) {
		frag := parseDigitFragment(tokens[i])
		if !frag.ok {
			out = append(out, tokens[i])
			i++
			continue
		}
		digits := frag.digit
		j := i + 1
		for frag.suffix == "" && j < len(tokens) {
			next := parseDigitFragment(tokens[j])
			if !next.ok || next.prefix != "" {
				break
			}
			digits += next.digit
			frag = next
			j++
		}
		if len(digits) >= 2 {
			out = append(out, parseDigitFragment(tokens[i]).prefix+digits+frag.suffix)
			i = j
			continue
		}
		out = append(out, tokens[i])
		i++
	}
	return strings.Join(out, " ")
}

func firstNonSpaceChar(s string) rune {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return r
		}
	}
	return 0
}

func lastNonSpaceChar(s string) rune {
	runes := []rune(s)
	for i := len(runes) - 1; i >= 0; i-- {
		if !unicode.IsSpace(runes[i]) {
			return runes[i]
		}
	}
	return 0
}

func secondNonSpaceChar(s string) rune {
	count := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			count++
			if count == 2 {
				return r
			}
		}
	}
	return 0
}

func isChineseRune(c rune) bool {
	return (c >= 0x4E00 && c <= 0x9FFF) || (c >= 0x3400 && c <= 0x4DBF)
}

func isAsciiLetterOrDigit(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// needSpace decides whether a space is needed between the end of a and the
// start of b (pdf-port/01).
func needSpace(a, b string) bool {
	left := lastNonSpaceChar(a)
	right := firstNonSpaceChar(b)
	if left == 0 || right == 0 {
		return false
	}
	if isAsciiLetterOrDigit(left) && isAsciiLetterOrDigit(right) {
		return true
	}
	if isChineseRune(left) && isAsciiLetterOrDigit(right) {
		return true
	}
	if isAsciiLetterOrDigit(left) && isChineseRune(right) {
		return true
	}
	return false
}

// shouldInsertSpaceByGeometry decides whether adjacent fragments should be
// joined with a space, based on both text adjacency and gap geometry
// (pdf-port/01). leftRect/rightRect may be nil.
func shouldInsertSpaceByGeometry(leftText, rightText string, leftRect, rightRect *geom.Box) bool {
	if !needSpace(leftText, rightText) {
		return false
	}
	if leftRect == nil || rightRect == nil {
		return true
	}
	gap := boxLLX(*rightRect) - boxURX(*leftRect)
	if gap <= 0.8 {
		return false
	}
	left := lastNonSpaceChar(leftText)
	right := firstNonSpaceChar(rightText)
	if unicode.IsDigit(left) && unicode.IsDigit(right) && gap < 3.0 {
		return false
	}
	if unicode.IsLetter(left) && unicode.IsLetter(right) && gap < 2.0 {
		return false
	}
	return true
}

func isCommonShortWord(w string, cfg Config) bool {
	_, ok := cfg.ShortStopwords[strings.ToLower(w)]
	return ok
}

func isPageNumberBlockText(raw string) bool {
	return pageNumberBlockRe.MatchString(strings.TrimSpace(raw))
}
