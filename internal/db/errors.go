package db

import (
	"errors"
	"regexp"
	"strings"

	"github.com/satiyap/banyan/internal/pycompat"
)

// Errors are *pycompat.Error values named after the Python exceptions the
// recordings in tests/golden expect, with the same messages; the HTTP layer
// maps each to a status. ValueError — the request is understood but the
// register refuses it — is the one every module shares.

// ValueError is the shared refusal.
var ValueError = pycompat.ValueError

func named(kind, message string) *pycompat.Error {
	return &pycompat.Error{Type: kind, Message: message}
}

// LookupError is Python LookupError: the row asked for is not in the register.
func LookupError(message string) *pycompat.Error { return named("LookupError", message) }

// KeyError is Python KeyError (a LookupError there too): a required field was not given.
func KeyError(message string) *pycompat.Error { return named("KeyError", message) }

// DatabaseError is Python sqlite3.DatabaseError: anything SQLite itself refused.
func DatabaseError(message string) *pycompat.Error { return named("DatabaseError", message) }

// IntegrityError is Python sqlite3.IntegrityError: a CHECK, NOT NULL, UNIQUE or FOREIGN KEY constraint failed.
func IntegrityError(message string) *pycompat.Error { return named("IntegrityError", message) }

// OperationalError is Python sqlite3.OperationalError: bad SQL (an unknown column, say), a locked or unreadable file.
func OperationalError(message string) *pycompat.Error { return named("OperationalError", message) }

// MigrationError is Python RuntimeError from the register migration: the rebuild left dangling references.
func MigrationError(message string) *pycompat.Error { return named("MigrationError", message) }

// The error each one extends, as the TypeScript classes do.
var parents = map[string]string{
	"KeyError":         "LookupError",
	"IntegrityError":   "DatabaseError",
	"OperationalError": "DatabaseError",
}

// IsA reports whether err is the named error or one that extends it:
// IsA(err, "LookupError") holds for a KeyError too.
func IsA(err error, kind string) bool {
	var e *pycompat.Error
	if !errors.As(err, &e) {
		return false
	}
	for t := e.Type; t != ""; t = parents[t] {
		if t == kind {
			return true
		}
	}
	return false
}

// SQLite primary result codes that Python's sqlite3 raises as OperationalError.
var operationalCodes = map[int]bool{
	1: true, 3: true, 4: true, 5: true, 6: true, 8: true, 9: true,
	10: true, 13: true, 14: true, 15: true, 16: true, 17: true,
}

// The driver writes '<sqlite3_errstr>: <sqlite3_errmsg> (<code>)', or
// '<sqlite3_errstr> (<code>)' when the two agree. The recordings hold
// sqlite3_errmsg alone, which is what Python and node:sqlite report.
var codeSuffix = regexp.MustCompile(`^(?s)(.*) \(\d+\)(?: \(SQLITE_BUSY\))?$`)

func sqliteMessage(text string) string {
	if m := codeSuffix.FindStringSubmatch(text); m != nil {
		text = m[1]
	}
	// No sqlite3_errstr text contains ': ', so the first one ends it.
	if _, msg, found := strings.Cut(text, ": "); found {
		return msg
	}
	return text
}

// translate names a driver error as the recordings do. An error that is
// already named passes through.
func translate(err error) error {
	if err == nil {
		return nil
	}
	var named *pycompat.Error
	if errors.As(err, &named) {
		return named
	}
	// *sqlite.Error, by its method: Code is SQLite's extended result code.
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		message := sqliteMessage(err.Error())
		switch primary := coded.Code() & 0xff; {
		case primary == 19 || primary == 20:
			return IntegrityError(message)
		case operationalCodes[primary]:
			return OperationalError(message)
		}
		return DatabaseError(message)
	}
	return &pycompat.Error{Type: "Error", Message: err.Error()}
}
