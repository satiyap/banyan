package desk

import (
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/satiyap/banyan/internal/calibration"
	"github.com/satiyap/banyan/internal/printing"
	"github.com/satiyap/banyan/internal/render"
)

func (a *App) health(rq *req) (*response, error) {
	c, err := a.calibration()
	if err != nil {
		return nil, err
	}
	rendererOK, rendererPath := a.renderer.Info()

	printers := printing.ListPrinters(true)
	// The queue the next cheque would go to. A queue existing is not enough:
	// the print system calls a switched-off printer "idle".
	var target *printing.Printer
	for i := range printers {
		if printers[i].IsDefault {
			target = &printers[i]
			break
		}
	}
	if target == nil && len(printers) > 0 {
		target = &printers[0]
	}
	printerOK := target != nil && (target.Online == nil || *target.Online)
	calError := a.calibrationError()
	return ok(obj{
		{"ok", rendererOK && printerOK && calError == ""},
		{"calibration", obj{
			{"version", c.Version},
			{"source", filepath.Base(c.Source)},
			{"warnings", warningsOf(c)},
			{"error", calError},
		}},
		{"renderer", obj{{"ok", rendererOK}, {"path", rendererPath}}},
		{"printing", obj{{"cups", printing.CupsAvailable()}, {"printers", printers}}},
		{"page_mm", obj{{"w", calibration.PageWMM}, {"h", calibration.PageHMM}}},
		{"db", a.cfg.DBPath},
		{"desk_version", a.version},
	})
}

func warningsOf(c *calibration.Calibration) []string {
	return append([]string{}, c.Warnings...)
}

func (a *App) getCalibration(rq *req) (*response, error) {
	c, err := a.calibration()
	if err != nil {
		return nil, err
	}
	return ok(c.AsDict())
}

func (a *App) printers(rq *req) (*response, error) {
	return ok(obj{{"printers", printing.ListPrinters(true)}})
}

// renderFor renders one page into the PDF folder; a failure is the request's answer.
func (a *App) renderFor(payload chequePayload, name string) (render.Result, error) {
	c, err := a.calibration()
	if err != nil {
		return render.Result{}, err
	}
	result, err := a.renderer.RenderPDF(c, payload, filepath.Join(a.cfg.PDFDir, name), false)
	if err != nil {
		if isType(err, "RenderError") {
			return render.Result{}, refuse(http.StatusInternalServerError, message(err))
		}
		return render.Result{}, err
	}
	return result, nil
}

// testPrint renders the diagnostic sheet, and prints it only when asked to.
//
// This is the sheet that gets held against a real leaf; it must never be run
// on the leaf itself.
func (a *App) testPrint(rq *req) (*response, error) {
	req := rq.body(testPrintRequestModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	payload := chequePayload{TestPattern: req.str("pattern") == "test"}
	result, err := a.renderFor(payload, req.str("pattern")+"-sheet.pdf")
	if err != nil {
		return nil, err
	}
	out := obj{{"page", result.AsDict()}, {"printed", false}, {"pdf", "/api/test-sheet.pdf"}}
	if req.bool("confirm") {
		printer, err := resolvePrinter(req.str("printer"))
		if err != nil {
			return nil, err
		}
		jobID, err := printing.Submit(result.PdfPath, printer, calibration.PageWMM, calibration.PageHMM, "cheque test sheet")
		if err != nil {
			return nil, err
		}
		out.set("job_id", jobID)
		out.set("printer", printer)
		out.set("printed", true)
	}
	return ok(out)
}

func (a *App) testSheet(rq *req) (*response, error) {
	path := filepath.Join(a.cfg.PDFDir, "test-sheet.pdf")
	if !isFile(path) {
		return nil, refuse(http.StatusNotFound, "render a test sheet first")
	}
	return a.pdfResponse(path, "cheque-test-sheet.pdf")
}

func (a *App) pdfResponse(path, filename string) (*response, error) {
	f, err := osFile(path)
	if err != nil {
		return nil, err
	}
	return fileResponse(f, fileOptions{mediaType: "application/pdf", filename: filename}), nil
}

// labelled is one cheque for plain paper, and what its event calls it.
type labelled struct {
	label   string
	payload chequePayload
}

// plainPaper puts cheques on plain paper through the real render and the real
// printer call. No leaf number, no register row, no stage: one event each.
func (a *App) plainPaper(payloads []labelled, printer, subject string) ([]obj, error) {
	type done struct {
		label  string
		result render.Result
	}
	var rendered []done
	for i, p := range payloads {
		result, err := a.renderFor(p.payload, fmt.Sprintf("plain-%02d.pdf", i))
		if err != nil {
			return nil, err
		}
		rendered = append(rendered, done{p.label, result})
	}
	out := []obj{}
	for _, r := range rendered {
		jobID, err := printing.Submit(r.result.PdfPath, printer, calibration.PageWMM, calibration.PageHMM, "plain-paper test")
		if err != nil {
			if printing.IsPrintError(err) {
				return nil, refuse(http.StatusBadGateway, "could not send to the printer: "+message(err))
			}
			return nil, err
		}
		if err := a.db.AddEvent("test-print", r.label+" on plain paper, "+jobID, eventAbout(subject)); err != nil {
			return nil, err
		}
		out = append(out, obj{{"label", r.label}, {"job_id", jobID}, {"page", r.result.AsDict()}})
	}
	return out, nil
}

// chequeTestPrint is one cheque on plain paper, before it goes on a leaf.
func (a *App) chequeTestPrint(rq *req) (*response, error) {
	body := rq.body(chequeTestPrintModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	if !body.bool("confirm") {
		return nil, refuse(http.StatusBadRequest, "sending to the printer needs an explicit confirmation")
	}
	req := chequeInputOf(body)
	composed, err := a.compose(req)
	if err != nil {
		return nil, err
	}
	if !composed.OK {
		return nil, refuse(http.StatusBadRequest, composed.Error)
	}
	printer, err := resolvePrinter(body.str("printer"))
	if err != nil {
		if printing.IsPrintError(err) {
			return nil, refuse(http.StatusBadRequest, message(err))
		}
		return nil, err
	}
	payload := payloadOf(req, composed.AmountWords, composed.AmountFigures)
	sent, err := a.plainPaper([]labelled{{req.Payee, payload}}, printer, "test")
	if err != nil {
		return nil, err
	}
	return ok(obj{{"sent", sent}})
}
