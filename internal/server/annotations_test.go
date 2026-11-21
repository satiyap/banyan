package server

// Heads and notes: the two things anyone with a key may change, on an entity
// or on one leaf. Every register here is invented.

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	banyan "github.com/satiyap/banyan"
	"github.com/satiyap/banyan/internal/db"
	"github.com/satiyap/banyan/internal/desk"
)

// annotated is a writing desk connected to the server, holding one entity
// with one printed leaf, and a member signed in on the web.
type annotated struct {
	w        *world
	d        *testDesk
	app      *desk.App
	entity   int64
	cheque   int64
	member   map[string]string
	treasure string
}

func newAnnotated(t *testing.T) *annotated {
	w := newWorld(t)
	d := w.desk()
	entity := addEntity(t, d.db, "Lift Care Services")
	cheque, err := d.db.AddCheque(db.NewCheque{
		db.F("cheque_no", "000101"), db.F("payee", "Lift Care Services"), db.F("amount_paise", int64(1250000)),
		db.F("amount_words", "Rupees Invented Amount Only"), db.F("cheque_date", "2026-10-05"),
		db.F("status", "printed"), db.F("entity_id", entity),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.sync.Connect(origin, w.issue("write", "treasurer")); err != nil {
		t.Fatal(err)
	}
	if w.latest() != 1 {
		t.Fatalf("the register did not reach the server")
	}
	dir := filepath.Dir(d.paths.DbPath)
	app, err := desk.New(desk.Config{
		DBPath: d.paths.DbPath, CalPath: calPath(t), PDFDir: filepath.Join(dir, "pdf"), Assets: banyan.Assets,
		DataStaticDir: filepath.Join(dir, "static"), ConnectionPath: d.paths.ConnectionPath, PrintDisabled: true,
	}, desk.Options{Sync: desk.ServerSync(d.sync), Today: func() string { return "2026-11-12" }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)

	session := w.signIn(w.issue("read", "flat 12"))
	home := string(w.get("/", map[string]string{"cookie": session.cookie}).body)
	version := regexp.MustCompile(`name="desk-version" content="([^"]+)"`).FindStringSubmatch(home)[1]
	return &annotated{w: w, d: d, app: app, entity: entity, cheque: cheque,
		member: map[string]string{"cookie": session.cookie, "x-desk-version": version}}
}

func (x *annotated) memberSets(subject, field, value string) reply {
	return x.w.postJSON("/api/annotations", map[string]string{"subject": subject, "field": field, "value": value}, x.member)
}

// deskCall asks the desk app itself, as its own window would.
func (x *annotated) deskCall(method, path string, body any) map[string]any {
	x.w.t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Desk-Version", x.app.Version())
	rec := httptest.NewRecorder()
	x.app.ServeHTTP(rec, req)
	if rec.Code != 200 {
		x.w.t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func leafOf(t *testing.T, leaves map[string]any) map[string]any {
	t.Helper()
	list := leaves["leaves"].([]any)
	if len(list) != 1 {
		t.Fatalf("leaves: %v", list)
	}
	return list[0].(map[string]any)
}

func headTotals(t *testing.T, report map[string]any) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, c := range report["categories"].([]any) {
		m := c.(map[string]any)
		out[m["category"].(string)] = m["paise"].(float64)
	}
	return out
}

func TestAMembersHeadAndNoteReachTheReportsAndTheDesk(t *testing.T) {
	x := newAnnotated(t)
	leaf, entity := "cheque:"+itoa(int(x.cheque)), "entity:"+itoa(int(x.entity))

	want(t, "entity head", x.memberSets(entity, "head", "Utilities").status, 200)
	want(t, "leaf head", x.memberSets(leaf, "head", "AMC").status, 200)
	saved := x.memberSets(leaf, "notes", "  October service visit  ").json(t)
	want(t, "note trimmed", saved["value"], "October service visit")
	want(t, "note by", saved["updated_by"], "flat 12")

	// Members see it at once, before the desk has heard of it.
	report := x.w.get("/api/reports?fy=2026", x.member).json(t)
	want(t, "AMC total", headTotals(t, report)["AMC"], float64(1250000))
	got := leafOf(t, x.w.get("/api/reports/leaves?fy=2026", x.member).json(t))
	want(t, "leaf head", got["head"], "AMC")
	want(t, "own head", got["own_head"], "AMC")
	want(t, "entity head", got["entity_head"], "Utilities")
	want(t, "note", got["notes"], "October service visit")
	want(t, "edited by", got["notes_edited"].(map[string]any)["by"], "flat 12")

	// The desk takes them into its register: the entity's columns, the
	// leaf's memo, and an event for each.
	if err := x.app.SyncAnnotations(); err != nil {
		t.Fatal(err)
	}
	e, _ := x.d.db.GetEntity(x.entity)
	want(t, "entity column", e.Head, "Utilities")
	c, _ := x.d.db.GetCheque(x.cheque)
	want(t, "memo column", c.Memo, "October service visit")
	events, _ := x.d.db.EventsFor(x.cheque)
	trail := ""
	for _, ev := range events {
		if ev.Kind == "annotated" {
			trail += ev.Detail + "\n"
		}
	}
	if !strings.Contains(trail, "head: — → AMC, by flat 12") || !strings.Contains(trail, "notes: — → October service visit, by flat 12") {
		t.Errorf("the leaf's trail: %q", trail)
	}
	want(t, "desk's own report", headTotals(t, x.deskCall("GET", "/api/reports?fy=2026", nil))["AMC"], float64(1250000))
	// The amount, the leaf and its status are untouched.
	want(t, "amount", c.AmountPaise, int64(1250000))
	want(t, "status", c.Status, "printed")
	want(t, "leaf no", c.ChequeNo, "000101")
}

func TestTheLastEditToReachTheServerWins(t *testing.T) {
	x := newAnnotated(t)
	leaf := "cheque:" + itoa(int(x.cheque))
	if err := x.app.SyncAnnotations(); err != nil {
		t.Fatal(err)
	}

	// The desk edits offline from the server's point of view; a member edits
	// after it; the desk's edit then arrives last, and wins.
	x.deskCall("POST", "/api/annotations", map[string]string{"subject": leaf, "field": "notes", "value": "from the desk"})
	want(t, "member", x.memberSets(leaf, "notes", "from a member").status, 200)
	if err := x.app.SyncAnnotations(); err != nil {
		t.Fatal(err)
	}
	want(t, "server after the desk's arrives", leafOf(t, x.w.get("/api/reports/leaves?fy=2026", x.member).json(t))["notes"], "from the desk")
	c, _ := x.d.db.GetCheque(x.cheque)
	want(t, "desk", c.Memo, "from the desk")
	want(t, "desk's edit is stamped as the desk", leafOf(t, x.w.get("/api/reports/leaves?fy=2026", x.member).json(t))["notes_edited"].(map[string]any)["by"], "treasurer")

	// A member's later edit replaces it everywhere.
	want(t, "member again", x.memberSets(leaf, "notes", "member, later").status, 200)
	if err := x.app.SyncAnnotations(); err != nil {
		t.Fatal(err)
	}
	c, _ = x.d.db.GetCheque(x.cheque)
	want(t, "desk after", c.Memo, "member, later")
	pending, _ := db.AnnotationsOf(x.d.db).Pending()
	want(t, "nothing left to send", len(pending), 0)

	// Every change is in the server's log, with who made it.
	log, err := x.w.server.Store.AnnotationLog(10)
	if err != nil {
		t.Fatal(err)
	}
	want(t, "log length", len(log), 3)
	want(t, "newest", log[0].By+": "+log[0].NewValue, "flat 12: member, later")
	want(t, "replaced", *log[0].OldValue, "from the desk")
}

func TestTheDesksOwnEntityFormGoesToTheServerToo(t *testing.T) {
	x := newAnnotated(t)
	e, _ := x.d.db.GetEntity(x.entity)
	x.deskCall("PUT", "/api/entities/"+itoa(int(x.entity)), map[string]any{
		"payee_name": e.PayeeName, "entity_name": e.EntityName, "kind": e.Kind, "head": "AMC", "notes": "Annual contract",
	})
	if err := x.app.SyncAnnotations(); err != nil {
		t.Fatal(err)
	}
	report := x.w.get("/api/reports?fy=2026", x.member).json(t)
	want(t, "server's report", headTotals(t, report)["AMC"], float64(1250000))
	ents := x.w.get("/api/reports/leaves?fy=2026", x.member).json(t)["entities"].([]any)
	want(t, "server's note", ents[0].(map[string]any)["notes"], "Annual contract")
}

func TestOnlyAHeadOrANoteCanBeChanged(t *testing.T) {
	x := newAnnotated(t)
	leaf := "cheque:" + itoa(int(x.cheque))
	want(t, "unknown head", x.memberSets(leaf, "head", "Food").status, 422)
	want(t, "another field", x.memberSets(leaf, "amount_paise", "1").status, 422)
	want(t, "another field", x.memberSets(leaf, "status", "void").status, 422)
	want(t, "not a subject", x.memberSets("batch:1", "notes", "x").status, 422)
	want(t, "no such leaf", x.memberSets("cheque:999", "notes", "x").status, 404)
	want(t, "too long", x.memberSets(leaf, "notes", strings.Repeat("x", 501)).status, 422)
	want(t, "a form post", x.w.call("POST", "/api/annotations",
		map[string]string{"cookie": x.member["cookie"], "content-type": "application/x-www-form-urlencoded"},
		[]byte("subject="+leaf+"&field=notes&value=x")).status, 415)
	want(t, "not signed in", x.w.postJSON("/api/annotations", map[string]string{"subject": leaf, "field": "notes", "value": "x"}, nil).status, 401)

	// A read key may send an edit, as a reports-only app does, but never
	// takes the whole set: that is the writing desk's.
	reader := bearerOf(x.w.deskToken("read"))
	want(t, "reader sends", x.w.postJSON("/api/v1/annotations", map[string]string{"subject": leaf, "field": "head", "value": "Misc"}, reader).status, 200)
	want(t, "reader takes the set", x.w.get("/api/v1/annotations", reader).status, 403)
	// The register on the server is never written by any of this.
	want(t, "server's register", x.w.latest(), int64(1))
}

func TestAnEmptyServerTakingItsFirstRegisterStartsAfresh(t *testing.T) {
	w := newWorld(t)
	if _, err := w.server.Store.Annotate("cheque:1", "notes", "left over", &Session{ID: 1, Role: "read"}); err != nil {
		t.Fatal(err)
	}
	token := w.deskToken("write")
	want(t, "first register", w.put(token, register(t, "Invented Payee"), "0").status, 200)
	left, _ := w.server.Store.Annotations()
	want(t, "annotations", len(left), 0)
}
