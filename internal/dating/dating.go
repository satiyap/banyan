// Package dating holds how an entity's cheques are dated, and reminders for
// post-dated ones.
//
// Most bills are dated the day they are written. Salaries are not: the cheque is
// written and handed over early, dated the pay day, and the bank will not honour
// it before then. An entity's date rule says which, and only proposes a date —
// every leaf can still be dated by hand before it prints.
//
// A post-dated cheque is one whose date is after the day it was written. Nothing
// about that is stored: it is read off the cheque's own date and the time its
// row was created, so no register row needs a new status. Reminders before its
// date are answered with events on the cheque, which change nothing else.
//
// Dates are 'YYYY-MM-DD' strings throughout; arithmetic is on calendar parts in UTC.
package dating

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/satiyap/banyan/internal/leaves"
	"github.com/satiyap/banyan/internal/pycompat"
)

// Python's `\d` is any Unicode decimal digit and its `$` also matches before a
// final newline. The patterns below spell those out.
var (
	RuleRE = regexp.MustCompile(`^(today|(next|this):([1-9]|[12]\p{Nd}|3[01]))\n?$`)
	DaysRE = regexp.MustCompile(`^(\p{Nd}{1,2}(,\p{Nd}{1,2}){0,3})?\n?$`)
)

// STAGE_RE and UNTIL_RE without their `\b` at each end: Go's regexp has no
// lookaround, so findBounded checks the characters on either side.
var (
	stageRe = regexp.MustCompile(`stage (\p{Nd}+)d`)
	untilRe = regexp.MustCompile(`until (\p{Nd}{4}-\p{Nd}{2}-\p{Nd}{2})`)
)

// Issued lists the statuses of a cheque that has left the desk or will.
var Issued = []string{"queued", "printed", "logged"}

// Characters `str.isdigit()` accepts that `int()` does not: superscripts,
// circled digits and the like (Unicode Numeric_Type=Digit, 16.0).
const digitOnly = `\x{B2}\x{B3}\x{B9}\x{1369}-\x{1371}\x{19DA}\x{2070}\x{2074}-\x{2079}\x{2080}-\x{2089}` +
	`\x{2460}-\x{2468}\x{2474}-\x{247C}\x{2488}-\x{2490}\x{24EA}\x{24F5}-\x{24FD}\x{24FF}` +
	`\x{2776}-\x{277E}\x{2780}-\x{2788}\x{278A}-\x{2792}\x{10A40}-\x{10A43}\x{10E60}-\x{10E68}` +
	`\x{11052}-\x{1105A}\x{1F100}-\x{1F10A}`

var isDigitRe = regexp.MustCompile(`^[\p{Nd}` + digitOnly + `]+$`)

// ReminderChequeRow is a `cheques` row as the reminders read it.
// AmountPaise is a number or text.
type ReminderChequeRow struct {
	ID          int    `json:"id"`
	ChequeNo    string `json:"cheque_no"`
	Payee       string `json:"payee"`
	EntityID    *int   `json:"entity_id"`
	AmountPaise any    `json:"amount_paise"`
	ChequeDate  string `json:"cheque_date"`
	Memo        string `json:"memo"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
}

// ReminderEntityRow is an `entities` row; only these two columns are read.
// A nil RemindDays is a null column: no reminders.
type ReminderEntityRow struct {
	EntityName *string `json:"entity_name"`
	RemindDays *string `json:"remind_days"`
	// Set when the row was decoded from JSON with no `remind_days` key at
	// all; the default days then apply.
	noRemindDays bool
}

// UnmarshalJSON keeps a missing `remind_days` apart from a null one.
func (r *ReminderEntityRow) UnmarshalJSON(data []byte) error {
	type plain ReminderEntityRow
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	*r = ReminderEntityRow(p)
	_, has := keys["remind_days"]
	r.noRemindDays = !has
	return nil
}

// ReminderEventRow is an `events` row of kind 'remind-ack' or 'remind-snooze'.
type ReminderEventRow struct {
	ChequeID *int   `json:"cheque_id"`
	Kind     string `json:"kind"`
	Detail   string `json:"detail"`
}

// Reminder is one post-dated cheque. AmountPaise and Stage are float64, like
// the JavaScript numbers they replace; they always hold whole numbers.
type Reminder struct {
	ChequeID     int      `json:"cheque_id"`
	ChequeNo     string   `json:"cheque_no"`
	Payee        string   `json:"payee"`
	EntityID     *int     `json:"entity_id"`
	EntityName   string   `json:"entity_name"`
	AmountPaise  float64  `json:"amount_paise"`
	ChequeDate   string   `json:"cheque_date"`
	Memo         string   `json:"memo"`
	Status       string   `json:"status"`
	WrittenOn    string   `json:"written_on"`
	DaysUntil    int      `json:"days_until"`
	Stage        *float64 `json:"stage"`
	Answered     bool     `json:"answered"`
	SnoozedUntil string   `json:"snoozed_until"`
	Due          bool     `json:"due"`
}

// onDay is the given day of a month, or its last day when the month is
// shorter — 'the 31st' in February is the 28th or 29th, not March.
func onDay(year, month, day int) string {
	return pycompat.IsoDate(year, month, min(day, pycompat.DaysInMonth(year, month)))
}

// RuleDate is the date an entity's next cheque would carry, written today.
// An empty rule is 'today'.
func RuleDate(rule, today string) (string, error) {
	if rule == "" {
		rule = "today"
	}
	m := RuleRE.FindStringSubmatch(rule)
	if m == nil || m[1] == "today" {
		return today, nil
	}
	which := m[2]
	day := int(pycompat.Int(m[3]))
	d, err := pycompat.ParseIsoDate(today)
	if err != nil {
		return "", err
	}
	nextYear, nextMonth := d.Year+d.Month/12, d.Month%12+1
	if which == "next" {
		return onDay(nextYear, nextMonth, day), nil
	}
	if thisMonth := onDay(d.Year, d.Month, day); thisMonth > today {
		return thisMonth, nil
	}
	return onDay(nextYear, nextMonth, day), nil
}

// RemindDays reads an entity's remind days: each once, furthest first.
func RemindDays(text string) ([]float64, error) {
	days := []float64{}
	for _, d := range strings.Split(text, ",") {
		if !isDigitRe.MatchString(pycompat.Strip(d)) {
			continue
		}
		// `isdigit()` lets through a few characters `int()` then refuses.
		n, err := pycompat.IntOf(d)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(days, n) {
			days = append(days, n)
		}
	}
	slices.SortFunc(days, func(a, b float64) int {
		switch {
		case a > b:
			return -1
		case a < b:
			return 1
		}
		return 0
	})
	return days, nil
}

// toInt is `int(x)` for a register amount: an integer stays, text is read as digits.
func toInt(value any) (float64, error) {
	switch v := value.(type) {
	case float64:
		return math.Trunc(v), nil
	case float32:
		return math.Trunc(float64(v)), nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case json.Number:
		if f, err := strconv.ParseFloat(string(v), 64); err == nil {
			return math.Trunc(f), nil
		}
		return pycompat.IntOf(string(v))
	case string:
		return pycompat.IntOf(v)
	case nil:
		return pycompat.IntOf("null")
	}
	return pycompat.IntOf(fmt.Sprint(value))
}

// Python's `\b` uses Unicode word characters.
func isWord(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// findBounded is the first group of the first match of re in s that has no
// word character directly before or after it.
func findBounded(re *regexp.Regexp, s string) (string, bool) {
	for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
		if m[0] > 0 {
			if r, _ := utf8.DecodeLastRuneInString(s[:m[0]]); isWord(r) {
				continue
			}
		}
		if m[1] < len(s) {
			if r, _ := utf8.DecodeRuneInString(s[m[1]:]); isWord(r) {
				continue
			}
		}
		return s[m[2]:m[3]], true
	}
	return "", false
}

// Reminders is every post-dated cheque whose date has not passed, soonest first.
//
// Each says which reminder it is in (the nearest of its entity's remind days
// that has been reached), and whether that one has been answered or put off.
// `Due` is true when the desk should ask about it now.
//
// `entities` is keyed by entity id; `today` is 'YYYY-MM-DD'.
func Reminders(cheques []ReminderChequeRow, entities map[int]ReminderEntityRow, events []ReminderEventRow, today string) ([]Reminder, error) {
	answered := map[int][]float64{}
	snoozed := map[int]string{}
	for _, e := range events {
		if e.ChequeID == nil {
			continue // no cheque has a null id
		}
		id := *e.ChequeID
		switch e.Kind {
		case "remind-ack":
			if stage, ok := findBounded(stageRe, e.Detail); ok {
				answered[id] = append(answered[id], pycompat.Int(stage))
			}
		case "remind-snooze":
			if until, ok := findBounded(untilRe, e.Detail); ok && pycompat.CmpStr(until, snoozed[id]) > 0 {
				snoozed[id] = until
			}
		}
	}

	todayParts, err := pycompat.ParseIsoDate(today)
	if err != nil {
		return nil, err
	}
	todayNumber := pycompat.DayNumber(todayParts)
	out := []Reminder{}
	for _, c := range cheques {
		if !slices.Contains(Issued, c.Status) {
			continue
		}
		datedParts, err := pycompat.ParseIsoDate(c.ChequeDate)
		if err != nil {
			return nil, err
		}
		dated := pycompat.IsoDate(datedParts.Year, datedParts.Month, datedParts.Day)
		if dated < today {
			continue
		}
		writtenOn, err := leaves.LocalDate(c.CreatedAt)
		if err != nil {
			return nil, err
		}
		if dated <= writtenOn {
			continue
		}
		var ent ReminderEntityRow
		found := false
		if c.EntityID != nil {
			ent, found = entities[*c.EntityID]
		}
		days := pycompat.DayNumber(datedParts) - todayNumber
		remindText := "3,1,0"
		if found && !ent.noRemindDays {
			remindText = ""
			if ent.RemindDays != nil {
				remindText = *ent.RemindDays
			}
		}
		stages, err := RemindDays(remindText)
		if err != nil {
			return nil, err
		}
		var stage *float64
		for _, s := range stages {
			if float64(days) <= s && (stage == nil || s < *stage) {
				stage = &s
			}
		}
		done := stage != nil && slices.ContainsFunc(answered[c.ID], func(a float64) bool { return a <= *stage })
		snoozedUntil := snoozed[c.ID]
		putOff := pycompat.CmpStr(snoozedUntil, today) > 0
		entityName := c.Payee
		if ent.EntityName != nil && *ent.EntityName != "" {
			entityName = *ent.EntityName
		}
		amount, err := toInt(c.AmountPaise)
		if err != nil {
			return nil, err
		}
		out = append(out, Reminder{
			ChequeID:     c.ID,
			ChequeNo:     c.ChequeNo,
			Payee:        c.Payee,
			EntityID:     c.EntityID,
			EntityName:   entityName,
			AmountPaise:  amount,
			ChequeDate:   c.ChequeDate,
			Memo:         c.Memo,
			Status:       c.Status,
			WrittenOn:    writtenOn,
			DaysUntil:    days,
			Stage:        stage,
			Answered:     done,
			SnoozedUntil: snoozedUntil,
			Due:          stage != nil && !done && !putOff,
		})
	}
	slices.SortStableFunc(out, func(a, b Reminder) int {
		if c := pycompat.CmpStr(a.ChequeDate, b.ChequeDate); c != 0 {
			return c
		}
		return a.ChequeID - b.ChequeID
	})
	return out, nil
}

// Tomorrow is the day after today.
func Tomorrow(today string) (string, error) {
	return pycompat.AddDays(today, 1)
}
