// Holds the package to its recorded answers: tests/golden/frequency.json holds
// what the rules answered over invented registers, and every answer here must
// match.
package frequency

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/satiyap/banyan/internal/pycompat"
)

type goldenRun struct {
	Today                string          `json:"today"`
	Analyse              json.RawMessage `json:"analyse"`
	AmountsPaise         json.RawMessage `json:"amounts_paise"`
	Summarise            json.RawMessage `json:"summarise"`
	AnalyseWithoutMeta   json.RawMessage `json:"analyse_without_meta"`
	SummariseWithoutMeta json.RawMessage `json:"summarise_without_meta"`
	AnalysePayee         json.RawMessage `json:"analyse_payee"`
}

type goldenCase struct {
	Name string        `json:"name"`
	Note string        `json:"note"`
	Rows []ChequeRow   `json:"rows"`
	Meta EntityMetaMap `json:"meta"`
	Runs []goldenRun   `json:"runs"`
}

type goldenFixture struct {
	Constants struct {
		Cadences      json.RawMessage `json:"CADENCES"`
		MinForCadence int             `json:"MIN_FOR_CADENCE"`
		DueSoonDays   int             `json:"DUE_SOON_DAYS"`
	} `json:"constants"`
	AddMonths [][3]any     `json:"add_months"`
	Cases     []goldenCase `json:"cases"`
}

func loadFixture(t *testing.T) goldenFixture {
	t.Helper()
	raw, err := os.ReadFile("../../tests/golden/frequency.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture goldenFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// tokens flattens JSON into its token stream, numbers as float64, so that key
// order is held and 0 equals 0.0.
func tokens(raw []byte) ([]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var out []any
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, tok)
	}
}

// expectSame holds the Go value to the recorded JSON: deep equality of the
// generic forms, and then the same token stream, which also pins the order of
// the keys. Floats are compared exactly.
func expectSame(t *testing.T, what string, actual any, expected json.RawMessage) {
	t.Helper()
	if expected == nil {
		t.Fatalf("%s: nothing recorded", what)
	}
	got, err := json.Marshal(actual)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if err := json.Unmarshal(expected, &b); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("%s differs\n got: %s\nwant: %s", what, got, expected)
	}
	x, err := tokens(got)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	y, err := tokens(expected)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if !reflect.DeepEqual(x, y) {
		t.Fatalf("%s: key order differs\n got: %s\nwant: %s", what, got, expected)
	}
}

func asDicts(rhythms []*PayeeRhythm) []PayeeRhythmDict {
	out := make([]PayeeRhythmDict, len(rhythms))
	for i, r := range rhythms {
		out[i] = r.AsDict()
	}
	return out
}

func amountsOf(rhythms []*PayeeRhythm) [][]int64 {
	out := make([][]int64, len(rhythms))
	for i, r := range rhythms {
		out[i] = r.AmountsPaise
	}
	return out
}

func TestGoldenConstants(t *testing.T) {
	fixture := loadFixture(t)
	if len(fixture.Cases) < 40 {
		t.Fatalf("only %d invented registers", len(fixture.Cases))
	}
	tuples := make([][3]any, len(Cadences))
	for i, c := range Cadences {
		tuples[i] = [3]any{c.Name, c.Period, c.Tolerance}
	}
	expectSame(t, "CADENCES", tuples, fixture.Constants.Cadences)
	if MinForCadence != fixture.Constants.MinForCadence {
		t.Errorf("MinForCadence = %d, want %d", MinForCadence, fixture.Constants.MinForCadence)
	}
	if DueSoonDays != fixture.Constants.DueSoonDays {
		t.Errorf("DueSoonDays = %d, want %d", DueSoonDays, fixture.Constants.DueSoonDays)
	}
}

func TestGoldenAddMonths(t *testing.T) {
	fixture := loadFixture(t)
	if len(fixture.AddMonths) <= 100 {
		t.Fatalf("only %d addMonths recordings", len(fixture.AddMonths))
	}
	for _, rec := range fixture.AddMonths {
		d, ok1 := rec[0].(string)
		months, ok2 := rec[1].(float64)
		want, ok3 := rec[2].(string)
		if !ok1 || !ok2 || !ok3 {
			t.Fatalf("malformed recording %v", rec)
		}
		got, err := AddMonths(d, int(months))
		if err != nil || got != want {
			t.Errorf("%s + %d months = %q, %v; want %q", d, int(months), got, err, want)
		}
	}
}

func TestGoldenCases(t *testing.T) {
	fixture := loadFixture(t)
	cases, runs := 0, 0
	for _, c := range fixture.Cases {
		cases++
		if len(c.Runs) == 0 {
			t.Errorf("%s: no runs recorded", c.Name)
		}
		frozenRows, err := json.Marshal(c.Rows)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range c.Runs {
			runs++
			t.Run(fmt.Sprintf("%s/today %s", c.Name, run.Today), func(t *testing.T) {
				rhythms, err := Analyse(c.Rows, run.Today, c.Meta)
				if err != nil {
					t.Fatal(err)
				}
				expectSame(t, "analyse", asDicts(rhythms), run.Analyse)
				expectSame(t, "amounts_paise", amountsOf(rhythms), run.AmountsPaise)
				summary, err := Summarise(rhythms, c.Rows, run.Today)
				if err != nil {
					t.Fatal(err)
				}
				expectSame(t, "summarise", summary, run.Summarise)

				if (run.AnalyseWithoutMeta == nil) != (run.SummariseWithoutMeta == nil) {
					t.Fatal("only one of the without-meta answers is recorded")
				}
				if run.AnalyseWithoutMeta != nil {
					bare, err := Analyse(c.Rows, run.Today, nil)
					if err != nil {
						t.Fatal(err)
					}
					expectSame(t, "analyse_without_meta", asDicts(bare), run.AnalyseWithoutMeta)
					bareSummary, err := Summarise(bare, c.Rows, run.Today)
					if err != nil {
						t.Fatal(err)
					}
					expectSame(t, "summarise_without_meta", bareSummary, run.SummariseWithoutMeta)
				}
				if run.AnalysePayee != nil {
					whole, err := AnalysePayee("Everyone", c.Rows, run.Today)
					if err != nil {
						t.Fatal(err)
					}
					expectSame(t, "analyse_payee", whole.AsDict(), run.AnalysePayee)
				}
				after, err := json.Marshal(c.Rows)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(after, frozenRows) {
					t.Error("the rows passed in were changed")
				}
			})
		}
	}
	t.Logf("checked %d cases, %d runs", cases, runs)
}

func gardeningRow(id int64, date string, paise int64) ChequeRow {
	return ChequeRow{
		ID:          id,
		ChequeNo:    fmt.Sprint(id),
		Payee:       "Greenleaf Gardening",
		AmountPaise: paise,
		ChequeDate:  date,
		Memo:        "",
		Status:      "printed",
	}
}

func TestDefaultsTodayToLocalDate(t *testing.T) {
	now := time.Now()
	local := now.Format("2006-01-02")
	rows := []ChequeRow{
		gardeningRow(1, "2025-01-05", 100),
		gardeningRow(2, "2025-02-05", 100),
		gardeningRow(3, "2025-03-05", 100),
	}
	implicit, err := Analyse(rows, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := Analyse(rows, local, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(asDicts(implicit), asDicts(explicit)) {
		t.Errorf("analyse without today differs from today %s", local)
	}
	a, err := Summarise(implicit, rows, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Summarise(explicit, rows, local)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("summarise without today differs from today %s", local)
	}
}

func TestRejectsNonIsoDates(t *testing.T) {
	_, err := Analyse([]ChequeRow{gardeningRow(1, "05/01/2025", 100)}, "2025-06-01", nil)
	var refusal *pycompat.Error
	if !errors.As(err, &refusal) || refusal.Type != "ValueError" ||
		refusal.Message != "Invalid isoformat string: '05/01/2025'" {
		t.Errorf("got %v", err)
	}
	if _, err := Analyse([]ChequeRow{gardeningRow(1, "2025-02-30", 100)}, "2025-06-01", nil); err == nil {
		t.Error("2025-02-30 was accepted")
	}
	if _, err := Summarise(nil, []ChequeRow{gardeningRow(1, "2025-02-30", 100)}, "2025-06-01"); err == nil {
		t.Error("summarise accepted 2025-02-30")
	}
}

func TestBadInputDoesNotPanic(t *testing.T) {
	if _, err := AnalysePayee("Nobody", nil, "2025-06-01"); err == nil {
		t.Error("no rows gave a rhythm")
	}
	if _, err := AddMonths("", 1); err == nil {
		t.Error("an empty date was accepted")
	}
	rhythms, err := Analyse(nil, "not a date", nil)
	if err != nil || rhythms == nil || len(rhythms) != 0 {
		t.Errorf("no rows: %v, %v", rhythms, err)
	}
	if _, err := Summarise([]*PayeeRhythm{nil}, nil, "2025-06-01"); err != nil {
		t.Error(err)
	}
	_ = (*PayeeRhythm)(nil).AsDict()
}
