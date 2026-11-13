// Holds the reports package to its recorded answers: tests/golden/reports.json
// holds what the rules answered over invented registers, and every answer here
// must match.
package reports

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/satiyap/banyan/internal/pycompat"
)

type goldenRun struct {
	Today string `json:"today"`
	Build []struct {
		Fy      int             `json:"fy"`
		Quarter *int            `json:"quarter"`
		Out     json.RawMessage `json:"out"`
	} `json:"build"`
	Monthly []struct {
		Fy   *int            `json:"fy"`
		Last *int            `json:"last"`
		Out  json.RawMessage `json:"out"`
	} `json:"monthly"`
}

type goldenCase struct {
	Name     string            `json:"name"`
	Note     string            `json:"note"`
	Rows     []ReportChequeRow `json:"rows"`
	Entities []ReportEntityRow `json:"entities"`
	Runs     []goldenRun       `json:"runs"`
}

type golden struct {
	Constants struct {
		MonthNames    []string `json:"MONTH_NAMES"`
		Heads         []string `json:"HEADS"`
		MiscHead      string   `json:"MISC_HEAD"`
		Uncategorised string   `json:"UNCATEGORISED"`
	} `json:"constants"`
	Dates []json.RawMessage `json:"dates"`
	Years []struct {
		Fy     int      `json:"fy"`
		Label  string   `json:"label"`
		Months [][2]int `json:"months"`
	} `json:"years"`
	Periods []struct {
		Fy       int             `json:"fy"`
		Quarter  *int            `json:"quarter"`
		Bounds   [2]string       `json:"bounds"`
		Label    string          `json:"label"`
		Previous json.RawMessage `json:"previous"`
	} `json:"periods"`
	HeadOf   [][2]json.RawMessage `json:"head_of"`
	Casefold struct {
		Unicode    string            `json:"unicode"`
		Folds      map[string]string `json:"folds"`
		Unassigned [][2]int          `json:"unassigned"`
	} `json:"casefold"`
	Cases []goldenCase `json:"cases"`
}

var fixture = func() golden {
	raw, err := os.ReadFile("../../tests/golden/reports.json")
	if err != nil {
		panic(err)
	}
	var g golden
	if err := json.Unmarshal(raw, &g); err != nil {
		panic(err)
	}
	return g
}()

// sameNumber compares two JSON numbers by value: as integers when both are
// written as integers, as doubles otherwise.
func sameNumber(a, b json.Number) bool {
	x, errX := strconv.ParseInt(a.String(), 10, 64)
	y, errY := strconv.ParseInt(b.String(), 10, 64)
	if errX == nil && errY == nil {
		return x == y
	}
	f, errF := a.Float64()
	g, errG := b.Float64()
	return errF == nil && errG == nil && f == g
}

// deepSame is deep equality of two decoded JSON values, numbers by value. It
// returns where they first differ.
func deepSame(path string, got, want any) error {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: got %T, want an object", path, got)
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				return fmt.Errorf("%s: key %q is not in the recording", path, k)
			}
		}
		for k, v := range w {
			x, ok := g[k]
			if !ok {
				return fmt.Errorf("%s: key %q is missing", path, k)
			}
			if err := deepSame(path+"."+k, x, v); err != nil {
				return err
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok {
			return fmt.Errorf("%s: got %T, want an array", path, got)
		}
		if len(g) != len(w) {
			return fmt.Errorf("%s: got %d items, want %d", path, len(g), len(w))
		}
		for i := range w {
			if err := deepSame(fmt.Sprintf("%s[%d]", path, i), g[i], w[i]); err != nil {
				return err
			}
		}
	case json.Number:
		g, ok := got.(json.Number)
		if !ok || !sameNumber(g, w) {
			return fmt.Errorf("%s: got %v, want %v", path, got, want)
		}
	default:
		if !reflect.DeepEqual(got, want) {
			return fmt.Errorf("%s: got %#v, want %#v", path, got, want)
		}
	}
	return nil
}

func decode(t *testing.T, raw []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// tokens is a JSON text as its stream of tokens, numbers by value. Two texts
// with the same stream also list their keys in the same order.
func tokens(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		switch v := tok.(type) {
		case json.Number:
			if n, err := strconv.ParseInt(v.String(), 10, 64); err == nil {
				out = append(out, "n"+strconv.FormatInt(n, 10))
			} else {
				f, _ := v.Float64()
				out = append(out, "n"+strconv.FormatFloat(f, 'g', -1, 64))
			}
		case string:
			out = append(out, "s"+v)
		default:
			out = append(out, fmt.Sprint(v))
		}
	}
}

// expectSame holds a Go value to a recorded one: the same JSON value, and
// then the same keys in the same order.
func expectSame(t *testing.T, at string, actual any, expected []byte) bool {
	t.Helper()
	raw, err := json.Marshal(actual)
	if err != nil {
		t.Errorf("%s: %v", at, err)
		return false
	}
	if err := deepSame("$", decode(t, raw), decode(t, expected)); err != nil {
		t.Errorf("%s: %v", at, err)
		return false
	}
	got, want := tokens(t, raw), tokens(t, expected)
	if !reflect.DeepEqual(got, want) {
		for i := range got {
			if i >= len(want) || got[i] != want[i] {
				t.Errorf("%s: key order differs at token %d: got %q", at, i, got[i])
				return false
			}
		}
		t.Errorf("%s: key order differs", at)
		return false
	}
	return true
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func show(q *int) string {
	if q == nil {
		return "null"
	}
	return strconv.Itoa(*q)
}

func TestEnoughRegisters(t *testing.T) {
	if len(fixture.Cases) < 40 {
		t.Fatalf("only %d cases", len(fixture.Cases))
	}
}

func TestConstants(t *testing.T) {
	c := fixture.Constants
	expectSame(t, "MONTH_NAMES", MONTH_NAMES, mustJSON(t, c.MonthNames))
	expectSame(t, "HEADS", HEADS, mustJSON(t, c.Heads))
	if MISC_HEAD != c.MiscHead || UNCATEGORISED != c.Uncategorised {
		t.Errorf("MISC_HEAD %q, UNCATEGORISED %q", MISC_HEAD, UNCATEGORISED)
	}
}

func TestFyOfAndQuarterOf(t *testing.T) {
	if len(fixture.Dates) <= 200 {
		t.Fatalf("only %d dates", len(fixture.Dates))
	}
	for _, raw := range fixture.Dates {
		var d string
		var fy, quarter int
		if err := json.Unmarshal(raw, &[]any{&d, &fy, &quarter}); err != nil {
			t.Fatal(err)
		}
		if got, err := FyOf(d); err != nil || got != fy {
			t.Errorf("FyOf(%s) = %d, %v; want %d", d, got, err, fy)
		}
		if got, err := QuarterOf(d); err != nil || got != quarter {
			t.Errorf("QuarterOf(%s) = %d, %v; want %d", d, got, err, quarter)
		}
	}
}

func TestFyLabelAndFyMonths(t *testing.T) {
	for _, y := range fixture.Years {
		if got := FyLabel(y.Fy); got != y.Label {
			t.Errorf("FyLabel(%d) = %q, want %q", y.Fy, got, y.Label)
		}
		expectSame(t, fmt.Sprintf("FyMonths(%d)", y.Fy), FyMonths(y.Fy), mustJSON(t, y.Months))
	}
}

func TestPeriods(t *testing.T) {
	for _, p := range fixture.Periods {
		at := fmt.Sprintf("fy %d quarter %s", p.Fy, show(p.Quarter))
		start, end, err := PeriodBounds(p.Fy, p.Quarter)
		if err != nil || [2]string{start, end} != p.Bounds {
			t.Errorf("%s: PeriodBounds = %s, %s, %v; want %v", at, start, end, err, p.Bounds)
		}
		if got, err := PeriodLabel(p.Fy, p.Quarter); err != nil || got != p.Label {
			t.Errorf("%s: PeriodLabel = %q, %v; want %q", at, got, err, p.Label)
		}
		pFy, pQ := PreviousPeriod(p.Fy, p.Quarter)
		expectSame(t, at+": PreviousPeriod", []any{pFy, pQ}, p.Previous)
	}
	five := 5
	_, _, err := PeriodBounds(2025, &five)
	var refusal *pycompat.Error
	if !errors.As(err, &refusal) || refusal.Type != "IndexError" {
		t.Errorf("PeriodBounds(2025, 5) = %v, want an IndexError", err)
	}
	if _, err := PeriodLabel(2025, &five); err == nil {
		t.Error("PeriodLabel(2025, 5) did not fail")
	}
}

func TestHeadOf(t *testing.T) {
	for _, pair := range fixture.HeadOf {
		var entity *ReportEntityRow
		var head string
		if err := json.Unmarshal(pair[0], &entity); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(pair[1], &head); err != nil {
			t.Fatal(err)
		}
		if got := HeadOf(entity); got != head {
			t.Errorf("HeadOf(%s) = %q, want %q", pair[0], got, head)
		}
	}
	if got := HeadOf(nil); got != UNCATEGORISED {
		t.Errorf("HeadOf(nil) = %q", got)
	}
}

// Casefold matches str.casefold() on every code point Python knows.
func TestCasefold(t *testing.T) {
	folds := fixture.Casefold.Folds
	if len(folds) <= 1000 {
		t.Fatalf("only %d folds", len(folds))
	}
	// Code points unassigned in Python's Unicode version are skipped: a newer
	// Unicode in Go may already know letters there.
	skip := make([]bool, 0x110000)
	for _, r := range fixture.Casefold.Unassigned {
		for cp := r[0]; cp <= r[1]; cp++ {
			skip[cp] = true
		}
	}
	var wrong []string
	checked := 0
	for cp := 0; cp < 0x110000; cp++ {
		if skip[cp] || (cp >= 0xd800 && cp <= 0xdfff) {
			continue
		}
		checked++
		ch := string(rune(cp))
		expected, ok := folds[strconv.FormatInt(int64(cp), 16)]
		if !ok {
			expected = ch
		}
		// Padded so the non-ASCII path is taken even for an ASCII code point.
		if Casefold(ch) != expected || Casefold("é"+ch) != "é"+expected {
			wrong = append(wrong, strconv.FormatInt(int64(cp), 16))
		}
	}
	if len(wrong) != 0 {
		t.Errorf("%d code points fold wrongly: %v", len(wrong), wrong)
	}
	if got := Casefold("Straße ΣΊΣΥΦΟΣ İstasyon"); got != "strasse σίσυφοσ i̇stasyon" {
		t.Errorf("Casefold = %q", got)
	}
	t.Logf("%d code points checked, %d recorded folds", checked, len(folds))
}

func TestGoldenCases(t *testing.T) {
	cases, runs, builds, monthlies := 0, 0, 0, 0
	for _, c := range fixture.Cases {
		cases++
		frozen := mustJSON(t, []any{c.Rows, c.Entities})
		for _, run := range c.Runs {
			runs++
			t.Run(fmt.Sprintf("%s/today %s", c.Name, run.Today), func(t *testing.T) {
				for _, b := range run.Build {
					at := fmt.Sprintf("build fy %d quarter %s", b.Fy, show(b.Quarter))
					out, err := Build(c.Rows, c.Entities, b.Fy, b.Quarter, run.Today)
					if err != nil {
						t.Errorf("%s: %v", at, err)
						continue
					}
					expectSame(t, at, out, b.Out)
					builds++
				}
				for _, m := range run.Monthly {
					at := fmt.Sprintf("monthly fy %s last %s", show(m.Fy), show(m.Last))
					out, err := Monthly(c.Rows, c.Entities, m.Fy, m.Last, run.Today)
					if err != nil {
						t.Errorf("%s: %v", at, err)
						continue
					}
					expectSame(t, at, out, m.Out)
					monthlies++
				}
				if !bytes.Equal(mustJSON(t, []any{c.Rows, c.Entities}), frozen) {
					t.Error("the rows passed in were not left untouched")
				}
			})
		}
	}
	t.Logf("%d cases, %d runs, %d build and %d monthly answers checked", cases, runs, builds, monthlies)
}

var outsideRows = []ReportChequeRow{
	{ID: 1, ChequeNo: "1", Payee: "Meridian Lifts", AmountPaise: 250000, ChequeDate: "2025-05-02", Memo: ""},
}

func TestTodayDefaultsToLocalDate(t *testing.T) {
	local := time.Now().Format("2006-01-02")
	six := 6
	a, errA := Build(outsideRows, nil, 2025, nil, "")
	b, errB := Build(outsideRows, nil, 2025, nil, local)
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	expectSame(t, "build", a, mustJSON(t, b))
	for _, last := range []*int{nil, &six} {
		a, errA := Monthly(outsideRows, nil, nil, last, "")
		b, errB := Monthly(outsideRows, nil, nil, last, local)
		if errA != nil || errB != nil {
			t.Fatal(errA, errB)
		}
		expectSame(t, "monthly last "+show(last), a, mustJSON(t, b))
	}
}

// A date that is not an ISO date is refused, as date.fromisoformat does.
func TestRejectsBadDate(t *testing.T) {
	bad := []ReportChequeRow{outsideRows[0]}
	bad[0].ChequeDate = "2 May 2025"
	const message = "Invalid isoformat string: '2 May 2025'"
	fy := 2025
	_, err := Build(bad, nil, 2025, nil, "2025-06-01")
	if err == nil || err.Error() != message {
		t.Errorf("Build: %v", err)
	}
	_, err = Monthly(bad, nil, &fy, nil, "2025-06-01")
	if err == nil || err.Error() != message {
		t.Errorf("Monthly: %v", err)
	}
}

// Input the register would never hold is answered or refused, never a panic.
func TestNeverPanics(t *testing.T) {
	id := int64(9)
	rows := []ReportChequeRow{
		{ID: 1, Payee: "A", AmountPaise: -5, ChequeDate: "2025-05-02", EntityID: &id},
		{ID: 2, Payee: "Ünï", AmountPaise: 0, ChequeDate: "", EntityID: nil},
		{ID: 3, Payee: "", AmountPaise: 1 << 60, ChequeDate: "2025-13-40"},
	}
	for _, fy := range []int{-5, 0, 1, 2025, 9999, 10000, 1 << 40} {
		for _, q := range []int{-7, -1, 0, 1, 4, 5, 99} {
			for _, today := range []string{"", "2025-06-01", "nonsense", "0000-00-00"} {
				Build(rows, nil, fy, &q, today)
				Build(rows[:1], nil, fy, &q, today)
				Build(nil, nil, fy, nil, today)
				Monthly(rows, nil, &fy, &q, today)
				Monthly(rows[:1], nil, &fy, nil, today)
				PeriodBounds(fy, &q)
				PeriodLabel(fy, &q)
				PreviousPeriod(fy, &q)
				FyLabel(fy)
				FyMonths(fy)
			}
		}
	}
	for _, d := range []string{"", "2025", "2025-02-30", "٢٠٢٥-٠١-٠١", "2025-01-01\n"} {
		if _, err := FyOf(d); err == nil {
			t.Errorf("FyOf(%q) did not fail", d)
		}
		if _, err := QuarterOf(d); err == nil {
			t.Errorf("QuarterOf(%q) did not fail", d)
		}
	}
	Casefold("\xff\xfe broken \xc3")
}
