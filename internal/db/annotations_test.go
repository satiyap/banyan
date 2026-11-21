package db

// Report heads and notes kept in the register. The registers are invented.

import (
	"path/filepath"
	"testing"
)

func TestAnEarlierDesksRegisterGainsAnnotationsOnlyWhenOneIsSet(t *testing.T) {
	path := copyGolden(t, "register-sample.db")
	d := must(Open(path, Options{}))(t)
	notes := AnnotationsOf(d)
	before := dump(t, path)

	// Reading changes nothing and copies nothing.
	eq(t, len(must(notes.List())(t)), 0)
	eq(t, len(must(notes.Pending())(t)), 0)
	eq(t, must(notes.Has("cheque:1", "notes"))(t), false)
	eq(t, dump(t, path), before)
	eq(t, filesIn(t, filepath.Dir(path)), []string{"register.db"})

	// The first one takes a copy, then adds the table.
	changed := must(notes.Set(AnnotationChange{Subject: "cheque:1", Field: "head", Value: "AMC", By: "flat 12"}))(t)
	eq(t, changed, true)
	eq(t, filesIn(t, filepath.Dir(path)), []string{"register.db", "register.db.pre-annotations"})
	eq(t, dump(t, path+".pre-annotations").schema, before.schema)
}

func TestSettingAHeadOrNoteWritesThroughAndLeavesATrail(t *testing.T) {
	d := must(Open(copyGolden(t, "register-sample.db"), Options{}))(t)
	notes := AnnotationsOf(d)

	must(notes.Set(AnnotationChange{Subject: "entity:1", Field: "head", Value: "AMC", By: "flat 12"}))(t)
	must(notes.Set(AnnotationChange{Subject: "entity:1", Field: "notes", Value: "Quarterly", By: "flat 12"}))(t)
	must(notes.Set(AnnotationChange{Subject: "cheque:1", Field: "notes", Value: "Paid early", By: "the desk", Pending: true}))(t)
	e := must(d.GetEntity(1))(t)
	eq(t, e.Head, "AMC")
	eq(t, e.Notes, "Quarterly")
	c := must(d.GetCheque(1))(t)
	eq(t, c.Memo, "Paid early")
	about := must(d.EventsAbout("entity:1"))(t)
	eq(t, about[len(about)-1].Detail, "notes: — → Quarterly, by flat 12")
	events := must(d.EventsFor(1))(t)
	eq(t, events[len(events)-1].Kind, "annotated")

	// The same value again is no change and no new event.
	n := len(must(d.EventsFor(1))(t))
	eq(t, must(notes.Set(AnnotationChange{Subject: "cheque:1", Field: "notes", Value: "Paid early", By: "x"}))(t), false)
	eq(t, len(must(d.EventsFor(1))(t)), n)

	_, err := notes.Set(AnnotationChange{Subject: "cheque:999", Field: "notes", Value: "x"})
	wantErr(t, err, "LookupError")
	_, err = notes.Set(AnnotationChange{Subject: "book:1", Field: "notes", Value: "x"})
	wantErr(t, err, "ValueError")
}

func TestTheServersSetIsTakenExceptWhatIsWaitingToBeSent(t *testing.T) {
	d := must(Open(copyGolden(t, "register-sample.db"), Options{}))(t)
	notes := AnnotationsOf(d)
	must(notes.Set(AnnotationChange{Subject: "cheque:1", Field: "notes", Value: "mine, unsent", By: "the desk", Pending: true}))(t)
	must(notes.Set(AnnotationChange{Subject: "cheque:2", Field: "notes", Value: "accepted", By: "the desk", Pending: true}))(t)
	must(0, notes.Settle("cheque:2", "notes", "accepted", "2026-11-12T10:00:00.000Z", "treasurer"))(t)

	changed := must(notes.ApplyServer([]AnnotationChange{
		{Subject: "cheque:1", Field: "notes", Value: "theirs", At: "2026-11-12T10:00:01.000Z", By: "flat 12"},
		{Subject: "cheque:1", Field: "head", Value: "Misc", At: "2026-11-12T10:00:02.000Z", By: "flat 12"},
		{Subject: "cheque:999", Field: "notes", Value: "not in this register", At: "2026-11-12T10:00:03.000Z", By: "flat 12"},
	}))(t)
	eq(t, changed, 1)
	eq(t, must(d.GetCheque(1))(t).Memo, "mine, unsent")

	// cheque:2 was accepted, but the server no longer has it: sent again.
	pending := must(notes.Pending())(t)
	subjects := []string{}
	for _, p := range pending {
		subjects = append(subjects, p.Subject+"/"+p.Field)
	}
	eq(t, subjects, []string{"cheque:2/notes", "cheque:1/notes"}) // oldest edit first

	// Settling only holds for the value that was sent.
	must(0, notes.Settle("cheque:1", "notes", "something older", "2026-11-12T10:00:04.000Z", "treasurer"))(t)
	eq(t, len(must(notes.Pending())(t)), 2)
	must(0, notes.Settle("cheque:1", "notes", "mine, unsent", "2026-11-12T10:00:05.000Z", "treasurer"))(t)
	eq(t, len(must(notes.Pending())(t)), 1)
}
