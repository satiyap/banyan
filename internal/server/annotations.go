package server

import (
	"database/sql"
	"errors"

	"github.com/satiyap/banyan/internal/annotate"
)

// Annotations is every head and note the server holds, for the desk to take
// and for the members' reports.
func (s *Store) Annotations() ([]annotate.Annotation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT subject, field, value, updated_at, updated_by FROM annotations ORDER BY subject, field")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []annotate.Annotation{}
	for rows.Next() {
		var a annotate.Annotation
		if err := rows.Scan(&a.Subject, &a.Field, &a.Value, &a.UpdatedAt, &a.UpdatedBy); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Annotate records an edit, stamped now: whatever arrived before it on the
// same field is replaced, and the change is logged.
func (s *Store) Annotate(subject, field, value string, session *Session) (annotate.Annotation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := s.stamp()
	by := authorOf(session)
	var old sql.NullString
	err := s.db.QueryRow("SELECT value FROM annotations WHERE subject = ? AND field = ?", subject, field).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return annotate.Annotation{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return annotate.Annotation{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		"INSERT INTO annotations (subject, field, value, updated_at, updated_by, token_id) VALUES (?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT (subject, field) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, "+
			"updated_by = excluded.updated_by, token_id = excluded.token_id",
		subject, field, value, at, by, session.ID); err != nil {
		return annotate.Annotation{}, err
	}
	if !old.Valid || old.String != value {
		if _, err := tx.Exec("INSERT INTO annotation_log (subject, field, old_value, new_value, at, by, token_id) VALUES (?, ?, ?, ?, ?, ?, ?)",
			subject, field, old, value, at, by, session.ID); err != nil {
			return annotate.Annotation{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return annotate.Annotation{}, err
	}
	return annotate.Annotation{Subject: subject, Field: field, Value: value, UpdatedAt: at, UpdatedBy: by}, nil
}

// LogEntry is one change to a head or note.
type LogEntry struct {
	Subject  string  `json:"subject"`
	Field    string  `json:"field"`
	OldValue *string `json:"old_value"`
	NewValue string  `json:"new_value"`
	At       string  `json:"at"`
	By       string  `json:"by"`
}

// AnnotationLog is every change to heads and notes, newest first.
func (s *Store) AnnotationLog(limit int) ([]LogEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT subject, field, old_value, new_value, at, by FROM annotation_log ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogEntry{}
	for rows.Next() {
		var e LogEntry
		var old sql.NullString
		if err := rows.Scan(&e.Subject, &e.Field, &old, &e.NewValue, &e.At, &e.By); err != nil {
			return nil, err
		}
		if old.Valid {
			e.OldValue = &old.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// authorOf is the name an edit is shown under: the label the key was issued
// with, or what kind of key it was.
func authorOf(session *Session) string {
	if session.Label != "" {
		return session.Label
	}
	if session.Role == "write" {
		return "the desk"
	}
	return "a member"
}
