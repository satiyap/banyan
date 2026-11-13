// Holds the package to its recorded answers: tests/golden/dating.json holds what
// the rules answered over invented cheques, entities and reminder events, and
// every answer here must match.
package dating

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
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

type fixture struct {
	Constants struct {
		Issued []string `json:"ISSUED"`
	} `json:"constants"`
	Rules    []*string `json:"rules"`
	RuleDate []struct {
		Today    string   `json:"today"`
		OK       []string `json:"ok"`
		Tomorrow string   `json:"tomorrow"`
	} `json:"rule_date"`
	RemindDays []struct {
		Input *string `json:"input"`
		outcome
	} `json:"remind_days"`
	Patterns []struct {
		Name  string `json:"name"`
		Input string `json:"input"`
		OK    bool   `json:"ok"`
	} `json:"patterns"`
	Entities  [][2]json.RawMessage `json:"entities"`
	Reminders []struct {
		Zone    string              `json:"zone"`
		Today   string              `json:"today"`
		Cheques []ReminderChequeRow `json:"cheques"`
		Events  []ReminderEventRow  `json:"events"`
		outcome
	} `json:"reminders"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	data, err := os.ReadFile("../../tests/golden/dating.json")
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

func TestConstantsAndPatterns(t *testing.T) {
	f := loadFixture(t)
	if !reflect.DeepEqual(Issued, f.Constants.Issued) {
		t.Errorf("Issued = %v; want %v", Issued, f.Constants.Issued)
	}
	patterns := map[string]*regexp.Regexp{"RULE_RE": RuleRE, "DAYS_RE": DaysRE}
	for _, c := range f.Patterns {
		re, ok := patterns[c.Name]
		if !ok {
			t.Fatalf("unknown pattern %s", c.Name)
		}
		if got := re.MatchString(c.Input); got != c.OK {
			t.Errorf("%s on %q = %v; want %v", c.Name, c.Input, got, c.OK)
		}
	}
	t.Logf("checked %d patterns", len(f.Patterns))
}

func TestRuleDate(t *testing.T) {
	f := loadFixture(t)
	n := 0
	for _, day := range f.RuleDate {
		if len(day.OK) != len(f.Rules) {
			t.Fatalf("%s: %d answers for %d rules", day.Today, len(day.OK), len(f.Rules))
		}
		for i, rule := range f.Rules {
			r := ""
			if rule != nil {
				r = *rule
			}
			got, err := RuleDate(r, day.Today)
			if err != nil || got != day.OK[i] {
				t.Errorf("RuleDate(%q, %s) = %q, %v; want %q", r, day.Today, got, err, day.OK[i])
			}
			n++
		}
		if got, err := Tomorrow(day.Today); err != nil || got != day.Tomorrow {
			t.Errorf("Tomorrow(%s) = %q, %v; want %q", day.Today, got, err, day.Tomorrow)
		}
	}
	t.Logf("checked %d rule dates (%d rules on %d days) and %d tomorrows", n, len(f.Rules), len(f.RuleDate), len(f.RuleDate))
}

func TestRemindDays(t *testing.T) {
	f := loadFixture(t)
	for _, c := range f.RemindDays {
		input := ""
		if c.Input != nil {
			input = *c.Input
		}
		got, err := RemindDays(input)
		check(t, fmt.Sprintf("RemindDays(%q)", input), got, err, c.outcome)
	}
	t.Logf("checked %d remind_days", len(f.RemindDays))
}

func TestReminders(t *testing.T) {
	f := loadFixture(t)
	entities := map[int]ReminderEntityRow{}
	for _, pair := range f.Entities {
		var id int
		var row ReminderEntityRow
		if err := json.Unmarshal(pair[0], &id); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(pair[1], &row); err != nil {
			t.Fatal(err)
		}
		entities[id] = row
	}
	for n, c := range f.Reminders {
		inZone(t, c.Zone, func() {
			got, err := Reminders(c.Cheques, entities, c.Events, c.Today)
			check(t, fmt.Sprintf("reminders %d (%s, today %s)", n, c.Zone, c.Today), got, err, c.outcome)
		})
	}
	t.Logf("checked %d reminders against %d entities", len(f.Reminders), len(entities))
}
