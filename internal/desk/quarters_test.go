package desk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/satiyap/banyan/internal/db"
)

// Invented figures throughout.
func TestQuarterlyIncomeAndBalance(t *testing.T) {
	folder := t.TempDir()
	cfg := configFor(t, folder)
	app := newApp(t, cfg, Options{Today: func() string { return "2026-11-23" }})
	versioned := map[string]string{"X-Desk-Version": app.Version(), "Content-Type": "application/json"}
	post := func(path, body string) (int, string) {
		res := send(app, "POST", path, versioned, &body)
		return res.Code, res.Body.String()
	}
	read := func() obj {
		res := get(app, "/api/reports/quarters", "X-Desk-Version", app.Version())
		if res.Code != 200 {
			t.Fatalf("/api/reports/quarters: %d %s", res.Code, res.Body)
		}
		return parse(t, res.Body.String()).(obj)
	}

	// Nothing typed in yet: no table is made by reading, and there is no balance.
	empty := read()
	if empty.value("start") != nil || len(empty.items("quarters")) != 0 || empty.object("current").text("label") != "Q3 FY 2026–27" {
		t.Errorf("before anything is typed: %v", empty)
	}
	if _, err := os.Stat(cfg.DBPath + ".pre-quarters"); err == nil {
		t.Errorf("reading made a copy of the register")
	}

	for _, c := range []struct {
		fy, q, amt int64
		date, status string
	}{
		{2026, 2, 10_000_00, "2026-08-20", "printed"},
		{2026, 2, 2_000_00, "2026-09-30", "logged"},
		{2026, 2, 9_999_00, "2026-09-01", "void"},   // void: not spending
		{2026, 3, 5_000_00, "2026-12-01", "printed"}, // post-dated into Q3: counted by its date
		{2026, 4, 7_000_00, "2027-01-05", "printed"}, // a quarter yet to come: not shown
	} {
		if _, err := app.db.AddCheque(db.NewCheque{
			db.F("payee", "Invented Vendor"), db.F("amount_paise", c.amt), db.F("amount_words", "x"),
			db.F("cheque_date", c.date), db.F("status", c.status),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if code, body := post("/api/quarters/start", `{"fy":2026,"quarter":1,"closing":"1,00,000"}`); code != 200 {
		t.Fatalf("starting balance: %d %s", code, body)
	}
	if _, err := os.Stat(cfg.DBPath + ".pre-quarters"); err != nil {
		t.Errorf("no copy of the register was taken before the table was added")
	}
	if code, body := post("/api/quarters", `{"fy":2026,"quarter":2,"income":"₹30,000.50","outside":"3000"}`); code != 200 || !strings.Contains(body, `"changed":true`) {
		t.Fatalf("Q2: %d %s", code, body)
	}
	if code, body := post("/api/quarters", `{"fy":2026,"quarter":2,"income":"30000.50","outside":"3,000"}`); code != 200 || !strings.Contains(body, `"changed":false`) {
		t.Errorf("the same figures again: %d %s", code, body)
	}

	got := read()
	if got.object("start").text("label") != "Q1 FY 2026–27" {
		t.Errorf("start: %v", got.value("start"))
	}
	qs := got.items("quarters")
	if len(qs) != 2 {
		t.Fatalf("quarters after the start up to now: %v", qs)
	}
	q2, q3 := qs[0].(obj), qs[1].(obj)
	want := func(o obj, key string, n float64) {
		t.Helper()
		if v, _ := o.value(key).(float64); v != n {
			t.Errorf("%s %s = %v, want %v", o.text("label"), key, o.value(key), n)
		}
	}
	// Q2: 30,000.50 in; 12,000 written in the register plus 3,000 outside it.
	want(q2, "income_paise", 3_000_050)
	want(q2, "register_paise", 1_200_000)
	want(q2, "outside_paise", 300_000)
	want(q2, "spent_paise", 1_500_000)
	want(q2, "net_paise", 1_500_050)
	want(q2, "closing_paise", 10_000_000+1_500_050)
	if q2.value("entered") != true || q3.value("entered") != false || q3.value("current") != true {
		t.Errorf("entered/current: %v %v", q2, q3)
	}
	// Q3: nothing typed in, the post-dated cheque written.
	want(q3, "spent_paise", 500_000)
	want(q3, "closing_paise", 10_000_000+1_500_050-500_000)

	// A quarter yet to come is refused; nonsense amounts too; nothing is recorded for either.
	if code, _ := post("/api/quarters", `{"fy":2026,"quarter":4,"income":"1"}`); code != 422 {
		t.Errorf("a future quarter: %d", code)
	}
	if code, _ := post("/api/quarters", `{"fy":2026,"quarter":3,"income":"12.345"}`); code != 422 {
		t.Errorf("three decimals: %d", code)
	}

	// Moving the starting point: one start at a time, and the old one keeps its figures.
	if code, body := post("/api/quarters/start", `{"fy":2026,"quarter":2,"closing":"2,00,000"}`); code != 200 {
		t.Fatalf("new start: %d %s", code, body)
	}
	moved := read()
	if moved.object("start").text("label") != "Q2 FY 2026–27" || len(moved.items("quarters")) != 1 {
		t.Errorf("after moving the start: %v", moved)
	}
	rows, err := db.QuartersOf(app.db).List()
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	for _, r := range rows {
		if r.ClosingPaise != nil {
			starts++
		}
		if r.Quarter == 2 && r.IncomePaise != 3_000_050 {
			t.Errorf("Q2 lost its income when it became the start: %v", r)
		}
	}
	if starts != 1 {
		t.Errorf("%d starting points", starts)
	}

	// Every change is an event about its quarter.
	for subject, kinds := range map[string][]string{
		"quarter:2026-Q1": {"quarter-start"},
		"quarter:2026-Q2": {"quarter-set", "quarter-start"},
	} {
		events, err := app.db.EventsAbout(subject)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range events {
			got = append(got, e.Kind)
		}
		if strings.Join(got, ",") != strings.Join(kinds, ",") {
			t.Errorf("%s events: %v, want %v", subject, got, kinds)
		}
	}
	if _, err := os.Stat(filepath.Join(folder, "register.db")); err != nil {
		t.Fatal(err)
	}
}
