package desk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
)

// The page reads the API's JSON as JavaScript wrote it, and the recorded
// sessions compare it as text, so the order of an object's keys is part of the
// contract. Nothing here is ever a Go map.

// kv is one property of an object.
type kv struct {
	K string
	V any
}

// obj is a JSON object with its properties in the order given: what an object
// literal is in the TypeScript, and what a request body's object is once read.
type obj []kv

func (o obj) get(key string) (any, bool) {
	for _, p := range o {
		if p.K == key {
			return p.V, true
		}
	}
	return nil, false
}

// set replaces the value under key where it stands, or appends it, as
// assigning to a JavaScript property does.
func (o *obj) set(key string, value any) {
	for i := range *o {
		if (*o)[i].K == key {
			(*o)[i].V = value
			return
		}
	}
	*o = append(*o, kv{key, value})
}

// MarshalJSON writes the properties in order. A nil obj is an empty object.
func (o obj) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	if err := appendJSON(&b, o); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// appendJSON writes v as JSON.stringify does: a number that is not finite is
// null, and negative zero is 0.
func appendJSON(b *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case float64:
		switch {
		case math.IsNaN(t) || math.IsInf(t, 0):
			b.WriteString("null")
		case t == 0:
			b.WriteByte('0')
		default:
			return appendEncoded(b, t)
		}
	case []any:
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := appendJSON(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case obj:
		b.WriteByte('{')
		for i, p := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := appendEncoded(b, p.K); err != nil {
				return err
			}
			b.WriteByte(':')
			if err := appendJSON(b, p.V); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return appendEncoded(b, v)
	}
	return nil
}

func appendEncoded(b *bytes.Buffer, v any) error {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	b.Truncate(b.Len() - 1) // Encode ends each value with a newline
	return nil
}

// encodeJSON is JSON.stringify(v).
func encodeJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := appendJSON(&b, v); err != nil {
		return nil, err
	}
	return plainEscapes(b.Bytes()), nil
}

// The escapes Go writes that JSON.stringify does not: the two line separators
// always, and < > & wherever a value wrote itself with the standard encoder.
var goOnlyEscapes = map[string]string{
	"2028": "\u2028", "2029": "\u2029", "003c": "<", "003e": ">", "0026": "&",
}

// plainEscapes writes those characters as themselves, as JavaScript does.
func plainEscapes(text []byte) []byte {
	if !bytes.Contains(text, []byte(`\u202`)) && !bytes.Contains(text, []byte(`\u00`)) {
		return text
	}
	out := make([]byte, 0, len(text))
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c != '\\' || i+1 >= len(text) {
			out = append(out, c)
			continue
		}
		// A backslash only ever appears inside a string, starting an escape.
		if i+5 < len(text) && text[i+1] == 'u' {
			if plain, found := goOnlyEscapes[string(text[i+2:i+6])]; found {
				out = append(out, plain...)
				i += 5
				continue
			}
		}
		out = append(out, c, text[i+1])
		i++
	}
	return out
}

// jsonSyntaxError is a body that is not JSON: where the parser gave up, and why.
type jsonSyntaxError struct {
	offset  int
	message string
}

func (e *jsonSyntaxError) Error() string { return e.message }

// parseJSON is JSON.parse(text): nil, bool, float64, string, []any or obj. A
// key sent twice keeps its first place and its last value; a number too large
// for a float64 is infinite.
func parseJSON(text []byte) (any, error) {
	if !json.Valid(text) {
		var probe any
		err := json.Unmarshal(text, &probe)
		syntax := &jsonSyntaxError{offset: len(text), message: "invalid JSON"}
		if err != nil {
			syntax.message = err.Error()
			if se, ok := err.(*json.SyntaxError); ok {
				syntax.offset = int(se.Offset)
			}
		}
		return nil, syntax
	}
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	value, err := parseValue(dec)
	if err != nil {
		return nil, &jsonSyntaxError{offset: int(dec.InputOffset()), message: err.Error()}
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, &jsonSyntaxError{offset: int(dec.InputOffset()), message: "unexpected data after the JSON value"}
	}
	return value, nil
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '[' {
			out := []any{}
			for dec.More() {
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
			_, err := dec.Token()
			return out, err
		}
		out := obj{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("object key is not a string")
			}
			v, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			out.set(name, v)
		}
		_, err := dec.Token()
		return out, err
	case json.Number:
		// Out of range reads as infinity or zero, as JSON.parse reads it.
		f, _ := strconv.ParseFloat(string(t), 64)
		return f, nil
	}
	return tok, nil
}
