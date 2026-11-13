package desk

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/satiyap/banyan/internal/pycompat"
)

// A bad request is answered with HTTP 422 and a list of what was wrong, in the
// shape and wording Pydantic gives (the API was first declared as Pydantic
// models). The page reads the `msg` of each entry, and the recorded sessions in
// tests/golden/http.json fix the checks, their order and wording.
//
// Pydantic reads input in ways the page can see: it accepts "1" and 1.0 for an
// integer and "yes" for a boolean, counts string length in code points, reports
// every wrong field rather than the first, and words its errors its own way.
// Each field kind below follows those rules.

// errorDetail is one entry of FastAPI's `{"detail": [...]}`.
type errorDetail struct {
	Type  string
	Loc   []any // strings and ints
	Msg   string
	Input any
	Ctx   obj // nil when the error carries none
}

func (e errorDetail) MarshalJSON() ([]byte, error) {
	loc := e.Loc
	if loc == nil {
		loc = []any{}
	}
	out := obj{{"type", e.Type}, {"loc", loc}, {"msg", e.Msg}, {"input", e.Input}}
	if e.Ctx != nil {
		out = append(out, kv{"ctx", e.Ctx})
	}
	return out.MarshalJSON()
}

// notSent is a key the request did not send: the field's default applies, or
// it reports that it is required.
type notSentType struct{}

var notSent any = notSentType{}

// issue is one failed check, at a path inside the value being validated.
type issue struct {
	path  []any
	typ   string
	msg   string
	input any
	ctx   obj
}

// checker collects the issues of one validation.
type checker struct {
	issues []issue
}

// field validates one value. It returns the value as the model carries it:
// string, int64, bool, nil, []any or vals. False means an issue was recorded.
type field func(c *checker, path []any, v any) (any, bool)

func (c *checker) fail(path []any, typ, msg string, input any, ctx obj) (any, bool) {
	c.issues = append(c.issues, issue{path: path, typ: typ, msg: msg, input: input, ctx: ctx})
	return nil, false
}

func (c *checker) required(path []any) (any, bool) {
	return c.fail(path, "missing", "Field required", notSent, nil)
}

func under(path []any, key any) []any {
	return append(path[:len(path):len(path)], key)
}

// validate checks one value (a body, or a single path or query parameter).
// `loc` is where it sits in the request.
func validate(f field, input any, loc []any) (any, []errorDetail) {
	c := &checker{}
	value, ok := f(c, nil, input)
	if ok {
		return value, nil
	}
	details := make([]errorDetail, 0, len(c.issues))
	for _, is := range c.issues {
		in := is.input
		if is.typ == "missing" {
			// Pydantic shows the object the field is missing from.
			in = nil
			if len(is.path) > 0 {
				in = walk(input, is.path[:len(is.path)-1])
			}
		}
		details = append(details, errorDetail{
			Type: is.typ, Loc: append(append([]any{}, loc...), is.path...), Msg: is.msg, Input: in, Ctx: is.ctx,
		})
	}
	return nil, details
}

func walk(root any, path []any) any {
	at := root
	for _, key := range path {
		switch k := key.(type) {
		case string:
			o, _ := at.(obj)
			at, _ = o.get(k)
		case int:
			list, _ := at.([]any)
			if k < 0 || k >= len(list) {
				return nil
			}
			at = list[k]
		}
	}
	return at
}

// ------------------------------------------------------------------ fields

// tidy is `' '.join(v.split())`: runs of whitespace become one space, the ends are trimmed.
func tidy(v string) string {
	return strings.Join(strings.FieldsFunc(v, pycompat.IsSpace), " ")
}

func plural(n int, word string) string {
	if n == 1 {
		return strconv.Itoa(n) + " " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// dflt is what a request that leaves a field out gets.
type dflt struct{ value any }

func def(value any) *dflt { return &dflt{value} }

// pattern is matched as Pydantic matches, with Rust's regex crate: `\d` is any
// Unicode digit and `$` is the very end.
type pattern struct {
	source string
	re     *regexp.Regexp
}

func unicodeDigits(source string) *pattern {
	return &pattern{source: source, re: regexp.MustCompile(strings.ReplaceAll(source, `\d`, `\p{Nd}`))}
}

type strOptions struct {
	min, max int // 0: no limit
	pattern  *pattern
	// Absent: the field is required.
	def      *dflt
	nullable bool
	// The field validators, in the order the model declares them. They run
	// only on a value that passed.
	after []func(string) (string, error)
}

func str(o strOptions) field {
	return func(c *checker, path []any, v any) (any, bool) {
		if v == notSent {
			if o.def != nil {
				return o.def.value, true
			}
			return c.required(path)
		}
		if v == nil && o.nullable {
			return nil, true
		}
		s, ok := v.(string)
		if !ok {
			return c.fail(path, "string_type", "Input should be a valid string", v, nil)
		}
		length := utf8.RuneCountInString(s)
		if o.min != 0 && length < o.min {
			return c.fail(path, "string_too_short", "String should have at least "+plural(o.min, "character"), v,
				obj{{"min_length", o.min}})
		}
		if o.max != 0 && length > o.max {
			return c.fail(path, "string_too_long", "String should have at most "+plural(o.max, "character"), v,
				obj{{"max_length", o.max}})
		}
		if o.pattern != nil && !o.pattern.re.MatchString(s) {
			return c.fail(path, "string_pattern_mismatch", "String should match pattern '"+o.pattern.source+"'", v,
				obj{{"pattern", o.pattern.source}})
		}
		out := s
		for _, check := range o.after {
			next, err := check(out)
			if err != nil {
				return c.fail(path, "value_error", "Value error, "+err.Error(), v, obj{{"error", obj{}}})
			}
			out = next
		}
		return out, true
	}
}

var intTextRE = regexp.MustCompile(`^[+-]?[0-9]+(?:_[0-9]+)*(?:\.0+)?$`)
var trailingZerosRE = regexp.MustCompile(`\.0+$`)

// whole is a JavaScript integer as an int64; one beyond its range is held at the end of it.
func whole(n float64) int64 {
	switch {
	case n >= math.MaxInt64:
		return math.MaxInt64
	case n <= math.MinInt64:
		return math.MinInt64
	}
	return int64(n)
}

// laxInt is Pydantic's lax integer: 3, 3.0, true, "3", " 3 ", "3.0", "1_000";
// nothing with a fraction.
func laxInt(c *checker, path []any, v any) (int64, bool) {
	switch t := v.(type) {
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			c.fail(path, "finite_number", "Input should be a finite number", v, nil)
			return 0, false
		}
		if t != math.Trunc(t) {
			c.fail(path, "int_from_float", "Input should be a valid integer, got a number with a fractional part", v, nil)
			return 0, false
		}
		return whole(t), true
	case string:
		text := pycompat.Strip(t)
		if !intTextRE.MatchString(text) {
			c.fail(path, "int_parsing", "Input should be a valid integer, unable to parse string as an integer", v, nil)
			return 0, false
		}
		n, _ := strconv.ParseFloat(strings.ReplaceAll(trailingZerosRE.ReplaceAllString(text, ""), "_", ""), 64)
		return whole(n), true
	}
	c.fail(path, "int_type", "Input should be a valid integer", v, nil)
	return 0, false
}

type intOptions struct {
	ge, le   *int64
	def      *dflt
	nullable bool
}

func bound(n int64) *int64 { return &n }

func integer(o intOptions) field {
	return func(c *checker, path []any, v any) (any, bool) {
		if v == notSent {
			if o.def != nil {
				return o.def.value, true
			}
			return c.required(path)
		}
		if v == nil && o.nullable {
			return nil, true
		}
		n, ok := laxInt(c, path, v)
		if !ok {
			return nil, false
		}
		if o.ge != nil && n < *o.ge {
			return c.fail(path, "greater_than_equal", "Input should be greater than or equal to "+strconv.FormatInt(*o.ge, 10), v,
				obj{{"ge", *o.ge}})
		}
		if o.le != nil && n > *o.le {
			return c.fail(path, "less_than_equal", "Input should be less than or equal to "+strconv.FormatInt(*o.le, 10), v,
				obj{{"le", *o.le}})
		}
		return n, true
	}
}

var trueWords = map[string]bool{"1": true, "t": true, "true": true, "on": true, "y": true, "yes": true}
var falseWords = map[string]bool{"0": true, "f": true, "false": true, "off": true, "n": true, "no": true}

// boolean is Pydantic's lax boolean: true, 1, 1.0, "true", "yes", "on", "t",
// "y", "1" and their opposites.
func boolean(d *dflt) field {
	return func(c *checker, path []any, v any) (any, bool) {
		if v == notSent {
			if d != nil {
				return d.value, true
			}
			return c.required(path)
		}
		unclear := func() (any, bool) {
			return c.fail(path, "bool_parsing", "Input should be a valid boolean, unable to interpret input", v, nil)
		}
		switch t := v.(type) {
		case bool:
			return t, true
		case float64:
			if t == 0 {
				return false, true
			}
			if t == 1 {
				return true, true
			}
			if !math.IsInf(t, 0) && t == math.Trunc(t) {
				return unclear()
			}
		case string:
			word := strings.ToLower(t)
			if trueWords[word] {
				return true, true
			}
			if falseWords[word] {
				return false, true
			}
			return unclear()
		}
		return c.fail(path, "bool_type", "Input should be a valid boolean", v, nil)
	}
}

type literalOptions struct {
	def      *dflt
	nullable bool
}

func literal(values []string, o literalOptions) field {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "'" + v + "'"
	}
	expected := quoted[0]
	if len(quoted) > 1 {
		expected = strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
	}
	return func(c *checker, path []any, v any) (any, bool) {
		if v == notSent {
			if o.def != nil {
				return o.def.value, true
			}
			return c.required(path)
		}
		if v == nil && o.nullable {
			return nil, true
		}
		if s, ok := v.(string); ok {
			for _, allowed := range values {
				if s == allowed {
					return s, true
				}
			}
		}
		return c.fail(path, "literal_error", "Input should be "+expected, v, obj{{"expected", expected}})
	}
}

// -- dates ------------------------------------------------------------------
// Pydantic reads a date with the speedate library: 'YYYY-MM-DD', or a datetime
// whose time is exactly midnight, or a Unix timestamp that falls on midnight.
// What follows reproduces its answers and its wording for everything a date
// field is likely to be sent.

const secondsPerDay = 86_400

// speedate reads a number above this as milliseconds rather than seconds.
const msWatershed = 20_000_000_000

// The furthest a JavaScript Date reaches either side of 1970, in milliseconds.
const maxDateMs = 8.64e15

// A date check stops at its first problem.
var errDate = errors.New("not a date")

type dateCheck struct {
	c    *checker
	path []any
	v    any
}

func (d dateCheck) problem(msg string) (string, error) {
	d.c.fail(d.path, "date_from_datetime_parsing", "Input should be a valid date or datetime, "+msg, d.v, obj{{"error", msg}})
	return "", errDate
}

func (d dateCheck) inexact() (string, error) {
	d.c.fail(d.path, "date_from_datetime_inexact",
		"Datetimes provided to dates should have zero time - e.g. be exact dates", d.v, nil)
	return "", errDate
}

func (d dateCheck) fromTimestamp(n float64) (string, error) {
	seconds := n
	if math.Abs(n) > msWatershed {
		seconds = n / 1000
	}
	ms := math.Trunc(seconds * 1000)
	if math.IsNaN(ms) || math.Abs(ms) > maxDateMs {
		return d.problem("dates after 9999 are not supported as unix timestamps")
	}
	t := time.UnixMilli(int64(ms)).UTC()
	if t.Year() > 9999 {
		return d.problem("dates after 9999 are not supported as unix timestamps")
	}
	if t.Year() < 1 {
		return d.problem("dates before 0000 are not supported as unix timestamps")
	}
	if math.Mod(seconds, secondsPerDay) != 0 {
		return d.inexact()
	}
	return pycompat.IsoDate(t.Year(), int(t.Month()), t.Day()), nil
}

// units is a string as JavaScript indexes it: UTF-16 code units.
type units []uint16

func (u units) slice(from, to int) units {
	if from > len(u) {
		from = len(u)
	}
	if to > len(u) {
		to = len(u)
	}
	return u[from:to]
}

func (u units) digits() bool {
	for _, c := range u {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(u) > 0
}

func (u units) number() int {
	n := 0
	for _, c := range u {
		n = n*10 + int(c-'0')
	}
	return n
}

func (u units) text() string { return string(utf16.Decode(u)) }

var fractionRE = regexp.MustCompile(`^[.,]([0-9]+)`)
var zoneRE = regexp.MustCompile(`^(?:[zZ]|[+-][0-9]{2}(?::?[0-9]{2})?)$`)

// midnight reads the time after a date's separator, and says whether it is exactly midnight.
func (d dateCheck) midnight(t units) (bool, error) {
	no := func(msg string) (bool, error) {
		_, err := d.problem(msg)
		return false, err
	}
	if len(t) < 5 {
		return no("input is too short")
	}
	if !t.slice(0, 2).digits() {
		return no("invalid character in hour")
	}
	if t[2] != ':' {
		return no("invalid time separator, expected `:`")
	}
	if !t.slice(3, 5).digits() {
		return no("invalid character in minute")
	}
	hour, minute := t.slice(0, 2).number(), t.slice(3, 5).number()
	if hour > 23 {
		return no("hour value is outside expected range of 0-23")
	}
	if minute > 59 {
		return no("minute value is outside expected range of 0-59")
	}
	rest := t[5:]
	second := 0
	fraction := ""
	if len(rest) > 0 && rest[0] == ':' {
		if len(rest) < 3 {
			return no("input is too short")
		}
		if !rest.slice(1, 3).digits() {
			return no("invalid character in second")
		}
		second = rest.slice(1, 3).number()
		if second > 59 {
			return no("second value is outside expected range of 0-59")
		}
		rest = rest[3:]
		if m := fractionRE.FindStringSubmatch(rest.text()); m != nil {
			fraction = m[1]
			rest = rest[len(m[0]):] // ASCII, so one unit each
		}
	}
	if len(rest) > 0 && !zoneRE.MatchString(rest.text()) {
		return no("unexpected extra characters at the end of the input")
	}
	return hour == 0 && minute == 0 && second == 0 && strings.Trim(fraction, "0") == "", nil
}

var timestampTextRE = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?$`)
var yearDashRE = regexp.MustCompile(`^[0-9]{4}-`)

func (d dateCheck) fromText(v string) (string, error) {
	// A string of digits is a timestamp, as a number would be.
	if timestampTextRE.MatchString(v) && !yearDashRE.MatchString(v) {
		n, _ := strconv.ParseFloat(v, 64)
		return d.fromTimestamp(n)
	}
	u := units(utf16.Encode([]rune(v)))
	if len(u) < 10 {
		return d.problem("input is too short")
	}
	if !u.slice(0, 4).digits() {
		return d.problem("invalid character in year")
	}
	if u[4] != '-' {
		return d.problem("invalid date separator, expected `-`")
	}
	if !u.slice(5, 7).digits() {
		return d.problem("invalid character in month")
	}
	if u[7] != '-' {
		return d.problem("invalid date separator, expected `-`")
	}
	if !u.slice(8, 10).digits() {
		return d.problem("invalid character in day")
	}
	year, month, day := u.slice(0, 4).number(), u.slice(5, 7).number(), u.slice(8, 10).number()
	if month < 1 || month > 12 {
		return d.problem("month value is outside expected range of 1-12")
	}
	if day < 1 || day > pycompat.DaysInMonth(year, month) {
		return d.problem("day value is outside expected range")
	}
	if len(u) > 10 {
		if !strings.ContainsRune("Tt_ ", rune(u[10])) {
			return d.problem("invalid datetime separator, expected `T`, `t`, `_` or space")
		}
		exact, err := d.midnight(u[11:])
		if err != nil {
			return "", err
		}
		if !exact {
			return d.inexact()
		}
	}
	if year == 0 {
		msg := "year 0 is out of range"
		d.c.fail(d.path, "date_parsing", "Input should be a valid date in the format YYYY-MM-DD, "+msg, d.v, obj{{"error", msg}})
		return "", errDate
	}
	return pycompat.IsoDate(year, month, day), nil
}

type dateOptions struct {
	def      *dflt
	nullable bool
}

// date is a calendar date, carried as 'YYYY-MM-DD'.
func date(o dateOptions) field {
	return func(c *checker, path []any, v any) (any, bool) {
		if v == notSent {
			if o.def != nil {
				return o.def.value, true
			}
			return c.required(path)
		}
		if v == nil && o.nullable {
			return nil, true
		}
		d := dateCheck{c: c, path: path, v: v}
		var out string
		var err error
		switch t := v.(type) {
		case string:
			out, err = d.fromText(t)
		case float64:
			out, err = d.fromTimestamp(t)
		default:
			return c.fail(path, "date_type", "Input should be a valid date", v, nil)
		}
		if err != nil {
			return nil, false
		}
		return out, true
	}
}

type listOptions struct {
	min, max int
	// True when a request may leave the list out and get an empty one.
	optional bool
}

// list is a list. Too many items is reported on its own; too few only once
// every item has passed.
func list(item field, o listOptions) field {
	return func(c *checker, path []any, v any) (any, bool) {
		if v == notSent {
			if o.optional {
				return []any{}, true
			}
			return c.required(path)
		}
		given, ok := v.([]any)
		if !ok {
			return c.fail(path, "list_type", "Input should be a valid list", v, nil)
		}
		if len(given) > o.max {
			return c.fail(path, "too_long",
				"List should have at most "+plural(o.max, "item")+" after validation, not "+strconv.Itoa(len(given)), v,
				obj{{"field_type", "List"}, {"max_length", o.max}, {"actual_length", len(given)}})
		}
		out := make([]any, len(given))
		passed := true
		for i, raw := range given {
			value, ok := item(c, under(path, i), raw)
			out[i] = value
			passed = passed && ok
		}
		if !passed {
			return nil, false
		}
		if o.min != 0 && len(out) < o.min {
			return c.fail(path, "too_short",
				"List should have at least "+plural(o.min, "item")+" after validation, not "+strconv.Itoa(len(out)), out,
				obj{{"field_type", "List"}, {"min_length", o.min}, {"actual_length", len(out)}})
		}
		return out, true
	}
}

// named is one field of a model.
type named struct {
	name  string
	check field
}

// vals is a validated model: its fields by name.
type vals map[string]any

func (v vals) str(name string) string {
	s, _ := v[name].(string)
	return s
}

// strp is a nullable string field.
func (v vals) strp(name string) *string {
	if s, ok := v[name].(string); ok {
		return &s
	}
	return nil
}

func (v vals) int(name string) int64 {
	n, _ := v[name].(int64)
	return n
}

// intp is a nullable integer field.
func (v vals) intp(name string) *int64 {
	if n, ok := v[name].(int64); ok {
		return &n
	}
	return nil
}

func (v vals) bool(name string) bool {
	b, _ := v[name].(bool)
	return b
}

func (v vals) list(name string) []any {
	l, _ := v[name].([]any)
	return l
}

// model is an object whose fields are checked in the order given, all of
// them, unknown keys ignored.
func model(fields ...named) field {
	return func(c *checker, path []any, v any) (any, bool) {
		given, ok := v.(obj)
		if !ok {
			return c.fail(path, "model_attributes_type",
				"Input should be a valid dictionary or object to extract fields from", v, nil)
		}
		out := vals{}
		passed := true
		for _, f := range fields {
			raw, sent := given.get(f.name)
			if !sent {
				raw = notSent
			}
			value, ok := f.check(c, under(path, f.name), raw)
			out[f.name] = value
			passed = passed && ok
		}
		if !passed {
			return nil, false
		}
		return out, true
	}
}

// ------------------------------------------------------------ field validators

func payeeNotBlank(v string) (string, error) {
	if v == "" {
		return "", pycompat.ValueError("enter who the cheque is for")
	}
	return v, nil
}

func payeeNameNotBlank(v string) (string, error) {
	if v == "" {
		return "", pycompat.ValueError("the payee name is what gets printed — it cannot be blank")
	}
	return v, nil
}

func tidied(v string) (string, error) { return tidy(v), nil }

// Characters `str.isdigit()` accepts beyond the decimal digits: superscripts,
// circled digits and the like (Unicode Numeric_Type=Digit).
var digitOnly = &unicode.RangeTable{
	R16: []unicode.Range16{
		{0xB2, 0xB3, 1}, {0xB9, 0xB9, 1}, {0x1369, 0x1371, 1}, {0x19DA, 0x19DA, 1}, {0x2070, 0x2070, 1},
		{0x2074, 0x2079, 1}, {0x2080, 0x2089, 1}, {0x2460, 0x2468, 1}, {0x2474, 0x247C, 1}, {0x2488, 0x2490, 1},
		{0x24EA, 0x24EA, 1}, {0x24F5, 0x24FD, 1}, {0x24FF, 0x24FF, 1}, {0x2776, 0x277E, 1}, {0x2780, 0x2788, 1},
		{0x278A, 0x2792, 1},
	},
	R32: []unicode.Range32{
		{0x10A40, 0x10A43, 1}, {0x10E60, 0x10E68, 1}, {0x11052, 0x1105A, 1}, {0x1F100, 0x1F10A, 1},
	},
	LatinOffset: 2,
}

func numbered(v string) (string, error) {
	v = pycompat.Strip(v)
	last, _ := utf8.DecodeLastRuneInString(v)
	if v == "" || !unicode.In(last, unicode.Nd, digitOnly) {
		return "", pycompat.ValueError(
			"the first cheque number must end in digits, so the following leaves can be numbered from it")
	}
	return v, nil
}
