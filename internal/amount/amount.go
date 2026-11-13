// Package amount holds rupee amounts: parsing, Indian-system words, and
// cheque-safe figures.
//
// Money is carried as an integer number of paise everywhere in this app. A cheque
// is a payment instruction, so a half-paise rounding artefact is not a cosmetic
// problem — floats are never used to hold a value.
//
// The words produced here are what gets printed on the leaf and what is stored in
// the register, so this package is the single authority on both.
package amount

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/satiyap/banyan/internal/pycompat"
)

var ONES = []string{
	"", "One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight",
	"Nine", "Ten", "Eleven", "Twelve", "Thirteen", "Fourteen", "Fifteen",
	"Sixteen", "Seventeen", "Eighteen", "Nineteen",
}

var TENS = []string{"", "", "Twenty", "Thirty", "Forty", "Fifty", "Sixty", "Seventy", "Eighty", "Ninety"}

// A cheque above this is refused rather than printed with a number the layout was
// never measured for. 100 crore in paise.
const MAX_PAISE int64 = 100_00_00_000 * 100

// Python's `\d` and `\s` are Unicode-aware: digits of any script are read as
// numbers and Python's own whitespace set is stripped. Both are mirrored here.
var (
	stripRe  = regexp.MustCompile(`[₹,` + pycompat.WS + `]`)
	amountRe = regexp.MustCompile(`^\p{Nd}+(\.\p{Nd}{0,2})?$`)
)

// AmountError is raised when a string cannot be read as a printable rupee amount.
func AmountError(format string, args ...any) *pycompat.Error {
	return &pycompat.Error{Type: "AmountError", Message: fmt.Sprintf(format, args...)}
}

// Amount is a rupee amount held as integer paise.
type Amount struct {
	PaiseTotal int64 `json:"paiseTotal"`
}

func (a Amount) Rupees() int64 { return floorDiv(a.PaiseTotal, 100) }

func (a Amount) Paise() int64 { return mod(a.PaiseTotal, 100) }

// -- presentation ----------------------------------------------------

// Words is the amount in words, uppercase, as printed after the RUPEES label.
// Empty for a negative amount, which ParseAmount never returns.
func (a Amount) Words() string {
	words, _ := AmountWords(a.PaiseTotal)
	return words
}

// Figures is the amount in digits for the figures box, always terminated by '/-'.
func (a Amount) Figures() string { return AmountFigures(a.PaiseTotal) }

// Plain is Indian-grouped digits with no terminator, for screens and CSV.
func (a Amount) Plain() string {
	return fmt.Sprintf("%s.%02d", GroupIndian(a.Rupees()), a.Paise())
}

func (a Amount) String() string { return a.Plain() }

// ParseAmount reads user input into an Amount, or returns an AmountError.
//
// Accepts '12,500', '₹12500.50', ' 12500 '. Rejects anything with more than
// two decimals rather than rounding it: silently turning 100.005 into 100.01
// on a payment instruction is not this function's decision to make.
//
// A number is read as Python reads an int (whole rupees) when it is an integer
// and refused as a float otherwise. JSON cannot tell 5.0 from 5, so 5.0 is rupees.
func ParseAmount(raw any) (Amount, error) {
	var text string
	switch v := raw.(type) {
	case Amount:
		return v, nil
	case *Amount:
		if v != nil {
			return *v, nil
		}
		text = "null"
	case float64:
		return parseNumber(v)
	case float32:
		return parseNumber(float64(v))
	case int:
		return parseNumber(float64(v))
	case int32:
		return parseNumber(float64(v))
	case int64:
		return parseNumber(float64(v))
	case string:
		text = v
	case nil:
		text = "null"
	default:
		text = fmt.Sprint(v)
	}

	clean := stripRe.ReplaceAllString(text, "")
	if clean == "" {
		return Amount{}, AmountError("enter an amount")
	}
	if !amountRe.MatchString(clean) {
		return Amount{}, AmountError("%s is not a valid amount", repr(text))
	}

	rupeePart, paisePart, _ := strings.Cut(clean, ".")
	paise := 0.0
	if paisePart != "" {
		// Sliced by code point: digits of other scripts are multi-byte.
		paise = pycompat.Int(string([]rune(paisePart + "00")[:2]))
	}
	return checked(pycompat.Int(rupeePart)*100 + paise)
}

// repr is pycompat.Repr that also writes a lone surrogate as Python does
// (\udXXX). Go strings carry one as its three-byte generalised UTF-8 form,
// which pycompat.Repr reads as invalid bytes.
func repr(s string) string {
	isSurrogate := func(i int) bool {
		return i+2 < len(s) && s[i] == 0xed && s[i+1] >= 0xa0 && s[i+1] <= 0xbf && s[i+2] >= 0x80 && s[i+2] <= 0xbf
	}
	if !strings.Contains(s, "\xed") || utf8.ValidString(s) {
		return pycompat.Repr(s)
	}
	// Each piece is quoted the way the whole string is.
	double := strings.Contains(s, "'") && !strings.Contains(s, `"`)
	piece := func(p string) string {
		if double {
			r := pycompat.Repr(p)
			return r[1 : len(r)-1]
		}
		r := pycompat.Repr(p + `"`)
		return r[1 : len(r)-2]
	}
	quote := "'"
	if double {
		quote = `"`
	}
	var b strings.Builder
	b.WriteString(quote)
	start := 0
	for i := 0; i < len(s); i++ {
		if isSurrogate(i) {
			b.WriteString(piece(s[start:i]))
			fmt.Fprintf(&b, `\u%04x`, 0xd000|int(s[i+1]&0x3f)<<6|int(s[i+2]&0x3f))
			i += 2
			start = i + 1
		}
	}
	b.WriteString(piece(s[start:]))
	b.WriteString(quote)
	return b.String()
}

func parseNumber(n float64) (Amount, error) {
	if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) {
		return Amount{}, AmountError("amounts must be given as text or paise, not a float")
	}
	return checked(n * 100)
}

// The total arrives as a float so that an over-long run of digits is refused
// by the limit instead of overflowing.
func checked(paiseTotal float64) (Amount, error) {
	if paiseTotal <= 0 {
		return Amount{}, AmountError("amount must be more than zero")
	}
	if paiseTotal >= float64(MAX_PAISE) {
		return Amount{}, AmountError("amount is above the ₹100 crore limit this layout was measured for")
	}
	return Amount{PaiseTotal: int64(paiseTotal)}, nil
}

// Python's sign rules for // and %, on int64.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func mod(a, b int64) int64 { return a - floorDiv(a, b)*b }

// -- words ---------------------------------------------------------------
func two(n int64) string {
	if n < 20 {
		return ONES[n]
	}
	s := TENS[n/10]
	if n%10 != 0 {
		s += " " + ONES[n%10]
	}
	return s
}

func three(n int64) string {
	hundreds, rest := n/100, n%100
	head := ""
	if hundreds != 0 {
		head = ONES[hundreds] + " Hundred"
	}
	if head != "" && rest != 0 {
		head += " "
	}
	if rest != 0 {
		head += two(rest)
	}
	return head
}

// IndianWords is a non-negative integer in the Indian crore/lakh/thousand system.
func IndianWords(n int64) (string, error) {
	if n < 0 {
		return "", pycompat.ValueError("cannot write a negative number in words: %d", n)
	}
	return indianWords(n), nil
}

func indianWords(n int64) string {
	if n == 0 {
		return "Zero"
	}
	var parts []string
	crore := n / 10_000_000
	n %= 10_000_000
	lakh := n / 100_000
	n %= 100_000
	thousand := n / 1_000
	n %= 1_000
	if crore != 0 {
		parts = append(parts, indianWords(crore)+" Crore")
	}
	if lakh != 0 {
		parts = append(parts, two(lakh)+" Lakh")
	}
	if thousand != 0 {
		parts = append(parts, two(thousand)+" Thousand")
	}
	if n != 0 {
		parts = append(parts, three(n))
	}
	return strings.Join(parts, " ")
}

// AmountWords is the cheque wording for an amount in paise.
//
// Sub-rupee amounts read 'FIFTY PAISE ONLY', not 'ZERO AND FIFTY PAISE ONLY' —
// a leading Zero on a payment instruction invites a query at the counter.
func AmountWords(paiseTotal int64) (string, error) {
	if paiseTotal < 0 {
		return "", pycompat.ValueError("cannot write a negative amount in words: %d", paiseTotal)
	}
	rupees, paise := paiseTotal/100, paiseTotal%100
	var words string
	switch {
	case rupees != 0 && paise != 0:
		words = indianWords(rupees) + " And " + indianWords(paise) + " Paise"
	case rupees != 0:
		words = indianWords(rupees)
	default:
		words = indianWords(paise) + " Paise"
	}
	return strings.ToUpper(words + " Only"), nil
}

// -- figures -------------------------------------------------------------

// GroupIndian groups digits 12,34,567 rather than 1,234,567.
func GroupIndian(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	head, tail := s[:len(s)-3], s[len(s)-3:]
	var parts []string
	for len(head) > 2 {
		parts = append([]string{head[len(head)-2:]}, parts...)
		head = head[:len(head)-2]
	}
	if head != "" {
		parts = append([]string{head}, parts...)
	}
	return strings.Join(append(parts, tail), ",")
}

// AmountFigures is the figures for the amount box.
//
// Always ends in '/-' so no digit can be appended in the blank space that
// follows it inside the printed box.
func AmountFigures(paiseTotal int64) string {
	rupees, paise := floorDiv(paiseTotal, 100), mod(paiseTotal, 100)
	body := GroupIndian(rupees)
	if paise != 0 {
		body += fmt.Sprintf(".%02d", paise)
	}
	return body + "/-"
}
