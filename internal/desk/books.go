package desk

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/satiyap/banyan/internal/dating"
	"github.com/satiyap/banyan/internal/db"
	"github.com/satiyap/banyan/internal/leaves"
	"github.com/satiyap/banyan/internal/pycompat"
	"github.com/satiyap/banyan/internal/reports"
)

// Everything here is read-mostly and additive: it groups what the cheque
// routes already record, and the few writes it makes are events, cheque books
// and blank leaves. None of it renders, numbers or settles a cheque.

func intOf(n *int64) *int {
	if n == nil {
		return nil
	}
	v := int(*n)
	return &v
}

func leafBooks(books []db.BookRow) []leaves.BookRow {
	out := make([]leaves.BookRow, len(books))
	for i, b := range books {
		until := b.AlertUntil
		out[i] = leaves.BookRow{
			ID: int(b.ID), Name: b.Name, FirstNo: b.FirstNo, LastNo: b.LastNo, ReceivedOn: b.ReceivedOn,
			Position: int(b.Position), AlertState: b.AlertState, AlertUntil: &until,
		}
	}
	return out
}

func leafCheques(cheques []db.ChequeWithEntity) []leaves.ChequeRow {
	out := make([]leaves.ChequeRow, len(cheques))
	for i, c := range cheques {
		name := c.EntityName
		out[i] = leaves.ChequeRow{
			ID: int(c.ID), ChequeNo: c.ChequeNo, Payee: c.Payee, EntityID: intOf(c.EntityID), EntityName: &name,
			AmountPaise: c.AmountPaise, ChequeDate: c.ChequeDate, Memo: c.Memo, Status: c.Status,
			BatchID: intOf(c.BatchID), CreatedAt: c.CreatedAt, PdfPath: c.PdfPath, CalVersion: int(c.CalVersion),
		}
	}
	return out
}

func leafBlanks(blanks []db.BlankLeafRow) []leaves.BlankLeafRow {
	out := make([]leaves.BlankLeafRow, len(blanks))
	for i, b := range blanks {
		out[i] = leaves.BlankLeafRow{ID: int(b.ID), ChequeNo: b.ChequeNo, Reason: b.Reason, CreatedAt: b.CreatedAt}
	}
	return out
}

// books is every cheque book with how far it has got.
func (a *App) books(now string) (leaves.BooksViewResult, error) {
	shelf, err := a.db.ListBooks()
	if err != nil {
		return leaves.BooksViewResult{}, err
	}
	cheques, err := a.listEverything()
	if err != nil {
		return leaves.BooksViewResult{}, err
	}
	blanks, err := a.db.ListBlankLeaves()
	if err != nil {
		return leaves.BooksViewResult{}, err
	}
	return leaves.BooksView(leafBooks(shelf), leafCheques(cheques), leafBlanks(blanks), now)
}

// reminders is every post-dated cheque whose date has not passed.
func (a *App) reminders(now string) ([]dating.Reminder, error) {
	rows, err := a.db.ListEntities()
	if err != nil {
		return nil, err
	}
	entities := map[int]dating.ReminderEntityRow{}
	for _, e := range rows {
		name, days := e.EntityName, e.RemindDays
		entities[int(e.ID)] = dating.ReminderEntityRow{EntityName: &name, RemindDays: &days}
	}
	answered, err := a.db.ChequeEvents([]string{"remind-ack", "remind-snooze"})
	if err != nil {
		return nil, err
	}
	events := make([]dating.ReminderEventRow, len(answered))
	for i, e := range answered {
		events[i] = dating.ReminderEventRow{ChequeID: intOf(e.ChequeID), Kind: e.Kind, Detail: e.Detail}
	}
	issued, err := a.db.IssuedCheques()
	if err != nil {
		return nil, err
	}
	cheques := make([]dating.ReminderChequeRow, len(issued))
	for i, c := range issued {
		cheques[i] = dating.ReminderChequeRow{
			ID: int(c.ID), ChequeNo: c.ChequeNo, Payee: c.Payee, EntityID: intOf(c.EntityID),
			AmountPaise: float64(c.AmountPaise), ChequeDate: c.ChequeDate, Memo: c.Memo, Status: c.Status,
			CreatedAt: c.CreatedAt,
		}
	}
	out, err := dating.Reminders(cheques, entities, events, now)
	if out == nil {
		out = []dating.Reminder{}
	}
	return out, err
}

// desk is one call for the Desk: who is due, what is at the printer, which
// post-dated cheques are coming up, and how the cheque book is doing.
func (a *App) desk(rq *req) (*response, error) {
	now := a.today()
	issued, err := a.db.IssuedCheques()
	if err != nil {
		return nil, err
	}
	rhythms, summary, err := a.rhythms(issued, now)
	if err != nil {
		return nil, err
	}
	queued, err := a.db.ListCheques(db.ListChequesOptions{Status: "queued"})
	if err != nil {
		return nil, err
	}
	batch, err := a.db.OpenBatch()
	if err != nil {
		return nil, err
	}
	since, err := pycompat.AddDays(now, -100)
	if err != nil {
		return nil, err
	}
	entities, err := a.listEntities()
	if err != nil {
		return nil, err
	}
	awaiting := []chequeDict{}
	for _, c := range queued {
		if c.BatchID == nil {
			awaiting = append(awaiting, rowToDict(c))
		}
	}
	var batchView any
	if batch != nil {
		if batchView, err = a.batchView(batch); err != nil {
			return nil, err
		}
	}
	type recentCheque struct {
		ID          int64  `json:"id"`
		EntityID    *int64 `json:"entity_id"`
		Payee       string `json:"payee"`
		ChequeNo    string `json:"cheque_no"`
		ChequeDate  string `json:"cheque_date"`
		AmountPaise int64  `json:"amount_paise"`
		Status      string `json:"status"`
	}
	recent := []recentCheque{}
	for _, c := range issued {
		if pycompat.CmpStr(c.ChequeDate, since) >= 0 {
			recent = append(recent, recentCheque{c.ID, c.EntityID, c.Payee, c.ChequeNo, c.ChequeDate, c.AmountPaise, c.Status})
		}
	}
	reminders, err := a.reminders(now)
	if err != nil {
		return nil, err
	}
	books, err := a.books(now)
	if err != nil {
		return nil, err
	}
	return ok(obj{
		{"today", now},
		{"entities", entities},
		{"rhythms", rhythms},
		{"summary", summary},
		{"awaiting", awaiting},
		{"batch", batchView},
		{"recent", recent},
		{"reminders", reminders},
		{"books", books},
	})
}

// register is every leaf, newest first, with gaps and duplicate numbers
// worked out here so the page never has to guess.
func (a *App) register(rq *req) (*response, error) {
	now := a.today()
	cheques, err := a.listEverything()
	if err != nil {
		return nil, err
	}
	blanks, err := a.db.ListBlankLeaves()
	if err != nil {
		return nil, err
	}
	shelf, err := a.db.ListBooks()
	if err != nil {
		return nil, err
	}
	view, err := leaves.RegisterView(leafCheques(cheques), leafBlanks(blanks), leafBooks(shelf), now)
	if err != nil {
		return nil, err
	}
	books, err := a.books(now)
	if err != nil {
		return nil, err
	}
	rows := view.Rows
	if rows == nil {
		rows = []leaves.RegisterRow{}
	}
	return ok(obj{{"rows", rows}, {"books", books}, {"today", now}})
}

// leaf says whether a leaf number is already used, and by what: asked before
// a single cheque is sent, so a leaf number is never written twice.
func (a *App) leaf(rq *req) (*response, error) {
	chequeNo, _ := rq.path("cheque_no", str(strOptions{})).(string)
	if err := rq.check(); err != nil {
		return nil, err
	}
	number := tidy(chequeNo)
	all, err := a.listEverything()
	if err != nil {
		return nil, err
	}
	rows := []chequeDict{}
	for _, r := range all {
		if r.ChequeNo == number {
			rows = append(rows, rowToDict(r))
		}
	}
	blanks, err := a.db.ListBlankLeaves()
	if err != nil {
		return nil, err
	}
	blank := []db.BlankLeafRow{}
	for _, b := range blanks {
		if b.ChequeNo == number {
			blank = append(blank, b)
		}
	}
	events, err := a.eventsAbout("leaf:" + number)
	if err != nil {
		return nil, err
	}
	return ok(obj{
		{"cheque_no", number},
		{"used", len(rows) > 0 || len(blank) > 0},
		{"cheques", rows},
		{"blank", blank},
		{"events", events},
	})
}

func (a *App) eventsAbout(subject string) ([]db.EventRow, error) {
	events, err := a.db.EventsAbout(subject)
	if events == nil {
		events = []db.EventRow{}
	}
	return events, err
}

// events are those about a batch, a book or a blank leaf rather than one cheque.
func (a *App) events(rq *req) (*response, error) {
	subject := rq.queryStr("subject", str(strOptions{min: 1, max: 60}))
	if err := rq.check(); err != nil {
		return nil, err
	}
	events, err := a.eventsAbout(subject)
	if err != nil {
		return nil, err
	}
	return ok(obj{{"events", events}})
}

func sortedUnique(items []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}

// accountFor records leaves that were used but never reached the register:
// written by hand, or spoiled blank. Every number is checked before anything
// is written, and each one gets its event.
func (a *App) accountFor(rq *req) (*response, error) {
	req := rq.body(accountForModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	items := make([]vals, 0, len(req.list("items")))
	for _, raw := range req.list("items") {
		items = append(items, raw.(vals))
	}
	used, err := a.db.UsedNumbers()
	if err != nil {
		return nil, err
	}
	times := map[string]int{}
	for _, it := range items {
		times[it.str("cheque_no")]++
	}
	var clash []string
	for _, it := range items {
		if n := it.str("cheque_no"); used[n] || times[n] > 1 {
			clash = append(clash, n)
		}
	}
	if len(clash) > 0 {
		return nil, refuse(http.StatusConflict, "already in the register: "+strings.Join(sortedUnique(clash), ", "))
	}
	type handwritten struct {
		item     vals
		composed composeResponse
	}
	var logged []handwritten
	for _, it := range items {
		if it.str("as") != "logged" {
			continue
		}
		number := it.str("cheque_no")
		if it.str("payee") == "" || it.str("amount") == "" || it.str("cheque_date") == "" {
			return nil, refuse(http.StatusBadRequest, "leaf "+number+": a handwritten cheque needs its payee, amount and date")
		}
		composed, err := a.compose(chequeInput{
			Payee: it.str("payee"), Amount: it.str("amount"), ChequeDate: it.str("cheque_date"), Crossing: true,
		})
		if err != nil {
			return nil, err
		}
		if !composed.OK {
			return nil, refuse(http.StatusBadRequest, "leaf "+number+": "+composed.Error)
		}
		if err := a.db.CheckEntity(it.intp("entity_id"), it.str("payee")); err != nil {
			if isType(err, "ValueError") {
				return nil, refuse(http.StatusConflict, "leaf "+number+": "+message(err))
			}
			return nil, err
		}
		logged = append(logged, handwritten{it, composed})
	}
	voided := []string{}
	var reasons []string
	for _, it := range items {
		if it.str("as") == "void" {
			voided = append(voided, it.str("cheque_no"))
			reason := it.str("reason")
			if reason == "" {
				reason = "used up blank"
			}
			reasons = append(reasons, reason)
		}
	}
	if len(voided) > 0 {
		if _, err := a.db.AddBlankLeaves(voided, strings.Join(sortedUnique(reasons), "; ")); err != nil {
			if isType(err, "ValueError") {
				return nil, refuse(http.StatusConflict, message(err))
			}
			return nil, err
		}
	}
	ids := []int64{}
	for _, l := range logged {
		version, err := a.calVersion()
		if err != nil {
			return nil, err
		}
		memo := l.item.str("memo")
		if memo == "" {
			memo = l.item.str("reason")
		}
		id, err := a.db.AddCheque(db.NewCheque{
			db.F("cheque_no", l.item.str("cheque_no")),
			db.F("payee", l.item.str("payee")),
			db.F("entity_id", idOrNil(l.item.intp("entity_id"))),
			db.F("amount_paise", l.composed.AmountPaise),
			db.F("amount_words", l.composed.AmountWords),
			db.F("cheque_date", l.item.str("cheque_date")),
			db.F("memo", memo),
			db.F("status", "logged"),
			db.F("cal_version", version),
		})
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ok(obj{{"logged", ids}, {"voided", voided}})
}

func (a *App) getBooks(rq *req) (*response, error) {
	books, err := a.books(a.today())
	if err != nil {
		return nil, err
	}
	return ok(books)
}

// addBook shelves a cheque book. Refused if any of its numbers is in another
// book, or already in the register unless it is said to be partly used.
func (a *App) addBook(rq *req) (*response, error) {
	req := rq.body(bookInputModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	name, firstNo, lastNo := req.str("name"), req.str("first_no"), req.str("last_no")
	shelf, err := a.db.ListBooks()
	if err != nil {
		return nil, err
	}
	for _, b := range shelf {
		if reports.Casefold(b.Name) == reports.Casefold(name) {
			return nil, refuse(http.StatusConflict, "there is already a book called "+pycompat.Repr(name))
		}
	}
	usedSet, err := a.db.UsedNumbers()
	if err != nil {
		return nil, err
	}
	used := make([]string, 0, len(usedSet))
	for number := range usedSet {
		used = append(used, number)
	}
	sort.Strings(used)
	info, err := leaves.CheckNewBook(firstNo, lastNo, leafBooks(shelf), used, req.bool("already_used"))
	if err != nil {
		if isType(err, "ValueError") {
			return nil, refuse(http.StatusConflict, message(err))
		}
		return nil, err
	}
	detail := firstNo + "–" + lastNo + ", " + jsNumber(info.Count) + " leaves"
	if info.InRegister != 0 {
		detail += ", " + strconv.Itoa(info.InRegister) + " already used"
	}
	bookID, err := a.db.AddBook(db.NewBook{
		Name:       name,
		FirstNo:    firstNo,
		LastNo:     lastNo,
		ReceivedOn: req.str("received_on"),
		Front:      req.str("start") == "now",
		Detail:     detail,
	})
	if err != nil {
		return nil, err
	}
	books, err := a.books(a.today())
	if err != nil {
		return nil, err
	}
	return jsonResponse(http.StatusCreated, obj{
		{"id", bookID},
		{"prefix", info.Prefix},
		{"first", info.First},
		{"last", info.Last},
		{"width", info.Width},
		{"count", info.Count},
		{"in_register", info.InRegister},
		{"alert_count", info.AlertCount},
		{"alert_leaf", info.AlertLeaf},
		{"books", books},
	})
}

func (a *App) updateBook(rq *req) (*response, error) {
	bookID := rq.id("book_id")
	req := rq.body(bookUpdateModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	book, err := a.db.GetBook(bookID)
	if err != nil {
		return nil, err
	}
	if book == nil {
		return nil, refuse(http.StatusNotFound, "no such book")
	}
	if name := req.str("name"); name != "" && name != book.Name {
		if err := a.db.UpdateBook(bookID, db.BookChanges{
			Kind: "book-renamed", Detail: book.Name + " → " + name, Columns: db.Fields{db.F("name", name)},
		}); err != nil {
			return nil, err
		}
	}
	if req.bool("use_now") {
		if err := a.db.UpdateBook(bookID, db.BookChanges{
			Kind: "book-use-now", Detail: "moved to the front of the shelf", Front: true,
		}); err != nil {
			return nil, err
		}
	}
	return a.getBooks(rq)
}

func (a *App) bookAlert(rq *req) (*response, error) {
	bookID := rq.id("book_id")
	req := rq.body(bookAlertAnswerModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	book, err := a.db.GetBook(bookID)
	if err != nil {
		return nil, err
	}
	if book == nil {
		return nil, refuse(http.StatusNotFound, "no such book")
	}
	changes := db.BookChanges{
		Kind:    "book-alert",
		Detail:  "a new book has been ordered",
		Columns: db.Fields{db.F("alert_state", "ordered"), db.F("alert_until", "")},
	}
	if req.str("answer") != "ordered" {
		until, err := pycompat.AddDays(a.today(), int(req.int("days")))
		if err != nil {
			return nil, err
		}
		changes = db.BookChanges{
			Kind:    "book-alert",
			Detail:  "remind again on " + until,
			Columns: db.Fields{db.F("alert_state", "snoozed"), db.F("alert_until", until)},
		}
	}
	if err := a.db.UpdateBook(bookID, changes); err != nil {
		return nil, err
	}
	return a.getBooks(rq)
}

func (a *App) getReminders(rq *req) (*response, error) {
	reminders, err := a.reminders(a.today())
	if err != nil {
		return nil, err
	}
	return ok(obj{{"reminders", reminders}, {"today", a.today()}})
}
