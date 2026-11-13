// Package pycompat holds small pieces of Python behaviour that the desk's
// rules depend on and that Go does differently by default.
//
// The rules were first written in Python, and the recordings in tests/golden
// hold every port to the answers that code gave. Nothing here is app logic.
// Each helper stands in for a Python built-in: str.strip(), int(), repr(),
// floor division, date.fromisoformat() and so on.
package pycompat

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Error is a refusal named as Python names it. The recordings compare both
// the type name and the message.
type Error struct {
	Type    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// ValueError is Python's ValueError, by name.
func ValueError(format string, args ...any) *Error {
	return &Error{Type: "ValueError", Message: fmt.Sprintf(format, args...)}
}

// TypeError is Python's TypeError, by name.
func TypeError(format string, args ...any) *Error {
	return &Error{Type: "TypeError", Message: fmt.Sprintf(format, args...)}
}

// IsSpace reports whether Python's str.isspace(), str.strip() and \s treat r
// as whitespace.
func IsSpace(r rune) bool {
	switch {
	case r >= '\t' && r <= '\r', r >= 0x1c && r <= 0x20, r == 0x85, r == 0xa0, r == 0x1680,
		r >= 0x2000 && r <= 0x200a, r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000:
		return true
	}
	return false
}

// WS is the same set as a regexp character-class body, for patterns that need Python's \s.
const WS = `\t-\r\x1c-\x20\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

// Strip is str.strip() with no argument.
func Strip(s string) string { return strings.TrimFunc(s, IsSpace) }

// NumericStrip strips the whitespace int() and float() ignore around a number:
// Python's whitespace except U+001C-U+001F, which CPython leaves in place.
func NumericStrip(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return IsSpace(r) && !(r >= 0x1c && r <= 0x1f) })
}

// CmpStr compares two strings by code point, as Python does. Go strings are
// UTF-8, whose byte order is code point order.
func CmpStr(a, b string) int { return strings.Compare(a, b) }

// digitValue is the value 0-9 of one Unicode decimal digit (category Nd), as
// int() reads it. Every run of Nd characters is a whole number of 0-9 blocks.
func digitValue(r rune) int {
	if r >= '0' && r <= '9' {
		return int(r - '0')
	}
	start := r
	for start > 0 && unicode.Is(unicode.Nd, start-1) {
		start--
	}
	return int(r-start) % 10
}

// Int is int(s) for a string already known to be all decimal digits (any
// script). Like the JavaScript it replaces, it works in float64, so it loses
// precision past 2**53 exactly where the recordings expect it to.
func Int(digits string) float64 {
	n := 0.0
	for _, r := range digits {
		n = n*10 + float64(digitValue(r))
	}
	return n
}

var intRe = regexp.MustCompile(`^([+-]?)(\p{Nd}+(?:_\p{Nd}+)*)$`)

// IntOf is int(text): optional sign, digits of any script, single underscores between digits.
func IntOf(text string) (float64, error) {
	m := intRe.FindStringSubmatch(NumericStrip(text))
	if m == nil {
		return 0, ValueError("invalid literal for int() with base 10: %s", Repr(text))
	}
	n := Int(strings.ReplaceAll(m[2], "_", ""))
	if m[1] == "-" {
		n = -n
	}
	return n, nil
}

// Repr is repr() of a str: Python's quoting and escaping rules.
func Repr(s string) string {
	quote := '\''
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = '"'
	}
	var b strings.Builder
	b.WriteRune(quote)
	for _, r := range s {
		switch {
		case r == quote || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == ' ' || unicode.IsPrint(r):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteRune(quote)
	return b.String()
}

// FloorDiv and Mod follow Python's sign rules (Go's / and % truncate).
func FloorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func Mod(a, b int) int { return a - FloorDiv(a, b)*b }

// -- dates ---------------------------------------------------------------
// Python date values are carried as 'YYYY-MM-DD' strings. All arithmetic on
// them is done on calendar parts in UTC, so no local time can shift a day.

// Ymd is a calendar date as parts.
type Ymd struct{ Year, Month, Day int }

// DaysInMonth is calendar.monthrange(year, month)[1].
func DaysInMonth(year, month int) int {
	switch month {
	case 2:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

// IsoDate is date(y, m, d).isoformat().
func IsoDate(year, month, day int) string { return fmt.Sprintf("%04d-%02d-%02d", year, month, day) }

func utc(year, month, day, hour, minute, second int) time.Time {
	return time.Date(year, time.Month(month), day, hour, minute, second, 0, time.UTC)
}

// DayNumber is days since the Unix epoch for a calendar date (differences
// match Python's (a - b).days).
func DayNumber(d Ymd) int {
	return FloorDiv(int(utc(d.Year, d.Month, d.Day, 0, 0, 0).Unix()), 86400)
}

// AddDays is the calendar date `days` after iso.
func AddDays(iso string, days int) (string, error) {
	d, err := ParseIsoDate(iso)
	if err != nil {
		return "", err
	}
	t := utc(d.Year, d.Month, d.Day+days, 0, 0, 0)
	return IsoDate(t.Year(), int(t.Month()), t.Day()), nil
}

func checkDate(year, month, day int) error {
	if year < 1 || year > 9999 {
		return ValueError("year must be in 1..9999, not %d", year)
	}
	if month < 1 || month > 12 {
		return ValueError("month must be in 1..12, not %d", month)
	}
	dim := DaysInMonth(year, month)
	if day < 1 || day > dim {
		return ValueError("day %d must be in range 1..%d for month %d in year %d", day, dim, month, year)
	}
	return nil
}

var (
	isoDashRe  = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)
	isoPlainRe = regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})$`)
	isoDashAt  = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})`)
	isoPlainAt = regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})`)
	timeRe     = regexp.MustCompile(`^(\d{2})(?::?(\d{2})(?::?(\d{2})(?:[.,](\d+))?)?)?` +
		`(?: ?(Z|[+-]\d{2}(?::?\d{2}(?::?\d{2}(?:\.\d+)?)?)?))?$`)
	zoneRe = regexp.MustCompile(`^([+-])(\d{2})(?::?(\d{2})(?::?(\d{2}))?)?`)
)

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// ParseIsoDate is date.fromisoformat(s) as calendar parts, with Python 3.14's
// error text. Accepts 'YYYY-MM-DD' and 'YYYYMMDD'. ISO week dates are refused
// as an invalid string.
func ParseIsoDate(s string) (Ymd, error) {
	m := isoDashRe.FindStringSubmatch(s)
	if m == nil {
		m = isoPlainRe.FindStringSubmatch(s)
	}
	if m == nil {
		return Ymd{}, ValueError("Invalid isoformat string: %s", Repr(s))
	}
	d := Ymd{atoi(m[1]), atoi(m[2]), atoi(m[3])}
	if err := checkDate(d.Year, d.Month, d.Day); err != nil {
		return Ymd{}, err
	}
	return d, nil
}

// CanonicalDate is a date string in canonical 'YYYY-MM-DD' form, validated
// like date.fromisoformat. Canonical strings compare the way the dates do.
func CanonicalDate(s string) (string, error) {
	d, err := ParseIsoDate(s)
	if err != nil {
		return "", err
	}
	return IsoDate(d.Year, d.Month, d.Day), nil
}

// LocalDateOf is datetime.fromisoformat(timestamp).astimezone().date(): the
// desk's own calendar date for a register timestamp.
//
// A timestamp with a UTC offset is converted to the system local zone
// (time.Local, read at each call). One with no offset is already local, so its
// date part is returned as written. The register writes
// '2026-11-12T07:00:00+00:00'.
func LocalDateOf(timestamp string) (string, error) {
	bad := func() (string, error) {
		return "", ValueError("Invalid isoformat string: %s", Repr(timestamp))
	}
	dm := isoDashAt.FindStringSubmatch(timestamp)
	if dm == nil {
		dm = isoPlainAt.FindStringSubmatch(timestamp)
	}
	if dm == nil {
		return bad()
	}
	year, month, day := atoi(dm[1]), atoi(dm[2]), atoi(dm[3])
	m := make([]string, 6)
	if len(timestamp) != len(dm[0]) {
		// After the date: any one separator character, then a time.
		rest := timestamp[len(dm[0]):]
		_, size := utf8.DecodeRuneInString(rest)
		m = timeRe.FindStringSubmatch(rest[size:])
		if m == nil {
			return bad()
		}
	}
	if err := checkDate(year, month, day); err != nil {
		return "", err
	}
	hour, minute, second := atoi(m[1]), atoi(m[2]), atoi(m[3])
	// 24:00:00 is the one hour past 23 that Python accepts: midnight ending the day.
	endOfDay := hour == 24 && minute == 0 && second == 0 && strings.Trim(m[4], "0") == ""
	if hour == 24 && !endOfDay {
		return "", ValueError("minute, second, and microsecond must be 0 when hour is 24")
	}
	if hour > 24 {
		return "", ValueError("hour must be in 0..23, not %d", hour)
	}
	if minute > 59 {
		return "", ValueError("minute must be in 0..59, not %d", minute)
	}
	if second > 59 {
		return "", ValueError("second must be in 0..59, not %d", second)
	}
	zone := m[5]
	if zone == "" {
		// Naive: already local. Only 24:00 can move it off the written date.
		if endOfDay {
			return AddDays(IsoDate(year, month, day), 1)
		}
		return IsoDate(year, month, day), nil
	}
	offset := 0
	if zone != "Z" {
		z := zoneRe.FindStringSubmatch(zone)
		offset = atoi(z[2])*3600 + atoi(z[3])*60 + atoi(z[4])
		if z[1] == "-" {
			offset = -offset
		}
	}
	local := utc(year, month, day, hour, minute, second).Add(-time.Duration(offset) * time.Second).In(time.Local)
	return IsoDate(local.Year(), int(local.Month()), local.Day()), nil
}
