package desk

import (
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/satiyap/banyan/internal/amount"
	"github.com/satiyap/banyan/internal/calibration"
	"github.com/satiyap/banyan/internal/db"
	"github.com/satiyap/banyan/internal/printing"
	"github.com/satiyap/banyan/internal/pycompat"
	"github.com/satiyap/banyan/internal/render"
)

// chequeDict is a register row as the page is sent it.
type chequeDict struct {
	db.ChequeWithEntity
	AmountPlain string `json:"amount_plain"`
}

func rowToDict(row db.ChequeWithEntity) chequeDict {
	return chequeDict{ChequeWithEntity: row, AmountPlain: rupee(row.AmountPaise)}
}

func rowsToDicts(rows []db.ChequeWithEntity) []chequeDict {
	out := make([]chequeDict, len(rows))
	for i, row := range rows {
		out[i] = rowToDict(row)
	}
	return out
}

// idOrNil is a nullable id as the register is given it.
func idOrNil(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

func eventAbout(subject string) db.EventOptions { return db.EventOptions{Subject: subject} }

func payloadOf(data chequeInput, amountWords, amountFigures string) chequePayload {
	return chequePayload{
		Payee:         data.Payee,
		ChequeDate:    data.ChequeDate,
		AmountWords:   amountWords,
		AmountFigures: amountFigures,
		Crossing:      data.Crossing,
		StrikeOrder:   data.StrikeOrder,
		StarFill:      data.StarFill,
	}
}

func composeFailed(why string) composeResponse {
	return composeResponse{Warnings: []string{}, Error: why, Cheque: obj{}}
}

// compose validates and expands an amount without touching the register or printer.
func (a *App) compose(data chequeInput) (composeResponse, error) {
	parsed, err := amount.ParseAmount(data.Amount)
	if err != nil {
		if isType(err, "AmountError") {
			return composeFailed(message(err)), nil
		}
		return composeResponse{}, err
	}
	c, err := a.calibration()
	if err != nil {
		return composeResponse{}, err
	}
	warnings := warningsOf(c)
	today := a.today()
	if data.ChequeDate < today {
		warnings = append(warnings, "This date is in the past.")
	}
	on, err := pycompat.ParseIsoDate(data.ChequeDate)
	if err != nil {
		return composeResponse{}, err
	}
	now, err := pycompat.ParseIsoDate(today)
	if err != nil {
		return composeResponse{}, err
	}
	if pycompat.DayNumber(on)-pycompat.DayNumber(now) > 90 {
		warnings = append(warnings,
			"This date is more than three months ahead; banks treat "+
				"a cheque as stale after three months from its date.")
	}
	return composeResponse{
		OK:            true,
		AmountPaise:   parsed.PaiseTotal,
		AmountPlain:   parsed.Plain(),
		AmountWords:   parsed.Words(),
		AmountFigures: parsed.Figures(),
		DateDigits:    fmt.Sprintf("%02d%02d%04d", on.Day, on.Month, on.Year),
		Warnings:      warnings,
		Cheque:        payloadOf(data, parsed.Words(), parsed.Figures()),
	}, nil
}

func (a *App) calVersion() (any, error) {
	c, err := a.calibration()
	if err != nil {
		return nil, err
	}
	return c.Version, nil
}

func (a *App) nextChequeNo(rq *req) (*response, error) {
	after := rq.queryStr("after", str(strOptions{def: def("")}))
	if err := rq.check(); err != nil {
		return nil, err
	}
	number, err := a.db.NextChequeNo(after)
	if err != nil {
		return nil, err
	}
	return ok(obj{{"cheque_no", number}})
}

func (a *App) composeRoute(rq *req) (*response, error) {
	data := rq.body(chequeInputModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	composed, err := a.compose(chequeInputOf(data))
	if err != nil {
		return nil, err
	}
	return ok(composed)
}

func (a *App) listCheques(rq *req) (*response, error) {
	status := rq.queryStr("status", str(strOptions{def: def("")}))
	payee := rq.queryStr("payee", str(strOptions{def: def("")}))
	search := rq.queryStr("search", str(strOptions{def: def("")}))
	entityID := rq.queryInt("entity_id", optionalInt)
	limit := rq.queryInt("limit", integer(intOptions{def: def(int64(500)), le: bound(2000)}))
	offset := rq.queryInt("offset", integer(intOptions{def: def(int64(0))}))
	if err := rq.check(); err != nil {
		return nil, err
	}
	rows, err := a.db.ListCheques(db.ListChequesOptions{
		Status: status, Payee: payee, Search: search, EntityID: entityID, Limit: limit, Offset: *offset,
	})
	if err != nil {
		return nil, err
	}
	return ok(obj{{"cheques", rowsToDicts(rows)}})
}

// chequeOr404 is a cheque that must exist for the request to mean anything.
func (a *App) chequeOr404(chequeID int64) (*db.ChequeWithEntity, error) {
	row, err := a.db.GetCheque(chequeID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, refuse(http.StatusNotFound, "no such cheque")
	}
	return row, nil
}

// knownCheque is a cheque the caller has just seen in the register.
func (a *App) knownCheque(chequeID int64) (*db.ChequeWithEntity, error) {
	row, err := a.db.GetCheque(chequeID)
	if err == nil && row == nil {
		err = fmt.Errorf("cheque %d is no longer in the register", chequeID)
	}
	return row, err
}

func (a *App) getCheque(rq *req) (*response, error) {
	chequeID := rq.id("cheque_id")
	if err := rq.check(); err != nil {
		return nil, err
	}
	row, err := a.chequeOr404(chequeID)
	if err != nil {
		return nil, err
	}
	events, err := a.db.EventsFor(chequeID)
	if err != nil {
		return nil, err
	}
	out := struct {
		chequeDict
		Events   []db.EventRow `json:"events"`
		JobState *string       `json:"job_state,omitempty"`
	}{chequeDict: rowToDict(*row), Events: events}
	if row.Status == "queued" && row.JobID != "" {
		state := printing.JobState(row.JobID)
		out.JobState = &state
	}
	return ok(out)
}

// issueCheque writes a cheque: render it, send it to the printer, and record
// it as awaiting confirmation. Recording as 'printed' happens only when the
// treasurer confirms the physical result.
func (a *App) issueCheque(rq *req) (*response, error) {
	body := rq.body(issueRequestModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	req := chequeInputOf(body)
	composed, err := a.compose(req)
	if err != nil {
		return nil, err
	}
	if !composed.OK {
		return nil, refuse(http.StatusBadRequest, composed.Error)
	}
	add := func(more ...db.Field) (int64, error) {
		version, err := a.calVersion()
		if err != nil {
			return 0, err
		}
		cheque := db.NewCheque{
			{Name: "cheque_no", Value: req.ChequeNo},
			{Name: "payee", Value: req.Payee},
			{Name: "entity_id", Value: idOrNil(req.EntityID)},
			{Name: "one_off", Value: req.OneOff},
			{Name: "amount_paise", Value: composed.AmountPaise},
			{Name: "amount_words", Value: composed.AmountWords},
			{Name: "cheque_date", Value: req.ChequeDate},
			{Name: "memo", Value: req.Memo},
		}
		cheque = append(cheque, more...)
		cheque = append(cheque, db.F("cal_version", version))
		id, err := a.db.AddCheque(cheque)
		if err != nil && isType(err, "ValueError") {
			return 0, refuse(http.StatusConflict, message(err))
		}
		return id, err
	}

	if body.str("action") == "log" {
		chequeID, err := add(db.F("status", "logged"))
		if err != nil {
			return nil, err
		}
		return jsonResponse(http.StatusCreated, obj{
			{"id", chequeID},
			{"status", "logged"},
			{"message", "Recorded as handwritten. Nothing was sent to the printer."},
		})
	}

	if !body.bool("confirm") {
		return nil, refuse(http.StatusBadRequest, "printing onto a cheque leaf needs an explicit confirmation")
	}

	printer, err := resolvePrinter(body.str("printer"))
	if err != nil {
		if printing.IsPrintError(err) {
			return nil, refuse(http.StatusBadRequest, message(err))
		}
		return nil, err
	}

	payload := payloadOf(req, composed.AmountWords, composed.AmountFigures)
	// Recorded before rendering, so an ambiguous payee stops here — before
	// anything reaches the printer.
	chequeID, err := add(db.F("status", "queued"), db.F("printer", printer))
	if err != nil {
		return nil, err
	}

	c, err := a.calibration()
	if err != nil {
		return nil, err
	}
	result, err := a.renderer.RenderPDF(c, payload, filepath.Join(a.cfg.PDFDir, "cheque-"+pad(chequeID, 6)+".pdf"), false)
	if err != nil {
		if !isType(err, "RenderError") {
			return nil, err
		}
		// Nothing reached the printer, so the leaf is unused — but the row
		// must not stay 'queued', where it would count as issued and could
		// later be confirmed as printed.
		if err := a.db.UpdateCheque(chequeID, db.ChequeChanges{
			Event: "render-failed", Detail: message(err), Columns: db.Fields{db.F("status", "failed")},
		}); err != nil {
			return nil, err
		}
		return nil, refuse(http.StatusInternalServerError, message(err))
	}
	if err := a.db.UpdateCheque(chequeID, db.ChequeChanges{
		Event:   "rendered",
		Detail:  pageSize(result),
		Columns: db.Fields{db.F("pdf_path", result.PdfPath)},
	}); err != nil {
		return nil, err
	}
	jobID, err := a.submit(chequeID, result.PdfPath, printer, "")
	if err != nil {
		return nil, err
	}
	return jsonResponse(http.StatusCreated, obj{
		{"id", chequeID},
		{"status", "queued"},
		{"job_id", jobID},
		{"printer", printer},
		{"pdf", "/api/cheques/" + itoa(chequeID) + "/pdf"},
		{"page", result.AsDict()},
		{"message", "Sent to the printer. Confirm the leaf once it comes out."},
	})
}

func pageSize(result render.Result) string {
	return fixed(result.PageWMM, 1) + "x" + fixed(result.PageHMM, 1) + "mm"
}

// submit hands one rendered leaf to the print system and records the job, or records why not.
func (a *App) submit(chequeID int64, pdf, printer, chequeNo string) (string, error) {
	jobID, err := printing.Submit(pdf, printer, calibration.PageWMM, calibration.PageHMM, "cheque "+itoa(chequeID))
	if err != nil {
		if !printing.IsPrintError(err) {
			return "", err
		}
		if err := a.db.UpdateCheque(chequeID, db.ChequeChanges{
			Event: "print-failed", Detail: message(err), Columns: db.Fields{db.F("status", "failed")},
		}); err != nil {
			return "", err
		}
		return "", refuse(http.StatusBadGateway, "could not send to the printer: "+message(err))
	}
	changes := db.ChequeChanges{Event: "queued", Detail: jobID, Columns: db.Fields{db.F("job_id", jobID)}}
	if chequeNo != "" {
		changes.Detail = jobID + " on leaf " + chequeNo
		changes.Columns = append(changes.Columns, db.F("cheque_no", chequeNo))
	}
	if err := a.db.UpdateCheque(chequeID, changes); err != nil {
		return "", err
	}
	return jobID, nil
}

// cancelQuietly cancels a misprint's job, which may still be held by a
// sleeping printer; left alone it would print onto whatever leaf is loaded next.
func cancelQuietly(jobID string) {
	if jobID != "" {
		// Best effort — the verdict stands either way.
		_ = printing.Cancel(jobID)
	}
}

func (a *App) confirmCheque(rq *req) (*response, error) {
	chequeID := rq.id("cheque_id")
	req := rq.body(confirmRequestModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	row, err := a.chequeOr404(chequeID)
	if err != nil {
		return nil, err
	}
	if row.BatchID != nil {
		return nil, refuse(http.StatusConflict, "this cheque is part of a batch — confirm it from the batch on the Write page")
	}
	// Only a cheque still awaiting its verdict can be confirmed. Letting a
	// misprint be flipped to 'printed' would quietly turn a wasted leaf back
	// into a payment in the register and in the frequency analysis;
	// correcting a mistake there is a void, which leaves a trail.
	outcome := req.str("outcome")
	moved, err := a.db.Settle([]db.Outcome{{ChequeID: chequeID, Outcome: outcome}}, req.str("detail"))
	if err != nil {
		return nil, err
	}
	if len(moved) == 0 {
		current, err := a.knownCheque(chequeID)
		if err != nil {
			return nil, err
		}
		return nil, refuse(http.StatusConflict, "this cheque is already recorded as "+current.Status)
	}
	if outcome == "failed" {
		cancelQuietly(row.JobID)
	}
	return ok(obj{{"id", chequeID}, {"status", outcome}})
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// editCheque corrects the cheque number or the note on a register entry.
//
// Both are bookkeeping, not the payment itself, so they can be fixed after
// the leaf has gone out. Every change writes an event recording what it was
// before — a register that can be edited without a trail is not a record.
func (a *App) editCheque(rq *req) (*response, error) {
	chequeID := rq.id("cheque_id")
	req := rq.body(chequeEditRequestModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	row, err := a.chequeOr404(chequeID)
	if err != nil {
		return nil, err
	}

	changes := db.Fields{}
	detail := ""
	note := func(text string) {
		if detail != "" {
			detail += "; "
		}
		detail += text
	}
	if no := req.strp("cheque_no"); no != nil && *no != row.ChequeNo {
		changes = append(changes, db.F("cheque_no", *no))
		note("no: " + orDash(row.ChequeNo) + " → " + orDash(*no))
	}
	if memo := req.strp("memo"); memo != nil && *memo != row.Memo {
		changes = append(changes, db.F("memo", *memo))
		note("note: " + orDash(row.Memo) + " → " + orDash(*memo))
	}
	if entityID := req.intp("entity_id"); entityID != nil && (row.EntityID == nil || *entityID != *row.EntityID) {
		target, err := a.db.GetEntity(*entityID)
		if err != nil {
			return nil, err
		}
		if target == nil {
			return nil, refuse(http.StatusNotFound, "no such entity")
		}
		if target.Kind != db.PerCheque && target.PayeeName != row.Payee {
			return nil, refuse(http.StatusConflict,
				"that entity prints as "+pycompat.Repr(target.PayeeName)+", but this leaf "+
					"says "+pycompat.Repr(row.Payee)+" — a cheque can only move between "+
					"entities that print the same name, or to one whose payee "+
					"changes per cheque")
		}
		changes = append(changes, db.F("entity_id", *entityID))
		note("entity: " + orDash(row.EntityName) + " → " + orDash(target.EntityName))
	}

	if len(changes) == 0 {
		return ok(obj{{"id", chequeID}, {"changed", false}})
	}
	if err := a.db.UpdateCheque(chequeID, db.ChequeChanges{Event: "edited", Detail: detail, Columns: changes}); err != nil {
		return nil, err
	}
	return ok(obj{{"id", chequeID}, {"changed", true}, {"detail", detail}})
}

// moveCheques counts several register entries against one entity, all or none.
//
// For tidying the register — say, folding GST cheques that each became their
// own entity into one payee-per-cheque entity. Only which entity a cheque is
// counted against changes; each move writes an event.
func (a *App) moveCheques(rq *req) (*response, error) {
	req := rq.body(moveChequesRequestModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	moved, err := a.db.MoveCheques(int64s(req.list("cheque_ids")), req.int("entity_id"))
	if err != nil {
		switch {
		case db.IsA(err, "LookupError"):
			return nil, refuse(http.StatusNotFound, message(err))
		case isType(err, "ValueError"):
			return nil, refuse(http.StatusConflict, message(err))
		}
		return nil, err
	}
	if moved == nil {
		moved = []int64{}
	}
	return ok(obj{{"moved", moved}})
}

func int64s(list []any) []int64 {
	out := make([]int64, 0, len(list))
	for _, v := range list {
		n, _ := v.(int64)
		out = append(out, n)
	}
	return out
}

func (a *App) voidCheque(rq *req) (*response, error) {
	chequeID := rq.id("cheque_id")
	req := rq.body(voidRequestModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	row, err := a.chequeOr404(chequeID)
	if err != nil {
		return nil, err
	}
	voided, err := a.db.Void(chequeID, req.str("reason"))
	if err != nil {
		return nil, err
	}
	if !voided {
		return nil, refuse(http.StatusConflict, "this cheque is already void")
	}
	if row.Status == "queued" {
		cancelQuietly(row.JobID)
	}
	return ok(obj{{"id", chequeID}, {"status", "void"}})
}

func (a *App) chequePDF(rq *req) (*response, error) {
	chequeID := rq.id("cheque_id")
	if err := rq.check(); err != nil {
		return nil, err
	}
	row, err := a.chequeOr404(chequeID)
	if err != nil {
		return nil, err
	}
	if row.PdfPath == "" || !isFile(row.PdfPath) {
		return nil, refuse(http.StatusNotFound, "no PDF was kept for this cheque")
	}
	return a.pdfResponse(row.PdfPath, "cheque-"+pad(chequeID, 6)+".pdf")
}

// remindAck answers a post-dated cheque's reminder. An event, and only an
// event: the cheque's status and date are untouched.
func (a *App) remindAck(rq *req) (*response, error) {
	chequeID := rq.id("cheque_id")
	req := rq.body(remindAnswerModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	now := a.today()
	all, err := a.reminders(now)
	if err != nil {
		return nil, err
	}
	var stage *float64
	for _, r := range all {
		if int64(r.ChequeID) == chequeID {
			stage = r.Stage
			break
		}
	}
	if stage == nil {
		return nil, refuse(http.StatusConflict, "this cheque has no reminder to answer")
	}
	changes := db.ChequeChanges{Event: "remind-ack", Detail: "stage " + jsNumber(*stage) + "d: funds are arranged"}
	if req.str("answer") != "ack" {
		until, err := pycompat.AddDays(now, int(req.int("days")))
		if err != nil {
			return nil, err
		}
		changes = db.ChequeChanges{Event: "remind-snooze", Detail: "until " + until}
	}
	if err := a.db.UpdateCheque(chequeID, changes); err != nil {
		return nil, err
	}
	all, err = a.reminders(now)
	if err != nil {
		return nil, err
	}
	return ok(obj{{"reminders", all}})
}
