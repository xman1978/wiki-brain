package localconvert

// OMML (Office Math Markup Language, the <m:oMath> subtree Word uses for
// equations) reader. docx_xml.go's parseParagraph previously had no case for
// "oMath"/"oMathPara", so the decoder fell through and walked into the math
// subtree like any other unhandled element — and because OMML's text-run
// element <m:t> shares the same Local name as Word's own <w:t> (namespace
// prefix differs, Local name doesn't), every <m:t> got scooped up by the
// existing case "t" and flattened onto the paragraph text with no structure
// at all: a fraction's numerator and denominator ran together with no "/"
// between them (e.g. "决算成本−预算成本预算成本" for
// (决算成本−预算成本)/预算成本).
//
// This file linearizes the common OMML structures into plain text instead:
// fractions as "(num)/(den)", scripts as base^sup / base_sub, radicals as
// "n√(x)", delimiter groups with their actual begChr/endChr/sepChr,
// n-ary operators (∑, ∏, ∫, ...) with their sub/sup, function application as
// "name(arg)", bar/accent/group-mark annotations, limits, equation arrays
// and matrices. Anything not recognized is skipped rather than flattened,
// to avoid silently reintroducing the same kind of corruption for a
// structure this reader doesn't understand yet.

import (
	"encoding/xml"
	"strings"
)

// mathStructural is the set of OMML element Local names treated as a nested
// math structure (as opposed to a run or an unrecognized element to skip)
// when encountered inside a generic argument sequence (readOMathArgs).
var mathStructural = map[string]bool{
	"f": true, "sSup": true, "sSub": true, "sSubSup": true,
	"rad": true, "d": true, "nary": true, "func": true,
	"bar": true, "acc": true, "groupChr": true,
	"limLow": true, "limUpp": true, "eqArr": true, "m": true, "box": true,
	"oMath": true,
}

// readOMathPara consumes an <m:oMathPara>...</m:oMathPara> subtree (start
// element already consumed) and returns the linearized text of every
// <m:oMath> block inside it, joined with a space — oMathPara can hold
// multiple oMath runs on one display line (e.g. "x = 1, y = 2").
func readOMathPara(dec *xml.Decoder) (string, error) {
	var parts []string
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "oMath" {
				s, err := readOMathArgs(dec, "oMath")
				if err != nil {
					return "", err
				}
				parts = append(parts, s)
			} else if err := skipElement(dec); err != nil {
				return "", err
			}
		case xml.EndElement:
			if t.Name.Local == "oMathPara" {
				return strings.Join(parts, " "), nil
			}
		}
	}
}

// readOMathArgs consumes the content of a math "argument" container whose
// start tag has already been read — <m:oMath>, <m:e>, <m:num>, <m:den>,
// <m:sub>, <m:sup>, <m:deg>, <m:lim>, <m:fName> all share this shape: a
// sequence of runs (<m:r>) and/or nested structural elements, concatenated
// in document order. closeName is the Local name of the container itself,
// used to recognize its own end tag.
func readOMathArgs(dec *xml.Decoder, closeName string) (string, error) {
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Local == "r":
				s, err := readOMathRun(dec)
				if err != nil {
					return "", err
				}
				sb.WriteString(s)
			case mathStructural[t.Name.Local]:
				s, err := readOMathStruct(dec, t.Name.Local)
				if err != nil {
					return "", err
				}
				sb.WriteString(s)
			default:
				// Property elements (fPr, sSupPr, ctrlPr, ...) and anything
				// else not part of the argument stream.
				if err := skipElement(dec); err != nil {
					return "", err
				}
			}
		case xml.EndElement:
			if t.Name.Local == closeName {
				return sb.String(), nil
			}
		}
	}
}

// readOMathRun consumes an <m:r>...</m:r> math run (start tag already
// consumed) and returns its text content from any <m:t> children.
func readOMathRun(dec *xml.Decoder) (string, error) {
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				text, err := readCharData(dec)
				if err != nil {
					return "", err
				}
				sb.WriteString(text)
			} else if err := skipElement(dec); err != nil {
				return "", err
			}
		case xml.EndElement:
			if t.Name.Local == "r" {
				return sb.String(), nil
			}
		}
	}
}

// readOMathStruct dispatches a structural OMML element (start tag already
// consumed, Local name given by name) to its dedicated linearizer.
func readOMathStruct(dec *xml.Decoder, name string) (string, error) {
	switch name {
	case "oMath":
		return readOMathArgs(dec, "oMath")
	case "f":
		return readOMathFrac(dec)
	case "sSup":
		return readOMathScript(dec, "sSup")
	case "sSub":
		return readOMathScript(dec, "sSub")
	case "sSubSup":
		return readOMathScript(dec, "sSubSup")
	case "rad":
		return readOMathRad(dec)
	case "d":
		return readOMathDelim(dec)
	case "nary":
		return readOMathNary(dec)
	case "func":
		return readOMathFunc(dec)
	case "bar":
		return readOMathBar(dec)
	case "acc":
		return readOMathAcc(dec)
	case "groupChr":
		return readOMathGroupChr(dec)
	case "limLow":
		return readOMathLim(dec, "_")
	case "limUpp":
		return readOMathLim(dec, "^")
	case "eqArr":
		return readOMathEqArr(dec)
	case "m":
		return readOMathMatrix(dec)
	case "box":
		return readOMathBox(dec)
	default:
		if err := skipElement(dec); err != nil {
			return "", err
		}
		return "", nil
	}
}

// wrapIfMulti parens-wraps a linearized sub/superscript or limit condition
// when it is more than a single character, so linear text like "x^2n"
// (ambiguous: could re-read as x^2 followed by "n") becomes "x^(2n)".
func wrapIfMulti(s string) string {
	if len([]rune(s)) <= 1 {
		return s
	}
	return "(" + s + ")"
}

// readOMathFrac consumes <m:f>...</m:f> (fraction) and returns "(num)/(den)".
func readOMathFrac(dec *xml.Decoder) (string, error) {
	var num, den string
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "num":
				s, err := readOMathArgs(dec, "num")
				if err != nil {
					return "", err
				}
				num = s
			case "den":
				s, err := readOMathArgs(dec, "den")
				if err != nil {
					return "", err
				}
				den = s
			default: // fPr and anything else
				if err := skipElement(dec); err != nil {
					return "", err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "f" {
				return "(" + num + ")/(" + den + ")", nil
			}
		}
	}
}

// readOMathScript consumes <m:sSup>/<m:sSub>/<m:sSubSup> (kind gives the
// element's own Local name, used to recognize its end tag) and returns
// base^sup, base_sub, or base_sub^sup.
func readOMathScript(dec *xml.Decoder, kind string) (string, error) {
	var base, sub, sup string
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "e":
				s, err := readOMathArgs(dec, "e")
				if err != nil {
					return "", err
				}
				base = s
			case "sub":
				s, err := readOMathArgs(dec, "sub")
				if err != nil {
					return "", err
				}
				sub = s
			case "sup":
				s, err := readOMathArgs(dec, "sup")
				if err != nil {
					return "", err
				}
				sup = s
			default:
				if err := skipElement(dec); err != nil {
					return "", err
				}
			}
		case xml.EndElement:
			if t.Name.Local == kind {
				out := base
				if sub != "" {
					out += "_" + wrapIfMulti(sub)
				}
				if sup != "" {
					out += "^" + wrapIfMulti(sup)
				}
				return out, nil
			}
		}
	}
}

// readOMathRad consumes <m:rad>...</m:rad> (radical) and returns
// "√(x)", or "n√(x)" when a degree is present (e.g. cube root).
func readOMathRad(dec *xml.Decoder) (string, error) {
	var deg, base string
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "deg":
				s, err := readOMathArgs(dec, "deg")
				if err != nil {
					return "", err
				}
				deg = s
			case "e":
				s, err := readOMathArgs(dec, "e")
				if err != nil {
					return "", err
				}
				base = s
			default: // radPr (may hide degree) and anything else
				if err := skipElement(dec); err != nil {
					return "", err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "rad" {
				if strings.TrimSpace(deg) == "" {
					return "√(" + base + ")", nil
				}
				return deg + "√(" + base + ")", nil
			}
		}
	}
}

// readOMathDelim consumes <m:d>...</m:d> (a delimiter-wrapped group, e.g.
// "(a, b)" or "|x|") honoring its actual begChr/endChr/sepChr when the
// document customizes them, defaulting to "(", ")", ",".
func readOMathDelim(dec *xml.Decoder) (string, error) {
	beg, end, sep := "(", ")", ","
	var args []string
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "dPr":
				b, e, s, err := readDelimProps(dec)
				if err != nil {
					return "", err
				}
				if b != "" || e != "" {
					beg, end = b, e
				}
				if s != "" {
					sep = s
				}
			case "e":
				s, err := readOMathArgs(dec, "e")
				if err != nil {
					return "", err
				}
				args = append(args, s)
			default:
				if err := skipElement(dec); err != nil {
					return "", err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "d" {
				return beg + strings.Join(args, sep) + end, nil
			}
		}
	}
}

func readDelimProps(dec *xml.Decoder) (beg, end, sep string, err error) {
	for {
		tok, tErr := dec.Token()
		if tErr != nil {
			return "", "", "", tErr
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "begChr":
				beg = attrVal(t.Attr, "val")
			case "endChr":
				end = attrVal(t.Attr, "val")
			case "sepChr":
				sep = attrVal(t.Attr, "val")
			}
			if err := skipElement(dec); err != nil {
				return "", "", "", err
			}
		case xml.EndElement:
			if t.Name.Local == "dPr" {
				return beg, end, sep, nil
			}
		}
	}
}

// readOMathNary consumes <m:nary>...</m:nary> (an n-ary operator: ∑, ∏, ∫,
// ...) and returns "chr_sub^sup base", e.g. "∑_(i=1)^(n) x_i". Defaults to
// "∑" when the document doesn't override the operator character.
func readOMathNary(dec *xml.Decoder) (string, error) {
	chr := "∑"
	var sub, sup, base string
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "naryPr":
				c, err := readNaryChr(dec)
				if err != nil {
					return "", err
				}
				if c != "" {
					chr = c
				}
			case "sub":
				s, err := readOMathArgs(dec, "sub")
				if err != nil {
					return "", err
				}
				sub = s
			case "sup":
				s, err := readOMathArgs(dec, "sup")
				if err != nil {
					return "", err
				}
				sup = s
			case "e":
				s, err := readOMathArgs(dec, "e")
				if err != nil {
					return "", err
				}
				base = s
			default:
				if err := skipElement(dec); err != nil {
					return "", err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "nary" {
				out := chr
				if sub != "" {
					out += "_" + wrapIfMulti(sub)
				}
				if sup != "" {
					out += "^" + wrapIfMulti(sup)
				}
				return out + base, nil
			}
		}
	}
}

func readNaryChr(dec *xml.Decoder) (string, error) {
	chr := ""
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "chr" {
				chr = attrVal(t.Attr, "val")
			}
			if err := skipElement(dec); err != nil {
				return "", err
			}
		case xml.EndElement:
			if t.Name.Local == "naryPr" {
				return chr, nil
			}
		}
	}
}

// readOMathFunc consumes <m:func>...</m:func> (function application) and
// returns "name(arg)", e.g. "sin(x)".
func readOMathFunc(dec *xml.Decoder) (string, error) {
	var name, arg string
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "fName":
				s, err := readOMathArgs(dec, "fName")
				if err != nil {
					return "", err
				}
				name = s
			case "e":
				s, err := readOMathArgs(dec, "e")
				if err != nil {
					return "", err
				}
				arg = s
			default:
				if err := skipElement(dec); err != nil {
					return "", err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "func" {
				return name + "(" + arg + ")", nil
			}
		}
	}
}

// readOMathBar consumes <m:bar>...</m:bar> (over/underline) and returns
// "over(x)" or "under(x)" depending on the bar's position.
func readOMathBar(dec *xml.Decoder) (string, error) {
	pos := "top"
	var base string
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "barPr":
				p, err := readBarPos(dec)
				if err != nil {
					return "", err
				}
				if p != "" {
					pos = p
				}
			case "e":
				s, err := readOMathArgs(dec, "e")
				if err != nil {
					return "", err
				}
				base = s
			default:
				if err := skipElement(dec); err != nil {
					return "", err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "bar" {
				if pos == "bot" {
					return "under(" + base + ")", nil
				}
				return "over(" + base + ")", nil
			}
		}
	}
}

func readBarPos(dec *xml.Decoder) (string, error) {
	pos := ""
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "pos" {
				pos = attrVal(t.Attr, "val")
			}
			if err := skipElement(dec); err != nil {
				return "", err
			}
		case xml.EndElement:
			if t.Name.Local == "barPr" {
				return pos, nil
			}
		}
	}
}

// readOMathAcc consumes <m:acc>...</m:acc> (accent mark, e.g. hat/tilde
// over a variable) and returns the base with the accent character appended.
func readOMathAcc(dec *xml.Decoder) (string, error) {
	var chr, base string
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "accPr":
				c, err := readChrProp(dec, "accPr")
				if err != nil {
					return "", err
				}
				chr = c
			case "e":
				s, err := readOMathArgs(dec, "e")
				if err != nil {
					return "", err
				}
				base = s
			default:
				if err := skipElement(dec); err != nil {
					return "", err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "acc" {
				return base + chr, nil
			}
		}
	}
}

// readOMathGroupChr consumes <m:groupChr>...</m:groupChr> (a group mark
// like an overbrace/underbrace, optionally with a label) and returns the
// base with the group character appended.
func readOMathGroupChr(dec *xml.Decoder) (string, error) {
	var chr, base string
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "groupChrPr":
				c, err := readChrProp(dec, "groupChrPr")
				if err != nil {
					return "", err
				}
				chr = c
			case "e":
				s, err := readOMathArgs(dec, "e")
				if err != nil {
					return "", err
				}
				base = s
			default:
				if err := skipElement(dec); err != nil {
					return "", err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "groupChr" {
				return base + chr, nil
			}
		}
	}
}

// readChrProp reads a "*Pr" property element (already consumed as
// StartElement) whose only content of interest is a child <m:chr val="..."/>,
// used by both accPr and groupChrPr.
func readChrProp(dec *xml.Decoder, closeName string) (string, error) {
	chr := ""
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "chr" {
				chr = attrVal(t.Attr, "val")
			}
			if err := skipElement(dec); err != nil {
				return "", err
			}
		case xml.EndElement:
			if t.Name.Local == closeName {
				return chr, nil
			}
		}
	}
}

// readOMathLim consumes <m:limLow>/<m:limUpp> (a limit expression, e.g.
// "lim_(x→0)") and returns base+op+condition, where op is "_" for limLow
// (condition below) or "^" for limUpp (condition above).
func readOMathLim(dec *xml.Decoder, op string) (string, error) {
	var base, lim string
	closeName := "limLow"
	if op == "^" {
		closeName = "limUpp"
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "e":
				s, err := readOMathArgs(dec, "e")
				if err != nil {
					return "", err
				}
				base = s
			case "lim":
				s, err := readOMathArgs(dec, "lim")
				if err != nil {
					return "", err
				}
				lim = s
			default:
				if err := skipElement(dec); err != nil {
					return "", err
				}
			}
		case xml.EndElement:
			if t.Name.Local == closeName {
				return base + op + wrapIfMulti(lim), nil
			}
		}
	}
}

// readOMathEqArr consumes <m:eqArr>...</m:eqArr> (a stacked equation array)
// and joins its rows with the same soft-break rune docx_xml.go uses for
// <w:br> ('\v'), since this too renders as multiple lines within one
// paragraph in Word.
func readOMathEqArr(dec *xml.Decoder) (string, error) {
	var rows []string
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "e" {
				s, err := readOMathArgs(dec, "e")
				if err != nil {
					return "", err
				}
				rows = append(rows, s)
			} else if err := skipElement(dec); err != nil {
				return "", err
			}
		case xml.EndElement:
			if t.Name.Local == "eqArr" {
				return strings.Join(rows, "\v"), nil
			}
		}
	}
}

// readOMathMatrix consumes <m:m>...</m:m> (a matrix) and returns its rows
// joined by the '\v' soft-break rune, cells within a row separated by " | ".
func readOMathMatrix(dec *xml.Decoder) (string, error) {
	var rows []string
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "mr" {
				r, err := readOMathMatrixRow(dec)
				if err != nil {
					return "", err
				}
				rows = append(rows, r)
			} else if err := skipElement(dec); err != nil {
				return "", err
			}
		case xml.EndElement:
			if t.Name.Local == "m" {
				return strings.Join(rows, "\v"), nil
			}
		}
	}
}

func readOMathMatrixRow(dec *xml.Decoder) (string, error) {
	var cells []string
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "e" {
				s, err := readOMathArgs(dec, "e")
				if err != nil {
					return "", err
				}
				cells = append(cells, s)
			} else if err := skipElement(dec); err != nil {
				return "", err
			}
		case xml.EndElement:
			if t.Name.Local == "mr" {
				return strings.Join(cells, " | "), nil
			}
		}
	}
}

// readOMathBox consumes <m:box>...</m:box> (a formatting-only wrapper with
// no semantic effect on content) and returns its inner content unchanged.
func readOMathBox(dec *xml.Decoder) (string, error) {
	var base string
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "e" {
				s, err := readOMathArgs(dec, "e")
				if err != nil {
					return "", err
				}
				base = s
			} else if err := skipElement(dec); err != nil {
				return "", err
			}
		case xml.EndElement:
			if t.Name.Local == "box" {
				return base, nil
			}
		}
	}
}
