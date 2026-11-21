// Package annotate holds the two things anyone with a key may change: the
// report head and the notes, on an entity or on one leaf.
//
// They are kept apart from the register's own rows. The desk writes, prints
// and voids cheques and owns everything about them; an annotation only
// re-labels. It can move an amount between report heads and change the text
// beside it, never the amount, the leaf, its status or what was printed.
//
// The Banyan server holds the authoritative set. Every edit, from the desk or
// from a member, is stamped with the server's clock when it arrives, and the
// last to arrive wins, field by field. The desk keeps a copy in its register
// and writes each value through to the register's own column where there is
// one, so a desk from before annotations still reads the same heads and notes.
package annotate

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/satiyap/banyan/internal/reports"
)

// The fields that may be annotated.
const (
	Head  = "head"
	Notes = "notes"
)

// MaxNotes is the longest note, in characters: the same limit the desk's own
// entity form has.
const MaxNotes = 500

// Annotation is one field's value on one subject, as the server last accepted it.
type Annotation struct {
	Subject   string `json:"subject"` // "entity:<id>" or "cheque:<id>"
	Field     string `json:"field"`   // Head or Notes
	Value     string `json:"value"`
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
}

// Subject names an entity or a cheque.
type Subject struct {
	Kind string // "entity" or "cheque"
	ID   int64
}

func (s Subject) String() string { return s.Kind + ":" + strconv.FormatInt(s.ID, 10) }

// EntitySubject and ChequeSubject name a subject.
func EntitySubject(id int64) string { return Subject{"entity", id}.String() }
func ChequeSubject(id int64) string { return Subject{"cheque", id}.String() }

// ParseSubject reads "entity:<id>" or "cheque:<id>".
func ParseSubject(text string) (Subject, bool) {
	kind, id, found := strings.Cut(text, ":")
	if !found || (kind != "entity" && kind != "cheque") {
		return Subject{}, false
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != id {
		return Subject{}, false
	}
	return Subject{kind, n}, true
}

// Clean checks an edit and returns the value as it is stored.
//
// A head is one of the report heads, or empty: on an entity, empty means it
// has none chosen; on a leaf, that the leaf follows its entity. Notes are
// trimmed and at most MaxNotes characters.
func Clean(subject, field, value string) (string, error) {
	if _, ok := ParseSubject(subject); !ok {
		return "", errors.New("that is not an entity or a cheque")
	}
	switch field {
	case Head:
		value = strings.TrimSpace(value)
		if value != "" && !slices.Contains(reports.HEADS, value) {
			return "", errors.New("a category is one of " + strings.Join(reports.HEADS, ", "))
		}
		return value, nil
	case Notes:
		value = strings.TrimSpace(value)
		if !utf8.ValidString(value) {
			return "", errors.New("the note is not readable text")
		}
		if utf8.RuneCountInString(value) > MaxNotes {
			return "", errors.New("a note is at most " + strconv.Itoa(MaxNotes) + " characters")
		}
		return value, nil
	}
	return "", errors.New("only the category and the note can be changed")
}

// Set is every annotation, looked up by subject and field.
type Set map[[2]string]Annotation

// NewSet indexes a list.
func NewSet(list []Annotation) Set {
	out := make(Set, len(list))
	for _, a := range list {
		out[[2]string{a.Subject, a.Field}] = a
	}
	return out
}

// Get is the annotation on a subject's field, if there is one.
func (s Set) Get(subject, field string) (Annotation, bool) {
	a, ok := s[[2]string{subject, field}]
	return a, ok
}

// Overlay puts annotations over the register's own values for reports: an
// entity's head, and a leaf's head and note. An empty leaf head is no
// override: the leaf follows its entity.
func (s Set) Overlay(rows []reports.ReportChequeRow, entities []reports.ReportEntityRow) {
	if len(s) == 0 {
		return
	}
	for i := range entities {
		if a, ok := s.Get(EntitySubject(entities[i].ID), Head); ok {
			head := a.Value
			entities[i].Head = &head
		}
	}
	for i := range rows {
		subject := ChequeSubject(rows[i].ID)
		if a, ok := s.Get(subject, Head); ok && a.Value != "" {
			head := a.Value
			rows[i].Head = &head
		}
		if a, ok := s.Get(subject, Notes); ok {
			rows[i].Memo = a.Value
		}
	}
}
