package annotate

import (
	"strings"
	"testing"

	"github.com/satiyap/banyan/internal/reports"
)

func TestSubjects(t *testing.T) {
	for text, ok := range map[string]bool{
		"entity:3": true, "cheque:12": true, "cheque:0": false, "cheque:-1": false, "cheque:01": false,
		"book:1": false, "cheque:": false, "cheque": false, "": false, "cheque:1:2": false,
	} {
		if _, got := ParseSubject(text); got != ok {
			t.Errorf("%q: %v", text, got)
		}
	}
}

func TestClean(t *testing.T) {
	cases := []struct {
		field, value, want string
		ok                 bool
	}{
		{Head, "AMC", "AMC", true},
		{Head, " Utilities ", "Utilities", true},
		{Head, "", "", true},
		{Head, "Food", "", false},
		{Head, "amc", "", false},
		{Notes, "  paid early ", "paid early", true},
		{Notes, strings.Repeat("é", MaxNotes), strings.Repeat("é", MaxNotes), true},
		{Notes, strings.Repeat("x", MaxNotes+1), "", false},
		{"amount_paise", "1", "", false},
		{"status", "void", "", false},
	}
	for _, c := range cases {
		got, err := Clean("cheque:1", c.field, c.value)
		if (err == nil) != c.ok || (c.ok && got != c.want) {
			t.Errorf("%s=%q: %q %v", c.field, c.value, got, err)
		}
	}
	if _, err := Clean("batch:1", Notes, "x"); err == nil {
		t.Error("a batch was annotated")
	}
}

func TestOverlay(t *testing.T) {
	entityID := int64(1)
	misc, kind := "Misc", "regular"
	rows := []reports.ReportChequeRow{{ID: 10, Memo: "old", EntityID: &entityID}, {ID: 11, Memo: "kept", EntityID: &entityID}}
	ents := []reports.ReportEntityRow{{ID: 1, Head: &misc, Kind: &kind}}
	NewSet([]Annotation{
		{Subject: "entity:1", Field: Head, Value: "Utilities"},
		{Subject: "cheque:10", Field: Head, Value: "AMC"},
		{Subject: "cheque:10", Field: Notes, Value: "new"},
		{Subject: "cheque:11", Field: Head, Value: ""}, // follows the entity
	}).Overlay(rows, ents)
	if reports.HeadOf(&ents[0]) != "Utilities" {
		t.Errorf("entity head %v", *ents[0].Head)
	}
	if reports.HeadOfRow(&ents[0], &rows[0]) != "AMC" || rows[0].Memo != "new" {
		t.Errorf("leaf 10: %v %q", reports.HeadOfRow(&ents[0], &rows[0]), rows[0].Memo)
	}
	if reports.HeadOfRow(&ents[0], &rows[1]) != "Utilities" || rows[1].Memo != "kept" {
		t.Errorf("leaf 11: %v %q", reports.HeadOfRow(&ents[0], &rows[1]), rows[1].Memo)
	}
}
