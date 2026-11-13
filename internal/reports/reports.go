// Package reports builds the financial-year reports on what the desk has paid.
//
// An Indian association keeps its books April to March, so every period here is a
// financial year or one of its quarters — Q1 is April to June, Q4 is January to
// March of the following calendar year. "FY 2026–27" is the year that starts on
// 1 April 2026, and is keyed by that start year throughout.
//
// What counts as paid is what counts as a leaf issued everywhere else: printed,
// handwritten and still-awaiting-confirmation cheques. Void and misprinted ones
// represent no payment and are left out.
//
// Money stays integer paise end to end; a share is the only float, and it is
// a display fraction, never an amount.
//
// Dates are ISO strings (YYYY-MM-DD); all date arithmetic is done in UTC on
// the calendar parts.
package reports

import (
	"bytes"
	"cmp"
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/satiyap/banyan/internal/pycompat"
)

var MONTH_NAMES = []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// The fixed heads the treasurer reports under. An entity's free-text category
// is only a note about what it is; these are what the books add up to.
var HEADS = []string{"Salary", "Utilities", "AMC", "Misc", "GST / Tax"}

const (
	MISC_HEAD     = "Misc"
	UNCATEGORISED = "Uncategorised"
)

// ReportChequeRow is an issued cheque as it comes out of the register; only
// the fields read here.
type ReportChequeRow struct {
	ID          int64  `json:"id"`
	ChequeNo    string `json:"cheque_no"`
	Payee       string `json:"payee"`
	AmountPaise int64  `json:"amount_paise"`
	ChequeDate  string `json:"cheque_date"`
	Memo        string `json:"memo"`
	EntityID    *int64 `json:"entity_id,omitempty"`
}

// ReportEntityRow is a row of the entity list; only the fields read here.
type ReportEntityRow struct {
	ID         int64   `json:"id"`
	PayeeName  string  `json:"payee_name"`
	EntityName string  `json:"entity_name"`
	Head       *string `json:"head,omitempty"`
	Kind       *string `json:"kind,omitempty"`
}

type FyOption struct {
	Fy    int    `json:"fy"`
	Label string `json:"label"`
}

type ReportColumn struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type ReportEntity struct {
	EntityID      *int64  `json:"entity_id"`
	EntityName    string  `json:"entity_name"`
	PayeeName     string  `json:"payee_name"`
	Category      string  `json:"category"`
	Kind          string  `json:"kind"`
	Count         int     `json:"count"`
	Paise         int64   `json:"paise"`
	LargestPaise  int64   `json:"largest_paise"`
	LastDate      string  `json:"last_date"`
	Columns       []int64 `json:"columns"`
	Share         float64 `json:"share"`
	AveragePaise  int64   `json:"average_paise"`
	PreviousPaise int64   `json:"previous_paise"`
}

type ReportCategory struct {
	Category      string  `json:"category"`
	Count         int     `json:"count"`
	Paise         int64   `json:"paise"`
	Share         float64 `json:"share"`
	EntityCount   int     `json:"entity_count"`
	PreviousPaise int64   `json:"previous_paise"`
	Columns       []int64 `json:"columns"`
}

type ReportPayee struct {
	Payee    string   `json:"payee"`
	Count    int      `json:"count"`
	Paise    int64    `json:"paise"`
	Share    float64  `json:"share"`
	Entities []string `json:"entities"`
}

type ReportQuarter struct {
	Quarter int    `json:"quarter"`
	Label   string `json:"label"`
	Name    string `json:"name"`
	Start   string `json:"start"`
	End     string `json:"end"`
	Count   int    `json:"count"`
	Paise   int64  `json:"paise"`
	// 'future', 'current' or 'closed'.
	State string `json:"state"`
}

type ReportMonth struct {
	Month   string      `json:"month"`
	Label   string      `json:"label"`
	Quarter int         `json:"quarter"`
	Count   int         `json:"count"`
	Paise   int64       `json:"paise"`
	Future  bool        `json:"future"`
	Heads   HeadAmounts `json:"heads"`
}

type ReportLargest struct {
	ID          int64  `json:"id"`
	ChequeNo    string `json:"cheque_no"`
	ChequeDate  string `json:"cheque_date"`
	Payee       string `json:"payee"`
	EntityName  string `json:"entity_name"`
	Category    string `json:"category"`
	AmountPaise int64  `json:"amount_paise"`
	Memo        string `json:"memo"`
}

type ReportPrevious struct {
	Label         string `json:"label"`
	Paise         int64  `json:"paise"`
	Count         int    `json:"count"`
	PerMonthPaise int64  `json:"per_month_paise"`
	MonthsElapsed int    `json:"months_elapsed"`
}

type ReportTotals struct {
	Paise         int64          `json:"paise"`
	Count         int            `json:"count"`
	EntityCount   int            `json:"entity_count"`
	PayeeCount    int            `json:"payee_count"`
	CategoryCount int            `json:"category_count"`
	AveragePaise  int64          `json:"average_paise"`
	PerMonthPaise int64          `json:"per_month_paise"`
	MonthsElapsed int            `json:"months_elapsed"`
	Since         string         `json:"since"`
	Previous      ReportPrevious `json:"previous"`
	InProgress    bool           `json:"in_progress"`
}

type Report struct {
	Fy           int              `json:"fy"`
	Quarter      *int             `json:"quarter"`
	Label        string           `json:"label"`
	Start        string           `json:"start"`
	End          string           `json:"end"`
	Years        []FyOption       `json:"years"`
	Columns      []ReportColumn   `json:"columns"`
	ColumnTotals []int64          `json:"column_totals"`
	Totals       ReportTotals     `json:"totals"`
	Quarters     []ReportQuarter  `json:"quarters"`
	Months       []ReportMonth    `json:"months"`
	Entities     []ReportEntity   `json:"entities"`
	Categories   []ReportCategory `json:"categories"`
	Payees       []ReportPayee    `json:"payees"`
	Largest      []ReportLargest  `json:"largest"`
}

type MonthlyEntity struct {
	Head  string `json:"head"`
	Name  string `json:"name"`
	Count int    `json:"count"`
	Paise int64  `json:"paise"`
}

type MonthlyMonth struct {
	Month    string          `json:"month"`
	Label    string          `json:"label"`
	Count    int             `json:"count"`
	Paise    int64           `json:"paise"`
	Future   bool            `json:"future"`
	Current  bool            `json:"current"`
	Heads    HeadAmounts     `json:"heads"`
	Entities []MonthlyEntity `json:"entities"`
}

type MonthlyHead struct {
	Head  string `json:"head"`
	Paise int64  `json:"paise"`
}

type MonthlyTotals struct {
	Paise         int64 `json:"paise"`
	Count         int   `json:"count"`
	MonthsPaid    int   `json:"months_paid"`
	PerMonthPaise int64 `json:"per_month_paise"`
}

type MonthlyReport struct {
	// 'last' or 'fy'.
	Mode   string         `json:"mode"`
	Fy     *int           `json:"fy"`
	Last   *int           `json:"last"`
	Label  string         `json:"label"`
	Months []MonthlyMonth `json:"months"`
	Heads  []MonthlyHead  `json:"heads"`
	Totals MonthlyTotals  `json:"totals"`
	Years  []FyOption     `json:"years"`
}

// HeadAmount is the paise under one head.
type HeadAmount struct {
	Head  string
	Paise int64
}

// HeadAmounts is paise by head, kept in the order the heads were first met.
// It marshals as a JSON object with its keys in that order.
type HeadAmounts []HeadAmount

// Get is the paise under a head, 0 when the head is not listed.
func (h HeadAmounts) Get(head string) int64 {
	for _, x := range h {
		if x.Head == head {
			return x.Paise
		}
	}
	return 0
}

func (h *HeadAmounts) add(head string, paise int64) {
	for i := range *h {
		if (*h)[i].Head == head {
			(*h)[i].Paise += paise
			return
		}
	}
	*h = append(*h, HeadAmount{head, paise})
}

func (h HeadAmounts) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, x := range h {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(x.Head)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(x.Paise, 10))
	}
	b.WriteByte('}')
	return b.Bytes(), nil
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

// pad is String(value).padStart(width, '0').
func pad(value, width int) string {
	s := strconv.Itoa(value)
	if len(s) < width {
		s = strings.Repeat("0", width-len(s)) + s
	}
	return s
}

func formatIso(year, month, day int) string {
	return pad(year, 4) + "-" + pad(month, 2) + "-" + pad(day, 2)
}

// dayBefore is the day before an ISO date, stepped in UTC so no timezone or
// DST can shift it.
func dayBefore(iso string) (string, error) {
	d, err := parseIso(iso)
	if err != nil {
		return "", err
	}
	at := time.Date(d.Year, time.Month(d.Month), d.Day-1, 0, 0, 0, 0, time.UTC)
	return formatIso(at.Year(), int(at.Month()), at.Day()), nil
}

// firstOfMonth is d.replace(day=1), on an ISO date that has already been
// parsed once.
func firstOfMonth(iso string) string {
	return iso[:8] + "01"
}

// localToday is today: the calendar date on this machine's clock, in its
// local timezone.
func localToday() string {
	now := time.Now()
	return formatIso(now.Year(), int(now.Month()), now.Day())
}

// floorDiv is Python's `//` on integers: rounds towards minus infinity, not
// towards zero. b must not be 0.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// roundTo is round(x, digits): correctly rounded on the exact value of the
// double, an exact half going to the even digit. FormatFloat rounds the exact
// decimal expansion the same way.
func roundTo(x float64, digits int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) >= 1e21 {
		return x
	}
	rounded, _ := strconv.ParseFloat(strconv.FormatFloat(math.Abs(x), 'f', digits, 64), 64)
	if math.Signbit(x) {
		return -rounded
	}
	return rounded
}

// monthName is MONTH_NAMES.at(month - 1).
func monthName(month int) string {
	return MONTH_NAMES[((month-1)%12+12)%12]
}

// lastTwo is String(n).slice(-2).
func lastTwo(n int) string {
	s := strconv.Itoa(n)
	if len(s) > 2 {
		s = s[len(s)-2:]
	}
	return s
}

func sumAmounts(rows []ReportChequeRow) int64 {
	var total int64
	for i := range rows {
		total += rows[i].AmountPaise
	}
	return total
}

// ---------------------------------------------------------------------------

// FyOf is the start year of the financial year a date falls in.
func FyOf(d string) (int, error) {
	at, err := parseIso(d)
	if err != nil {
		return 0, err
	}
	if at.Month >= 4 {
		return at.Year, nil
	}
	return at.Year - 1, nil
}

func FyLabel(fy int) string {
	return "FY " + strconv.Itoa(fy) + "–" + lastTwo(fy+1)
}

func quarterOfMonth(month int) int {
	return (((month-4)%12+12)%12)/3 + 1
}

// QuarterOf is 1..4 within the financial year: Apr–Jun is 1, Jan–Mar is 4.
func QuarterOf(d string) (int, error) {
	at, err := parseIso(d)
	if err != nil {
		return 0, err
	}
	return quarterOfMonth(at.Month), nil
}

// FyMonths is [year, month] for April of fy through March of fy + 1.
func FyMonths(fy int) [][2]int {
	out := make([][2]int, 0, 12)
	for _, m := range []int{4, 5, 6, 7, 8, 9, 10, 11, 12, 1, 2, 3} {
		y := fy
		if m < 4 {
			y++
		}
		out = append(out, [2]int{y, m})
	}
	return out
}

// quarterMonths slices the year's months as a Python or JavaScript slice
// does: a negative bound counts from the end, and one out of range is clamped.
func quarterMonths(fy, quarter int) [][2]int {
	months := FyMonths(fy)
	bound := func(i int) int {
		if i < 0 {
			i += len(months)
		}
		return min(max(i, 0), len(months))
	}
	from, to := bound((quarter-1)*3), bound(quarter*3)
	if from >= to {
		return nil
	}
	return months[from:to]
}

// given reports a quarter that is set: neither absent nor 0.
func given(quarter *int) bool {
	return quarter != nil && *quarter != 0
}

func indexError() error {
	return &pycompat.Error{Type: "IndexError", Message: "list index out of range"}
}

// PeriodBounds is the first and last day of a financial year, or of one of
// its quarters.
func PeriodBounds(fy int, quarter *int) (start, end string, err error) {
	months := FyMonths(fy)
	if given(quarter) {
		months = quarterMonths(fy, *quarter)
	}
	// A quarter outside 1..4 leaves no months and this fails.
	if len(months) == 0 {
		return "", "", indexError()
	}
	first, last := months[0], months[len(months)-1]
	y1, m1 := last[0], last[1]
	if m1 == 12 {
		y1++
	}
	end, err = dayBefore(formatIso(y1, m1%12+1, 1))
	if err != nil {
		return "", "", err
	}
	return formatIso(first[0], first[1], 1), end, nil
}

func PeriodLabel(fy int, quarter *int) (string, error) {
	if !given(quarter) {
		return FyLabel(fy), nil
	}
	months := quarterMonths(fy, *quarter)
	if len(months) == 0 {
		return "", indexError()
	}
	first := monthName(months[0][1])
	last := monthName(months[len(months)-1][1])
	return "Q" + strconv.Itoa(*quarter) + " " + FyLabel(fy) + " (" + first + "–" + last + ")", nil
}

func PreviousPeriod(fy int, quarter *int) (int, *int) {
	if !given(quarter) {
		return fy - 1, nil
	}
	q := 4
	if *quarter > 1 {
		q = *quarter - 1
		return fy, &q
	}
	return fy - 1, &q
}

// HeadOf is the head an entity's cheques report under. A one-off with none
// chosen is Misc; anything else unassigned stays visibly Uncategorised, never
// guessed.
func HeadOf(entity *ReportEntityRow) string {
	if entity != nil && entity.Head != nil && slices.Contains(HEADS, *entity.Head) {
		return *entity.Head
	}
	if entity != nil && entity.Kind != nil && *entity.Kind == "one_off" {
		return MISC_HEAD
	}
	return UNCATEGORISED
}

func entityIndex(entities []ReportEntityRow) map[int64]*ReportEntityRow {
	index := make(map[int64]*ReportEntityRow, len(entities))
	for i := range entities {
		index[entities[i].ID] = &entities[i]
	}
	return index
}

func entityFor(index map[int64]*ReportEntityRow, row *ReportChequeRow) *ReportEntityRow {
	if row.EntityID == nil {
		return nil
	}
	return index[*row.EntityID]
}

// entityKey stands in for Python's dict key: the entity id, or ('payee', name).
type entityKey struct {
	entity bool
	id     int64
	payee  string
}

// entityOf is the key, id and display name of whoever a cheque was paid to.
func entityOf(index map[int64]*ReportEntityRow, row *ReportChequeRow) (key entityKey, id *int64, name string) {
	e := entityFor(index, row)
	if e == nil {
		return entityKey{payee: row.Payee}, nil, row.Payee
	}
	return entityKey{entity: true, id: e.ID}, &e.ID, cmp.Or(e.EntityName, e.PayeeName)
}

func yearsWithData(rows []ReportChequeRow, today string) ([]FyOption, error) {
	var years []int
	add := func(d string) error {
		fy, err := FyOf(d)
		if err != nil {
			return err
		}
		if !slices.Contains(years, fy) {
			years = append(years, fy)
		}
		return nil
	}
	for i := range rows {
		if err := add(rows[i].ChequeDate); err != nil {
			return nil, err
		}
	}
	if err := add(today); err != nil {
		return nil, err
	}
	slices.SortFunc(years, func(a, b int) int { return cmp.Compare(b, a) })
	out := make([]FyOption, 0, len(years))
	for _, y := range years {
		out = append(out, FyOption{Fy: y, Label: FyLabel(y)})
	}
	return out, nil
}

// inRange picks the rows dated start to end, both included.
func inRange(rows []ReportChequeRow, start, end string) []ReportChequeRow {
	out := []ReportChequeRow{}
	for i := range rows {
		if start <= rows[i].ChequeDate && rows[i].ChequeDate <= end {
			out = append(out, rows[i])
		}
	}
	return out
}

// Build is every report for one period, from issued cheques and the entity
// list.
//
// rows are issued cheques (any period — the full list is used to offer the
// years that have data and to compare against the period before).
//
// An empty today is the local calendar date, as date.today() was.
func Build(rows []ReportChequeRow, entities []ReportEntityRow, fy int, quarter *int, today string) (*Report, error) {
	if today == "" {
		today = localToday()
	}
	if _, err := parseIso(today); err != nil {
		return nil, err
	}
	// Every date is parsed up front, so a bad one fails before anything else.
	rowFy := make([]int, len(rows))
	rowQuarter := make([]int, len(rows))
	for i := range rows {
		at, err := parseIso(rows[i].ChequeDate)
		if err != nil {
			return nil, err
		}
		rowFy[i], _ = FyOf(rows[i].ChequeDate)
		rowQuarter[i] = quarterOfMonth(at.Month)
	}
	ent := entityIndex(entities)

	categoryOf := func(r *ReportChequeRow) string { return HeadOf(entityFor(ent, r)) }

	start, end, err := PeriodBounds(fy, quarter)
	if err != nil {
		return nil, err
	}
	inPeriod := inRange(rows, start, end)
	fyRows := []ReportChequeRow{}
	fyRowQuarter := []int{}
	for i := range rows {
		if rowFy[i] == fy {
			fyRows = append(fyRows, rows[i])
			fyRowQuarter = append(fyRowQuarter, rowQuarter[i])
		}
	}
	pFy, pQ := PreviousPeriod(fy, quarter)
	pStart, pEnd, err := PeriodBounds(pFy, pQ)
	if err != nil {
		return nil, err
	}
	prev := inRange(rows, pStart, pEnd)

	total := sumAmounts(inPeriod)

	share := func(paise int64) float64 {
		if total == 0 {
			return 0
		}
		return roundTo(float64(paise)/float64(total), 4)
	}

	// Columns that break a row down across the period: the four quarters of
	// a year, or the three months of a quarter.
	columns := []ReportColumn{}
	var columnOf func(r *ReportChequeRow) string
	if given(quarter) {
		for _, ym := range quarterMonths(fy, *quarter) {
			columns = append(columns, ReportColumn{
				Key:   strconv.Itoa(ym[0]) + "-" + pad(ym[1], 2),
				Label: MONTH_NAMES[ym[1]-1] + " " + lastTwo(ym[0]),
			})
		}
		columnOf = func(r *ReportChequeRow) string { return r.ChequeDate[:7] }
	} else {
		for q := 1; q <= 4; q++ {
			columns = append(columns, ReportColumn{Key: "Q" + strconv.Itoa(q), Label: "Q" + strconv.Itoa(q)})
		}
		columnOf = func(r *ReportChequeRow) string {
			q, _ := QuarterOf(r.ChequeDate)
			return "Q" + strconv.Itoa(q)
		}
	}
	columnAt := make(map[string]int, len(columns))
	for i, c := range columns {
		columnAt[c.Key] = i
	}
	addToColumn := func(into []int64, r *ReportChequeRow) {
		if i, ok := columnAt[columnOf(r)]; ok {
			into[i] += r.AmountPaise
		}
	}

	// -- by entity
	type entityTally struct {
		key entityKey
		out ReportEntity
	}
	byEntity := map[entityKey]*entityTally{}
	entityOrder := []*entityTally{}
	for i := range inPeriod {
		r := &inPeriod[i]
		key, id, name := entityOf(ent, r)
		e := entityFor(ent, r)
		s := byEntity[key]
		if s == nil {
			s = &entityTally{key: key, out: ReportEntity{
				EntityID:   id,
				EntityName: name,
				PayeeName:  r.Payee,
				Category:   categoryOf(r),
				Kind:       "regular",
				Columns:    make([]int64, len(columns)),
			}}
			if e != nil {
				s.out.PayeeName = e.PayeeName
				if e.Kind != nil {
					s.out.Kind = *e.Kind
				}
			}
			byEntity[key] = s
			entityOrder = append(entityOrder, s)
		}
		amt := r.AmountPaise
		s.out.Count++
		s.out.Paise += amt
		s.out.LargestPaise = max(s.out.LargestPaise, amt)
		if s.out.LastDate < r.ChequeDate {
			s.out.LastDate = r.ChequeDate
		}
		addToColumn(s.out.Columns, r)
	}

	prevByEntity := map[entityKey]int64{}
	for i := range prev {
		key, _, _ := entityOf(ent, &prev[i])
		prevByEntity[key] += prev[i].AmountPaise
	}

	type foldedEntity struct {
		out    ReportEntity
		folded string
	}
	sortedEntities := make([]foldedEntity, 0, len(entityOrder))
	for _, s := range entityOrder {
		s.out.Share = share(s.out.Paise)
		s.out.AveragePaise = floorDiv(s.out.Paise, int64(s.out.Count))
		s.out.PreviousPaise = prevByEntity[s.key]
		sortedEntities = append(sortedEntities, foldedEntity{s.out, Casefold(s.out.EntityName)})
	}
	slices.SortStableFunc(sortedEntities, func(a, b foldedEntity) int {
		return cmp.Or(cmp.Compare(b.out.Paise, a.out.Paise), pycompat.CmpStr(a.folded, b.folded))
	})
	entitiesOut := make([]ReportEntity, 0, len(sortedEntities))
	for _, s := range sortedEntities {
		entitiesOut = append(entitiesOut, s.out)
	}

	// -- by category (expense head)
	// Every head is always listed, so a nil quarter reads as nil, not missing.
	type headTally struct {
		name     string
		count    int
		paise    int64
		entities map[entityKey]bool
		columns  []int64
	}
	byCat := map[string]*headTally{}
	catOrder := []*headTally{}
	newHead := func(name string) *headTally {
		s := &headTally{name: name, entities: map[entityKey]bool{}, columns: make([]int64, len(columns))}
		byCat[name] = s
		catOrder = append(catOrder, s)
		return s
	}
	for _, name := range HEADS {
		newHead(name)
	}
	for i := range inPeriod {
		r := &inPeriod[i]
		name := categoryOf(r)
		s := byCat[name]
		if s == nil {
			s = newHead(name)
		}
		s.count++
		s.paise += r.AmountPaise
		key, _, _ := entityOf(ent, r)
		s.entities[key] = true
		addToColumn(s.columns, r)
	}
	prevByCat := map[string]int64{}
	for i := range prev {
		prevByCat[categoryOf(&prev[i])] += prev[i].AmountPaise
	}
	categoriesOut := make([]ReportCategory, 0, len(catOrder))
	categoryCount := 0
	for _, s := range catOrder {
		if s.count != 0 {
			categoryCount++
		}
		categoriesOut = append(categoriesOut, ReportCategory{
			Category:      s.name,
			Count:         s.count,
			Paise:         s.paise,
			Share:         share(s.paise),
			EntityCount:   len(s.entities),
			PreviousPaise: prevByCat[s.name],
			Columns:       s.columns,
		})
	}
	slices.SortStableFunc(categoriesOut, func(a, b ReportCategory) int {
		last := func(c ReportCategory) int {
			if c.Category == UNCATEGORISED {
				return 1
			}
			return 0
		}
		return cmp.Or(cmp.Compare(last(a), last(b)), cmp.Compare(b.Paise, a.Paise))
	})

	// -- by printed payee: what the bank sees going to one account, however
	// many entities the desk splits it into.
	byPayee := map[string]*ReportPayee{}
	payeeOrder := []*ReportPayee{}
	for i := range inPeriod {
		r := &inPeriod[i]
		s := byPayee[r.Payee]
		if s == nil {
			s = &ReportPayee{Payee: r.Payee, Entities: []string{}}
			byPayee[r.Payee] = s
			payeeOrder = append(payeeOrder, s)
		}
		s.Count++
		s.Paise += r.AmountPaise
		if _, _, name := entityOf(ent, r); !slices.Contains(s.Entities, name) {
			s.Entities = append(s.Entities, name)
		}
	}
	payeesOut := make([]ReportPayee, 0, len(payeeOrder))
	for _, s := range payeeOrder {
		s.Share = share(s.Paise)
		slices.SortStableFunc(s.Entities, pycompat.CmpStr)
		payeesOut = append(payeesOut, *s)
	}
	slices.SortStableFunc(payeesOut, func(a, b ReportPayee) int { return cmp.Compare(b.Paise, a.Paise) })

	// -- the year at a glance: every quarter and month of the financial year,
	// whichever period is selected, so the selection has context.
	quartersOut := make([]ReportQuarter, 0, 4)
	for q := 1; q <= 4; q++ {
		qStart, qEnd, err := PeriodBounds(fy, &q)
		if err != nil {
			return nil, err
		}
		name, err := PeriodLabel(fy, &q)
		if err != nil {
			return nil, err
		}
		out := ReportQuarter{Quarter: q, Label: "Q" + strconv.Itoa(q), Name: name, Start: qStart, End: qEnd}
		for i := range fyRows {
			if fyRowQuarter[i] == q {
				out.Count++
				out.Paise += fyRows[i].AmountPaise
			}
		}
		switch {
		case qStart > today:
			out.State = "future"
		case today <= qEnd:
			out.State = "current"
		default:
			out.State = "closed"
		}
		quartersOut = append(quartersOut, out)
	}
	monthsOut := make([]ReportMonth, 0, 12)
	for _, ym := range FyMonths(fy) {
		y, m := ym[0], ym[1]
		out := ReportMonth{
			Month:   strconv.Itoa(y) + "-" + pad(m, 2),
			Label:   MONTH_NAMES[m-1],
			Quarter: quarterOfMonth(m),
			Future:  formatIso(y, m, 1) > today,
			Heads:   HeadAmounts{},
		}
		for i := range fyRows {
			r := &fyRows[i]
			if r.ChequeDate[:7] != out.Month {
				continue
			}
			out.Count++
			out.Paise += r.AmountPaise
			out.Heads.add(categoryOf(r), r.AmountPaise)
		}
		monthsOut = append(monthsOut, out)
	}

	largest := slices.Clone(inPeriod)
	slices.SortStableFunc(largest, func(a, b ReportChequeRow) int {
		return cmp.Or(cmp.Compare(b.AmountPaise, a.AmountPaise), pycompat.CmpStr(a.ChequeDate, b.ChequeDate))
	})
	largest = largest[:min(len(largest), 10)]
	largestOut := make([]ReportLargest, 0, len(largest))
	for i := range largest {
		r := &largest[i]
		_, _, name := entityOf(ent, r)
		largestOut = append(largestOut, ReportLargest{
			ID:          r.ID,
			ChequeNo:    r.ChequeNo,
			ChequeDate:  r.ChequeDate,
			Payee:       r.Payee,
			EntityName:  name,
			Category:    categoryOf(r),
			AmountPaise: r.AmountPaise,
			Memo:        r.Memo,
		})
	}

	first := today
	for i := range rows {
		if i == 0 || rows[i].ChequeDate < first {
			first = rows[i].ChequeDate
		}
	}

	// Average per month over the months of a period that have begun,
	// counted from when the register did — a desk started in August has no
	// April to average over. The current month counts as whole, so a bill paid
	// on the 2nd is not divided by a sliver of a month. A cheque post-dated
	// into a month not yet begun stays out, or its amount would be shared
	// among months it was not paid in.
	monthly := func(picked []ReportChequeRow, s, e string) (perMonth int64, months int, since string, err error) {
		since = max(s, firstOfMonth(first))
		last := min(e, today)
		a, err := parseIso(since)
		if err != nil {
			return 0, 0, "", err
		}
		b, err := parseIso(last)
		if err != nil {
			return 0, 0, "", err
		}
		if since <= last {
			months = (b.Year-a.Year)*12 + b.Month - a.Month + 1
		}
		var paise int64
		for i := range picked {
			if firstOfMonth(picked[i].ChequeDate) <= today {
				paise += picked[i].AmountPaise
			}
		}
		if months != 0 {
			perMonth = floorDiv(paise, int64(months))
		}
		return perMonth, months, since, nil
	}

	perMonth, monthsElapsed, since, err := monthly(inPeriod, start, end)
	if err != nil {
		return nil, err
	}
	prevPerMonth, prevMonths, _, err := monthly(prev, pStart, pEnd)
	if err != nil {
		return nil, err
	}

	label, err := PeriodLabel(fy, quarter)
	if err != nil {
		return nil, err
	}
	years, err := yearsWithData(rows, today)
	if err != nil {
		return nil, err
	}
	columnTotals := make([]int64, len(columns))
	for _, s := range categoriesOut {
		for i := range columnTotals {
			columnTotals[i] += s.Columns[i]
		}
	}
	var averagePaise int64
	if len(inPeriod) != 0 {
		averagePaise = floorDiv(total, int64(len(inPeriod)))
	}
	prevLabel, err := PeriodLabel(pFy, pQ)
	if err != nil {
		return nil, err
	}

	return &Report{
		Fy:           fy,
		Quarter:      quarter,
		Label:        label,
		Start:        start,
		End:          end,
		Years:        years,
		Columns:      columns,
		ColumnTotals: columnTotals,
		Totals: ReportTotals{
			Paise:         total,
			Count:         len(inPeriod),
			EntityCount:   len(byEntity),
			PayeeCount:    len(byPayee),
			CategoryCount: categoryCount,
			AveragePaise:  averagePaise,
			PerMonthPaise: perMonth,
			MonthsElapsed: monthsElapsed,
			Since:         since,
			Previous: ReportPrevious{
				Label:         prevLabel,
				Paise:         sumAmounts(prev),
				Count:         len(prev),
				PerMonthPaise: prevPerMonth,
				MonthsElapsed: prevMonths,
			},
			InProgress: start <= today && today <= end,
		},
		Quarters:   quartersOut,
		Months:     monthsOut,
		Entities:   entitiesOut,
		Categories: categoriesOut,
		Payees:     payeesOut,
		Largest:    largestOut,
	}, nil
}

// Monthly is spend per month under each head, with the entities behind it.
//
// Either one financial year (April–March, every month listed so a nil
// month reads as nil) or the last `last` months ending with the current one,
// which may straddle two years.
//
// An empty today is the local calendar date, as date.today() was.
func Monthly(rows []ReportChequeRow, entities []ReportEntityRow, fy *int, last *int, today string) (*MonthlyReport, error) {
	if today == "" {
		today = localToday()
	}
	now, err := parseIso(today)
	if err != nil {
		return nil, err
	}
	ent := entityIndex(entities)
	fyOut := fy
	byLast := last != nil && *last != 0
	months := [][2]int{}
	var label string
	if byLast {
		y, m := now.Year, now.Month
		for i := 0; i < *last; i++ {
			months = append(months, [2]int{y, m})
			if m > 1 {
				m--
			} else {
				y--
				m = 12
			}
		}
		slices.Reverse(months)
		label = "Last " + strconv.Itoa(*last) + " months"
	} else {
		if fyOut == nil {
			y, _ := FyOf(today)
			fyOut = &y
		}
		months = FyMonths(*fyOut)
		label = FyLabel(*fyOut)
	}

	type entityKey struct{ head, name string }
	type monthTally struct {
		out      MonthlyMonth
		heads    HeadAmounts
		entities map[entityKey]*MonthlyEntity
		order    []*MonthlyEntity
	}
	tallies := make([]*monthTally, 0, len(months))
	byMonth := make(map[string]*monthTally, len(months))
	for _, ym := range months {
		y, m := ym[0], ym[1]
		s := &monthTally{
			out: MonthlyMonth{
				Month:   strconv.Itoa(y) + "-" + pad(m, 2),
				Label:   MONTH_NAMES[m-1] + " " + strconv.Itoa(y),
				Future:  formatIso(y, m, 1) > today,
				Current: y == now.Year && m == now.Month,
			},
			entities: map[entityKey]*MonthlyEntity{},
		}
		tallies = append(tallies, s)
		byMonth[s.out.Month] = s
	}
	for i := range rows {
		r := &rows[i]
		if len(r.ChequeDate) < 7 {
			continue
		}
		s := byMonth[r.ChequeDate[:7]]
		if s == nil {
			continue
		}
		e := entityFor(ent, r)
		head := HeadOf(e)
		name := r.Payee
		if e != nil {
			name = cmp.Or(e.EntityName, e.PayeeName)
		}
		amt := r.AmountPaise
		s.out.Count++
		s.out.Paise += amt
		s.heads.add(head, amt)
		key := entityKey{head, name}
		x := s.entities[key]
		if x == nil {
			x = &MonthlyEntity{Head: head, Name: name}
			s.entities[key] = x
			s.order = append(s.order, x)
		}
		x.Count++
		x.Paise += amt
	}
	extra := []string{}
	for _, s := range tallies {
		for _, h := range s.heads {
			if !slices.Contains(HEADS, h.Head) && !slices.Contains(extra, h.Head) {
				extra = append(extra, h.Head)
			}
		}
	}
	slices.SortStableFunc(extra, pycompat.CmpStr)
	heads := append(slices.Clone(HEADS), extra...)

	type foldedEntity struct {
		out    MonthlyEntity
		folded string
	}
	monthList := make([]MonthlyMonth, 0, len(tallies))
	headTotals := make([]MonthlyHead, 0, len(heads))
	for _, h := range heads {
		headTotals = append(headTotals, MonthlyHead{Head: h})
	}
	totals := MonthlyTotals{}
	for _, s := range tallies {
		s.out.Heads = make(HeadAmounts, 0, len(heads))
		for i, h := range heads {
			paise := s.heads.Get(h)
			s.out.Heads = append(s.out.Heads, HeadAmount{h, paise})
			headTotals[i].Paise += paise
		}
		sorted := make([]foldedEntity, 0, len(s.order))
		for _, x := range s.order {
			sorted = append(sorted, foldedEntity{*x, Casefold(x.Name)})
		}
		slices.SortStableFunc(sorted, func(a, b foldedEntity) int {
			return cmp.Or(cmp.Compare(b.out.Paise, a.out.Paise), pycompat.CmpStr(a.folded, b.folded))
		})
		s.out.Entities = make([]MonthlyEntity, 0, len(sorted))
		for _, x := range sorted {
			s.out.Entities = append(s.out.Entities, x.out)
		}
		totals.Paise += s.out.Paise
		totals.Count += s.out.Count
		if s.out.Count != 0 {
			totals.MonthsPaid++
		}
		monthList = append(monthList, s.out)
	}
	if totals.MonthsPaid != 0 {
		totals.PerMonthPaise = floorDiv(totals.Paise, int64(totals.MonthsPaid))
	}
	years, err := yearsWithData(rows, today)
	if err != nil {
		return nil, err
	}
	mode := "fy"
	if byLast {
		mode = "last"
	}
	return &MonthlyReport{
		Mode:   mode,
		Fy:     fyOut,
		Last:   last,
		Label:  label,
		Months: monthList,
		Heads:  headTotals,
		Totals: totals,
		Years:  years,
	}, nil
}
