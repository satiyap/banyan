package calibration

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/satiyap/banyan/internal/pycompat"
)

// -- Python's view of the decoded values ------------------------------------

var (
	nan    = math.NaN()
	posInf = math.Inf(1)
	negInf = math.Inf(-1)
)

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case pyInt:
		return "int"
	case pyFloatValue:
		return "float"
	case string:
		return "str"
	case []any:
		return "list"
	}
	return "dict"
}

// numberOf is the number a value is in arithmetic (bool counts as 0/1), if it is one.
func numberOf(v any) (float64, bool) {
	switch v := v.(type) {
	case pyInt:
		return v.value, true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	case pyFloatValue:
		return float64(v), true
	}
	return 0, false
}

// truthy is bool(v).
func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case pyInt:
		return v.value != 0
	case pyFloatValue:
		return v != 0
	case string:
		return v != ""
	case []any:
		return len(v) > 0
	case *Object:
		return v.Len() > 0
	}
	return false
}

// floatRepr is repr(float).
func floatRepr(x float64) string {
	switch {
	case math.IsNaN(x):
		return "nan"
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	}
	// Go's 'e' form is already Python's: shortest digits, exponent of at least two.
	sci := strconv.FormatFloat(x, 'e', -1, 64)
	exp, _ := strconv.Atoi(sci[strings.IndexByte(sci, 'e')+1:])
	if exp < -4 || exp >= 16 {
		return sci
	}
	s := strconv.FormatFloat(x, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// pyStr is repr(v) when nested, else str(v) — how a value reads inside an f-string.
func pyStr(v any, nested bool) string {
	switch v := v.(type) {
	case nil:
		return "None"
	case bool:
		if v {
			return "True"
		}
		return "False"
	case pyInt:
		if v.text != "" {
			return v.text
		}
		return strconv.FormatFloat(v.value, 'f', -1, 64)
	case pyFloatValue:
		return floatRepr(float64(v))
	case string:
		if nested {
			return pycompat.Repr(v)
		}
		return v
	case []any:
		parts := make([]string, len(v))
		for i, x := range v {
			parts[i] = pyStr(x, true)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *Object:
		parts := make([]string, len(v.keys))
		for i, k := range v.keys {
			parts[i] = pycompat.Repr(k) + ": " + pyStr(v.values[k], true)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

// getOf is v.get(key); a TypeError (Python's AttributeError) when v is not a dict.
func getOf(v any, key string) (any, error) {
	dict, ok := v.(*Object)
	if !ok {
		return nil, pycompat.TypeError("'%s' object has no attribute 'get'", typeName(v))
	}
	return dict.value(key), nil
}

var (
	floatTextRe = regexp.MustCompile(`^[+-]?(\d+(_\d+)*\.?(\d+(_\d+)*)?|\.\d+(_\d+)*)([eE][+-]?\d+(_\d+)*)?$`)
	floatWordRe = regexp.MustCompile(`^(?i)([+-]?)(inf|infinity|nan)$`)
)

// pyFloat is float(v): numbers, bools and numeric text convert; anything else
// is refused as Python refuses it.
func pyFloat(v any) (float64, error) {
	if n, ok := numberOf(v); ok {
		return n, nil
	}
	text, ok := v.(string)
	if !ok {
		return 0, pycompat.TypeError("float() argument must be a string or a real number, not '%s'", typeName(v))
	}
	s := pycompat.NumericStrip(text)
	if floatTextRe.MatchString(s) {
		// Out of range reads as ±Inf or 0, as in Python.
		n, _ := strconv.ParseFloat(strings.ReplaceAll(s, "_", ""), 64)
		return n, nil
	}
	if word := floatWordRe.FindStringSubmatch(s); word != nil {
		switch {
		case strings.EqualFold(word[2], "nan"):
			return nan, nil
		case word[1] == "-":
			return negInf, nil
		}
		return posInf, nil
	}
	return 0, pycompat.ValueError("could not convert string to float: %s", pycompat.Repr(text))
}

// pyAdd is a + b for the few types JSON can hold.
func pyAdd(a, b any) (any, error) {
	x, xok := numberOf(a)
	y, yok := numberOf(b)
	if xok && yok {
		_, af := a.(pyFloatValue)
		_, bf := b.(pyFloatValue)
		if af || bf {
			return pyFloatValue(x + y), nil
		}
		return pyInt{value: x + y}, nil
	}
	switch a := a.(type) {
	case string:
		if b, ok := b.(string); ok {
			return a + b, nil
		}
		return nil, pycompat.TypeError(`can only concatenate str (not "%s") to str`, typeName(b))
	case []any:
		if b, ok := b.([]any); ok {
			return append(append([]any{}, a...), b...), nil
		}
		return nil, pycompat.TypeError(`can only concatenate list (not "%s") to list`, typeName(b))
	}
	return nil, pycompat.TypeError("unsupported operand type(s) for +: '%s' and '%s'", typeName(a), typeName(b))
}

// orderable is the number in a, for an ordering comparison against a float constant.
func orderable(a any, op string) (float64, error) {
	n, ok := numberOf(a)
	if !ok {
		return 0, pycompat.TypeError("'%s' not supported between instances of '%s' and 'float'", op, typeName(a))
	}
	return n, nil
}

// fixed1 is f'{x:.1f}'. Go rounds the exact value half to even, as Python does.
func fixed1(x float64) string {
	switch {
	case math.IsNaN(x):
		return "nan"
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	}
	return strconv.FormatFloat(x, 'f', 1, 64)
}

// toPlain drops the int/float distinction: every number becomes a float64.
func toPlain(v any) any {
	switch v := v.(type) {
	case pyInt:
		return v.value
	case pyFloatValue:
		return float64(v)
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = toPlain(x)
		}
		return out
	case *Object:
		out := newObject()
		for _, k := range v.keys {
			out.set(k, toPlain(v.values[k]))
		}
		return out
	}
	return v
}

// utf8Problem is the UnicodeDecodeError that bytes.decode('utf-8') raises, as
// a ValueError (its parent class), or nil when the bytes are valid UTF-8.
func utf8Problem(data []byte) error {
	for i := 0; i < len(data); {
		c := data[i]
		if c < 0x80 {
			i++
			continue
		}
		n, lo, hi := 0, byte(0x80), byte(0xbf)
		switch {
		case c < 0xc2 || c > 0xf4:
			return pycompat.ValueError("'utf-8' codec can't decode byte 0x%02x in position %d: invalid start byte", c, i)
		case c < 0xe0:
			n = 2
		case c < 0xf0:
			n = 3
			if c == 0xe0 {
				lo = 0xa0
			} else if c == 0xed {
				hi = 0x9f
			}
		default:
			n = 4
			if c == 0xf0 {
				lo = 0x90
			} else if c == 0xf4 {
				hi = 0x8f
			}
		}
		for k := 1; k < n; k++ {
			reason := ""
			if i+k >= len(data) {
				reason = "unexpected end of data"
			} else if b := data[i+k]; b < lo || b > hi {
				reason = "invalid continuation byte"
			}
			if reason == "" {
				lo, hi = 0x80, 0xbf
				continue
			}
			if k == 1 {
				return pycompat.ValueError("'utf-8' codec can't decode byte 0x%02x in position %d: %s", c, i, reason)
			}
			return pycompat.ValueError("'utf-8' codec can't decode bytes in position %d-%d: %s", i, i+k-1, reason)
		}
		i += n
	}
	return nil
}
