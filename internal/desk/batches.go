package desk

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/satiyap/banyan/internal/db"
	"github.com/satiyap/banyan/internal/printing"
	"github.com/satiyap/banyan/internal/render"
)

// A batch prints its first cheque alone and waits for the treasurer to say it
// came out right before sending the rest, so a bad feed or a calibration slip
// costs one leaf rather than the whole stack. Each step claims the batch's
// stage before it touches the printer: a second click on the same button finds
// the stage already moved on and sends nothing.

var batchFields = []string{
	"payee", "entity_id", "amount", "cheque_date", "memo", "crossing", "strike_order", "star_fill", "one_off",
}

// need is `item[key]`: a stored batch item without a key the code relies on is
// a fault, not a default.
func need(item db.BatchItem, key string) (any, error) {
	value, found := item.Get(key)
	if !found {
		return nil, fmt.Errorf("KeyError: '%s'", key)
	}
	return value, nil
}

// chequeIDOf is the register row a batch item has, or 0 while it is a draft.
func chequeIDOf(item db.BatchItem) int64 {
	value, _ := item.Get("cheque_id")
	switch id := value.(type) {
	case int64:
		return id
	case float64:
		return int64(id)
	}
	return 0
}

// asRequest is a stored value as a request body would carry it.
func asRequest(value any) any {
	switch v := value.(type) {
	case int64:
		return float64(v)
	case db.Fields:
		out := obj{}
		for _, f := range v {
			out = append(out, kv{f.Name, asRequest(f.Value)})
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = asRequest(item)
		}
		return out
	}
	return value
}

func itemInput(item db.BatchItem) (chequeInput, error) {
	given := obj{}
	for _, k := range batchFields {
		if value, found := item.Get(k); found {
			given = append(given, kv{k, asRequest(value)})
		}
	}
	checked, errs := validate(chequeInputModel, given, nil)
	if errs != nil {
		return chequeInput{}, errors.New("a stored batch item is not a valid cheque")
	}
	return chequeInputOf(checked.(vals)), nil
}

func (a *App) composeItem(item db.BatchItem) (chequeInput, composeResponse, error) {
	input, err := itemInput(item)
	if err != nil {
		return chequeInput{}, composeResponse{}, err
	}
	composed, err := a.compose(input)
	return input, composed, err
}

// batchView is the batch as the Write page shows it: every cheque with its
// words, the leaf it is or will be printed on, and what the next step asks for.
func (a *App) batchView(batch *db.BatchRow) (obj, error) {
	items := []obj{}
	var totalPaise int64
	var drafts []string
	number := batch.NextNo
	for i, item := range batch.Items {
		_, composed, err := a.composeItem(item)
		if err != nil {
			return nil, err
		}
		var row *db.ChequeWithEntity
		if id := chequeIDOf(item); id != 0 {
			if row, err = a.db.GetCheque(id); err != nil {
				return nil, err
			}
		}
		var leaf, status, entityName string
		if row != nil {
			leaf, status, entityName = row.ChequeNo, row.Status, row.EntityName
		} else {
			leaf, status = number, "draft"
			number = db.IncrementNo(number)
			entityID, err := need(item, "entity_id")
			if err != nil {
				return nil, err
			}
			payee, err := need(item, "payee")
			if err != nil {
				return nil, err
			}
			if entityName, err = a.entityName(entityID, payee); err != nil {
				return nil, err
			}
		}
		shown := obj{{"index", i}}
		for _, k := range batchFields {
			value, _ := item.Get(k)
			shown = append(shown, kv{k, value})
		}
		chequeID, _ := item.Get("cheque_id")
		warnings := composed.Warnings
		if warnings == nil {
			warnings = []string{}
		}
		shown = append(shown,
			kv{"amount_paise", composed.AmountPaise},
			kv{"amount_words", composed.AmountWords},
			kv{"amount_figures", composed.AmountFigures},
			kv{"warnings", warnings},
			kv{"cheque_no", leaf},
			kv{"status", status},
			kv{"cheque_id", chequeID},
			kv{"entity_name", entityName},
		)
		items = append(items, shown)
		totalPaise += composed.AmountPaise
		if status == "draft" {
			drafts = append(drafts, leaf)
		}
	}
	firstLeaf, lastLeaf := "", ""
	if len(drafts) > 0 {
		firstLeaf, lastLeaf = drafts[0], drafts[len(drafts)-1]
	}
	return obj{
		{"id", batch.ID},
		{"status", batch.Status},
		{"printer", batch.Printer},
		{"items", items},
		{"count", len(items)},
		{"total_paise", totalPaise},
		{"to_print", len(drafts)},
		{"first_leaf", firstLeaf},
		{"last_leaf", lastLeaf},
	}, nil
}

// entityName is what the desk calls the entity a draft cheque is for.
func (a *App) entityName(entityID, payee any) (string, error) {
	if entityID != nil {
		id, _ := entityID.(int64)
		if f, isFloat := entityID.(float64); isFloat {
			id = int64(f)
		}
		e, err := a.db.GetEntity(id)
		if err != nil || e == nil {
			return "", err
		}
		return e.EntityName, nil
	}
	all, err := a.db.ListEntities()
	if err != nil {
		return "", err
	}
	name, matches := "", 0
	for _, e := range all {
		if e.PayeeName == payee {
			name = e.EntityName
			matches++
		}
	}
	if matches != 1 {
		return "", nil
	}
	return name, nil
}

func (a *App) batchOr404(batchID int64) (*db.BatchRow, error) {
	batch, err := a.db.GetBatch(batchID)
	if err != nil {
		return nil, err
	}
	if batch == nil {
		return nil, refuse(http.StatusNotFound, "no such batch")
	}
	return batch, nil
}

// knownBatch is a batch the caller has just seen in the register.
func (a *App) knownBatch(batchID int64) (*db.BatchRow, error) {
	batch, err := a.db.GetBatch(batchID)
	if err == nil && batch == nil {
		err = fmt.Errorf("batch %d is no longer in the register", batchID)
	}
	return batch, err
}

func (a *App) batchViewOf(batchID int64) (obj, error) {
	batch, err := a.knownBatch(batchID)
	if err != nil {
		return nil, err
	}
	return a.batchView(batch)
}

// leftovers are the cheques that still need a leaf: never printed, or
// misprinted. They go back to the Write page as drafts for a new batch.
func (a *App) leftovers(batch *db.BatchRow) ([]obj, error) {
	out := []obj{}
	for _, item := range batch.Items {
		var row *db.ChequeWithEntity
		if id := chequeIDOf(item); id != 0 {
			var err error
			if row, err = a.db.GetCheque(id); err != nil {
				return nil, err
			}
		}
		if row == nil || row.Status == "failed" {
			draft := obj{}
			for _, k := range batchFields {
				if value, found := item.Get(k); found {
					draft = append(draft, kv{k, value})
				}
			}
			out = append(out, draft)
		}
	}
	return out, nil
}

func (a *App) openBatch(rq *req) (*response, error) {
	batch, err := a.db.OpenBatch()
	if err != nil {
		return nil, err
	}
	if batch == nil {
		return ok(obj{{"batch", nil}})
	}
	view, err := a.batchView(batch)
	if err != nil {
		return nil, err
	}
	return ok(obj{{"batch", view}})
}

// createBatch checks every cheque and fixes the leaf numbers. Nothing is printed.
func (a *App) createBatch(rq *req) (*response, error) {
	req := rq.body(batchCreateModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	inputs := make([]chequeInput, 0, len(req.list("items")))
	for _, raw := range req.list("items") {
		inputs = append(inputs, chequeInputOf(raw.(vals)))
	}
	problems := ""
	problem := func(text string) {
		if problems != "" {
			problems += " "
		}
		problems += text
	}
	number := req.str("first_cheque_no")
	for index, item := range inputs {
		i := strconv.Itoa(index + 1)
		composed, err := a.compose(item)
		if err != nil {
			return nil, err
		}
		if !composed.OK {
			problem("Cheque " + i + " (" + item.Payee + "): " + composed.Error)
		}
		if err := a.db.CheckEntity(item.EntityID, item.Payee); err != nil {
			if !isType(err, "ValueError") {
				return nil, err
			}
			problem("Cheque " + i + ": " + message(err))
		}
		used, err := a.db.ChequeNoUsed(number)
		if err != nil {
			return nil, err
		}
		if used {
			problem("Leaf " + number + " is already in the register.")
		}
		number = db.IncrementNo(number)
	}
	if problems != "" {
		return nil, refuse(http.StatusBadRequest, problems)
	}
	printer, err := resolvePrinter(req.str("printer"))
	if err != nil {
		if printing.IsPrintError(err) {
			return nil, refuse(http.StatusBadRequest, message(err))
		}
		return nil, err
	}
	// Stored in the model's own field order, as Pydantic dumped them.
	items := make([]db.BatchItem, 0, len(inputs))
	for _, item := range inputs {
		items = append(items, db.BatchItem{
			db.F("payee", item.Payee),
			db.F("entity_id", idOrNil(item.EntityID)),
			db.F("one_off", item.OneOff),
			db.F("amount", item.Amount),
			db.F("cheque_date", item.ChequeDate),
			db.F("memo", item.Memo),
			db.F("crossing", item.Crossing),
			db.F("strike_order", item.StrikeOrder),
			db.F("star_fill", item.StarFill),
			db.F("cheque_id", nil),
		})
	}
	batchID, err := a.db.CreateBatch(items, req.str("first_cheque_no"), printer)
	if err != nil {
		if isType(err, "ValueError") {
			return nil, refuse(http.StatusConflict, message(err))
		}
		return nil, err
	}
	view, err := a.batchViewOf(batchID)
	if err != nil {
		return nil, err
	}
	return jsonResponse(http.StatusCreated, view)
}

// printItems renders every leaf first, then sends them in feed order.
//
// Rendering all of them before sending any means a render failure leaves the
// tray untouched. Sending stops at the first failure: the leaves behind it
// are pre-numbered, so carrying on would put a cheque on the wrong leaf.
// Each leaf takes its number only once the print system has accepted it.
func (a *App) printItems(batch *db.BatchRow, indexes []int) ([]string, error) {
	type leaf struct {
		index    int
		composed composeResponse
		result   render.Result
	}
	var rendered []leaf
	for _, i := range indexes {
		input, composed, err := a.composeItem(batch.Items[i])
		if err != nil {
			return nil, err
		}
		payload := payloadOf(input, composed.AmountWords, composed.AmountFigures)
		c, err := a.calibration()
		if err != nil {
			return nil, err
		}
		name := "batch-" + pad(batch.ID, 4) + "-" + pad(int64(i), 2) + ".pdf"
		result, err := a.renderer.RenderPDF(c, payload, filepath.Join(a.cfg.PDFDir, name), false)
		if err != nil {
			if !isType(err, "RenderError") {
				return nil, err
			}
			return nil, refuse(http.StatusInternalServerError,
				"cheque "+strconv.Itoa(i+1)+" could not be rendered, so nothing was sent: "+message(err))
		}
		rendered = append(rendered, leaf{i, composed, result})
	}

	sent := []string{}
	number := batch.NextNo
	send := func() error {
		for _, r := range rendered {
			item := &batch.Items[r.index]
			used, err := a.db.ChequeNoUsed(number)
			if err != nil {
				return err
			}
			if used {
				return refuse(http.StatusConflict, "leaf "+number+" is already in the register — nothing further was sent")
			}
			payee, err := need(*item, "payee")
			if err != nil {
				return err
			}
			entityID, err := need(*item, "entity_id")
			if err != nil {
				return err
			}
			oneOff, _ := item.Get("one_off")
			if oneOff == nil {
				oneOff = false
			}
			chequeDate, err := need(*item, "cheque_date")
			if err != nil {
				return err
			}
			memo, err := need(*item, "memo")
			if err != nil {
				return err
			}
			version, err := a.calVersion()
			if err != nil {
				return err
			}
			chequeID, err := a.db.AddCheque(db.NewCheque{
				db.F("cheque_no", ""),
				db.F("payee", payee),
				db.F("entity_id", entityID),
				db.F("one_off", oneOff),
				db.F("amount_paise", r.composed.AmountPaise),
				db.F("amount_words", r.composed.AmountWords),
				db.F("cheque_date", chequeDate),
				db.F("memo", memo),
				db.F("status", "queued"),
				db.F("printer", batch.Printer),
				db.F("cal_version", version),
				db.F("batch_id", batch.ID),
				db.F("pdf_path", r.result.PdfPath),
			})
			if err != nil {
				return err
			}
			if err := a.db.UpdateCheque(chequeID, db.ChequeChanges{
				Event:  "rendered",
				Detail: pageSize(r.result) + ", batch " + itoa(batch.ID) + " cheque " + strconv.Itoa(r.index+1),
			}); err != nil {
				return err
			}
			item.Set("cheque_id", chequeID)
			if _, err := a.submit(chequeID, r.result.PdfPath, batch.Printer, number); err != nil {
				// The print system refused it, so the leaf is still blank: the
				// row stays as a failed attempt with no leaf number, and the
				// cheque goes back to being a draft.
				var refused *httpError
				if errors.As(err, &refused) {
					item.Set("cheque_id", nil)
				}
				return err
			}
			sent = append(sent, number)
			number = db.IncrementNo(number)
		}
		return nil
	}
	err := send()
	// Whatever happened, the batch records which cheques now have a row and
	// which leaf comes next.
	if saveErr := a.db.SaveBatchItems(batch.ID, batch.Items, number); saveErr != nil {
		return nil, saveErr
	}
	if err != nil {
		return nil, err
	}
	return sent, nil
}

func isRefusal(err error) bool {
	var refused *httpError
	return errors.As(err, &refused)
}

const needsLeafConfirmation = "printing onto a cheque leaf needs an explicit confirmation"

func (a *App) batchPrintFirst(rq *req) (*response, error) {
	batchID := rq.id("batch_id")
	req := rq.body(batchStepModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	if !req.bool("confirm") {
		return nil, refuse(http.StatusBadRequest, needsLeafConfirmation)
	}
	batch, err := a.batchOr404(batchID)
	if err != nil {
		return nil, err
	}
	first := -1
	for i, item := range batch.Items {
		if chequeIDOf(item) == 0 {
			first = i
			break
		}
	}
	claimed := false
	if first != -1 {
		if claimed, err = a.db.MoveBatch(batchID, []string{"review"}, "first_sent", nil); err != nil {
			return nil, err
		}
	}
	if !claimed {
		return nil, refuse(http.StatusConflict, "this batch is not waiting to print its first cheque")
	}
	sent, err := a.printItems(batch, []int{first})
	if err != nil {
		if isRefusal(err) {
			if _, backErr := a.db.MoveBatch(batchID, []string{"first_sent"}, "review", nil); backErr != nil {
				return nil, backErr
			}
		}
		return nil, err
	}
	view, err := a.batchViewOf(batchID)
	if err != nil {
		return nil, err
	}
	return ok(obj{{"sent", sent}, {"batch", view}})
}

// queuedIn is the batch's cheques still awaiting their verdict, in item order.
func (a *App) queuedIn(batch *db.BatchRow) ([]int64, error) {
	queued := []int64{}
	for _, item := range batch.Items {
		id := chequeIDOf(item)
		if id == 0 {
			continue
		}
		row, err := a.knownCheque(id)
		if err != nil {
			return nil, err
		}
		if row.Status == "queued" {
			queued = append(queued, id)
		}
	}
	return queued, nil
}

func (a *App) batchFirstOutcome(rq *req) (*response, error) {
	batchID := rq.id("batch_id")
	req := rq.body(batchFirstOutcomeModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	batch, err := a.batchOr404(batchID)
	if err != nil {
		return nil, err
	}
	if batch.Status != "first_sent" {
		return nil, refuse(http.StatusConflict, "this batch is not waiting on its first cheque")
	}
	queued, err := a.queuedIn(batch)
	if err != nil {
		return nil, err
	}
	if len(queued) != 1 {
		return nil, refuse(http.StatusConflict, "could not find the first cheque awaiting its verdict")
	}
	chequeID := queued[0]
	detail := req.str("detail")
	if detail == "" {
		detail = "batch " + itoa(batchID) + ", first leaf"
	}
	outcome := req.str("outcome")
	if _, err := a.db.Settle([]db.Outcome{{ChequeID: chequeID, Outcome: outcome}}, detail); err != nil {
		return nil, err
	}
	if outcome == "printed" {
		if _, err := a.db.MoveBatch(batchID, []string{"first_sent"}, "first_ok", nil); err != nil {
			return nil, err
		}
	} else {
		row, err := a.knownCheque(chequeID)
		if err != nil {
			return nil, err
		}
		cancelQuietly(row.JobID)
		// The spoiled leaf keeps its number in the register; the cheque itself
		// goes back to the front of the batch to be printed on the next leaf.
		for i := range batch.Items {
			if chequeIDOf(batch.Items[i]) == chequeID {
				batch.Items[i].Set("cheque_id", nil)
			}
		}
		changes := db.BatchChanges{db.F("items", batch.Items)}
		if _, err := a.db.MoveBatch(batchID, []string{"first_sent"}, "review", changes); err != nil {
			return nil, err
		}
	}
	view, err := a.batchViewOf(batchID)
	if err != nil {
		return nil, err
	}
	return ok(obj{{"batch", view}})
}

func (a *App) batchPrintRest(rq *req) (*response, error) {
	batchID := rq.id("batch_id")
	req := rq.body(batchStepModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	if !req.bool("confirm") {
		return nil, refuse(http.StatusBadRequest, "printing onto cheque leaves needs an explicit confirmation")
	}
	batch, err := a.batchOr404(batchID)
	if err != nil {
		return nil, err
	}
	var rest []int
	for i, item := range batch.Items {
		if chequeIDOf(item) == 0 {
			rest = append(rest, i)
		}
	}
	if len(rest) == 0 {
		return nil, refuse(http.StatusConflict, "every cheque in this batch has already been printed")
	}
	claimed, err := a.db.MoveBatch(batchID, []string{"first_ok"}, "rest_sent", nil)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, refuse(http.StatusConflict, "the first cheque must be confirmed as printed before the rest are sent")
	}
	sent, err := a.printItems(batch, rest)
	if err != nil {
		if isRefusal(err) {
			now, nowErr := a.knownBatch(batchID)
			if nowErr != nil {
				return nil, nowErr
			}
			anySent := false
			for _, i := range rest {
				anySent = anySent || chequeIDOf(now.Items[i]) != 0
			}
			if !anySent { // nothing went out
				if _, backErr := a.db.MoveBatch(batchID, []string{"rest_sent"}, "first_ok", nil); backErr != nil {
					return nil, backErr
				}
			}
		}
		return nil, err
	}
	view, err := a.batchViewOf(batchID)
	if err != nil {
		return nil, err
	}
	return ok(obj{{"sent", sent}, {"batch", view}})
}

// batchRestOutcome is one click for the whole stack: everything printed, except any listed.
func (a *App) batchRestOutcome(rq *req) (*response, error) {
	batchID := rq.id("batch_id")
	req := rq.body(batchRestOutcomeModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	batch, err := a.batchOr404(batchID)
	if err != nil {
		return nil, err
	}
	if batch.Status != "rest_sent" {
		return nil, refuse(http.StatusConflict, "this batch is not waiting on its remaining cheques")
	}
	queued, err := a.queuedIn(batch)
	if err != nil {
		return nil, err
	}
	awaiting := map[int64]bool{}
	for _, id := range queued {
		awaiting[id] = true
	}
	misprinted := int64s(req.list("misprinted"))
	spoiled := map[int64]bool{}
	for _, id := range misprinted {
		if !awaiting[id] {
			return nil, refuse(http.StatusBadRequest,
				"some of the cheques marked as misprinted are not awaiting a verdict in this batch")
		}
		spoiled[id] = true
	}
	// Outcomes are applied in ascending order of id, deliberately: the
	// recorded sessions depend on it.
	ids := make([]int64, 0, len(awaiting))
	for id := range awaiting {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	outcomes := make([]db.Outcome, 0, len(ids))
	for _, id := range ids {
		outcome := "printed"
		if spoiled[id] {
			outcome = "failed"
		}
		outcomes = append(outcomes, db.Outcome{ChequeID: id, Outcome: outcome})
	}
	detail := req.str("detail")
	if detail == "" {
		detail = "batch " + itoa(batchID)
	}
	if _, err := a.db.Settle(outcomes, detail); err != nil {
		return nil, err
	}
	for _, id := range misprinted {
		row, err := a.knownCheque(id)
		if err != nil {
			return nil, err
		}
		cancelQuietly(row.JobID)
	}
	if _, err := a.db.MoveBatch(batchID, []string{"rest_sent"}, "done", nil); err != nil {
		return nil, err
	}
	if batch, err = a.knownBatch(batchID); err != nil {
		return nil, err
	}
	view, err := a.batchView(batch)
	if err != nil {
		return nil, err
	}
	left, err := a.leftovers(batch)
	if err != nil {
		return nil, err
	}
	return ok(obj{{"batch", view}, {"leftovers", left}})
}

// batchCancel stops a batch that has nothing at the printer. Printed cheques
// stay in the register; the rest come back as drafts.
func (a *App) batchCancel(rq *req) (*response, error) {
	batchID := rq.id("batch_id")
	if err := rq.check(); err != nil {
		return nil, err
	}
	if _, err := a.batchOr404(batchID); err != nil {
		return nil, err
	}
	moved, err := a.db.MoveBatch(batchID, []string{"review", "first_ok"}, "cancelled", nil)
	if err != nil {
		return nil, err
	}
	if !moved {
		return nil, refuse(http.StatusConflict, "a leaf from this batch is waiting for its verdict — confirm it before cancelling")
	}
	view, err := a.batchViewOf(batchID)
	if err != nil {
		return nil, err
	}
	batch, err := a.knownBatch(batchID)
	if err != nil {
		return nil, err
	}
	left, err := a.leftovers(batch)
	if err != nil {
		return nil, err
	}
	return ok(obj{{"batch", view}, {"leftovers", left}})
}

// batchTestPrint is the run's first cheque (or every one) on plain paper, before any leaf.
func (a *App) batchTestPrint(rq *req) (*response, error) {
	batchID := rq.id("batch_id")
	req := rq.body(batchTestPrintModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	if !req.bool("confirm") {
		return nil, refuse(http.StatusBadRequest, "sending to the printer needs an explicit confirmation")
	}
	batch, err := a.batchOr404(batchID)
	if err != nil {
		return nil, err
	}
	if batch.Status != "review" {
		return nil, refuse(http.StatusConflict, "the plain-paper test comes before the first leaf")
	}
	var payloads []labelled
	for i, item := range batch.Items {
		if chequeIDOf(item) != 0 {
			continue
		}
		if req.str("which") == "first" && len(payloads) == 1 {
			break
		}
		input, composed, err := a.composeItem(item)
		if err != nil {
			return nil, err
		}
		payee, err := need(item, "payee")
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, labelled{
			label:   fmt.Sprintf("cheque %d (%s)", i+1, textOf(payee)),
			payload: payloadOf(input, composed.AmountWords, composed.AmountFigures),
		})
	}
	sent, err := a.plainPaper(payloads, batch.Printer, "batch:"+itoa(batchID))
	if err != nil {
		return nil, err
	}
	return ok(obj{{"sent", sent}})
}
