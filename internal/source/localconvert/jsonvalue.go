package localconvert

import (
	"strconv"
	"strings"
)

// jsonKV/jsonObj/jsonArr/jsonDouble implement an ordered-key JSON value tree
// rendered with Jackson's DefaultPrettyPrinter layout (space before/after
// colon, 2-space indent, object-array items inlined as "[ {" ... "} ]"),
// matching byte-for-byte the shape FileView (Aspose + Jackson) produces —
// required so Unit extraction and downstream normalize.go regexes that were
// tuned against FileView output keep working unchanged.
type jsonKV struct {
	key string
	val interface{} // nil, string, int, jsonDouble, jsonObj, jsonArr
}

type jsonObj []jsonKV

type jsonArr []interface{}

// jsonDouble marks a float64 that must always render with a decimal point
// (e.g. 100.0, not 100), mirroring Jackson's serialization of a Java Double.
type jsonDouble float64

func (o jsonObj) set(key string, val interface{}) jsonObj {
	return append(o, jsonKV{key: key, val: val})
}

// MarshalJackson renders a root object using the Jackson pretty-print layout.
func MarshalJackson(root jsonObj) string {
	var b strings.Builder
	writeObject(&b, root, 0)
	return b.String()
}

func writeValue(b *strings.Builder, v interface{}, indent int) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case string:
		writeJSONString(b, x)
	case int:
		b.WriteString(strconv.Itoa(x))
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case jsonDouble:
		b.WriteString(formatJavaDouble(float64(x)))
	case jsonObj:
		writeObject(b, x, indent)
	case jsonArr:
		writeArray(b, x, indent)
	default:
		b.WriteString("null")
	}
}

func writeObject(b *strings.Builder, o jsonObj, indent int) {
	if len(o) == 0 {
		b.WriteString("{ }")
		return
	}
	b.WriteString("{\n")
	child := indent + 1
	for i, f := range o {
		b.WriteString(strings.Repeat("  ", child))
		writeJSONString(b, f.key)
		b.WriteString(" : ")
		writeValue(b, f.val, child)
		if i != len(o)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString(strings.Repeat("  ", indent))
	b.WriteString("}")
}

func writeArray(b *strings.Builder, a jsonArr, indent int) {
	if len(a) == 0 {
		b.WriteString("[ ]")
		return
	}
	allObj := true
	for _, e := range a {
		if _, ok := e.(jsonObj); !ok {
			allObj = false
			break
		}
	}
	b.WriteString("[ ")
	for i, e := range a {
		if allObj {
			writeObject(b, e.(jsonObj), indent)
		} else {
			writeValue(b, e, indent)
		}
		if i != len(a)-1 {
			b.WriteString(", ")
		}
	}
	b.WriteString(" ]")
}

func writeJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				b.WriteString(strconv.QuoteToASCII(string(r)))
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// formatJavaDouble mirrors Jackson's serialization of a Java double: shortest
// round-tripping decimal representation, always with a fractional part
// (e.g. "100.0" not "100").
func formatJavaDouble(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}
