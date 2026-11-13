// Holds the amount package to its recorded answers: tests/golden/amount.json
// holds what the rules answered over invented amounts, and every answer here
// must match.
package amount

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/satiyap/banyan/internal/pycompat"
)

type parsed struct {
	PaiseTotal int64  `json:"paise_total"`
	Rupees     int64  `json:"rupees"`
	Paise      int64  `json:"paise"`
	Words      string `json:"words"`
	Figures    string `json:"figures"`
	Plain      string `json:"plain"`
	Str        string `json:"str"`
}

type refusal struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type functionCase struct {
	N             int64  `json:"n"`
	IndianWords   string `json:"indian_words"`
	GroupIndian   string `json:"group_indian"`
	AmountWords   string `json:"amount_words"`
	AmountFigures string `json:"amount_figures"`
}

type fixture struct {
	Constants struct {
		ONES      []string `json:"ONES"`
		TENS      []string `json:"TENS"`
		MAX_PAISE int64    `json:"MAX_PAISE"`
	} `json:"constants"`
	Parse []struct {
		Input input    `json:"input"`
		Ok    *parsed  `json:"ok"`
		Error *refusal `json:"error"`
	} `json:"parse"`
	Functions []functionCase `json:"functions"`
	Negatives []struct {
		N           int64  `json:"n"`
		GroupIndian string `json:"group_indian"`
	} `json:"negatives"`
}

// input is a recorded JSON value. A string keeps a lone surrogate, which
// encoding/json would replace with U+FFFD, as its three-byte form.
type input struct{ value any }

func (in *input) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || data[0] != '"' || !strings.Contains(string(data), `\ud`) {
		return json.Unmarshal(data, &in.value)
	}
	var b strings.Builder
	body := string(data[1 : len(data)-1])
	surrogate := func(i int) (rune, bool) {
		if i+6 > len(body) || body[i] != '\\' || body[i+1] != 'u' {
			return 0, false
		}
		n, err := strconv.ParseUint(body[i+2:i+6], 16, 16)
		return rune(n), err == nil && n >= 0xd800 && n <= 0xdfff
	}
	for i := 0; i < len(body); {
		hi, ok := surrogate(i)
		if !ok {
			// Any other escape goes through encoding/json unchanged.
			end := i + 1
			if body[i] == '\\' {
				end = i + 2
				if body[i+1] == 'u' {
					end = i + 6
				}
			}
			var s string
			if err := json.Unmarshal([]byte(`"`+body[i:end]+`"`), &s); err != nil {
				return err
			}
			b.WriteString(s)
			i = end
			continue
		}
		if lo, ok := surrogate(i + 6); ok && hi < 0xdc00 && lo >= 0xdc00 {
			b.WriteRune(utf16.DecodeRune(hi, lo))
			i += 12
			continue
		}
		b.Write([]byte{0xed, 0x80 | byte(hi>>6&0x3f), 0x80 | byte(hi&0x3f)})
		i += 6
	}
	in.value = b.String()
	return nil
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	data, err := os.ReadFile("../../tests/golden/amount.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden fixture
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	return golden
}

// The recorded error name, mapped as harness.ts `expected` maps it.
func expectedType(name string) string {
	if name == "AttributeError" {
		return "TypeError"
	}
	return name
}

func TestConstants(t *testing.T) {
	golden := loadFixture(t)
	if !reflect.DeepEqual(ONES, golden.Constants.ONES) {
		t.Errorf("ONES = %q, want %q", ONES, golden.Constants.ONES)
	}
	if !reflect.DeepEqual(TENS, golden.Constants.TENS) {
		t.Errorf("TENS = %q, want %q", TENS, golden.Constants.TENS)
	}
	if MAX_PAISE != golden.Constants.MAX_PAISE {
		t.Errorf("MAX_PAISE = %d, want %d", MAX_PAISE, golden.Constants.MAX_PAISE)
	}
}

func TestParse(t *testing.T) {
	golden := loadFixture(t)
	if len(golden.Parse) < 3000 {
		t.Fatalf("only %d parse cases recorded", len(golden.Parse))
	}
	for i, c := range golden.Parse {
		if (c.Ok == nil) == (c.Error == nil) {
			t.Fatalf("parse[%d]: case has neither or both of ok and error", i)
		}
		a, err := ParseAmount(c.Input.value)
		if c.Error != nil {
			var pe *pycompat.Error
			if !errors.As(err, &pe) {
				t.Errorf("parse[%d] ParseAmount(%#v) = %+v, %v; want %+v", i, c.Input.value, a, err, *c.Error)
				continue
			}
			if got := (refusal{pe.Type, pe.Message}); got != (refusal{expectedType(c.Error.Type), c.Error.Message}) {
				t.Errorf("parse[%d] ParseAmount(%#v) refused with %+v, want %+v", i, c.Input.value, got, *c.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("parse[%d] ParseAmount(%#v) refused with %v, want %+v", i, c.Input.value, err, *c.Ok)
			continue
		}
		got := parsed{a.PaiseTotal, a.Rupees(), a.Paise(), a.Words(), a.Figures(), a.Plain(), a.String()}
		if got != *c.Ok {
			t.Errorf("parse[%d] ParseAmount(%#v) = %+v, want %+v", i, c.Input.value, got, *c.Ok)
		}
	}
	t.Logf("checked %d parse cases", len(golden.Parse))
}

func TestRefusesAndPassesThrough(t *testing.T) {
	_, err := ParseAmount("abc")
	var pe *pycompat.Error
	if !errors.As(err, &pe) || pe.Type != "AmountError" {
		t.Fatalf("ParseAmount(\"abc\") error = %v, want AmountError", err)
	}
	a, err := ParseAmount("1,250.50")
	if err != nil || a.PaiseTotal != 125050 {
		t.Fatalf("ParseAmount(\"1,250.50\") = %+v, %v", a, err)
	}
	if again, err := ParseAmount(a); err != nil || again != a {
		t.Errorf("ParseAmount(Amount) = %+v, %v; want %+v", again, err, a)
	}
	if again, err := ParseAmount(&a); err != nil || again != a {
		t.Errorf("ParseAmount(*Amount) = %+v, %v; want %+v", again, err, a)
	}
}

func TestFunctions(t *testing.T) {
	golden := loadFixture(t)
	if len(golden.Functions) == 0 || len(golden.Negatives) == 0 {
		t.Fatal("no function cases recorded")
	}
	for _, c := range golden.Functions {
		got := functionCase{N: c.N, GroupIndian: GroupIndian(c.N), AmountFigures: AmountFigures(c.N)}
		var err error
		if got.IndianWords, err = IndianWords(c.N); err != nil {
			t.Errorf("IndianWords(%d): %v", c.N, err)
		}
		if got.AmountWords, err = AmountWords(c.N); err != nil {
			t.Errorf("AmountWords(%d): %v", c.N, err)
		}
		if got != c {
			t.Errorf("n=%d: got %+v, want %+v", c.N, got, c)
		}
	}
	for _, c := range golden.Negatives {
		if got := GroupIndian(c.N); got != c.GroupIndian {
			t.Errorf("GroupIndian(%d) = %q, want %q", c.N, got, c.GroupIndian)
		}
	}
	t.Logf("checked %d function cases and %d negatives", len(golden.Functions), len(golden.Negatives))
}

func TestNegativeWordsAreRefused(t *testing.T) {
	if _, err := IndianWords(-1); err == nil {
		t.Error("IndianWords(-1) did not refuse")
	}
	if _, err := AmountWords(-1); err == nil {
		t.Error("AmountWords(-1) did not refuse")
	}
}
