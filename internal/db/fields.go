package db

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/satiyap/banyan/internal/pycompat"
)

// Field is one named value: nil, bool, int64, float64, string, or (in a batch
// item) a nested Fields or []any.
type Field struct {
	Name  string
	Value any
}

// F is a Field, for building Fields by hand.
func F(name string, value any) Field { return Field{Name: name, Value: value} }

// Fields is a set of named values in the order given, as the keyword
// arguments and JSON objects of the TypeScript are. The order is kept because
// it decides the column order of the SQL written from it and the text of a
// batch's items column.
type Fields []Field

// Get is the value under name, and whether it was given.
func (f Fields) Get(name string) (any, bool) {
	for _, field := range f {
		if field.Name == name {
			return field.Value, true
		}
	}
	return nil, false
}

// Has reports whether name was given.
func (f Fields) Has(name string) bool {
	_, ok := f.Get(name)
	return ok
}

// Set replaces the value under name where it stands, or appends it.
func (f *Fields) Set(name string, value any) {
	for i := range *f {
		if (*f)[i].Name == name {
			(*f)[i].Value = value
			return
		}
	}
	*f = append(*f, Field{name, value})
}

// Delete removes name.
func (f *Fields) Delete(name string) {
	for i := range *f {
		if (*f)[i].Name == name {
			*f = append((*f)[:i:i], (*f)[i+1:]...)
			return
		}
	}
}

func (f Fields) clone() Fields { return append(make(Fields, 0, len(f)+3), f...) }

func (f Fields) names() []string {
	out := make([]string, len(f))
	for i, field := range f {
		out[i] = field.Name
	}
	return out
}

func (f Fields) values() []any {
	out := make([]any, len(f))
	for i, field := range f {
		out[i] = field.Value
	}
	return out
}

// MarshalJSON writes a JSON object with the keys in order; nil is null.
func (f Fields) MarshalJSON() ([]byte, error) {
	if f == nil {
		return []byte("null"), nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	b.WriteByte('{')
	for i, field := range f {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := enc.Encode(field.Name); err != nil {
			return nil, err
		}
		b.Truncate(b.Len() - 1) // Encode ends each value with a newline
		b.WriteByte(':')
		if err := enc.Encode(field.Value); err != nil {
			return nil, err
		}
		b.Truncate(b.Len() - 1)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// UnmarshalJSON reads a JSON object, keeping the order of its keys. A whole
// number becomes int64, any other number float64, an object Fields, an array []any.
func (f *Fields) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := decodeValue(dec)
	if err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("unexpected data after JSON value")
	}
	switch v := value.(type) {
	case nil:
		*f = nil
	case Fields:
		*f = v
	default:
		return fmt.Errorf("JSON value is not an object")
	}
	return nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '[' {
			out := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
			_, err := dec.Token()
			return out, err
		}
		out := Fields{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			out.Set(key.(string), v)
		}
		_, err := dec.Token()
		return out, err
	case json.Number:
		return number(t), nil
	}
	return tok, nil
}

func number(n json.Number) any {
	if !strings.ContainsAny(string(n), ".eE") {
		if i, err := strconv.ParseInt(string(n), 10, 64); err == nil {
			return i
		}
	}
	f, _ := strconv.ParseFloat(string(n), 64)
	return f
}

// -- values as JavaScript and Python treat them ---------------------------

// The largest whole number a float64 holds exactly (Number.MAX_SAFE_INTEGER).
const maxSafeInteger = 1<<53 - 1

// whole is v as an int64 when v is an integer of any Go type.
func whole(v any) (int64, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return int64(rv.Uint()), true
	}
	return 0, false
}

// bind is a value as it is given to SQLite, the way Python's sqlite3 binds
// it: True and False as 1 and 0, and a whole number as an INTEGER.
func bind(value any, position int) (any, error) {
	switch v := value.(type) {
	case nil, string, int64, []byte:
		return v, nil
	case bool:
		if v {
			return int64(1), nil
		}
		return int64(0), nil
	case float64:
		if v == math.Trunc(v) && math.Abs(v) <= maxSafeInteger {
			return int64(v), nil
		}
		return v, nil
	case float32:
		return bind(float64(v), position)
	case json.Number:
		return bind(number(v), position)
	}
	if i, ok := whole(value); ok {
		return i, nil
	}
	return nil, pycompat.TypeError("Provided value cannot be bound to SQLite parameter %d.", position)
}

// truthy is JavaScript's truthiness.
func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0 && !math.IsNaN(v)
	}
	if i, ok := whole(value); ok {
		return i != 0
	}
	return true
}

// same is JavaScript's === for the values a column or an argument holds.
func same(a, b any) bool {
	if x, ok := a.(string); ok {
		y, ok := b.(string)
		return ok && x == y
	}
	if x, ok := a.(bool); ok {
		y, ok := b.(bool)
		return ok && x == y
	}
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	x, xok := asFloat(a)
	y, yok := asFloat(b)
	return xok && yok && x == y
}

func asFloat(v any) (float64, bool) {
	if f, ok := v.(float64); ok {
		return f, true
	}
	if i, ok := whole(v); ok {
		return float64(i), true
	}
	return 0, false
}

// jsNumber is String(n) for a JavaScript number.
func jsNumber(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "Infinity"
	case math.IsInf(v, -1):
		return "-Infinity"
	case v == 0:
		return "0"
	}
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	// Shortest digits that read back as v, and the decimal exponent.
	mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(v, 'e', -1, 64), "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	e, _ := strconv.Atoi(exponent)
	k, n := len(digits), e+1
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}
	out := digits[:1]
	if k > 1 {
		out += "." + digits[1:]
	}
	if n-1 < 0 {
		return sign + out + "e-" + strconv.Itoa(1-n)
	}
	return sign + out + "e+" + strconv.Itoa(n-1)
}

// text is a value inside a JavaScript template string.
func text(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return jsNumber(v)
	case []byte:
		return string(v)
	}
	if i, ok := whole(value); ok {
		return strconv.FormatInt(i, 10)
	}
	return fmt.Sprint(value)
}

// repr is Python's repr() of a name.
func repr(value any) string { return pycompat.Repr(text(value)) }

// or is JavaScript's a || b.
func or(a, b any) any {
	if truthy(a) {
		return a
	}
	return b
}

func pyJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r >= 0x10000:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(b, `\u%04x\u%04x`, hi, lo)
		case r < 0x20 || r > 0x7e:
			fmt.Fprintf(b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

func pyJSONValue(b *strings.Builder, value any) {
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
		return
	case bool:
		b.WriteString(strconv.FormatBool(v))
		return
	case string:
		pyJSONString(b, v)
		return
	case float64:
		b.WriteString(jsNumber(v))
		return
	case json.Number:
		pyJSONValue(b, number(v))
		return
	case Fields:
		if v == nil {
			b.WriteString("null")
			return
		}
		b.WriteByte('{')
		for i, field := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			pyJSONString(b, field.Name)
			b.WriteString(": ")
			pyJSONValue(b, field.Value)
		}
		b.WriteByte('}')
		return
	}
	if i, ok := whole(value); ok {
		b.WriteString(strconv.FormatInt(i, 10))
		return
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		b.WriteByte('[')
		for i := 0; i < rv.Len(); i++ {
			if i > 0 {
				b.WriteString(", ")
			}
			pyJSONValue(b, rv.Index(i).Interface())
		}
		b.WriteByte(']')
	case reflect.Map:
		// A Go map has no order of its own; its keys are written sorted.
		keys := make([]string, 0, rv.Len())
		byKey := map[string]reflect.Value{}
		for _, k := range rv.MapKeys() {
			name := fmt.Sprint(k.Interface())
			keys = append(keys, name)
			byKey[name] = rv.MapIndex(k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, name := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			pyJSONString(b, name)
			b.WriteString(": ")
			pyJSONValue(b, byKey[name].Interface())
		}
		b.WriteByte('}')
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			b.WriteString("null")
		} else {
			pyJSONValue(b, rv.Elem().Interface())
		}
	default:
		pyJSONString(b, fmt.Sprint(value))
	}
}

// pyJSONDumps is Python's json.dumps with its defaults — ', ' and ': '
// separators, non-ASCII escaped — so the items column holds the same text
// whichever desk wrote it.
func pyJSONDumps(value any) string {
	var b strings.Builder
	pyJSONValue(&b, value)
	return b.String()
}
