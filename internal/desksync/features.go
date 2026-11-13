package desksync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Features is what a server says it can do, kept in the order it said it so
// the page is handed the same object.
type Features struct {
	names  []string
	values map[string]json.RawMessage
}

// Has reports whether the server offers a feature.
func (f Features) Has(name string) bool {
	raw := f.values[name]
	switch string(raw) {
	case "", "false", "null", `""`:
		return false
	}
	var number float64
	if isJSONNumber(raw) && json.Unmarshal(raw, &number) == nil {
		return number != 0
	}
	return true
}

// Names lists the features in the order the server gave them.
func (f Features) Names() []string { return append([]string(nil), f.names...) }

func (f Features) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, name := range f.names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(jsQuote(name))
		b.WriteByte(':')
		b.Write(f.values[name])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (f *Features) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return errors.New("features must be an object")
	}
	f.names, f.values = nil, map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, _ := token.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, value); err != nil {
			return err
		}
		if _, seen := f.values[name]; !seen {
			f.names = append(f.names, name)
		}
		f.values[name] = compact.Bytes()
	}
	_, err := decoder.Token()
	return err
}

// jsQuote writes a string the way JSON.stringify does.
func jsQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
