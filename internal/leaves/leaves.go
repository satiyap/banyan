// Package leaves holds cheque books and the register in leaf order.
//
// A cheque book is a run of numbered leaves used front to back, so how far a book
// has got is simply the highest of its numbers the register knows. Nothing here is
// stored on a cheque: a book is a number range, and which book a cheque came from
// is worked out from its number. Adding a book therefore rewrites no register
// row, and a register from before books existed reads exactly as it did.
//
// Every leaf counts as used once its number is in the register in any status —
// printed, handwritten, misprinted, void — or recorded as a blank leaf. That is
// the same rule the batch path uses to refuse a number, applied to whole books.
package leaves

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/satiyap/banyan/internal/pycompat"
)

// LeafRE splits a leaf number into prefix and digits. Python's `.` stops only
// at \n and its `\d` is any Unicode decimal digit.
var LeafRE = regexp.MustCompile(`^([^\n]*?)(\p{Nd}+)$`)

const (
	MaxBookLeaves = 500
	AlertShare    = 0.8
	// Without books, a jump this small between two numbers in the same series
	// looks like leaves skipped rather than a new book.
	LooseGap = 10
)

// Leaf is (prefix, value, digit width) of a leaf number. Value is a float64,
// like the JavaScript number it replaces.
type Leaf struct {
	Prefix string
	Value  float64
	Width  int
}

// BookRow is a `cheque_books` row.
type BookRow struct {
	ID         int     `json:"id"`
	Name       string  `json:"name"`
	FirstNo    string  `json:"first_no"`
	LastNo     string  `json:"last_no"`
	ReceivedOn string  `json:"received_on"`
	Position   int     `json:"position"`
	AlertState string  `json:"alert_state"`
	AlertUntil *string `json:"alert_until"`
}

// ChequeRow is a `cheques` row as the register lists it (with the entity's name joined in).
type ChequeRow struct {
	ID          int     `json:"id"`
	ChequeNo    string  `json:"cheque_no"`
	Payee       string  `json:"payee"`
	EntityID    *int    `json:"entity_id"`
	EntityName  *string `json:"entity_name"`
	AmountPaise int64   `json:"amount_paise"`
	ChequeDate  string  `json:"cheque_date"`
	Memo        string  `json:"memo"`
	Status      string  `json:"status"`
	BatchID     *int    `json:"batch_id"`
	CreatedAt   string  `json:"created_at"`
	PdfPath     string  `json:"pdf_path"`
	CalVersion  int     `json:"cal_version"`
}

// BlankLeafRow is a `blank_leaves` row.
type BlankLeafRow struct {
	ID        int    `json:"id"`
	ChequeNo  string `json:"cheque_no"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"created_at"`
}

type NewBookCheck struct {
	Prefix     string  `json:"prefix"`
	First      float64 `json:"first"`
	Last       float64 `json:"last"`
	Width      int     `json:"width"`
	Count      float64 `json:"count"`
	InRegister int     `json:"in_register"`
	AlertCount float64 `json:"alert_count"`
	AlertLeaf  string  `json:"alert_leaf"`
}

type BookState string

const (
	BookFinished BookState = "finished"
	BookInUse    BookState = "in_use"
	BookNext     BookState = "next"
	BookShelf    BookState = "shelf"
	BookPaused   BookState = "paused"
)

type BookStatuses struct {
	Printed int `json:"printed"`
	Logged  int `json:"logged"`
	Queued  int `json:"queued"`
	Spoiled int `json:"spoiled"`
	Void    int `json:"void"`
	Failed  int `json:"failed"`
	Blank   int `json:"blank"`
}

type BookView struct {
	ID           int          `json:"id"`
	Name         string       `json:"name"`
	FirstNo      string       `json:"first_no"`
	LastNo       string       `json:"last_no"`
	ReceivedOn   string       `json:"received_on"`
	Position     int          `json:"position"`
	Count        float64      `json:"count"`
	Consumed     float64      `json:"consumed"`
	Left         float64      `json:"left"`
	NextLeaf     string       `json:"next_leaf"`
	Finished     bool         `json:"finished"`
	AlertCount   float64      `json:"alert_count"`
	AlertLeaf    string       `json:"alert_leaf"`
	AlertReached bool         `json:"alert_reached"`
	AlertState   string       `json:"alert_state"`
	AlertUntil   *string      `json:"alert_until"`
	Statuses     BookStatuses `json:"statuses"`
	Missing      []string     `json:"missing"`
	State        BookState    `json:"state"`
}

type BookAlert struct {
	BookID      int     `json:"book_id"`
	Book        string  `json:"book"`
	Consumed    float64 `json:"consumed"`
	Count       float64 `json:"count"`
	Left        float64 `json:"left"`
	NextBook    string  `json:"next_book"`
	NeedsAnswer bool    `json:"needs_answer"`
}

// BooksViewResult is what BooksView answers (the TypeScript type `BooksView`).
type BooksViewResult struct {
	Books    []BookView `json:"books"`
	InUse    *int       `json:"in_use"`
	NextLeaf string     `json:"next_leaf"`
	Alert    *BookAlert `json:"alert"`
}

// Pdc is what PdcState answers (the TypeScript type `PdcState`).
type Pdc string

const (
	PdcNone      Pdc = ""
	PdcPostDated Pdc = "post-dated"
	PdcNowValid  Pdc = "now-valid"
)

// RegisterRow is one of *RegisterChequeRow, *RegisterBlankRow, *RegisterGapRow.
type RegisterRow interface{ registerRow() }

type RegisterChequeRow struct {
	Kind        string  `json:"kind"`
	ID          int     `json:"id"`
	ChequeNo    string  `json:"cheque_no"`
	Payee       string  `json:"payee"`
	EntityID    *int    `json:"entity_id"`
	EntityName  *string `json:"entity_name"`
	AmountPaise int64   `json:"amount_paise"`
	ChequeDate  string  `json:"cheque_date"`
	Memo        string  `json:"memo"`
	Status      string  `json:"status"`
	BatchID     *int    `json:"batch_id"`
	CreatedAt   string  `json:"created_at"`
	PdfPath     string  `json:"pdf_path"`
	CalVersion  int     `json:"cal_version"`
	Pdc         Pdc     `json:"pdc"`
	Duplicate   bool    `json:"duplicate"`
	// Numbered is false for a cheque with no leaf number; `book_id` is then
	// absent from its JSON rather than null.
	Numbered bool `json:"-"`
	BookID   *int `json:"-"`
}

// MarshalJSON writes `book_id` last, and only for a cheque with a leaf number.
func (r RegisterChequeRow) MarshalJSON() ([]byte, error) {
	type plain RegisterChequeRow
	if !r.Numbered {
		return json.Marshal(plain(r))
	}
	return json.Marshal(struct {
		plain
		BookID *int `json:"book_id"`
	}{plain(r), r.BookID})
}

type RegisterBlankRow struct {
	Kind      string `json:"kind"`
	ID        int    `json:"id"`
	ChequeNo  string `json:"cheque_no"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"created_at"`
	Status    string `json:"status"`
	Duplicate bool   `json:"duplicate"`
	BookID    *int   `json:"book_id"`
}

type RegisterGapRow struct {
	Kind    string   `json:"kind"`
	BookID  *int     `json:"book_id"`
	Sure    bool     `json:"sure"`
	FromNo  string   `json:"from_no"`
	ToNo    string   `json:"to_no"`
	Numbers []string `json:"numbers"`
	Count   float64  `json:"count"`
}

func (*RegisterChequeRow) registerRow() {}
func (*RegisterBlankRow) registerRow()  {}
func (*RegisterGapRow) registerRow()    {}

// RegisterViewResult is what RegisterView answers (the TypeScript type `RegisterView`).
type RegisterViewResult struct {
	Rows []RegisterRow `json:"rows"`
}

// jsNumber is String(n) for a JavaScript number.
func jsNumber(n float64) string {
	switch {
	case math.IsNaN(n):
		return "NaN"
	case math.IsInf(n, 1):
		return "Infinity"
	case math.IsInf(n, -1):
		return "-Infinity"
	case math.Abs(n) >= 1e21:
		return strconv.FormatFloat(n, 'e', -1, 64)
	}
	return strconv.FormatFloat(n, 'f', -1, 64)
}

// ParseLeaf is (prefix, value, digit width) of a leaf number; ok is false if
// it does not end in digits.
func ParseLeaf(number string) (leaf Leaf, ok bool) {
	m := LeafRE.FindStringSubmatch(pycompat.Strip(number))
	if m == nil {
		return Leaf{}, false
	}
	return Leaf{m[1], pycompat.Int(m[2]), utf8.RuneCountInString(m[2])}, true
}

func FmtLeaf(prefix string, value float64, width int) string {
	// Python's `0{width}d` pads after the sign.
	digits := jsNumber(math.Abs(value))
	sign := ""
	if value < 0 {
		sign = "-"
		width--
	}
	if pad := width - len(digits); pad > 0 {
		digits = strings.Repeat("0", pad) + digits
	}
	return prefix + sign + digits
}

// BookRange is (prefix, first, last, width) of a stored book.
func BookRange(book BookRow) (prefix string, first, last float64, width int, err error) {
	a, okA := ParseLeaf(book.FirstNo)
	b, okB := ParseLeaf(book.LastNo)
	if !okA || !okB {
		return "", 0, 0, 0, pycompat.TypeError("cannot unpack non-iterable NoneType object")
	}
	return a.Prefix, a.Value, b.Value, a.Width, nil
}

// CheckNewBook validates a book about to be shelved. It returns the book's
// range and the checks that passed, or a ValueError naming the first that did not.
func CheckNewBook(firstNo, lastNo string, books []BookRow, used []string, alreadyUsed bool) (NewBookCheck, error) {
	a, okA := ParseLeaf(firstNo)
	b, okB := ParseLeaf(lastNo)
	if !okA || !okB {
		return NewBookCheck{}, pycompat.ValueError("both leaf numbers must end in digits")
	}
	if a.Prefix != b.Prefix || a.Width != b.Width {
		return NewBookCheck{}, pycompat.ValueError(
			"the first and last numbers are from different series — " +
				"they should have the same length and any prefix")
	}
	prefix, first, width := a.Prefix, a.Value, a.Width
	last := b.Value
	if last < first {
		return NewBookCheck{}, pycompat.ValueError("the last leaf number is before the first")
	}
	count := last - first + 1
	if count > MaxBookLeaves {
		return NewBookCheck{}, pycompat.ValueError(
			"%s leaves is more than any cheque book holds — check the two numbers", jsNumber(count))
	}
	for _, other := range books {
		op, of, ol, _, err := BookRange(other)
		if err != nil {
			return NewBookCheck{}, err
		}
		if op == prefix && first <= ol && of <= last {
			return NewBookCheck{}, pycompat.ValueError(
				"these numbers overlap %s (%s–%s)", other.Name, other.FirstNo, other.LastNo)
		}
	}
	// `used` is a set in Python: each number counts once.
	seen := map[string]bool{}
	var inRegister []string
	for _, n := range used {
		if seen[n] {
			continue
		}
		seen[n] = true
		if p, ok := ParseLeaf(n); ok && p.Prefix == prefix && first <= p.Value && p.Value <= last {
			inRegister = append(inRegister, n)
		}
	}
	slices.SortFunc(inRegister, pycompat.CmpStr)
	if len(inRegister) > 0 && !alreadyUsed {
		span := inRegister[0]
		if len(inRegister) > 1 {
			span += "…" + inRegister[len(inRegister)-1]
		}
		return NewBookCheck{}, pycompat.ValueError(
			"%d of these leaves are already in the register (%s). "+
				"If this is the book you are using now, say it is already in use", len(inRegister), span)
	}
	alertCount := math.Ceil(count * AlertShare)
	return NewBookCheck{
		Prefix:     prefix,
		First:      first,
		Last:       last,
		Width:      width,
		Count:      count,
		InRegister: len(inRegister),
		AlertCount: alertCount,
		AlertLeaf:  FmtLeaf(prefix, first+alertCount-1, width),
	}, nil
}

// BooksView is every book with how far it has got, which one is in use, and
// whether the new-book alert is due. Only `cheque_no` and `status` of a cheque
// and `cheque_no` of a blank are read. `today` is 'YYYY-MM-DD'.
func BooksView(books []BookRow, cheques []ChequeRow, blanks []BlankLeafRow, today string) (BooksViewResult, error) {
	byNumber := map[string][]string{}
	for _, c := range cheques {
		if c.ChequeNo != "" {
			byNumber[c.ChequeNo] = append(byNumber[c.ChequeNo], c.Status)
		}
	}
	for _, b := range blanks {
		byNumber[b.ChequeNo] = append(byNumber[b.ChequeNo], "blank")
	}

	out := make([]BookView, 0, len(books))
	for _, book := range books {
		prefix, first, last, width, err := BookRange(book)
		if err != nil {
			return BooksViewResult{}, err
		}
		tally := map[string]int{}
		values := map[float64]bool{}
		known := false
		highest := 0.0
		for number, sts := range byNumber {
			p, ok := ParseLeaf(number)
			if ok && p.Prefix == prefix && first <= p.Value && p.Value <= last {
				values[p.Value] = true
				if !known || p.Value > highest {
					known, highest = true, p.Value
				}
				for _, s := range sts {
					tally[s]++
				}
			}
		}
		count := last - first + 1
		consumed := 0.0
		if known {
			consumed = highest - first + 1
		}
		alertCount := math.Ceil(count * AlertShare)
		missing := []string{}
		if known {
			for v := first; v < highest; v++ {
				if !values[v] {
					missing = append(missing, FmtLeaf(prefix, v, width))
				}
			}
		}
		nextLeaf := book.FirstNo
		if known && highest < last {
			nextLeaf = FmtLeaf(prefix, highest+1, width)
		} else if known {
			nextLeaf = ""
		}
		out = append(out, BookView{
			ID:           book.ID,
			Name:         book.Name,
			FirstNo:      book.FirstNo,
			LastNo:       book.LastNo,
			ReceivedOn:   book.ReceivedOn,
			Position:     book.Position,
			Count:        count,
			Consumed:     consumed,
			Left:         count - consumed,
			NextLeaf:     nextLeaf,
			Finished:     known && highest == last,
			AlertCount:   alertCount,
			AlertLeaf:    FmtLeaf(prefix, first+alertCount-1, width),
			AlertReached: consumed >= alertCount,
			AlertState:   book.AlertState,
			AlertUntil:   book.AlertUntil,
			Statuses: BookStatuses{
				Printed: tally["printed"],
				Logged:  tally["logged"],
				Queued:  tally["queued"],
				Spoiled: tally["failed"] + tally["void"] + tally["blank"],
				Void:    tally["void"],
				Failed:  tally["failed"],
				Blank:   tally["blank"],
			},
			Missing: missing,
			State:   BookPaused, // set below, once the book in use is known
		})
	}

	inUse := slices.IndexFunc(out, func(b BookView) bool { return !b.Finished })
	shelf := -1 // the first untouched book behind the one in use
	for i, b := range out {
		if inUse >= 0 && i != inUse && !b.Finished && b.Consumed == 0 {
			shelf = i
			break
		}
	}
	for i := range out {
		b := &out[i]
		switch {
		case b.Finished:
			b.State = BookFinished
		case i == inUse:
			b.State = BookInUse
		case i == shelf:
			b.State = BookNext
		case b.Consumed == 0:
			b.State = BookShelf
		default:
			b.State = BookPaused
		}
	}

	res := BooksViewResult{Books: out}
	if inUse < 0 {
		return res, nil
	}
	using := out[inUse]
	res.InUse = &using.ID
	res.NextLeaf = using.NextLeaf
	if using.AlertReached {
		snoozed := false
		if using.AlertState == "snoozed" {
			if using.AlertUntil == nil {
				return BooksViewResult{}, pycompat.TypeError("'>' not supported between instances of 'NoneType' and 'str'")
			}
			snoozed = pycompat.CmpStr(*using.AlertUntil, today) > 0
		}
		answered := using.AlertState == "ordered" || snoozed
		nextBook := ""
		if shelf >= 0 {
			nextBook = out[shelf].Name
		}
		res.Alert = &BookAlert{
			BookID:   using.ID,
			Book:     using.Name,
			Consumed: using.Consumed,
			Count:    using.Count,
			Left:     using.Left,
			NextBook: nextBook,
			// With a book waiting on the shelf there is nothing to do but
			// know it; with none, the desk asks until it is answered.
			NeedsAnswer: shelf < 0 && !answered,
		}
	}
	return res, nil
}

// LocalDate is the desk's own date for a UTC timestamp in the register, as
// 'YYYY-MM-DD'.
//
// Timezone: like Python's `datetime.fromisoformat(ts).astimezone().date()`,
// this is the date in the system local zone of the machine running the app
// (time.Local), because the desk and the treasurer are in the same place. A
// timestamp with no offset is read as already local.
func LocalDate(timestamp string) (string, error) {
	return pycompat.LocalDateOf(timestamp)
}

// PdcState is 'post-dated' while a cheque dated after the day it was written
// is not yet valid, 'now-valid' once its date has come, "" for any other
// cheque. Only `created_at` and `cheque_date` are read. `today` is 'YYYY-MM-DD'.
func PdcState(cheque ChequeRow, today string) (Pdc, error) {
	written, err := LocalDate(cheque.CreatedAt)
	if err != nil {
		return "", err
	}
	dated, err := pycompat.CanonicalDate(cheque.ChequeDate)
	if err != nil {
		return "", err
	}
	if dated <= written {
		return PdcNone, nil
	}
	if dated > today {
		return PdcPostDated, nil
	}
	return PdcNowValid, nil
}

type bookSpan struct {
	id          int
	prefix      string
	first, last float64
}

// A cheque or blank row with a leaf number, as the gap search walks them.
type leafRow struct {
	leaf   Leaf
	row    RegisterRow
	id     int
	bookID *int
}

func sameBook(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// RegisterView is the register newest first, with leaves the register has no
// record of shown where they fall in their book, and duplicate numbers
// flagged. Gaps are found in leaf order; cheques with no leaf number are
// listed by date with the rest. `today` is 'YYYY-MM-DD'.
func RegisterView(cheques []ChequeRow, blanks []BlankLeafRow, books []BookRow, today string) (RegisterViewResult, error) {
	ranges := make([]bookSpan, 0, len(books))
	for _, b := range books {
		prefix, first, last, _, err := BookRange(b)
		if err != nil {
			return RegisterViewResult{}, err
		}
		ranges = append(ranges, bookSpan{b.ID, prefix, first, last})
	}
	bookOf := func(p Leaf) *int {
		for _, r := range ranges {
			if p.Prefix == r.prefix && r.first <= p.Value && p.Value <= r.last {
				id := r.id
				return &id
			}
		}
		return nil
	}

	seen := map[string]int{}
	for _, c := range cheques {
		if c.ChequeNo != "" {
			seen[c.ChequeNo]++
		}
	}
	for _, b := range blanks {
		seen[b.ChequeNo]++
	}

	var leaves []leafRow
	var unnumbered []*RegisterChequeRow
	for _, c := range cheques {
		pdc, err := PdcState(c, today)
		if err != nil {
			return RegisterViewResult{}, err
		}
		row := &RegisterChequeRow{
			Kind:        "cheque",
			ID:          c.ID,
			ChequeNo:    c.ChequeNo,
			Payee:       c.Payee,
			EntityID:    c.EntityID,
			EntityName:  c.EntityName,
			AmountPaise: c.AmountPaise,
			ChequeDate:  c.ChequeDate,
			Memo:        c.Memo,
			Status:      c.Status,
			BatchID:     c.BatchID,
			CreatedAt:   c.CreatedAt,
			PdfPath:     c.PdfPath,
			CalVersion:  c.CalVersion,
			Pdc:         pdc,
			Duplicate:   c.ChequeNo != "" && seen[c.ChequeNo] > 1,
		}
		p, ok := ParseLeaf(c.ChequeNo)
		if !ok {
			unnumbered = append(unnumbered, row)
			continue
		}
		row.Numbered = true
		row.BookID = bookOf(p)
		leaves = append(leaves, leafRow{p, row, row.ID, row.BookID})
	}
	for _, b := range blanks {
		p, ok := ParseLeaf(b.ChequeNo)
		row := &RegisterBlankRow{
			Kind:      "blank",
			ID:        b.ID,
			ChequeNo:  b.ChequeNo,
			Reason:    b.Reason,
			CreatedAt: b.CreatedAt,
			Status:    "blank",
			Duplicate: seen[b.ChequeNo] > 1,
		}
		if ok {
			row.BookID = bookOf(p)
			leaves = append(leaves, leafRow{p, row, row.ID, row.BookID})
		}
	}

	slices.SortStableFunc(leaves, func(a, b leafRow) int {
		if c := pycompat.CmpStr(a.leaf.Prefix, b.leaf.Prefix); c != 0 {
			return c
		}
		if d := a.leaf.Value - b.leaf.Value; d < 0 {
			return -1
		} else if d > 0 {
			return 1
		}
		return a.id - b.id
	})
	rows := []RegisterRow{}

	gap := func(prefix string, lo, hi float64, width int, bookID *int, sure bool) {
		numbers := []string{}
		for v := lo; v <= hi; v++ {
			numbers = append(numbers, FmtLeaf(prefix, v, width))
		}
		rows = append(rows, &RegisterGapRow{
			Kind:    "gap",
			BookID:  bookID,
			Sure:    sure,
			FromNo:  FmtLeaf(prefix, lo, width),
			ToNo:    FmtLeaf(prefix, hi, width),
			Numbers: numbers,
			Count:   hi - lo + 1,
		})
	}

	// Leaves skipped at the front of a book come before its first known one.
	starts := map[int]float64{}
	for _, r := range ranges {
		starts[r.id] = r.first
	}
	var prev *leafRow
	for i := range leaves {
		cur := &leaves[i]
		p, bookID := cur.leaf, cur.bookID
		if prev == nil || !sameBook(prev.bookID, bookID) || prev.leaf.Prefix != p.Prefix {
			if bookID != nil {
				if start, ok := starts[*bookID]; ok && p.Value > start {
					gap(p.Prefix, start, p.Value-1, p.Width, bookID, true)
				}
			}
		} else {
			step := p.Value - prev.leaf.Value
			if step > 1 && (bookID != nil || step-1 <= LooseGap) {
				gap(p.Prefix, prev.leaf.Value+1, p.Value-1, p.Width, bookID, bookID != nil)
			}
		}
		rows = append(rows, cur.row)
		prev = cur
	}
	slices.Reverse(rows)

	// Shown newest first by when each was written, not by leaf number: a new
	// book can be numbered below the last one, and leaf order would bury it.
	// A gap or blank leaf stays under the leaf above it, where it was found.
	var groups [][]RegisterRow
	for _, row := range rows {
		if _, isCheque := row.(*RegisterChequeRow); isCheque || len(groups) == 0 {
			groups = append(groups, []RegisterRow{row})
		} else {
			groups[len(groups)-1] = append(groups[len(groups)-1], row)
		}
	}
	for _, row := range unnumbered {
		groups = append(groups, []RegisterRow{row})
	}
	key := func(row RegisterRow) (createdAt string, id int) {
		switch r := row.(type) {
		case *RegisterChequeRow:
			return r.CreatedAt, r.ID
		case *RegisterBlankRow:
			return r.CreatedAt, r.ID
		}
		return "", 0
	}
	// Descending and stable: groups that tie keep the order they were found in, as Python's reverse sort does.
	slices.SortStableFunc(groups, func(a, b []RegisterRow) int {
		ca, ia := key(a[0])
		cb, ib := key(b[0])
		if c := pycompat.CmpStr(cb, ca); c != 0 {
			return c
		}
		return ib - ia
	})
	rows = rows[:0]
	for _, g := range groups {
		rows = append(rows, g...)
	}
	return RegisterViewResult{Rows: rows}, nil
}
