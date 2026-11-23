package printing

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Nothing here reaches a real printer. The one test that spools at all sends
// its job to a file through Microsoft Print to PDF, and only when asked to
// (BANYAN_SPOOL_TEST=1, as the Windows CI run does).
func TestMain(m *testing.M) {
	Run = func(cmd string, args []string, _ time.Duration) (Completed, error) {
		panic(fmt.Sprintf("a test reached the real command runner: %s %v", cmd, args))
	}
	dial = func(address string, _ time.Duration) bool {
		panic("a test reached the real network probe: " + address)
	}
	os.Exit(m.Run())
}

const virtualPrinter = "Microsoft Print to PDF"

func TestSplitJob(t *testing.T) {
	for id, want := range map[string]struct {
		printer string
		job     uint32
		ok      bool
	}{
		"HP LaserJet Pro MFP M125-M126 PCLmS-42": {"HP LaserJet Pro MFP M125-M126 PCLmS", 42, true},
		"Office-7":                               {"Office", 7, true},
		"-7":                                     {"", 0, false},
		"Office":                                 {"", 0, false},
		"Office-seven":                           {"", 0, false},
		"":                                       {"", 0, false},
	} {
		printer, job, ok := splitJob(id)
		if printer != want.printer || job != want.job || ok != want.ok {
			t.Errorf("splitJob(%q) = %q, %d, %v", id, printer, job, ok)
		}
	}
}

func TestSubmitDisabled(t *testing.T) {
	SetPrintingDisabled(true)
	defer SetPrintingDisabled(false)
	pdf := filepath.Join(t.TempDir(), "x.pdf")
	os.WriteFile(pdf, []byte("%PDF-1.4\n"), 0o600)
	_, err := Submit(pdf, virtualPrinter, 92, 204, "x")
	if err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Fatalf("got %v", err)
	}
}

// inkBox is the bounding box of everything darker than mid-grey, in mm from
// the page's top-left corner, rasterised at 10 pixels to the millimetre.
type inkBox struct{ left, top, right, bottom float64 }

const pxPerMM = 10

func measure(t *testing.T, path string) (w, h float64, box inkBox, found bool) {
	t.Helper()
	lib, err := loadPdfium()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pdfiumMu.Lock()
	defer pdfiumMu.Unlock()
	doc, err := openPDF(lib, data)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	defer doc.close()
	if n := doc.pages(); n != 1 {
		t.Fatalf("%s has %d pages", path, n)
	}
	w, h, _ = doc.sizeMM(0)
	pw, ph := int(math.Round(w*pxPerMM)), int(math.Round(h*pxPerMM))
	px, err := doc.grey(0, pw, ph)
	if err != nil {
		t.Fatal(err)
	}
	minX, minY, maxX, maxY := pw, ph, -1, -1
	for y := 0; y < ph; y++ {
		for x := 0; x < pw; x++ {
			if px[y*pw+x] < 128 {
				minX, minY = min(minX, x), min(minY, y)
				maxX, maxY = max(maxX, x), max(maxY, y)
			}
		}
	}
	if maxX < 0 {
		return w, h, inkBox{}, false
	}
	return w, h, inkBox{float64(minX) / pxPerMM, float64(minY) / pxPerMM, float64(maxX+1) / pxPerMM, float64(maxY+1) / pxPerMM}, true
}

func compareBoxes(t *testing.T, what string, got, want inkBox, tolerance, shorter float64) {
	t.Helper()
	d := [4]float64{got.left - want.left, got.top - want.top, got.right - want.right, got.bottom - want.bottom}
	t.Logf("%s: ink %.1f..%.1f x %.1f..%.1f mm, off by left %+.2f top %+.2f right %+.2f bottom %+.2f mm",
		what, got.left, got.right, got.top, got.bottom, d[0], d[1], d[2], d[3])
	for _, v := range d[:3] {
		if math.Abs(v) > tolerance {
			t.Errorf("%s lands more than %.1fmm away from the reference", what, tolerance)
			return
		}
	}
	if d[3] > tolerance {
		t.Errorf("%s runs %.2fmm further than the reference", what, d[3])
	} else if d[3] < -shorter {
		t.Errorf("%s ends %.2fmm short of the reference", what, -d[3])
	}
}

// Text runs down the page (along the cheque), so the bottom of the ink is
// where a line of text ends. The print font is "Helvetica Neue", Helvetica,
// Arial: Windows has neither Helvetica and draws Arial, which is narrower, so
// a line may end a little sooner than on the Mac. Shorter is safe (nothing
// reaches further towards the next field or the MICR band); longer is not.
// Where every field starts is held to the full tolerance.
const fontShorterMM = 2.0

// The Windows renderer (Edge) against the Mac's (WebKit), field by field: the
// calibration was measured through the Mac's, so ink must land in the same
// place. BANYAN_MAC_RENDER and BANYAN_WIN_RENDER hold the probe PDFs that
// internal/render's TestKeepProbes writes on each system.
func TestRenderMatchesMac(t *testing.T) {
	macDir, winDir := os.Getenv("BANYAN_MAC_RENDER"), os.Getenv("BANYAN_WIN_RENDER")
	if macDir == "" || winDir == "" {
		t.Skip("BANYAN_MAC_RENDER and BANYAN_WIN_RENDER are not set")
	}
	probes, _ := filepath.Glob(filepath.Join(macDir, "probe-*.pdf"))
	if len(probes) == 0 {
		t.Fatal("no probes from the Mac")
	}
	for _, mac := range probes {
		name := filepath.Base(mac)
		mw, mh, want, ok := measure(t, mac)
		if !ok {
			t.Fatalf("%s from the Mac has no ink", name)
		}
		ww, wh, got, ok := measure(t, filepath.Join(winDir, name))
		if !ok {
			t.Errorf("%s from Windows has no ink", name)
			continue
		}
		// Edge's page is a tenth of a millimetre off 92 x 204 (92.12 x 203.88);
		// the app's page check allows render.PageToleranceMM.
		if math.Abs(mw-ww) > 0.5 || math.Abs(mh-wh) > 0.5 {
			t.Errorf("%s: page %.2f x %.2f on Windows, %.2f x %.2f on the Mac", name, ww, wh, mw, mh)
		}
		compareBoxes(t, name, got, want, 0.5, fontShorterMM)
	}
}

// The whole Windows print path short of paper: a rendered cheque through
// PDFium and the spooler onto Microsoft Print to PDF, whose file is then
// measured against the cheque that went in.
func TestSpoolOntoVirtualPrinter(t *testing.T) {
	if os.Getenv("BANYAN_SPOOL_TEST") != "1" {
		t.Skip("BANYAN_SPOOL_TEST is not set")
	}
	in := filepath.Join(os.Getenv("BANYAN_WIN_RENDER"), "probe-cheque.pdf")
	if _, err := os.Stat(in); err != nil {
		t.Fatalf("no rendered cheque at %s", in)
	}
	found := false
	for _, p := range ListPrinters(false) {
		t.Logf("printer %q state %s default %v", p.Name, p.State, p.IsDefault)
		found = found || p.Name == virtualPrinter
	}
	if !found {
		t.Fatalf("%s is not installed", virtualPrinter)
	}

	out := filepath.Join(t.TempDir(), "spooled.pdf")
	job, err := spool(in, virtualPrinter, 92, 204, "banyan spool test", out)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("job %d; sheet %+v", job, lastSheet)
	id := virtualPrinter + "-" + itoa(job)
	t.Logf("state just after: %s", JobState(id))

	// The spooler writes the file after the job is handed over.
	deadline := time.Now().Add(60 * time.Second)
	var size int64 = -1
	for {
		if info, err := os.Stat(out); err == nil && info.Size() > 0 && info.Size() == size {
			break
		} else if err == nil {
			size = info.Size()
		}
		if time.Now().After(deadline) {
			t.Fatalf("no output from %s (job state %s)", virtualPrinter, JobState(id))
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("state after: %s", JobState(id))

	iw, ih, want, ok := measure(t, in)
	if !ok {
		t.Fatal("the rendered cheque has no ink")
	}
	ow, oh, got, ok := measure(t, out)
	if !ok {
		t.Fatal("the spooled page has no ink")
	}
	t.Logf("page in %.2f x %.2f mm, out %.2f x %.2f mm", iw, ih, ow, oh)
	if math.Abs(ow-92) > 0.5 || math.Abs(oh-204) > 0.5 {
		t.Errorf("the spooled page is %.2f x %.2f mm, not 92 x 204", ow, oh)
	}
	compareBoxes(t, "spooled cheque", got, want, 0.5, 0.5)
}
