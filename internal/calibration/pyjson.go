package calibration

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/satiyap/banyan/internal/pycompat"
)

// -- the file as Python sees it ---------------------------------------------
// The validation depends on things encoding/json throws away: whether a number
// was written as an int or a float (`"version": 15.0` is refused, and `210.0`
// is quoted back as written), the order of keys, and the exact wording and
// position of a syntax error, which is shown to the person who just edited the
// file. So the file is read by a small decoder that follows CPython's `json`
// step for step, into these values: nil, bool, pyInt, pyFloatValue, string,
// []any and *Object.

// pyInt is a JSON number written without a fraction or exponent (a Python int).
// text is the literal, kept so a long int is quoted back digit for digit.
type pyInt struct {
	value float64
	text  string
}

// pyFloatValue is a JSON number written with a fraction or exponent (a Python float).
type pyFloatValue float64

// jsonDecodeError is json.JSONDecodeError, by its message.
type jsonDecodeError string

func (e jsonDecodeError) Error() string { return string(e) }

// decodeError is the JSONDecodeError for a problem at code point pos.
func decodeError(msg string, doc []rune, pos int) error {
	line, lastNewline := 1, -1
	for i := 0; i < pos && i < len(doc); i++ {
		if doc[i] == '\n' {
			line++
			lastNewline = i
		}
	}
	return jsonDecodeError(fmt.Sprintf("%s: line %d column %d (char %d)", msg, line, pos-lastNewline, pos))
}

var escapes = map[rune]rune{
	'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t',
}

func isDigit(ch rune) bool     { return ch >= '0' && ch <= '9' }
func isJSONSpace(ch rune) bool { return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' }

type decoder struct {
	doc   []rune
	depth int
}

// maxDepth is how deep lists and objects may nest. Python stops a deeper
// document with a RecursionError too, though at a depth that varies.
const maxDepth = 10000

func (d *decoder) enter() error {
	if d.depth++; d.depth > maxDepth {
		return &pycompat.Error{Type: "RecursionError", Message: "maximum recursion depth exceeded"}
	}
	return nil
}

// at is the code point at i, or -1 past either end.
func (d *decoder) at(i int) rune {
	if i < 0 || i >= len(d.doc) {
		return -1
	}
	return d.doc[i]
}

func (d *decoder) skip(i int) int {
	for isJSONSpace(d.at(i)) {
		i++
	}
	return i
}

func (d *decoder) literal(i int, word string) bool {
	for _, r := range word {
		if d.at(i) != r {
			return false
		}
		i++
	}
	return true
}

// hex4 reads the four hex digits at i, if they are there.
func (d *decoder) hex4(i int) (rune, bool) {
	var n rune
	for k := i; k < i+4; k++ {
		ch := d.at(k)
		switch {
		case isDigit(ch):
			n = n<<4 | (ch - '0')
		case ch >= 'a' && ch <= 'f':
			n = n<<4 | (ch - 'a' + 10)
		case ch >= 'A' && ch <= 'F':
			n = n<<4 | (ch - 'A' + 10)
		default:
			return 0, false
		}
	}
	return n, true
}

// scanString scans a string whose opening quote is at begin; it returns the
// string and the index after the closing quote.
func (d *decoder) scanString(begin int) (string, int, error) {
	doc, size := d.doc, len(d.doc)
	var out strings.Builder
	end := begin + 1
	for {
		next := end
		var c rune
		for ; next < size; next++ {
			c = doc[next]
			if c == '"' || c == '\\' {
				break
			}
			if c <= 0x1f {
				return "", 0, decodeError("Invalid control character at", doc, next)
			}
		}
		if next >= size {
			return "", 0, decodeError("Unterminated string starting at", doc, begin)
		}
		out.WriteString(string(doc[end:next]))
		next++
		if c == '"' {
			return out.String(), next, nil
		}
		if next == size {
			return "", 0, decodeError("Unterminated string starting at", doc, begin)
		}
		c = doc[next]
		if c != 'u' {
			plain, ok := escapes[c]
			if !ok {
				return "", 0, decodeError(`Invalid \escape`, doc, next-1)
			}
			out.WriteRune(plain)
			end = next + 1
			continue
		}
		next++
		unit, ok := d.hex4(next)
		if !ok || next+4 >= size {
			return "", 0, decodeError(`Invalid \uXXXX escape`, doc, next-1)
		}
		end = next + 4
		// A high surrogate followed by an escaped low surrogate is one character. Any
		// other surrogate cannot be held in a Go string and is written as U+FFFD.
		if unit >= 0xd800 && unit <= 0xdbff && end+6 < size && doc[end] == '\\' && doc[end+1] == 'u' {
			if low, ok := d.hex4(end + 2); ok && low >= 0xdc00 && low <= 0xdfff {
				unit = 0x10000 + ((unit-0xd800)<<10 | (low - 0xdc00))
				end += 6
			}
		}
		out.WriteRune(unit)
	}
}

func (d *decoder) scanNumber(start int) (any, int, error) {
	i := start
	if d.at(i) == '-' {
		i++
	}
	switch ch := d.at(i); {
	case ch >= '1' && ch <= '9':
		for isDigit(d.at(i)) {
			i++
		}
	case ch == '0':
		i++
	default:
		return nil, 0, decodeError("Expecting value", d.doc, start)
	}
	isFloat := false
	if d.at(i) == '.' && isDigit(d.at(i+1)) {
		isFloat = true
		i += 2
		for isDigit(d.at(i)) {
			i++
		}
	}
	if d.at(i) == 'e' || d.at(i) == 'E' {
		eStart := i
		i++
		if d.at(i) == '+' || d.at(i) == '-' {
			i++
		}
		for isDigit(d.at(i)) {
			i++
		}
		if isDigit(d.at(i - 1)) {
			isFloat = true
		} else {
			i = eStart
		}
	}
	text := string(d.doc[start:i])
	// Out of float64's range reads as ±Inf, or 0 for an underflow.
	value, _ := strconv.ParseFloat(text, 64)
	if isFloat {
		return pyFloatValue(value), i, nil
	}
	if value == 0 {
		// An int written '-0' is plain 0 in Python.
		return pyInt{value: 0, text: "0"}, i, nil
	}
	return pyInt{value: value, text: text}, i, nil
}

func (d *decoder) scanOnce(i int) (any, int, error) {
	if i >= len(d.doc) {
		return nil, 0, decodeError("Expecting value", d.doc, i)
	}
	switch d.doc[i] {
	case '"':
		return d.scanString(i)
	case '{':
		return d.scanObject(i + 1)
	case '[':
		return d.scanArray(i + 1)
	case 'n':
		if d.literal(i, "null") {
			return nil, i + 4, nil
		}
	case 't':
		if d.literal(i, "true") {
			return true, i + 4, nil
		}
	case 'f':
		if d.literal(i, "false") {
			return false, i + 5, nil
		}
	case 'N':
		if d.literal(i, "NaN") {
			return pyFloatValue(nan), i + 3, nil
		}
	case 'I':
		if d.literal(i, "Infinity") {
			return pyFloatValue(posInf), i + 8, nil
		}
	case '-':
		if d.literal(i, "-Infinity") {
			return pyFloatValue(negInf), i + 9, nil
		}
	}
	return d.scanNumber(i)
}

func (d *decoder) scanObject(i int) (any, int, error) {
	if err := d.enter(); err != nil {
		return nil, 0, err
	}
	defer func() { d.depth-- }()
	out := newObject()
	i = d.skip(i)
	if d.at(i) != '}' {
		for {
			if d.at(i) != '"' {
				return nil, 0, decodeError("Expecting property name enclosed in double quotes", d.doc, i)
			}
			key, afterKey, err := d.scanString(i)
			if err != nil {
				return nil, 0, err
			}
			i = d.skip(afterKey)
			if d.at(i) != ':' {
				return nil, 0, decodeError("Expecting ':' delimiter", d.doc, i)
			}
			value, afterValue, err := d.scanOnce(d.skip(i + 1))
			if err != nil {
				return nil, 0, err
			}
			out.set(key, value)
			i = d.skip(afterValue)
			if d.at(i) == '}' {
				break
			}
			if d.at(i) != ',' {
				return nil, 0, decodeError("Expecting ',' delimiter", d.doc, i)
			}
			comma := i
			i = d.skip(i + 1)
			if d.at(i) == '}' {
				return nil, 0, decodeError("Illegal trailing comma before end of object", d.doc, comma)
			}
		}
	}
	return out, i + 1, nil
}

func (d *decoder) scanArray(i int) (any, int, error) {
	if err := d.enter(); err != nil {
		return nil, 0, err
	}
	defer func() { d.depth-- }()
	out := []any{}
	i = d.skip(i)
	if d.at(i) != ']' {
		for {
			value, afterValue, err := d.scanOnce(i)
			if err != nil {
				return nil, 0, err
			}
			out = append(out, value)
			i = d.skip(afterValue)
			if d.at(i) == ']' {
				break
			}
			if d.at(i) != ',' {
				return nil, 0, decodeError("Expecting ',' delimiter", d.doc, i)
			}
			comma := i
			i = d.skip(i + 1)
			if d.at(i) == ']' {
				return nil, 0, decodeError("Illegal trailing comma before end of array", d.doc, comma)
			}
		}
	}
	return out, i + 1, nil
}

// pyJSONLoads is json.loads(text): same values, same errors at the same
// positions (counted in code points).
func pyJSONLoads(text string) (any, error) {
	d := &decoder{doc: []rune(text)}
	if d.at(0) == 0xfeff {
		return nil, decodeError("Unexpected UTF-8 BOM (decode using utf-8-sig)", d.doc, 0)
	}
	value, afterValue, err := d.scanOnce(d.skip(0))
	if err != nil {
		return nil, err
	}
	if end := d.skip(afterValue); end != len(d.doc) {
		return nil, decodeError("Extra data", d.doc, end)
	}
	return value, nil
}
