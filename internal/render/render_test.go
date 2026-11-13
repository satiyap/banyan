package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/satiyap/banyan/internal/calibration"
	"github.com/satiyap/banyan/internal/reports"
	"github.com/satiyap/banyan/internal/webpdf"
)

// WebKit works on the main thread, so the tests run beside it.
func init() { runtime.LockOSThread() }

func TestMain(m *testing.M) {
	done := make(chan struct{})
	code := 0
	go func() {
		code = m.Run()
		close(done)
	}()
	webpdf.ServeMainThread(done)
	os.Exit(code)
}

const root = "../.."

// Everything here is invented. Nothing is printed: a PDF file is written and measured.
var cheque = map[string]any{
	"payee":          "Greenleaf Gardening Services",
	"cheque_date":    "2026-11-12",
	"amount_words":   "Twelve Thousand Four Hundred Fifty Only",
	"amount_figures": "12,450/-",
	"crossing":       true,
	"strike_order":   false,
	"star_fill":      true,
	"test_pattern":   false,
}

func renderer() *Renderer { return &Renderer{Assets: os.DirFS(root)} }

func TestSubstitute(t *testing.T) {
	got, err := substitute("a $x ${y} $$ z", map[string]string{"x": "1", "y": "2"})
	if err != nil || got != "a 1 2 $ z" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := substitute("$nope", nil); err == nil || err.Error() != "the print template asks for an unknown value: nope" {
		t.Fatalf("got %v", err)
	}
}

func TestBuildHTMLEscapesTheData(t *testing.T) {
	cal, err := calibration.Load(filepath.Join(root, "cheque-calibration.json"))
	if err != nil {
		t.Fatal(err)
	}
	html, err := renderer().BuildHTML(cal, map[string]any{"payee": "</script><b>&"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "</script><b>&") {
		t.Fatal("the payee reached the page unescaped")
	}
	if !strings.Contains(html, "@page { size: 92mm 204mm; margin: 0; }") {
		t.Fatal("the page size is not declared")
	}
}

func TestChequeIsOneExactPage(t *testing.T) {
	if !webpdf.Available {
		t.Skip("no renderer on this system")
	}
	cal, err := calibration.Load(filepath.Join(root, "cheque-calibration.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "cheque.pdf")
	result, err := renderer().RenderPDF(cal, cheque, out, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Pages != 1 || result.AsDict().PageWMM != 92 || result.AsDict().PageHMM != 204 {
		t.Fatalf("got %+v", result.AsDict())
	}
	if keep := os.Getenv("BANYAN_KEEP_RENDER"); keep != "" {
		data, _ := os.ReadFile(out)
		_ = os.WriteFile(filepath.Join(keep, "cheque.pdf"), data, 0o644)
	}
}

func TestAWrongSizedPageIsRefused(t *testing.T) {
	cal, err := calibration.Load(filepath.Join(root, "cheque-calibration.json"))
	if err != nil {
		t.Fatal(err)
	}
	r := renderer()
	r.Engine = func(_, pdf string, _ webpdf.Page) error {
		return os.WriteFile(pdf, []byte("%PDF-1.4\n/MediaBox [0 0 595 842]\n%%EOF\n"), 0o644)
	}
	_, err = r.RenderPDF(cal, cheque, filepath.Join(t.TempDir(), "x.pdf"), false)
	want := "rendered page is 209.9 x 297.0mm but must be 92 x 204mm — not safe to print"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v", err)
	}
}

func TestReportRenders(t *testing.T) {
	if !webpdf.Available {
		t.Skip("no renderer on this system")
	}
	var fixture struct {
		Cases []struct {
			Rows     []reports.ReportChequeRow `json:"rows"`
			Entities []reports.ReportEntityRow `json:"entities"`
			Runs     []struct {
				Today string `json:"today"`
			} `json:"runs"`
		} `json:"cases"`
	}
	data, err := os.ReadFile(filepath.Join(root, "tests/golden/reports.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	// The invented register with the most cheques.
	best := 0
	for i, c := range fixture.Cases {
		if len(c.Rows) > len(fixture.Cases[best].Rows) {
			best = i
		}
	}
	c := fixture.Cases[best]
	report, err := reports.Monthly(c.Rows, c.Entities, nil, nil, c.Runs[0].Today)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "report.pdf")
	if _, err := renderer().RenderReport(report, out, time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	w, h, pages, err := ReadPageBox(out)
	if err != nil || pages < 1 || w < 209.5 || w > 210.5 || h < 296.5 || h > 297.5 {
		t.Fatalf("got %.1f x %.1f, %d pages, %v", w, h, pages, err)
	}
	if keep := os.Getenv("BANYAN_KEEP_RENDER"); keep != "" {
		data, _ := os.ReadFile(out)
		_ = os.WriteFile(filepath.Join(keep, "report.pdf"), data, 0o644)
	}
}
