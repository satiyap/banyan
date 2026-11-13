// Package calibration loads cheque-calibration.json — the measured geometry of
// the physical leaf.
//
// That file stays the single source of truth for millimetres. Nothing here
// invents a coordinate: if the JSON is missing or disagrees with the leaf this
// app was measured for, loading fails loudly rather than printing a
// plausible-looking cheque at the wrong offsets onto real bank stationery.
package calibration

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/satiyap/banyan/internal/pycompat"
)

// Physical facts. The leaf is landscape; the printer page is that leaf rotated
// 90 degrees, because 92mm is below the M126nw's minimum feed length.
const (
	ChequeWMM = 204.0
	ChequeHMM = 92.0
	PageWMM   = 92.0
	PageHMM   = 204.0
)

// The M126nw PPD declares an ImageableArea inset of 11.9906pt on all four edges.
// Nothing closer than this to a paper edge receives toner, whatever the CSS says.
const HardwareInsetMM = 4.23

// The MICR band and everything below it must stay clear of dynamic content.
const MicrTopMM = 76.0

// RequiredFields are the fields every calibration file must measure.
var RequiredFields = []string{"date", "payee", "words1", "words2", "amount", "cross", "bearer"}

// ErrorType is the Type of the *pycompat.Error returned when the calibration
// file is missing, malformed, or for a different leaf.
const ErrorType = "CalibrationError"

// CalibrationError builds that refusal.
func CalibrationError(format string, args ...any) *pycompat.Error {
	return &pycompat.Error{Type: ErrorType, Message: fmt.Sprintf(format, args...)}
}

// Size is a width and height in millimetres.
type Size struct {
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Dict is what Calibration.AsDict returns — the shape served to the browser.
type Dict struct {
	Version         any      `json:"version"`
	DX              float64  `json:"dx"`
	DY              float64  `json:"dy"`
	Flip            bool     `json:"flip"`
	Paper           string   `json:"paper"`
	A4X             float64  `json:"a4x"`
	A4Y             float64  `json:"a4y"`
	Fields          *Object  `json:"fields"`
	Leaf            Size     `json:"leaf"`
	Page            Size     `json:"page"`
	HardwareInsetMM float64  `json:"hardware_inset_mm"`
	MicrTopMM       float64  `json:"micr_top_mm"`
	Source          string   `json:"source"`
	Warnings        []string `json:"warnings"`
}

// MarshalJSON writes the fields in the order above. A NaN or infinite number
// is written as null, as JSON.stringify writes it.
func (d Dict) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	size := func(s Size) {
		b.WriteString(`{"w":`)
		writeNumber(&b, s.W)
		b.WriteString(`,"h":`)
		writeNumber(&b, s.H)
		b.WriteByte('}')
	}
	b.WriteString(`{"version":`)
	if err := writeJSON(&b, d.Version); err != nil {
		return nil, err
	}
	b.WriteString(`,"dx":`)
	writeNumber(&b, d.DX)
	b.WriteString(`,"dy":`)
	writeNumber(&b, d.DY)
	b.WriteString(`,"flip":`)
	writeJSON(&b, d.Flip)
	b.WriteString(`,"paper":`)
	writeJSON(&b, d.Paper)
	b.WriteString(`,"a4x":`)
	writeNumber(&b, d.A4X)
	b.WriteString(`,"a4y":`)
	writeNumber(&b, d.A4Y)
	b.WriteString(`,"fields":`)
	if err := writeJSON(&b, d.Fields); err != nil {
		return nil, err
	}
	b.WriteString(`,"leaf":`)
	size(d.Leaf)
	b.WriteString(`,"page":`)
	size(d.Page)
	b.WriteString(`,"hardware_inset_mm":`)
	writeNumber(&b, d.HardwareInsetMM)
	b.WriteString(`,"micr_top_mm":`)
	writeNumber(&b, d.MicrTopMM)
	b.WriteString(`,"source":`)
	writeJSON(&b, d.Source)
	b.WriteString(`,"warnings":[`)
	for i, w := range d.Warnings {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSON(&b, w)
	}
	b.WriteString("]}")
	return b.Bytes(), nil
}

// Calibration is a loaded, validated calibration file.
type Calibration struct {
	// As in the file: a float64, or a bool when the file says true/false.
	// Python accepts those (bool is an int there), and so does this.
	Version any
	DX      float64
	DY      float64
	Flip    bool
	// "a4" or "exact".
	Paper string
	A4X   float64
	A4Y   float64
	// Field name -> *Object of its measurements, in file order.
	Fields *Object
	// Path the calibration was loaded from.
	Source   string
	Warnings []string
}

// Leaf is the leaf's width and height in millimetres.
func (c *Calibration) Leaf() [2]float64 { return [2]float64{ChequeWMM, ChequeHMM} }

// Field is one field's measurements, exactly as written in the file (x, y, w,
// h, size, pitch, ...), or nil when the file has no such field.
func (c *Calibration) Field(name string) *Object { return c.Fields.Object(name) }

// VersionNumber is Version as a number (true counts as 1, false as 0).
func (c *Calibration) VersionNumber() float64 {
	switch v := c.Version.(type) {
	case float64:
		return v
	case bool:
		if v {
			return 1
		}
	}
	return 0
}

// AsDict is the shape served to the browser.
func (c *Calibration) AsDict() Dict {
	warnings := c.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return Dict{
		Version:         c.Version,
		DX:              c.DX,
		DY:              c.DY,
		Flip:            c.Flip,
		Paper:           c.Paper,
		A4X:             c.A4X,
		A4Y:             c.A4Y,
		Fields:          c.Fields,
		Leaf:            Size{W: ChequeWMM, H: ChequeHMM},
		Page:            Size{W: PageWMM, H: PageHMM},
		HardwareInsetMM: HardwareInsetMM,
		MicrTopMM:       MicrTopMM,
		Source:          filepath.Base(c.Source),
		Warnings:        warnings,
	}
}

// MarshalJSON serialises the calibration as AsDict does.
func (c *Calibration) MarshalJSON() ([]byte, error) { return c.AsDict().MarshalJSON() }

// readText is Path.read_text(): UTF-8, strict, with \r\n and \r read as \n.
func readText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := utf8Problem(data); err != nil {
		return "", err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n"), nil
}

// Load reads and validates the calibration file.
func Load(path string) (*Calibration, error) {
	name := filepath.Base(path)
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return nil, CalibrationError("%s not found — cannot print without measured geometry", name)
	}
	text, err := readText(path)
	if err != nil {
		return nil, err
	}
	parsed, err := pyJSONLoads(text)
	if syntax, ok := err.(jsonDecodeError); ok {
		return nil, CalibrationError("%s is not valid JSON: %s", name, string(syntax))
	} else if err != nil {
		return nil, err
	}

	raw, ok := parsed.(*Object)
	if !ok || !raw.Has("fields") {
		return nil, CalibrationError(`%s has no "fields" section`, name)
	}
	var version any
	switch v := raw.value("version").(type) {
	case pyInt:
		version = v.value
	case bool:
		version = v
	default:
		return nil, CalibrationError(`%s needs an integer "version"`, name)
	}

	if leafRaw := raw.value("leaf"); truthy(leafRaw) {
		w, err := getOf(leafRaw, "w")
		if err != nil {
			return nil, err
		}
		h, err := getOf(leafRaw, "h")
		if err != nil {
			return nil, err
		}
		wn, wok := numberOf(w)
		hn, hok := numberOf(h)
		if !wok || !hok || wn != ChequeWMM || hn != ChequeHMM {
			return nil, CalibrationError("%s is for a %s×%smm leaf, but this app is built for %s×%smm",
				name, pyStr(w, false), pyStr(h, false), jsNumber(ChequeWMM), jsNumber(ChequeHMM))
		}
	}

	fieldsRaw, ok := raw.value("fields").(*Object)
	if !ok {
		return nil, pycompat.TypeError("'%s' object has no attribute 'items'", typeName(raw.value("fields")))
	}
	fields := newObject()
	for _, k := range fieldsRaw.keys {
		if v, isDict := fieldsRaw.values[k].(*Object); isDict && !strings.HasPrefix(k, "_") {
			fields.set(k, v)
		}
	}
	var missing []string
	for _, k := range RequiredFields {
		if !fields.Has(k) {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, CalibrationError("%s is missing field(s): %s", name, strings.Join(missing, ", "))
	}

	number := func(key string) (float64, error) {
		if !raw.Has(key) {
			return 0, nil
		}
		return pyFloat(raw.value(key))
	}
	// Evaluated in the order Python evaluates them, so the first bad value is the one reported.
	cal := &Calibration{Version: version, Paper: "exact", Source: path}
	if cal.DX, err = number("dx"); err != nil {
		return nil, err
	}
	if cal.DY, err = number("dy"); err != nil {
		return nil, err
	}
	cal.Flip = truthy(raw.value("flip"))
	if raw.value("paper") == "a4" {
		cal.Paper = "a4"
	}
	if cal.A4X, err = number("a4x"); err != nil {
		return nil, err
	}
	if cal.A4Y, err = number("a4y"); err != nil {
		return nil, err
	}
	cal.Fields = toPlain(fields).(*Object)
	if cal.Warnings, err = geometryWarnings(fields, cal.DY); err != nil {
		return nil, err
	}
	return cal, nil
}

// geometryWarnings flags geometry that would print somewhere it must not.
//
// These are warnings rather than refusals: the crossing is deliberately
// anchored inside the printer's non-printable margin, so a blanket refusal
// would block a settled design decision.
func geometryWarnings(fields *Object, dy float64) ([]string, error) {
	warnings := []string{}
	for _, name := range fields.keys {
		f := fields.values[name].(*Object)
		y := f.value("y")
		if y == nil {
			continue
		}
		var height any = pyInt{}
		if size := f.value("size"); truthy(size) {
			height = size
		} else if h := f.value("h"); truthy(h) {
			height = h
		}
		sum, err := pyAdd(y, height)
		if err == nil {
			sum, err = pyAdd(sum, pyInt{value: 2})
		}
		if err == nil {
			sum, err = pyAdd(sum, pyFloatValue(dy))
		}
		if err != nil {
			return nil, err
		}
		bottom, err := orderable(sum, ">=")
		if err != nil {
			return nil, err
		}
		if bottom >= MicrTopMM {
			warnings = append(warnings, fmt.Sprintf(`field "%s" reaches y=%smm, inside the MICR band at %smm`,
				name, fixed1(bottom), jsNumber(MicrTopMM)))
		}
		if name != "cross" {
			var x any = pyInt{}
			if f.Has("x") {
				x = f.value("x")
			}
			xn, err := orderable(x, "<")
			if err != nil {
				return nil, err
			}
			inside := xn < HardwareInsetMM
			if !inside {
				yn, err := orderable(y, "<")
				if err != nil {
					return nil, err
				}
				inside = yn < HardwareInsetMM
			}
			if inside {
				warnings = append(warnings, fmt.Sprintf(
					`field "%s" starts within the %smm non-printable margin and may not receive toner`,
					name, jsNumber(HardwareInsetMM)))
			}
		}
	}
	return warnings, nil
}
