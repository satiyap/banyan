// Holds the package to its recorded answers: tests/golden/calibration.json
// holds what the rules answered over invented calibration files: valid ones,
// every validation failure, and several hundred hand-edit accidents.
package calibration

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/satiyap/banyan/internal/pycompat"
)

type recordedError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// outcome is the recorded result of one case: the value returned, or the error raised.
type outcome struct {
	OK    json.RawMessage `json:"ok"`
	Error *recordedError  `json:"error"`
}

type goldenCase struct {
	Label    string  `json:"label"`
	Filename string  `json:"filename"`
	Kind     string  `json:"kind"`
	Text     *string `json:"text"`
	Base64   *string `json:"base64"`
	outcome
}

type goldenFixture struct {
	Constants json.RawMessage `json:"constants"`
	Cases     []goldenCase    `json:"cases"`
	Real      *struct {
		Filename string `json:"filename"`
		Text     string `json:"text"`
		outcome
	} `json:"real"`
}

func loadGolden(t *testing.T) goldenFixture {
	t.Helper()
	data, err := os.ReadFile("../../tests/golden/calibration.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden goldenFixture
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	return golden
}

// plain is v as encoding/json reads it back, so both sides compare as JSON values.
func plain(t *testing.T, label string, v any) any {
	t.Helper()
	data, ok := v.(json.RawMessage)
	if !ok {
		var err error
		if data, err = json.Marshal(v); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	return out
}

func describeCal(cal *Calibration) map[string]any {
	return map[string]any{
		"as_dict":     cal.AsDict(),
		"leaf":        cal.Leaf(),
		"version":     cal.Version,
		"dx":          cal.DX,
		"dy":          cal.DY,
		"flip":        cal.Flip,
		"paper":       cal.Paper,
		"a4x":         cal.A4X,
		"a4y":         cal.A4Y,
		"fields":      cal.Fields,
		"source_name": cal.AsDict().Source,
		"warnings":    cal.Warnings,
	}
}

// expectedType is the recorded error name as this port names it.
func expectedType(recorded string) string {
	switch recorded {
	case "AttributeError":
		// Go, like JS, has no AttributeError; TypeError is returned for both.
		return "TypeError"
	case "UnicodeDecodeError":
		// Bytes that are not UTF-8: a ValueError, its parent class in Python.
		return "ValueError"
	}
	return recorded
}

// check compares one load against its recording and reports whether it was a refusal.
func check(t *testing.T, label string, want outcome, got func() any, err error) {
	t.Helper()
	if want.Error != nil {
		var refusal *pycompat.Error
		if !errors.As(err, &refusal) {
			t.Errorf("%s: got %v, want %s: %s", label, err, want.Error.Type, want.Error.Message)
			return
		}
		if refusal.Type != expectedType(want.Error.Type) || refusal.Message != want.Error.Message {
			t.Errorf("%s:\n got %s: %s\nwant %s: %s", label, refusal.Type, refusal.Message,
				expectedType(want.Error.Type), want.Error.Message)
		}
		return
	}
	if err != nil {
		t.Errorf("%s: refused with %v, want it loaded", label, err)
		return
	}
	if want.OK == nil {
		t.Fatalf("%s: the recording holds neither a value nor an error", label)
	}
	if g, w := plain(t, label, got()), plain(t, label, want.OK); !reflect.DeepEqual(g, w) {
		gj, _ := json.Marshal(g)
		wj, _ := json.Marshal(w)
		t.Errorf("%s:\n got %s\nwant %s", label, gj, wj)
	}
}

func TestConstants(t *testing.T) {
	golden := loadGolden(t)
	got := map[string]any{
		"CHEQUE_W_MM":       ChequeWMM,
		"CHEQUE_H_MM":       ChequeHMM,
		"PAGE_W_MM":         PageWMM,
		"PAGE_H_MM":         PageHMM,
		"HARDWARE_INSET_MM": HardwareInsetMM,
		"MICR_TOP_MM":       MicrTopMM,
		"REQUIRED_FIELDS":   RequiredFields,
	}
	if g, w := plain(t, "constants", got), plain(t, "constants", golden.Constants); !reflect.DeepEqual(g, w) {
		t.Errorf("constants:\n got %v\nwant %v", g, w)
	}
}

func TestGoldenCases(t *testing.T) {
	golden := loadGolden(t)
	if len(golden.Cases) == 0 {
		t.Fatal("the recording holds no cases")
	}
	dir := t.TempDir()
	loaded, refused := 0, 0
	for n, c := range golden.Cases {
		folder := filepath.Join(dir, strconv.Itoa(n))
		if err := os.Mkdir(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(folder, c.Filename)
		switch c.Kind {
		case "dir":
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
		case "file":
			var data []byte
			switch {
			case c.Base64 != nil:
				var err error
				if data, err = base64.StdEncoding.DecodeString(*c.Base64); err != nil {
					t.Fatalf("%s: %v", c.Label, err)
				}
			case c.Text != nil:
				data = []byte(*c.Text)
			default:
				t.Fatalf("%s: a file case with no contents", c.Label)
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
		case "missing":
		default:
			t.Fatalf("%s: unknown kind %q", c.Label, c.Kind)
		}
		cal, err := Load(path)
		check(t, c.Label, c.outcome, func() any { return describeCal(cal) }, err)
		if c.Error != nil {
			refused++
		} else {
			loaded++
		}
	}
	t.Logf("checked %d cases: %d loaded, %d refused", len(golden.Cases), loaded, refused)
}

func TestRefusalIsCalibrationError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nowhere.json"))
	var refusal *pycompat.Error
	if !errors.As(err, &refusal) || refusal.Type != ErrorType {
		t.Fatalf("got %v, want a CalibrationError", err)
	}
}

func TestRealFile(t *testing.T) {
	real := loadGolden(t).Real
	if real == nil {
		t.Fatal("the recording holds no cheque-calibration.json")
	}
	path := filepath.Join("../..", real.Filename)
	cal, err := Load(path)
	// The file is recalibrated from time to time. Whatever it says, it must load.
	if err != nil {
		t.Fatalf("%s: %v", real.Filename, err)
	}
	// While it is still the file that was recorded, it must also read the same.
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) == real.Text {
		check(t, real.Filename, real.outcome, func() any {
			return map[string]any{"as_dict": cal.AsDict(), "leaf": cal.Leaf(), "warnings": cal.Warnings}
		}, nil)
	} else {
		t.Logf("%s has changed since it was recorded; only checked that it loads", real.Filename)
	}
	if _, ok := cal.Version.(float64); !ok {
		t.Errorf("version is %T, want a number", cal.Version)
	}
	if x, ok := cal.Field("date").Number("x"); !ok || x <= 0 {
		t.Errorf("date.x = %v, %v", x, ok)
	}
}

// The browser gets keys in file order, and null where JSON has no number.
func TestJSONShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cal.json")
	text := `{"version": true, "dx": "nan", "dy": -0.0, "fields": {"10": {"x": 5, "y": 5}, "2": {"x": 1e21, "note": "a\u00d7b"},` +
		` "date": {}, "payee": {}, "words1": {}, "words2": {}, "amount": {}, "cross": {}, "bearer": {}}}`
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	cal, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsNaN(cal.DX) || cal.VersionNumber() != 1 || cal.Field("memo") != nil {
		t.Errorf("dx = %v, version = %v, memo = %v", cal.DX, cal.Version, cal.Field("memo"))
	}
	got, err := json.Marshal(cal)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":true,"dx":null,"dy":0,"flip":false,"paper":"exact","a4x":0,"a4y":0,` +
		`"fields":{"10":{"x":5,"y":5},"2":{"x":1e+21,"note":"a×b"},` +
		`"date":{},"payee":{},"words1":{},"words2":{},"amount":{},"cross":{},"bearer":{}},` +
		`"leaf":{"w":204,"h":92},"page":{"w":92,"h":204},"hardware_inset_mm":4.23,"micr_top_mm":76,` +
		`"source":"cal.json","warnings":[]}`
	if string(got) != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
}
