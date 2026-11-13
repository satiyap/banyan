package calibration

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Object is a JSON object with its keys in file order. Its values are nil,
// bool, float64, string, []any or *Object. A nil *Object reads as empty.
type Object struct {
	keys   []string
	values map[string]any
}

func newObject() *Object { return &Object{values: map[string]any{}} }

// set stores a value; a repeated key keeps its first position, as in a Python dict.
func (o *Object) set(key string, v any) {
	if _, seen := o.values[key]; !seen {
		o.keys = append(o.keys, key)
	}
	o.values[key] = v
}

// value is the value under key, or nil when there is none.
func (o *Object) value(key string) any {
	if o == nil {
		return nil
	}
	return o.values[key]
}

// Keys are the keys in file order.
func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	return append([]string(nil), o.keys...)
}

// Len is the number of keys.
func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Has reports whether key is present (even with a null value).
func (o *Object) Has(key string) bool {
	if o == nil {
		return false
	}
	_, ok := o.values[key]
	return ok
}

// Get is the value under key and whether the key is present.
func (o *Object) Get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.values[key]
	return v, ok
}

// Number is the value under key when it is a JSON number.
func (o *Object) Number(key string) (float64, bool) {
	n, ok := o.value(key).(float64)
	return n, ok
}

// Text is the value under key when it is a JSON string.
func (o *Object) Text(key string) (string, bool) {
	s, ok := o.value(key).(string)
	return s, ok
}

// Object is the value under key when it is a JSON object, else nil.
func (o *Object) Object(key string) *Object {
	child, _ := o.value(key).(*Object)
	return child
}

// MarshalJSON writes the keys in file order.
func (o *Object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	if err := writeJSON(&b, o); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// jsNumber is String(x) in JavaScript for a finite number, which is also how
// the recorded messages print the constants (204, 92, 76, 4.23).
func jsNumber(x float64) string {
	if x == 0 {
		return "0"
	}
	if abs := math.Abs(x); abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	// Go pads the exponent to two digits (1e-07); JavaScript does not.
	mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(x, 'e', -1, 64), "e")
	n, _ := strconv.Atoi(exponent[1:])
	return mantissa + "e" + exponent[:1] + strconv.Itoa(n)
}

// writeNumber writes a number as JSON.stringify does: null when it is not finite.
func writeNumber(b *bytes.Buffer, x float64) {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		b.WriteString("null")
		return
	}
	b.WriteString(jsNumber(x))
}

func writeJSON(b *bytes.Buffer, v any) error {
	switch v := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case float64:
		writeNumber(b, v)
	case string:
		enc := json.NewEncoder(b)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return err
		}
		b.Truncate(b.Len() - 1) // the encoder's newline
	case []any:
		b.WriteByte('[')
		for i, x := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeJSON(b, x); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case *Object:
		if v == nil {
			b.WriteString("null")
			return nil
		}
		b.WriteByte('{')
		for i, k := range v.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeJSON(b, k); err != nil {
				return err
			}
			b.WriteByte(':')
			if err := writeJSON(b, v.values[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		b.Write(data)
	}
	return nil
}
