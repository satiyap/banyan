// Package render composes a cheque into a print-ready PDF, and the monthly
// report into an A4 one.
//
// A cheque page is rendered at 92 x 204mm and the resulting PDF is checked
// against that size before anyone is allowed to spend a cheque leaf on it. The
// print dialog is not involved, so it cannot silently rescale or refit the
// page: the one failure mode that is indistinguishable from a geometry bug.
//
// The page box is verified on every single render. A PDF that is not
// 92 x 204mm at 1:1 is an error, never a warning.
package render

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/satiyap/banyan/internal/calibration"
	"github.com/satiyap/banyan/internal/pycompat"
	"github.com/satiyap/banyan/internal/webpdf"
)

// PageToleranceMM: anything further than this from the declared page size
// means the render did not come out at 1:1 and must not be printed.
const PageToleranceMM = 0.5

// Error is a RenderError: no usable PDF came out.
func Error(format string, args ...any) *pycompat.Error {
	return &pycompat.Error{Type: "RenderError", Message: fmt.Sprintf(format, args...)}
}

// Engine turns one HTML file into one PDF file.
type Engine func(htmlPath, pdfPath string, page webpdf.Page) error

// Renderer holds what a render needs: the app's own files and the engine.
type Renderer struct {
	// Assets holds templates/cheque_print.html.tpl, static/cheque.css,
	// static/cheque-layout.js and static/fonts.
	Assets fs.FS
	// Engine defaults to the Mac's own WebKit.
	Engine Engine
}

func (r *Renderer) engine() Engine {
	if r.Engine != nil {
		return r.Engine
	}
	return webpdf.Render
}

// Info says what renders PDFs here, for the health check.
func (r *Renderer) Info() (ok bool, path string) {
	if r.Engine != nil || webpdf.Available {
		return true, "built-in"
	}
	return false, "no PDF renderer on this system"
}

// Result is one rendered cheque.
type Result struct {
	PdfPath  string
	PageWMM  float64
	PageHMM  float64
	Pages    int
	HTMLPath string // empty unless the page was kept
}

// ResultDict is the shape a Result is reported in.
type ResultDict struct {
	Pdf     string  `json:"pdf"`
	PageWMM float64 `json:"page_w_mm"`
	PageHMM float64 `json:"page_h_mm"`
	Pages   int     `json:"pages"`
}

func round2(n float64) float64 { return math.Floor(n*100+0.5) / 100 }

func (r Result) AsDict() ResultDict {
	return ResultDict{Pdf: r.PdfPath, PageWMM: round2(r.PageWMM), PageHMM: round2(r.PageHMM), Pages: r.Pages}
}

// g is Python's {x:g}: six significant digits, no trailing zeros.
func g(n float64) string { return strconv.FormatFloat(n, 'g', 6, 64) }

var placeholderRe = regexp.MustCompile(`\$\$|\$\{(\w+)\}|\$(\w+)`)

// substitute is Python's string.Template.substitute for $name and ${name}.
func substitute(template string, values map[string]string) (string, error) {
	var missing string
	out := placeholderRe.ReplaceAllStringFunc(template, func(match string) string {
		if match == "$$" {
			return "$"
		}
		m := placeholderRe.FindStringSubmatch(match)
		key := m[1]
		if key == "" {
			key = m[2]
		}
		value, ok := values[key]
		if !ok && missing == "" {
			missing = key
		}
		return value
	})
	if missing != "" {
		return "", Error("the print template asks for an unknown value: %s", missing)
	}
	return out, nil
}

// BuildHTML inlines everything into one self-contained page.
//
// Self-contained matters: the page is written to a temp file and opened as a
// file, where any external fetch would be blocked. If data were fetched rather
// than inlined, a blocked request would print an empty or default cheque
// instead of failing.
func (r *Renderer) BuildHTML(cal *calibration.Calibration, cheque any) (string, error) {
	pageW, pageH := calibration.PageWMM, calibration.PageHMM
	values := map[string]string{"page_w": g(pageW), "page_h": g(pageH)}
	if cal.Paper == "a4" {
		values["page_css"] = "@page { size: A4; margin: 0; }"
		values["root_w"], values["root_h"] = "210mm", "297mm"
		values["sheet_x"], values["sheet_y"] = g(cal.A4X)+"mm", g(cal.A4Y)+"mm"
	} else {
		values["page_css"] = fmt.Sprintf("@page { size: %smm %smm; margin: 0; }", g(pageW), g(pageH))
		values["root_w"], values["root_h"] = g(pageW)+"mm", g(pageH)+"mm"
		values["sheet_x"], values["sheet_y"] = "0mm", "0mm"
	}
	// A payee named "</script>" would otherwise close the tag it is sitting
	// in. encoding/json writes <, > and & as <, > and &, which
	// are valid inside JSON strings and parse back to the originals.
	payload, err := json.Marshal(struct {
		Cal    *calibration.Calibration `json:"cal"`
		Cheque any                      `json:"cheque"`
	}{cal, cheque})
	if err != nil {
		return "", Error("the cheque could not be written into the print page: %v", err)
	}
	values["data"] = string(payload)
	for key, name := range map[string]string{
		"cheque_css": "static/cheque.css",
		"layout_js":  "static/cheque-layout.js",
	} {
		text, err := fs.ReadFile(r.Assets, name)
		if err != nil {
			return "", Error("the app is missing %s", name)
		}
		values[key] = string(text)
	}
	template, err := fs.ReadFile(r.Assets, "templates/cheque_print.html.tpl")
	if err != nil {
		return "", Error("the app is missing the print template")
	}
	return substitute(string(template), values)
}

// RenderPDF renders one cheque to PDF and verifies the page box before returning.
func (r *Renderer) RenderPDF(cal *calibration.Calibration, cheque any, outPdf string, keepHTML bool) (Result, error) {
	if err := os.MkdirAll(filepath.Dir(outPdf), 0o755); err != nil {
		return Result{}, Error("the PDF folder could not be made: %v", err)
	}
	html, err := r.BuildHTML(cal, cheque)
	if err != nil {
		return Result{}, err
	}
	tmpDir, err := os.MkdirTemp("", "cheque-render-")
	if err != nil {
		return Result{}, Error("no temporary folder for the print page: %v", err)
	}
	if !keepHTML {
		defer os.RemoveAll(tmpDir)
	}
	htmlPath := filepath.Join(tmpDir, "cheque.html")
	if err := os.WriteFile(htmlPath, []byte(html), 0o600); err != nil {
		return Result{}, Error("the print page could not be written: %v", err)
	}
	// A PDF left over from an earlier render must never pass for this one.
	_ = os.Remove(outPdf)

	expected := [2]float64{calibration.PageWMM, calibration.PageHMM}
	if cal.Paper == "a4" {
		expected = [2]float64{210, 297}
	}
	if err := r.engine()(htmlPath, outPdf, webpdf.Page{W: expected[0], H: expected[1]}); err != nil {
		return Result{}, asRenderError(err)
	}
	width, height, pages, err := ReadPageBox(outPdf)
	if err != nil {
		return Result{}, err
	}
	off := math.Max(math.Abs(width-expected[0]), math.Abs(height-expected[1]))
	if off > PageToleranceMM {
		return Result{}, Error("rendered page is %.1f x %.1fmm but must be %s x %smm — not safe to print",
			width, height, g(expected[0]), g(expected[1]))
	}
	if pages != 1 {
		return Result{}, Error("rendered %d pages; a cheque must be exactly one", pages)
	}
	result := Result{PdfPath: outPdf, PageWMM: width, PageHMM: height, Pages: pages}
	if keepHTML {
		result.HTMLPath = htmlPath
	}
	return result, nil
}

func asRenderError(err error) *pycompat.Error {
	if e, ok := err.(*pycompat.Error); ok && e.Type == "RenderError" {
		return e
	}
	return Error("%s", err.Error())
}

var mediaBoxRe = regexp.MustCompile(`/MediaBox\s*\[\s*([\d.\-]+)\s+([\d.\-]+)\s+([\d.\-]+)\s+([\d.\-]+)`)

// ReadPageBox is the page width and height in mm and the page count, straight from the PDF.
func ReadPageBox(pdf string) (width, height float64, pages int, err error) {
	data, readErr := os.ReadFile(pdf)
	if readErr != nil {
		return 0, 0, 0, Error("no PDF was produced")
	}
	boxes := mediaBoxRe.FindAllSubmatch(data, -1)
	if len(boxes) == 0 {
		return 0, 0, 0, Error("could not read the page size back from the PDF")
	}
	var v [4]float64
	for i := range v {
		v[i], _ = strconv.ParseFloat(string(boxes[0][i+1]), 64)
	}
	return (v[2] - v[0]) / 72 * 25.4, (v[3] - v[1]) / 72 * 25.4, len(boxes), nil
}
