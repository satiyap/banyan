package desk

// Report heads and notes that anyone with a key may change, on an entity or
// on one leaf. See internal/annotate for the rules. The desk keeps a copy in
// the register, sends its own edits to the server, and takes the server's
// set back, last edit to reach the server winning.

import (
	"cmp"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/satiyap/banyan/internal/annotate"
	"github.com/satiyap/banyan/internal/db"
	"github.com/satiyap/banyan/internal/reports"
)

// deskAuthor is the name an edit made on the desk is shown under until the
// server stamps it with the desk's own key label.
const deskAuthor = "the desk"

// annotationSet is every annotation reports are drawn with: the server's
// own set when this desk is the server's report engine, otherwise this
// register's copy.
func (a *App) annotationSet() (annotate.Set, error) {
	if a.opts.Annotations != nil {
		list, err := a.opts.Annotations()
		if err != nil {
			return nil, err
		}
		return annotate.NewSet(list), nil
	}
	rows, err := a.notes.List()
	if err != nil {
		return nil, err
	}
	list := make([]annotate.Annotation, len(rows))
	for i, r := range rows {
		list[i] = annotate.Annotation{Subject: r.Subject, Field: r.Field, Value: r.Value, UpdatedAt: r.UpdatedAt, UpdatedBy: r.UpdatedBy}
	}
	return annotate.NewSet(list), nil
}

// HasSubject is true when the register holds the entity or cheque an
// annotation names. The server asks before it accepts an edit.
func (a *App) HasSubject(subject string) (bool, error) {
	s, ok := annotate.ParseSubject(subject)
	if !ok {
		return false, nil
	}
	if s.Kind == "entity" {
		e, err := a.db.GetEntity(s.ID)
		return e != nil, err
	}
	c, err := a.db.GetCheque(s.ID)
	return c != nil, err
}

// -- the leaves report ---------------------------------------------------

type edited struct {
	By string `json:"by"`
	At string `json:"at"`
}

type leafRow struct {
	ID          int64   `json:"id"`
	ChequeNo    string  `json:"cheque_no"`
	ChequeDate  string  `json:"cheque_date"`
	Payee       string  `json:"payee"`
	EntityID    *int64  `json:"entity_id"`
	EntityName  string  `json:"entity_name"`
	AmountPaise int64   `json:"amount_paise"`
	Head        string  `json:"head"`
	OwnHead     string  `json:"own_head"`
	EntityHead  string  `json:"entity_head"`
	Notes       string  `json:"notes"`
	HeadEdited  *edited `json:"head_edited"`
	NotesEdited *edited `json:"notes_edited"`
}

type leafEntity struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	PayeeName   string  `json:"payee_name"`
	Kind        string  `json:"kind"`
	Head        string  `json:"head"`
	HeadSet     string  `json:"head_set"`
	Notes       string  `json:"notes"`
	Count       int     `json:"count"`
	Paise       int64   `json:"paise"`
	HeadEdited  *edited `json:"head_edited"`
	NotesEdited *edited `json:"notes_edited"`
}

func editedOf(set annotate.Set, subject, field string) *edited {
	if x, ok := set.Get(subject, field); ok {
		return &edited{By: x.UpdatedBy, At: x.UpdatedAt}
	}
	return nil
}

// leaves is every leaf issued in a period, with its head and note, and the
// entities they were paid to: the page where heads and notes are changed.
func (a *App) leaves(rq *req) (*response, error) {
	fy := rq.queryInt("fy", optionalInt)
	quarter := rq.queryInt("quarter", optionalInt)
	if err := rq.check(); err != nil {
		return nil, err
	}
	if quarter != nil && (*quarter < 1 || *quarter > 4) {
		return nil, refuse(http.StatusBadRequest, "quarter must be 1, 2, 3 or 4")
	}
	rows, entities, err := a.reportInputs()
	if err != nil {
		return nil, err
	}
	set, err := a.annotationSet()
	if err != nil {
		return nil, err
	}
	year := 0
	if fy != nil {
		year = int(*fy)
	} else if year, err = reports.FyOf(a.today()); err != nil {
		return nil, err
	}
	if year < 2000 || year > 2100 {
		return nil, refuse(http.StatusBadRequest, "that financial year is out of range")
	}
	start, end, err := reports.PeriodBounds(year, intOf(quarter))
	if err != nil {
		return nil, err
	}
	label, err := reports.PeriodLabel(year, intOf(quarter))
	if err != nil {
		return nil, err
	}
	years, err := reports.Years(rows, a.today())
	if err != nil {
		return nil, err
	}

	notes := map[int64]string{}
	all, err := a.db.ListEntities()
	if err != nil {
		return nil, err
	}
	for _, e := range all {
		notes[e.ID] = e.Notes
	}
	byID := map[int64]*reports.ReportEntityRow{}
	for i := range entities {
		byID[entities[i].ID] = &entities[i]
	}

	out := []leafRow{}
	tally := map[int64]*leafEntity{}
	order := []int64{}
	for i := range rows {
		r := &rows[i]
		if r.ChequeDate < start || r.ChequeDate > end {
			continue
		}
		var e *reports.ReportEntityRow
		if r.EntityID != nil {
			e = byID[*r.EntityID]
		}
		subject := annotate.ChequeSubject(r.ID)
		row := leafRow{
			ID: r.ID, ChequeNo: r.ChequeNo, ChequeDate: r.ChequeDate, Payee: r.Payee, EntityID: r.EntityID,
			EntityName: r.Payee, AmountPaise: r.AmountPaise, Head: reports.HeadOfRow(e, r),
			EntityHead: reports.HeadOf(e), Notes: r.Memo,
			HeadEdited: editedOf(set, subject, annotate.Head), NotesEdited: editedOf(set, subject, annotate.Notes),
		}
		if r.Head != nil {
			row.OwnHead = *r.Head
		}
		if e != nil {
			row.EntityName = cmp.Or(e.EntityName, e.PayeeName)
			t := tally[e.ID]
			if t == nil {
				es := annotate.EntitySubject(e.ID)
				t = &leafEntity{
					ID: e.ID, Name: row.EntityName, PayeeName: e.PayeeName, Head: reports.HeadOf(e),
					HeadEdited: editedOf(set, es, annotate.Head), NotesEdited: editedOf(set, es, annotate.Notes),
				}
				if e.Kind != nil {
					t.Kind = *e.Kind
				}
				if e.Head != nil && slices.Contains(reports.HEADS, *e.Head) {
					t.HeadSet = *e.Head
				}
				t.Notes = notes[e.ID]
				if x, ok := set.Get(es, annotate.Notes); ok {
					t.Notes = x.Value
				}
				tally[e.ID] = t
				order = append(order, e.ID)
			}
			t.Count++
			t.Paise += r.AmountPaise
		}
		out = append(out, row)
	}
	slices.SortStableFunc(out, func(x, y leafRow) int {
		return cmp.Or(cmp.Compare(y.ChequeDate, x.ChequeDate), cmp.Compare(y.ChequeNo, x.ChequeNo))
	})
	ents := make([]*leafEntity, 0, len(order))
	for _, id := range order {
		ents = append(ents, tally[id])
	}
	slices.SortStableFunc(ents, func(x, y *leafEntity) int { return cmp.Compare(y.Paise, x.Paise) })

	var q any
	if quarter != nil {
		q = *quarter
	}
	return ok(obj{
		{"fy", year}, {"quarter", q}, {"label", label}, {"years", years},
		{"heads", reports.HEADS}, {"leaves", out}, {"entities", ents},
	})
}

// -- changing a head or a note -------------------------------------------

var annotationModel = model(
	named{"subject", str(strOptions{})},
	named{"field", str(strOptions{})},
	named{"value", str(strOptions{def: def("")})},
)

// annotateRoute is an edit made on this desk: put in place now, and sent to
// the server, where arriving later than any other edit it wins.
func (a *App) annotateRoute(rq *req) (*response, error) {
	req := rq.body(annotationModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	subject, field := req.str("subject"), req.str("field")
	value, err := annotate.Clean(subject, field, req.str("value"))
	if err != nil {
		return nil, refuse(http.StatusUnprocessableEntity, err.Error())
	}
	changed, err := a.notes.Set(db.AnnotationChange{
		Subject: subject, Field: field, Value: value, By: deskAuthor, Pending: true,
	})
	if err != nil {
		if db.IsA(err, "LookupError") {
			return nil, refuse(http.StatusNotFound, message(err))
		}
		return nil, err
	}
	a.kickAnnotations()
	return ok(obj{{"subject", subject}, {"field", field}, {"value", value}, {"changed", changed}})
}

// noteDeskEdit records that the desk's own entity form or register edit
// changed a head or note, so the change goes to the server like any other.
// The register column is already written; this only adds the pending copy.
func (a *App) noteDeskEdit(subject, field, value string) {
	// A desk with no server has nobody to tell, unless an annotation already
	// stands on this field: then it would hide the column just written.
	if status := a.syncer.Status(); !status.Connected || status.ReadOnly {
		if has, err := a.notes.Has(subject, field); err != nil || !has {
			return
		}
	}
	if _, err := a.notes.Set(db.AnnotationChange{
		Subject: subject, Field: field, Value: value, By: deskAuthor, Pending: true,
	}); err != nil {
		log.Printf("desk: could not note an edit to %s for the server: %v", subject, err)
		return
	}
	a.kickAnnotations()
}

// -- keeping level with the server -----------------------------------------

// annotationPoll is how often the desk asks the server for members' edits.
const annotationPoll = 2 * time.Minute

// watchAnnotations runs until the desk closes: a round with the server on
// every kick, and every annotationPoll.
func (a *App) watchAnnotations() {
	tick := time.NewTicker(annotationPoll)
	defer tick.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-a.kick:
		case <-tick.C:
		}
		if err := a.SyncAnnotations(); err != nil {
			log.Printf("desk: heads and notes did not sync: %v", err)
		}
	}
}

// kickAnnotations asks for a round with the server soon. It never waits.
func (a *App) kickAnnotations() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// SyncAnnotations sends this desk's edits and takes the server's set back.
// Only the writing desk does this: a reports-only app has no register, and a
// desk whose register the server does not hold has nothing to agree on.
func (a *App) SyncAnnotations() error {
	a.annMu.Lock()
	defer a.annMu.Unlock()
	if a.syncer.ReadOnly() {
		return nil
	}
	status := a.syncer.Status()
	if !status.Connected || status.Retired || status.Choice != nil {
		return nil
	}
	// The register first, so the server knows any entity or leaf an edit names.
	if err := a.syncer.Flush(); err != nil {
		return err
	}
	if status = a.syncer.Status(); status.Pending || status.Choice != nil || status.Retired {
		return nil
	}

	pending, err := a.notes.Pending()
	if err != nil {
		return err
	}
	for _, p := range pending {
		body, _ := json.Marshal(map[string]string{"subject": p.Subject, "field": p.Field, "value": p.Value})
		var got annotate.Annotation
		status, err := a.sendJSON(http.MethodPost, "/api/v1/annotations", body, &got)
		if err != nil {
			return err
		}
		switch {
		case status == http.StatusOK:
			if err := a.notes.Settle(p.Subject, p.Field, p.Value, got.UpdatedAt, got.UpdatedBy); err != nil {
				return err
			}
		case status == http.StatusNotFound || status == http.StatusUnprocessableEntity:
			// The server's register does not hold it yet, or never will:
			// kept, and tried again on the next round.
		default:
			return &httpError{status: status, detail: "the server refused a head or note"}
		}
	}

	var server struct {
		Annotations []annotate.Annotation `json:"annotations"`
	}
	status2, err := a.sendJSON(http.MethodGet, "/api/v1/annotations", nil, &server)
	if err != nil {
		return err
	}
	if status2 != http.StatusOK {
		return &httpError{status: status2, detail: "the server did not send heads and notes"}
	}
	changes := make([]db.AnnotationChange, len(server.Annotations))
	for i, x := range server.Annotations {
		changes[i] = db.AnnotationChange{Subject: x.Subject, Field: x.Field, Value: x.Value, At: x.UpdatedAt, By: x.UpdatedBy}
	}
	a.writing.Lock()
	changed, err := a.notes.ApplyServer(changes)
	a.writing.Unlock()
	if err != nil {
		return err
	}
	if changed > 0 {
		a.syncer.Schedule()
	}
	return nil
}

// sendJSON makes one call to the server as this desk and reads a JSON answer.
func (a *App) sendJSON(method, path string, body []byte, into any) (int, error) {
	reply, err := a.syncer.Send(method, path, body)
	if err != nil {
		return 0, err
	}
	defer reply.Body.Close()
	data, err := io.ReadAll(io.LimitReader(reply.Body, 32<<20))
	if err != nil {
		return 0, err
	}
	if reply.StatusCode == http.StatusOK && into != nil {
		if err := json.Unmarshal(data, into); err != nil {
			return 0, err
		}
	}
	return reply.StatusCode, nil
}
