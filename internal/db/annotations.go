package db

import (
	"strconv"
	"strings"
)

// AnnotationRow is a report head or note on an entity or a leaf, as this
// register holds it. See the annotations table.
type AnnotationRow struct {
	Subject   string `json:"subject"`
	Field     string `json:"field"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
	Pending   int64  `json:"pending"`
}

// AnnotationChange is one value to put in place.
type AnnotationChange struct {
	Subject, Field, Value string
	At, By                string
	// Pending marks an edit made on this desk, for the server to accept.
	Pending bool
}

// annotated is where an annotation's subject lives in the register.
type annotated struct {
	kind string // "entity" or "cheque"
	id   int64
}

func parseAnnotated(subject string) (annotated, bool) {
	kind, id, ok := strings.Cut(subject, ":")
	n, err := strconv.ParseInt(id, 10, 64)
	if !ok || err != nil || n <= 0 || (kind != "entity" && kind != "cheque") {
		return annotated{}, false
	}
	return annotated{kind, n}, true
}

// column is the register column a field is written through to, if any.
func (a annotated) column(field string) (table, column string) {
	switch {
	case a.kind == "entity" && (field == "head" || field == "notes"):
		return "entities", field
	case a.kind == "cheque" && field == "notes":
		return "cheques", "memo"
	}
	return "", ""
}

// hasAnnotations is true once the register has its annotations table.
func hasAnnotations(con *conn) (bool, error) {
	row, err := con.get("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'annotations'")
	return row != nil, err
}

// ensure creates the annotations table the first time it is
// needed, taking a copy of the register beside it first: this file is the
// only record of which leaves were used.
func (n *Annotations) ensure() error {
	has, err := connect(n.d, hasAnnotations)
	if err != nil || has {
		return err
	}
	backup := n.d.backupPath("pre-annotations")
	if !exists(backup) {
		if err := n.d.do(func(con *conn) error { return n.d.backupTo(con, backup) }); err != nil {
			return err
		}
	}
	return n.d.do(func(con *conn) error { return con.exec(annotationsTable) })
}

// Annotations is the register's report heads and notes. It is a type of its
// own, not more methods on Database: those are the register's original
// operations, each held in place by the recordings in tests/golden.
type Annotations struct{ d *Database }

// AnnotationsOf is the annotations kept in a register.
func AnnotationsOf(d *Database) *Annotations { return &Annotations{d} }

// List is every annotation in the register.
func (n *Annotations) List() ([]AnnotationRow, error) {
	return n.rows("SELECT * FROM annotations ORDER BY subject, field")
}

func (n *Annotations) rows(query string) ([]AnnotationRow, error) {
	return connect(n.d, func(con *conn) ([]AnnotationRow, error) {
		if has, err := hasAnnotations(con); err != nil || !has {
			return []AnnotationRow{}, err
		}
		return list[AnnotationRow](con, query)
	})
}

// current is a field's value as the register stands: the annotation if there
// is one, else the column it writes through to. exists is false when the
// subject is not in the register.
func current(con *conn, a annotated, subject, field string) (value string, exists bool, err error) {
	table := map[string]string{"entity": "entities", "cheque": "cheques"}[a.kind]
	row, err := con.get("SELECT 1 FROM "+table+" WHERE id = ?", a.id)
	if err != nil || row == nil {
		return "", false, err
	}
	row, err = con.get("SELECT value FROM annotations WHERE subject = ? AND field = ?", subject, field)
	if err != nil {
		return "", true, err
	}
	if row != nil {
		v, _ := row[0].(string)
		return v, true, nil
	}
	if table, column := a.column(field); table != "" {
		row, err = con.get("SELECT "+column+" FROM "+table+" WHERE id = ?", a.id)
		if err != nil || row == nil {
			return "", true, err
		}
		v, _ := row[0].(string)
		return v, true, nil
	}
	return "", true, nil
}

// Set puts a head or note in place: the annotation, the register column
// it writes through to, and an event when the value changed. It refuses a
// subject the register does not hold. changed is false when the value was
// already this one.
func (n *Annotations) Set(change AnnotationChange) (changed bool, err error) {
	a, ok := parseAnnotated(change.Subject)
	if !ok {
		return false, ValueError("not an entity or a cheque: " + change.Subject)
	}
	if err := n.ensure(); err != nil {
		return false, err
	}
	return connect(n.d, func(con *conn) (bool, error) {
		before, exists, err := current(con, a, change.Subject, change.Field)
		if err != nil {
			return false, err
		}
		if !exists {
			return false, LookupError("no such " + a.kind)
		}
		return n.put(con, a, change, before)
	})
}

func (n *Annotations) put(con *conn, a annotated, change AnnotationChange, before string) (bool, error) {
	at := change.At
	if at == "" {
		at = n.d.utcnow()
	}
	pending := int64(0)
	if change.Pending {
		pending = 1
	}
	if _, err := con.run(
		"INSERT INTO annotations (subject, field, value, updated_at, updated_by, pending) VALUES (?,?,?,?,?,?) "+
			"ON CONFLICT (subject, field) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, "+
			"updated_by = excluded.updated_by, pending = excluded.pending",
		change.Subject, change.Field, change.Value, at, change.By, pending,
	); err != nil {
		return false, err
	}
	if before == change.Value {
		return false, nil
	}
	if table, column := a.column(change.Field); table != "" {
		if _, err := con.run("UPDATE "+table+" SET "+column+" = ? WHERE id = ?", change.Value, a.id); err != nil {
			return false, err
		}
	}
	detail := change.Field + ": " + dash(before) + " → " + dash(change.Value)
	if change.By != "" {
		detail += ", by " + change.By
	}
	var chequeID any
	subject := change.Subject
	if a.kind == "cheque" {
		chequeID, subject = a.id, ""
	}
	_, err := con.run("INSERT INTO events (cheque_id, kind, detail, at, subject) VALUES (?,?,?,?,?)",
		chequeID, "annotated", detail, n.d.utcnow(), subject)
	return err == nil, err
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ApplyServer brings this register's annotations level with the server's
// full set. An edit made here and not yet accepted is left alone: it will be
// sent, and arriving later, it wins. An annotation this register holds as
// accepted but the server no longer has — a server that was cleared — is
// marked pending, so it is sent again. Subjects this register does not hold
// are skipped. It returns how many values changed.
func (n *Annotations) ApplyServer(server []AnnotationChange) (int, error) {
	if len(server) == 0 {
		if rows, err := n.List(); err != nil || len(rows) == 0 {
			return 0, err
		}
	}
	if err := n.ensure(); err != nil {
		return 0, err
	}
	return connect(n.d, func(con *conn) (int, error) {
		local, err := list[AnnotationRow](con, "SELECT * FROM annotations")
		if err != nil {
			return 0, err
		}
		mine := make(map[[2]string]AnnotationRow, len(local))
		for _, row := range local {
			mine[[2]string{row.Subject, row.Field}] = row
		}
		seen := map[[2]string]bool{}
		changed := 0
		for _, change := range server {
			key := [2]string{change.Subject, change.Field}
			seen[key] = true
			if row, ok := mine[key]; ok && (row.Pending == 1 || (row.Value == change.Value && row.UpdatedAt == change.At)) {
				continue
			}
			a, ok := parseAnnotated(change.Subject)
			if !ok {
				continue
			}
			before, exists, err := current(con, a, change.Subject, change.Field)
			if err != nil {
				return 0, err
			}
			if !exists {
				continue
			}
			change.Pending = false
			did, err := n.put(con, a, change, before)
			if err != nil {
				return 0, err
			}
			if did {
				changed++
			}
		}
		for key, row := range mine {
			if !seen[key] && row.Pending == 0 {
				if _, err := con.run("UPDATE annotations SET pending = 1 WHERE subject = ? AND field = ?", key[0], key[1]); err != nil {
					return 0, err
				}
			}
		}
		return changed, nil
	})
}

// Has is true when a subject's field has an annotation.
func (n *Annotations) Has(subject, field string) (bool, error) {
	return connect(n.d, func(con *conn) (bool, error) {
		if has, err := hasAnnotations(con); err != nil || !has {
			return false, err
		}
		row, err := con.get("SELECT 1 FROM annotations WHERE subject = ? AND field = ?", subject, field)
		return row != nil, err
	})
}

// Pending is every edit made here that the server has not accepted.
func (n *Annotations) Pending() ([]AnnotationRow, error) {
	return n.rows("SELECT * FROM annotations WHERE pending = 1 ORDER BY updated_at, subject, field")
}

// Settle records that the server accepted an edit, with the time
// and name it stamped. Only if the value is still the one that was sent: an
// edit made since stays pending.
func (n *Annotations) Settle(subject, field, value, at, by string) error {
	return n.d.do(func(con *conn) error {
		if has, err := hasAnnotations(con); err != nil || !has {
			return err
		}
		_, err := con.run(
			"UPDATE annotations SET pending = 0, updated_at = ?, updated_by = ? "+
				"WHERE subject = ? AND field = ? AND value = ? AND pending = 1",
			at, by, subject, field, value)
		return err
	})
}
