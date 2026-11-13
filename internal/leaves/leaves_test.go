// Holds the package to its recorded answers: tests/golden/leaves.json holds what
// the rules answered over invented books, cheques and blank leaves, and every
// answer here must match.
package leaves

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/satiyap/banyan/internal/pycompat"
)

type refusal struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// The recorded outcome of one case: the value returned, or the error raised.
type outcome struct {
	OK    json.RawMessage `json:"ok"`
	Error *refusal        `json:"error"`
}

type viewArgs struct {
	Books   []BookRow      `json:"books"`
	Cheques []ChequeRow    `json:"cheques"`
	Blanks  []BlankLeafRow `json:"blanks"`
	Today   string         `json:"today"`
}

type fixture struct {
	Constants struct {
		MaxBookLeaves float64 `json:"MAX_BOOK_LEAVES"`
		AlertShare    float64 `json:"ALERT_SHARE"`
		LooseGap      float64 `json:"LOOSE_GAP"`
	} `json:"constants"`
	ParseLeaf []struct {
		Input *string `json:"input"`
		outcome
	} `json:"parse_leaf"`
	FmtLeaf []struct {
		Args []any  `json:"args"`
		OK   string `json:"ok"`
	} `json:"fmt_leaf"`
	BookRange []struct {
		Book BookRow `json:"book"`
		outcome
	} `json:"book_range"`
	CheckNewBook []struct {
		FirstNo     string    `json:"first_no"`
		LastNo      string    `json:"last_no"`
		Books       []BookRow `json:"books"`
		Used        []string  `json:"used"`
		AlreadyUsed bool      `json:"already_used"`
		outcome
	} `json:"check_new_book"`
	LocalDate []struct {
		Zone  string `json:"zone"`
		Cases []struct {
			Input string `json:"input"`
			outcome
		} `json:"cases"`
	} `json:"local_date"`
	PdcState []struct {
		Zone  string `json:"zone"`
		Cases []struct {
			Cheque ChequeRow `json:"cheque"`
			Today  string    `json:"today"`
			outcome
		} `json:"cases"`
	} `json:"pdc_state"`
	BooksView []struct {
		Zone string   `json:"zone"`
		Args viewArgs `json:"args"`
		outcome
	} `json:"books_view"`
	Registers []struct {
		Zone         string   `json:"zone"`
		Args         viewArgs `json:"args"`
		RegisterView outcome  `json:"register_view"`
		BooksView    outcome  `json:"books_view"`
	} `json:"registers"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	data, err := os.ReadFile("../../tests/golden/leaves.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// Runs fn with time.Local set to the given IANA zone, as the recording was made.
func inZone(t *testing.T, zone string, fn func()) {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Local
	time.Local = loc
	defer func() { time.Local = before }()
	fn()
}

// A value as plain JSON data, so structs and recordings compare key by key.
func plain(t *testing.T, v any) any {
	t.Helper()
	data, ok := v.(json.RawMessage)
	if !ok {
		var err error
		if data, err = json.Marshal(v); err != nil {
			t.Fatal(err)
		}
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func short(v any) string {
	s := fmt.Sprintf("%v", v)
	if len(s) > 600 {
		s = s[:600] + "…"
	}
	return s
}

// Holds one answer to its recording: the value, or the error's type and message.
func check(t *testing.T, label string, got any, err error, want outcome) {
	t.Helper()
	if want.Error != nil {
		// Go has no AttributeError; TypeError is returned for both.
		wantType := want.Error.Type
		if wantType == "AttributeError" {
			wantType = "TypeError"
		}
		var e *pycompat.Error
		if !errors.As(err, &e) {
			t.Errorf("%s: got %s, %v; want %s: %s", label, short(got), err, wantType, want.Error.Message)
		} else if e.Type != wantType || e.Message != want.Error.Message {
			t.Errorf("%s: got %s: %s; want %s: %s", label, e.Type, e.Message, wantType, want.Error.Message)
		}
		return
	}
	if want.OK == nil {
		t.Fatalf("%s: recording has neither ok nor error", label)
	}
	if err != nil {
		t.Errorf("%s: got error %v; want %s", label, err, short(string(want.OK)))
		return
	}
	if g, w := plain(t, got), plain(t, want.OK); !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got %s\nwant %s", label, short(g), short(w))
	}
}

func TestConstants(t *testing.T) {
	c := loadFixture(t).Constants
	if MaxBookLeaves != c.MaxBookLeaves || AlertShare != c.AlertShare || LooseGap != c.LooseGap {
		t.Errorf("constants %v %v %v; want %+v", MaxBookLeaves, AlertShare, LooseGap, c)
	}
}

func TestLeafNumbers(t *testing.T) {
	f := loadFixture(t)
	for _, c := range f.ParseLeaf {
		input := ""
		if c.Input != nil {
			input = *c.Input
		}
		var got any
		if leaf, ok := ParseLeaf(input); ok {
			got = []any{leaf.Prefix, leaf.Value, leaf.Width}
		}
		check(t, fmt.Sprintf("ParseLeaf(%q)", input), got, nil, c.outcome)
	}
	for _, c := range f.FmtLeaf {
		got := FmtLeaf(c.Args[0].(string), c.Args[1].(float64), int(c.Args[2].(float64)))
		if got != c.OK {
			t.Errorf("FmtLeaf(%v) = %q; want %q", c.Args, got, c.OK)
		}
	}
	for _, c := range f.BookRange {
		prefix, first, last, width, err := BookRange(c.Book)
		check(t, fmt.Sprintf("BookRange(%q, %q)", c.Book.FirstNo, c.Book.LastNo),
			[]any{prefix, first, last, width}, err, c.outcome)
	}
	t.Logf("checked %d parse_leaf, %d fmt_leaf, %d book_range", len(f.ParseLeaf), len(f.FmtLeaf), len(f.BookRange))
}

func TestCheckNewBook(t *testing.T) {
	f := loadFixture(t)
	for _, c := range f.CheckNewBook {
		got, err := CheckNewBook(c.FirstNo, c.LastNo, c.Books, c.Used, c.AlreadyUsed)
		label := fmt.Sprintf("%s..%s used=%d already=%v", c.FirstNo, c.LastNo, len(c.Used), c.AlreadyUsed)
		check(t, label, got, err, c.outcome)
	}
	_, err := CheckNewBook("x", "y", nil, nil, false)
	var e *pycompat.Error
	if !errors.As(err, &e) || e.Type != "ValueError" {
		t.Errorf("CheckNewBook(x, y) = %v; want a ValueError", err)
	}
	t.Logf("checked %d check_new_book", len(f.CheckNewBook))
}

func TestLocalDate(t *testing.T) {
	n := 0
	for _, z := range loadFixture(t).LocalDate {
		inZone(t, z.Zone, func() {
			for _, c := range z.Cases {
				got, err := LocalDate(c.Input)
				check(t, fmt.Sprintf("%s LocalDate(%q)", z.Zone, c.Input), got, err, c.outcome)
				n++
			}
		})
	}
	t.Logf("checked %d local_date", n)
}

func TestPdcState(t *testing.T) {
	n := 0
	for _, z := range loadFixture(t).PdcState {
		inZone(t, z.Zone, func() {
			for _, c := range z.Cases {
				got, err := PdcState(c.Cheque, c.Today)
				label := fmt.Sprintf("%s PdcState(%q, %q, today=%s)", z.Zone, c.Cheque.CreatedAt, c.Cheque.ChequeDate, c.Today)
				check(t, label, got, err, c.outcome)
				n++
			}
		})
	}
	t.Logf("checked %d pdc_state", n)
}

func TestBooksView(t *testing.T) {
	f := loadFixture(t)
	n := 0
	view := func(zone string, a viewArgs, want outcome) {
		inZone(t, zone, func() {
			got, err := BooksView(a.Books, a.Cheques, a.Blanks, a.Today)
			check(t, fmt.Sprintf("books view %d", n), got, err, want)
		})
		n++
	}
	for _, c := range f.BooksView {
		view(c.Zone, c.Args, c.outcome)
	}
	for _, c := range f.Registers {
		view(c.Zone, c.Args, c.BooksView)
	}
	t.Logf("checked %d books views", n)
}

func TestRegisterView(t *testing.T) {
	f := loadFixture(t)
	for n, c := range f.Registers {
		inZone(t, c.Zone, func() {
			a := c.Args
			got, err := RegisterView(a.Cheques, a.Blanks, a.Books, a.Today)
			label := fmt.Sprintf("register %d (%s)", n, c.Zone)
			// Row by row first, so a mismatch names the row rather than dumping the register.
			var want struct {
				Rows []json.RawMessage `json:"rows"`
			}
			if err == nil && c.RegisterView.OK != nil && json.Unmarshal(c.RegisterView.OK, &want) == nil {
				for i, row := range got.Rows {
					if i < len(want.Rows) && !reflect.DeepEqual(plain(t, row), plain(t, want.Rows[i])) {
						t.Errorf("%s row %d:\n got %s\nwant %s", label, i, short(plain(t, row)), short(plain(t, want.Rows[i])))
						return
					}
				}
			}
			check(t, label, got, err, c.RegisterView)
		})
	}
	t.Logf("checked %d registers", len(f.Registers))
}
