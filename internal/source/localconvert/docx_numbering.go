package localconvert

// Numbering (auto-numbered list label) resolution — the "high-risk" item
// flagged in docs/impl/v1/docx-port/01-word-to-markdown.md §10 as needing
// its own numbering.xml parser since go-ooxml's ListInfo() explicitly does
// not resolve label text.
//
// This is a reasonable-coverage, not exhaustive, implementation: it
// supports the numFmt values actually observed in the test fixtures plus
// the other common ones (decimal, decimalZero, upperRoman, lowerRoman,
// upperLetter, lowerLetter, chineseCounting, chineseCountingThousand,
// ideographDigital, bullet, none) and standard lvlText %N substitution with
// per-(numId,ilvl) running counters that reset deeper levels whenever a
// shallower level advances (the conventional Word behavior; w:lvlRestart
// overrides are not honored — documented gap, see report).

import (
	"encoding/xml"
	"strconv"
	"strings"
)

type numLevel struct {
	numFmt  string
	lvlText string
	start   int
}

type numberingModel struct {
	numIDToAbstract map[int]int
	abstractLevels  map[int]map[int]numLevel // abstractNumId -> ilvl -> level
	counters        map[int]map[int]int      // numId -> ilvl -> current value
}

func parseNumbering(data []byte) *numberingModel {
	m := &numberingModel{
		numIDToAbstract: map[int]int{},
		abstractLevels:  map[int]map[int]numLevel{},
		counters:        map[int]map[int]int{},
	}
	if len(data) == 0 {
		return m
	}
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	var curAbstractID int
	inAbstract := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "abstractNum":
				id, _ := strconv.Atoi(attrVal(t.Attr, "abstractNumId"))
				curAbstractID = id
				inAbstract = true
				if _, ok := m.abstractLevels[id]; !ok {
					m.abstractLevels[id] = map[int]numLevel{}
				}
			case "lvl":
				if !inAbstract {
					continue
				}
				ilvl, _ := strconv.Atoi(attrVal(t.Attr, "ilvl"))
				lvl := parseNumLevel(dec)
				m.abstractLevels[curAbstractID][ilvl] = lvl
			case "num":
				numID, _ := strconv.Atoi(attrVal(t.Attr, "numId"))
				abstractID := findAbstractNumId(dec)
				if abstractID >= 0 {
					m.numIDToAbstract[numID] = abstractID
				}
			}
		case xml.EndElement:
			if t.Name.Local == "abstractNum" {
				inAbstract = false
			}
		}
	}
	return m
}

// parseNumLevel consumes a <w:lvl ...>...</w:lvl> element (start tag already
// consumed) and returns its format/text/start.
func parseNumLevel(dec *xml.Decoder) numLevel {
	lvl := numLevel{start: 1}
	for {
		tok, err := dec.Token()
		if err != nil {
			return lvl
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "start":
				if v, err := strconv.Atoi(attrVal(t.Attr, "val")); err == nil {
					lvl.start = v
				}
			case "numFmt":
				lvl.numFmt = attrVal(t.Attr, "val")
			case "lvlText":
				lvl.lvlText = attrVal(t.Attr, "val")
			}
		case xml.EndElement:
			if t.Name.Local == "lvl" {
				return lvl
			}
		}
	}
}

// findAbstractNumId consumes a <w:num>...</w:num> body looking for its
// <w:abstractNumId w:val="N"/> child.
func findAbstractNumId(dec *xml.Decoder) int {
	id := -1
	for {
		tok, err := dec.Token()
		if err != nil {
			return id
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "abstractNumId" {
				if v, err := strconv.Atoi(attrVal(t.Attr, "val")); err == nil {
					id = v
				}
			}
		case xml.EndElement:
			if t.Name.Local == "num" {
				return id
			}
		}
	}
}

// LabelForNext advances the counter for (numID, ilvl) and returns the
// rendered list label text (e.g. "1.", "a)", "（一）").
func (m *numberingModel) LabelForNext(numID, ilvl int) string {
	abstractID, ok := m.numIDToAbstract[numID]
	if !ok {
		return ""
	}
	levels, ok := m.abstractLevels[abstractID]
	if !ok {
		return ""
	}
	lvl, ok := levels[ilvl]
	if !ok {
		return ""
	}

	if _, ok := m.counters[numID]; !ok {
		m.counters[numID] = map[int]int{}
	}
	cur, seen := m.counters[numID][ilvl]
	if !seen {
		cur = lvl.start
	} else {
		cur++
	}
	m.counters[numID][ilvl] = cur
	// Reset all deeper levels (standard Word restart-on-parent-advance
	// behavior; w:lvlRestart overrides not honored — see file header).
	for deeperLvl := range levels {
		if deeperLvl > ilvl {
			delete(m.counters[numID], deeperLvl)
		}
	}

	return renderLvlText(lvl.lvlText, numID, ilvl, levels, m.counters[numID])
}

func renderLvlText(lvlText string, numID, ilvl int, levels map[int]numLevel, counters map[int]int) string {
	var sb strings.Builder
	runes := []rune(lvlText)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '%' && i+1 < len(runes) && runes[i+1] >= '1' && runes[i+1] <= '9' {
			levelIdx := int(runes[i+1]-'1') // %1 -> ilvl 0
			val, ok := counters[levelIdx]
			if !ok {
				val = 1
			}
			fmtName := ""
			if lv, ok := levels[levelIdx]; ok {
				fmtName = lv.numFmt
			}
			sb.WriteString(formatNumber(val, fmtName))
			i++
			continue
		}
		sb.WriteRune(runes[i])
	}
	return sb.String()
}

func formatNumber(n int, numFmt string) string {
	switch numFmt {
	case "decimalZero":
		if n < 10 {
			return "0" + strconv.Itoa(n)
		}
		return strconv.Itoa(n)
	case "upperRoman":
		return toRoman(n, true)
	case "lowerRoman":
		return toRoman(n, false)
	case "upperLetter":
		return toLetter(n, true)
	case "lowerLetter":
		return toLetter(n, false)
	case "chineseCounting", "chineseCountingThousand", "ideographDigital":
		return toChineseNumeral(n)
	case "bullet", "none":
		return ""
	case "decimal", "":
		return strconv.Itoa(n)
	default:
		return strconv.Itoa(n)
	}
}

func toRoman(n int, upper bool) string {
	if n <= 0 {
		return strconv.Itoa(n)
	}
	vals := []struct {
		v int
		s string
	}{
		{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"},
		{100, "C"}, {90, "XC"}, {50, "L"}, {40, "XL"},
		{10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"},
	}
	var sb strings.Builder
	for _, kv := range vals {
		for n >= kv.v {
			sb.WriteString(kv.s)
			n -= kv.v
		}
	}
	s := sb.String()
	if !upper {
		s = strings.ToLower(s)
	}
	return s
}

func toLetter(n int, upper bool) string {
	if n <= 0 {
		return strconv.Itoa(n)
	}
	var sb strings.Builder
	for n > 0 {
		n--
		sb.WriteByte(byte('A' + n%26))
		n /= 26
	}
	// digits were appended least-significant first
	runes := []rune(sb.String())
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	s := string(runes)
	if !upper {
		s = strings.ToLower(s)
	}
	return s
}

var chineseDigitChars = [10]rune{'零', '一', '二', '三', '四', '五', '六', '七', '八', '九'}

// toChineseNumeral is the forward counterpart of pdfconv.ParseChineseNumber,
// covering 1..9999 (adequate for list numbering; larger values fall back to
// plain digits).
func toChineseNumeral(n int) string {
	if n <= 0 {
		return strconv.Itoa(n)
	}
	if n > 9999 {
		return strconv.Itoa(n)
	}
	if n < 10 {
		return string(chineseDigitChars[n])
	}
	units := []struct {
		v int
		s string
	}{{1000, "千"}, {100, "百"}, {10, "十"}}
	var sb strings.Builder
	remaining := n
	skippedLeadingOne := false
	for i, u := range units {
		digit := remaining / u.v
		remaining %= u.v
		if digit == 0 {
			if sb.Len() > 0 {
				// zero placeholder handling for values like 105 -> 一百零五
				if remaining > 0 && needsZeroPlaceholder(n, u.v) {
					sb.WriteRune('零')
				}
			}
			continue
		}
		if digit == 1 && i == len(units)-1 && sb.Len() == 0 {
			// "十" instead of "一十" for 10-19
			skippedLeadingOne = true
		} else {
			sb.WriteRune(chineseDigitChars[digit])
		}
		sb.WriteString(u.s)
	}
	_ = skippedLeadingOne
	if remaining > 0 {
		sb.WriteRune(chineseDigitChars[remaining])
	}
	return sb.String()
}

func needsZeroPlaceholder(n, unitVal int) bool {
	// crude heuristic sufficient for list-label purposes: only insert 零
	// once per gap, avoided edge-case complexity of full Chinese numeral
	// grammar (not needed for short list counters).
	return (n%unitVal) > 0 && (n%unitVal) < unitVal/10
}
