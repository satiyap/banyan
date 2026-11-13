// Package frequency finds the rhythm in the cheque register.
//
// A treasurer pays the same handful of people over and over: the security
// agency every month, the tanker supplier every week or two, the auditor once a
// year. This package reads the issued-cheque history and works out, per payee,
// how often a cheque goes out, how regular that is, roughly how much it is for,
// and when the next one is due.
//
// The statistics are deliberately robust rather than clever. The median and the
// median absolute deviation are used throughout, because one emergency plumbing
// cheque should not convince the app that a monthly payment is now fortnightly.
//
// Dates are ISO strings (YYYY-MM-DD); all date arithmetic is done in UTC on
// the calendar parts.
package frequency

import (
	"cmp"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/satiyap/banyan/internal/pycompat"
)

// Cadence is a named rhythm: its period and the tolerance, in days, within
// which a median interval is recognised as that cadence.
type Cadence struct {
	Name      string
	Period    float64
	Tolerance float64
}

// Cadences holds the named cadences. Values are real calendar averages, not
// round numbers: a monthly payment averages 30.44 days, not 30.
var Cadences = [...]Cadence{
	{"weekly", 7.0, 2.0},
	{"fortnightly", 14.0, 3.5},
	{"monthly", 30.44, 5.0},
	{"two-monthly", 60.9, 8.0},
	{"quarterly", 91.3, 12.0},
	{"half-yearly", 182.6, 20.0},
	{"yearly", 365.25, 30.0},
}

const (
	MinForCadence = 3  // cheques needed before a rhythm is claimed
	DueSoonDays   = 10 // how far ahead the "due soon" window reaches
)

// ChequeRow is an issued cheque as it comes out of the register; only the
// fields read here.
type ChequeRow struct {
	ID          int64  `json:"id"`
	ChequeNo    string `json:"cheque_no"`
	Payee       string `json:"payee"`
	AmountPaise int64  `json:"amount_paise"`
	ChequeDate  string `json:"cheque_date"`
	Memo        string `json:"memo"`
	Status      string `json:"status"`
	EntityID    *int64 `json:"entity_id,omitempty"`
}

// EntityMeta is what the entity list knows about one entity, keyed by entity
// id in Analyse. A nil field is one the entity list did not give.
type EntityMeta struct {
	Category   *string `json:"category,omitempty"`
	Notes      *string `json:"notes,omitempty"`
	EntityName *string `json:"entity_name,omitempty"`
	Kind       *string `json:"kind,omitempty"`
}

// EntityMetaMap is the entity list, by entity id.
type EntityMetaMap map[int64]EntityMeta

type RecentCheque struct {
	ID          int64  `json:"id"`
	ChequeDate  string `json:"cheque_date"`
	AmountPaise int64  `json:"amount_paise"`
	ChequeNo    string `json:"cheque_no"`
	Status      string `json:"status"`
	Memo        string `json:"memo"`
}

// PayeeRhythmDict is the JSON shape of a rhythm, as sent to the frontend.
type PayeeRhythmDict struct {
	Payee              string         `json:"payee"`
	Count              int            `json:"count"`
	FirstDate          string         `json:"first_date"`
	LastDate           string         `json:"last_date"`
	Dates              []string       `json:"dates"`
	TotalPaise         int64          `json:"total_paise"`
	TypicalPaise       int64          `json:"typical_paise"`
	AmountPattern      string         `json:"amount_pattern"`
	Cadence            string         `json:"cadence"`
	MedianIntervalDays *float64       `json:"median_interval_days"`
	Regularity         float64        `json:"regularity"`
	Confidence         string         `json:"confidence"`
	NextDue            *string        `json:"next_due"`
	DaysUntilDue       *int           `json:"days_until_due"`
	Status             string         `json:"status"`
	Category           string         `json:"category"`
	Notes              string         `json:"notes"`
	EntityName         string         `json:"entity_name"`
	EntityID           *int64         `json:"entity_id"`
	Kind               string         `json:"kind"`
	Recent             []RecentCheque `json:"recent"`
}

type FrequencySummary struct {
	TotalCheques         int      `json:"total_cheques"`
	TotalPaise           int64    `json:"total_paise"`
	MonthCount           int      `json:"month_count"`
	MonthPaise           int64    `json:"month_paise"`
	YearCount            int      `json:"year_count"`
	YearPaise            int64    `json:"year_paise"`
	PayeeCount           int      `json:"payee_count"`
	RecurringCount       int      `json:"recurring_count"`
	CommittedYearlyPaise int64    `json:"committed_yearly_paise"`
	Overdue              []string `json:"overdue"`
	DueSoon              []string `json:"due_soon"`
}

// PayeeRhythm is one payee's rhythm. The field names are the JSON keys too.
type PayeeRhythm struct {
	Payee              string         `json:"payee"`
	Count              int            `json:"count"`
	FirstDate          string         `json:"first_date"`
	LastDate           string         `json:"last_date"`
	Dates              []string       `json:"dates"`
	AmountsPaise       []int64        `json:"amounts_paise"`
	TotalPaise         int64          `json:"total_paise"`
	TypicalPaise       int64          `json:"typical_paise"`
	AmountPattern      string         `json:"amount_pattern"` // fixed | near-fixed | varies | one-off
	Cadence            string         `json:"cadence"`        // weekly … yearly | irregular | too-few
	MedianIntervalDays *float64       `json:"median_interval_days"`
	Regularity         float64        `json:"regularity"` // 0..1, 1 = perfectly evenly spaced
	Confidence         string         `json:"confidence"` // high | medium | low | none
	NextDue            *string        `json:"next_due"`
	DaysUntilDue       *int           `json:"days_until_due"`
	Status             string         `json:"status"` // overdue | due-now | due-soon | on-track | dormant | one-off
	Category           string         `json:"category"`
	Notes              string         `json:"notes"`
	EntityName         string         `json:"entity_name"`
	EntityID           *int64         `json:"entity_id"`
	Kind               string         `json:"kind"`
	Recent             []RecentCheque `json:"recent"`
}

// AsDict is the rhythm as sent to the frontend: no amounts list, and the
// regularity rounded to three places.
func (r *PayeeRhythm) AsDict() PayeeRhythmDict {
	if r == nil {
		return PayeeRhythmDict{Dates: []string{}, Recent: []RecentCheque{}}
	}
	nextDue := r.NextDue
	if nextDue != nil && *nextDue == "" {
		nextDue = nil
	}
	recent := r.Recent
	if recent == nil {
		recent = []RecentCheque{}
	}
	return PayeeRhythmDict{
		Payee:              r.Payee,
		Count:              r.Count,
		FirstDate:          r.FirstDate,
		LastDate:           r.LastDate,
		Dates:              append([]string{}, r.Dates...),
		TotalPaise:         r.TotalPaise,
		TypicalPaise:       r.TypicalPaise,
		AmountPattern:      r.AmountPattern,
		Cadence:            r.Cadence,
		MedianIntervalDays: r.MedianIntervalDays,
		Regularity:         roundTo(r.Regularity, 3),
		Confidence:         r.Confidence,
		NextDue:            nextDue,
		DaysUntilDue:       r.DaysUntilDue,
		Status:             r.Status,
		Category:           r.Category,
		Notes:              r.Notes,
		EntityName:         r.EntityName,
		EntityID:           r.EntityID,
		Kind:               r.Kind,
		Recent:             recent,
	}
}

// ---- Python built-ins, mirrored ------------------------------------------

var isoRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)

// parseIso is date.fromisoformat, for the YYYY-MM-DD form the register stores.
func parseIso(value string) (pycompat.Ymd, error) {
	if m := isoRe.FindStringSubmatch(value); m != nil {
		year, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		day, _ := strconv.Atoi(m[3])
		if year >= 1 && month >= 1 && month <= 12 && day >= 1 && day <= pycompat.DaysInMonth(year, month) {
			return pycompat.Ymd{Year: year, Month: month, Day: day}, nil
		}
	}
	return pycompat.Ymd{}, pycompat.ValueError("Invalid isoformat string: '%s'", value)
}

// dayNumberOf is whole days since the Unix epoch for an ISO date.
func dayNumberOf(iso string) (int, error) {
	d, err := parseIso(iso)
	if err != nil {
		return 0, err
	}
	return pycompat.DayNumber(d), nil
}

func fromDayNumber(days int) string {
	at := time.Unix(int64(days)*86400, 0).UTC()
	return pycompat.IsoDate(at.Year(), int(at.Month()), at.Day())
}

// localToday is the calendar date on this machine's clock, in its local timezone.
func localToday() string {
	now := time.Now()
	return pycompat.IsoDate(now.Year(), int(now.Month()), now.Day())
}

// median is statistics.median: the middle value, or the mean of the middle
// two. Callers never pass an empty list.
func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	data := slices.Clone(values)
	slices.Sort(data)
	i := len(data) / 2
	if len(data)%2 == 1 {
		return data[i]
	}
	return (data[i-1] + data[i]) / 2
}

// roundHalfEven is round(x): to the nearest integer, an exact half going to
// the even neighbour.
func roundHalfEven(x float64) float64 { return math.RoundToEven(x) }

// roundTo is round(x, digits): correctly rounded on the exact value of the
// double, an exact half going to the even digit. strconv does exactly that.
func roundTo(x float64, digits int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) >= 1e21 {
		return x
	}
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(x, 'f', digits, 64), 64)
	if err != nil {
		return x
	}
	return rounded
}

// ---------------------------------------------------------------------------

// AddMonths advances by whole calendar months, clamping to the end of a short
// month.
//
// A cheque written on the 31st recurs on the 30th in a 30-day month, which is
// what a person means by 'monthly' — adding 30.44 days would drift.
func AddMonths(d string, months int) (string, error) {
	at, err := parseIso(d)
	if err != nil {
		return "", err
	}
	total := at.Month - 1 + months
	year := at.Year + pycompat.FloorDiv(total, 12)
	month := pycompat.Mod(total, 12) + 1
	return pycompat.IsoDate(year, month, min(at.Day, pycompat.DaysInMonth(year, month))), nil
}

// classify returns the closest named cadence and how well it fits (0..1).
func classify(medianDays float64) (string, float64) {
	best := "irregular"
	bestFit := 0.0
	for _, c := range Cadences {
		distance := math.Abs(medianDays - c.Period)
		if distance <= c.Tolerance {
			fit := 1.0 - distance/c.Tolerance
			if fit > bestFit {
				best = c.Name
				bestFit = fit
			}
		}
	}
	return best, bestFit
}

func amountPattern(amounts []float64) string {
	if len(amounts) < 2 {
		return "one-off"
	}
	fixed := true
	for _, a := range amounts[1:] {
		if a != amounts[0] {
			fixed = false
			break
		}
	}
	if fixed {
		return "fixed"
	}
	mid := median(amounts)
	if mid <= 0 {
		return "varies"
	}
	spread := median(absDeviations(amounts, mid)) / mid
	if spread <= 0.02 {
		return "near-fixed"
	}
	if spread <= 0.25 {
		return "varies"
	}
	return "irregular"
}

func absDeviations(values []float64, mid float64) []float64 {
	out := make([]float64, len(values))
	for i, v := range values {
		out[i] = math.Abs(v - mid)
	}
	return out
}

var cadenceMonths = map[string]int{
	"monthly":     1,
	"two-monthly": 2,
	"quarterly":   3,
	"half-yearly": 6,
	"yearly":      12,
}

// AnalysePayee works out one payee's rhythm from their issued cheques (oldest
// first).
func AnalysePayee(payee string, rows []ChequeRow, today string) (*PayeeRhythm, error) {
	dayNumbers := make([]int, len(rows))
	amounts := make([]int64, len(rows))
	floatAmounts := make([]float64, len(rows))
	var total int64
	for i, r := range rows {
		day, err := dayNumberOf(r.ChequeDate)
		if err != nil {
			return nil, err
		}
		dayNumbers[i] = day
		amounts[i] = r.AmountPaise
		floatAmounts[i] = float64(r.AmountPaise)
		total += r.AmountPaise
	}

	// Two cheques on one day are one payment event as far as rhythm goes.
	uniqueDays := slices.Clone(dayNumbers)
	slices.Sort(uniqueDays)
	uniqueDays = slices.Compact(uniqueDays)
	intervals := make([]float64, 0, len(uniqueDays))
	for i := 1; i < len(uniqueDays); i++ {
		intervals = append(intervals, float64(uniqueDays[i]-uniqueDays[i-1]))
	}

	cadence := "too-few"
	regularity := 0.0
	confidence := "none"
	var medianInterval *float64

	if len(intervals) >= 1 {
		mid := median(intervals)
		medianInterval = &mid
		if mid > 0 && len(uniqueDays) >= MinForCadence {
			deviation := median(absDeviations(intervals, mid))
			regularity = math.Max(0.0, 1.0-deviation/mid)
			name, fit := classify(mid)
			cadence = name
			switch {
			case cadence == "irregular":
				confidence = "low"
			case len(uniqueDays) >= 4 && regularity >= 0.75 && fit >= 0.4:
				confidence = "high"
			case regularity >= 0.5:
				confidence = "medium"
			default:
				confidence = "low"
			}
		} else if mid > 0 {
			// Two payments is a hint, not a rhythm.
			if name, _ := classify(mid); name != "irregular" {
				cadence = "maybe " + name
			} else {
				cadence = "too-few"
			}
			confidence = "low"
		}
	}

	// The last date is read here, which fails when there are none.
	if len(uniqueDays) == 0 {
		return nil, &pycompat.Error{Type: "IndexError", Message: "list index out of range"}
	}
	lastDay := uniqueDays[len(uniqueDays)-1]
	last := fromDayNumber(lastDay)
	var nextDue *string
	if medianInterval != nil && *medianInterval != 0 && confidence != "none" && cadence != "too-few" {
		base := strings.Replace(cadence, "maybe ", "", 1)
		var due string
		if months := cadenceMonths[base]; months != 0 {
			var err error
			if due, err = AddMonths(last, months); err != nil {
				return nil, err
			}
		} else {
			due = fromDayNumber(lastDay + int(roundHalfEven(*medianInterval)))
		}
		nextDue = &due
	}

	var daysUntil *int
	if nextDue != nil {
		dueDay, err := dayNumberOf(*nextDue)
		if err != nil {
			return nil, err
		}
		todayDay, err := dayNumberOf(today)
		if err != nil {
			return nil, err
		}
		days := dueDay - todayDay
		daysUntil = &days
	}
	status := statusOf(nextDue, daysUntil, medianInterval, len(uniqueDays))

	// Newest first; cheques sharing a date keep the order they arrived in, as
	// Python's stable sort with reverse=True leaves them.
	sorted := slices.Clone(rows)
	slices.SortStableFunc(sorted, func(a, b ChequeRow) int { return strings.Compare(b.ChequeDate, a.ChequeDate) })
	recent := make([]RecentCheque, 0, 6)
	for _, r := range sorted[:min(6, len(sorted))] {
		recent = append(recent, RecentCheque{
			ID:          r.ID,
			ChequeDate:  r.ChequeDate,
			AmountPaise: r.AmountPaise,
			ChequeNo:    r.ChequeNo,
			Status:      r.Status,
			Memo:        r.Memo,
		})
	}

	dates := make([]string, len(uniqueDays))
	for i, day := range uniqueDays {
		dates[i] = fromDayNumber(day)
	}

	return &PayeeRhythm{
		Payee:              payee,
		Count:              len(rows),
		FirstDate:          dates[0],
		LastDate:           last,
		Dates:              dates,
		AmountsPaise:       amounts,
		TotalPaise:         total,
		TypicalPaise:       int64(median(floatAmounts)),
		AmountPattern:      amountPattern(floatAmounts),
		Cadence:            cadence,
		MedianIntervalDays: medianInterval,
		Regularity:         regularity,
		Confidence:         confidence,
		NextDue:            nextDue,
		DaysUntilDue:       daysUntil,
		Status:             status,
		Kind:               "regular",
		Recent:             recent,
	}, nil
}

func statusOf(nextDue *string, daysUntil *int, medianDays *float64, count int) string {
	if count < 2 {
		return "one-off"
	}
	if nextDue == nil || daysUntil == nil || medianDays == nil {
		return "on-track"
	}
	// A late cheque is only "overdue" once it is late by more than a sensible
	// fraction of its own cycle — a monthly payment three days late is normal.
	grace := math.Max(3, roundHalfEven(*medianDays*0.2))
	days := float64(*daysUntil)
	switch {
	case days < -grace*4:
		return "dormant"
	case days < -grace:
		return "overdue"
	case days < 0:
		return "due-now"
	case days <= DueSoonDays:
		return "due-soon"
	}
	return "on-track"
}

var statusOrder = map[string]int{
	"overdue":  0,
	"due-now":  1,
	"due-soon": 2,
	"on-track": 3,
	"dormant":  4,
	"one-off":  5,
}

func orDefault(value *string, fallback string) string {
	if value != nil {
		return *value
	}
	return fallback
}

// Analyse groups issued cheques by entity and analyses each one's rhythm.
//
// By entity, not by printed name: one vendor paid for two different things
// prints the same name on both, but each has its own cadence. A cheque not
// yet linked to an entity falls back to its printed name.
//
// An empty today means the local calendar date, as date.today() did. meta may
// be nil.
func Analyse(rows []ChequeRow, today string, meta EntityMetaMap) ([]*PayeeRhythm, error) {
	now := today
	if now == "" {
		now = localToday()
	}
	type group struct {
		entityID *int64
		rows     []ChequeRow
	}
	var groups []*group
	index := map[string]*group{}
	for _, row := range rows {
		var key string
		var entityID *int64
		if row.EntityID != nil {
			id := *row.EntityID
			entityID = &id
			key = "entity:" + strconv.FormatInt(id, 10)
		} else {
			key = "payee:" + row.Payee
		}
		g := index[key]
		if g == nil {
			g = &group{entityID: entityID}
			index[key] = g
			groups = append(groups, g)
		}
		g.rows = append(g.rows, row)
	}

	out := make([]*PayeeRhythm, 0, len(groups))
	for _, g := range groups {
		rhythm, err := AnalysePayee(g.rows[len(g.rows)-1].Payee, g.rows, now)
		if err != nil {
			return nil, err
		}
		rhythm.EntityID = g.entityID
		var info EntityMeta
		if g.entityID != nil {
			info = meta[*g.entityID]
		}
		rhythm.Category = orDefault(info.Category, "")
		rhythm.Notes = orDefault(info.Notes, "")
		rhythm.EntityName = orDefault(info.EntityName, "")
		rhythm.Kind = orDefault(info.Kind, "regular")
		if rhythm.Kind == "one_off" {
			// Someone paid once is not expected again, however the dates fall:
			// no cadence, no due date, and never "overdue".
			rhythm.Cadence = "one-off"
			rhythm.Confidence = "none"
			rhythm.Status = "one-off"
			rhythm.NextDue = nil
			rhythm.DaysUntilDue = nil
		}
		out = append(out, rhythm)
	}

	// Most actionable first: anything owed, then by how regular and how frequent.
	rank := func(r *PayeeRhythm) [3]int {
		order, ok := statusOrder[r.Status]
		if !ok {
			order = 9
		}
		days := 9999
		if r.DaysUntilDue != nil {
			days = *r.DaysUntilDue
		}
		return [3]int{order, days, -r.Count}
	}
	slices.SortStableFunc(out, func(a, b *PayeeRhythm) int {
		x, y := rank(a), rank(b)
		return cmp.Or(cmp.Compare(x[0], y[0]), cmp.Compare(x[1], y[1]), cmp.Compare(x[2], y[2]))
	})
	return out, nil
}

// Summarise gives the headline numbers for the dashboard.
//
// An empty today means the local calendar date, as date.today() did.
func Summarise(rhythms []*PayeeRhythm, rows []ChequeRow, today string) (FrequencySummary, error) {
	if today == "" {
		today = localToday()
	}
	now, err := parseIso(today)
	if err != nil {
		return FrequencySummary{}, err
	}
	monthStart := pycompat.DayNumber(pycompat.Ymd{Year: now.Year, Month: now.Month, Day: 1})
	// The financial year, April to March — the year the association's books are kept in.
	startYear := now.Year
	if now.Month < 4 {
		startYear--
	}
	yearStart := pycompat.DayNumber(pycompat.Ymd{Year: startYear, Month: 4, Day: 1})

	summary := FrequencySummary{
		TotalCheques: len(rows),
		PayeeCount:   len(rhythms), // entities paid, not distinct names
		Overdue:      []string{},
		DueSoon:      []string{},
	}
	for _, r := range rows {
		day, err := dayNumberOf(r.ChequeDate)
		if err != nil {
			return FrequencySummary{}, err
		}
		summary.TotalPaise += r.AmountPaise
		if day >= monthStart {
			summary.MonthCount++
			summary.MonthPaise += r.AmountPaise
		}
		if day >= yearStart {
			summary.YearCount++
			summary.YearPaise += r.AmountPaise
		}
	}

	committed := 0.0
	for _, r := range rhythms {
		if r == nil {
			continue
		}
		if r.Confidence == "high" || r.Confidence == "medium" {
			summary.RecurringCount++
			if r.MedianIntervalDays != nil && *r.MedianIntervalDays > 0 {
				committed += roundHalfEven(float64(r.TypicalPaise) * (365.25 / *r.MedianIntervalDays))
			}
		}
		name := r.EntityName
		if name == "" {
			name = r.Payee
		}
		switch r.Status {
		case "overdue":
			summary.Overdue = append(summary.Overdue, name)
		case "due-now", "due-soon":
			summary.DueSoon = append(summary.DueSoon, name)
		}
	}
	// This is a projection, not a ledger figure. Showing it to the paise would
	// imply a precision that annualising a median interval does not have.
	summary.CommittedYearlyPaise = int64(roundHalfEven(committed/100) * 100)
	return summary, nil
}
