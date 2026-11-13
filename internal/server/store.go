// Package server is the Banyan server: the one place outside the treasurer's
// Mac that holds the register. It takes the register from the writing desk,
// hands it back to a desk that needs to restore, and shows members the
// reports, read-only, in a browser.
//
// It reads and writes the same files as the TypeScript server it replaces
// (server.db, registers/, current.db), so a data folder moves between the two
// unchanged and the tokens already handed out keep working.
package server

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// What the store keeps: login keys, the tokens they were redeemed for, and
// every copy of the register a desk has sent.
//
// A key is single-use. Redeeming it yields a long-lived token and spends the
// key. There are two privileges: `write` (the treasurer's desk) and `read`
// (everyone else, who see reports). Only one desk may write at a time:
// redeeming a write key for a desk retires the desk that held it before.
//
// The register arrives whole. Each accepted copy is a numbered revision, and a
// desk must say which revision it started from, so a desk that has fallen
// behind can never overwrite a newer register.

// Session is who a token belongs to.
type Session struct {
	ID    int64
	Role  string // "write" or "read"
	Kind  string // "desk" or "web"
	Label string
}

// Revision is one accepted copy of the register.
type Revision struct {
	Revision int64  `json:"revision"`
	SHA256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
	SyncedAt string `json:"synced_at"`
}

// StoreError is a refusal with the HTTP status that reports it.
type StoreError struct {
	Message string
	Status  int
	// Latest is set when a desk sent a register built on an old revision.
	Latest *Revision
	stale  bool
}

func (e *StoreError) Error() string { return e.Message }

func refuse(status int, message string) *StoreError {
	return &StoreError{Message: message, Status: status}
}

// staleRegister: the desk sent a register built on a revision that is no
// longer the latest.
func staleRegister(latest *Revision) *StoreError {
	return &StoreError{
		Message: "The server holds a newer register than the one this desk started from. Nothing was replaced.",
		Status:  409,
		Latest:  latest,
		stale:   true,
	}
}

const schema = `
CREATE TABLE IF NOT EXISTS keys (
  id INTEGER PRIMARY KEY,
  hash TEXT NOT NULL UNIQUE,
  role TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  redeemed_at TEXT
);
CREATE TABLE IF NOT EXISTS tokens (
  id INTEGER PRIMARY KEY,
  hash TEXT NOT NULL UNIQUE,
  role TEXT NOT NULL,
  kind TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  last_seen_at TEXT,
  revoked_at TEXT
);
CREATE TABLE IF NOT EXISTS revisions (
  revision INTEGER PRIMARY KEY AUTOINCREMENT,
  sha256 TEXT NOT NULL,
  bytes INTEGER NOT NULL,
  synced_at TEXT NOT NULL,
  token_id INTEGER
);
`

const (
	keyDays       = 7
	keepRevisions = 200
	// MaxRegisterBytes is the largest file the server will take as a register.
	MaxRegisterBytes = 64 * 1024 * 1024
	// No 0/O or 1/I/L: a key is read off a screen and typed.
	keyAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
)

var sqliteMagic = []byte("SQLite format 3\x00")

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

var notKeyChar = regexp.MustCompile(`[^A-Z0-9]`)

// normalKey is a key as it is compared: case and the dashes between groups do
// not matter.
func normalKey(key string) string {
	return notKeyChar.ReplaceAllString(strings.ToUpper(key), "")
}

func randomBytes(n int) []byte {
	out := make([]byte, n)
	if _, err := rand.Read(out); err != nil {
		panic(err)
	}
	return out
}

func newKey() string {
	raw := randomBytes(25)
	var out strings.Builder
	for i, b := range raw {
		out.WriteByte(keyAlphabet[int(b)%len(keyAlphabet)])
		if i%5 == 4 && i < 24 {
			out.WriteByte('-')
		}
	}
	return out.String()
}

// isoTime is a time as the server has always written it: UTC, to the
// millisecond. Stored times are compared as text, so the form must not change.
func isoTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// Store is the server's own database and its folder of registers.
type Store struct {
	// One statement at a time, as the server it replaces ran them.
	mu        sync.Mutex
	db        *sql.DB
	dir       string
	registers string
	// CurrentPath is the latest register, kept under a fixed name for the
	// report pages to read.
	CurrentPath string
	now         func() time.Time
}

func sqliteURI(path string, query string) string {
	u := url.URL{Scheme: "file", Path: path, RawQuery: query}
	return u.String()
}

// OpenStore opens, or creates, the store in dir.
func OpenStore(dir string, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	registers := filepath.Join(dir, "registers")
	if err := os.MkdirAll(registers, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteURI(filepath.Join(dir, "server.db"), "_pragma=busy_timeout(5000)"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{
		db:          db,
		dir:         dir,
		registers:   registers,
		CurrentPath: filepath.Join(dir, "current.db"),
		now:         now,
	}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) stamp() string { return isoTime(s.now()) }

// ------------------------------------------------------------------- keys

// IssuedKey is a new key, shown once.
type IssuedKey struct {
	Key       string `json:"key"`
	Role      string `json:"role"`
	Label     string `json:"label"`
	ExpiresAt string `json:"expires_at"`
}

// IssueKey makes a new single-use key. It is returned once: only its hash is kept.
func (s *Store) IssueKey(role, label string) (IssuedKey, error) {
	if role != "write" && role != "read" {
		return IssuedKey{}, refuse(400, `role must be "write" or "read"`)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := newKey()
	expires := isoTime(s.now().Add(keyDays * 24 * time.Hour))
	_, err := s.db.Exec("INSERT INTO keys (hash, role, label, created_at, expires_at) VALUES (?, ?, ?, ?, ?)",
		sha([]byte(normalKey(key))), role, label, s.stamp(), expires)
	if err != nil {
		return IssuedKey{}, err
	}
	return IssuedKey{Key: key, Role: role, Label: label, ExpiresAt: expires}, nil
}

// KeyRow and TokenRow are what the admin sees: nothing that could sign in.
type KeyRow struct {
	ID         int64   `json:"id"`
	Role       string  `json:"role"`
	Label      string  `json:"label"`
	CreatedAt  string  `json:"created_at"`
	ExpiresAt  string  `json:"expires_at"`
	RedeemedAt *string `json:"redeemed_at"`
}

type TokenRow struct {
	ID         int64   `json:"id"`
	Role       string  `json:"role"`
	Kind       string  `json:"kind"`
	Label      string  `json:"label"`
	CreatedAt  string  `json:"created_at"`
	LastSeenAt *string `json:"last_seen_at"`
	RevokedAt  *string `json:"revoked_at"`
}

// Access is every key and token.
type Access struct {
	Keys   []KeyRow   `json:"keys"`
	Tokens []TokenRow `json:"tokens"`
}

// ListAccess lists every key and token, without anything that could be used
// to sign in.
func (s *Store) ListAccess() (Access, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Access{Keys: []KeyRow{}, Tokens: []TokenRow{}}
	keys, err := s.db.Query("SELECT id, role, label, created_at, expires_at, redeemed_at FROM keys ORDER BY id")
	if err != nil {
		return out, err
	}
	for keys.Next() {
		var row KeyRow
		if err := keys.Scan(&row.ID, &row.Role, &row.Label, &row.CreatedAt, &row.ExpiresAt, &row.RedeemedAt); err != nil {
			keys.Close()
			return out, err
		}
		out.Keys = append(out.Keys, row)
	}
	if err := keys.Close(); err != nil {
		return out, err
	}
	tokens, err := s.db.Query("SELECT id, role, kind, label, created_at, last_seen_at, revoked_at FROM tokens ORDER BY id")
	if err != nil {
		return out, err
	}
	defer tokens.Close()
	for tokens.Next() {
		var row TokenRow
		if err := tokens.Scan(&row.ID, &row.Role, &row.Kind, &row.Label, &row.CreatedAt, &row.LastSeenAt, &row.RevokedAt); err != nil {
			return out, err
		}
		out.Tokens = append(out.Tokens, row)
	}
	return out, tokens.Err()
}

// Redeemed is what a spent key buys.
type Redeemed struct {
	Token string `json:"token"`
	Role  string `json:"role"`
	Label string `json:"label"`
}

// Redeem spends a key and returns the token it buys.
//
// want lets the desk ask for a write key specifically: a read key offered to
// a desk is turned away without being spent.
func (s *Store) Redeem(key, kind, want string) (Redeemed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var (
		id         int64
		role       string
		label      string
		expiresAt  string
		redeemedAt sql.NullString
	)
	err := s.db.QueryRow("SELECT id, role, label, expires_at, redeemed_at FROM keys WHERE hash = ?",
		sha([]byte(normalKey(key)))).Scan(&id, &role, &label, &expiresAt, &redeemedAt)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && redeemedAt.Valid && redeemedAt.String != "") {
		return Redeemed{}, refuse(401, "That key is not valid, or it has already been used.")
	}
	if err != nil {
		return Redeemed{}, err
	}
	if expiresAt < s.stamp() {
		return Redeemed{}, refuse(401, "That key has expired. Ask for a new one.")
	}
	if want != "" && role != want {
		return Redeemed{}, refuse(403, fmt.Sprintf("That is a %s key. This needs a %s key. It has not been used up.", role, want))
	}
	token := hex.EncodeToString(randomBytes(32))
	at := s.stamp()
	tx, err := s.db.Begin()
	if err != nil {
		return Redeemed{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE keys SET redeemed_at = ? WHERE id = ?", at, id); err != nil {
		return Redeemed{}, err
	}
	// One desk writes at a time. The desk that held the pen before is retired.
	if role == "write" && kind == "desk" {
		if _, err := tx.Exec("UPDATE tokens SET revoked_at = ? WHERE role = 'write' AND kind = 'desk' AND revoked_at IS NULL", at); err != nil {
			return Redeemed{}, err
		}
	}
	if _, err := tx.Exec("INSERT INTO tokens (hash, role, kind, label, created_at) VALUES (?, ?, ?, ?, ?)",
		sha([]byte(token)), role, kind, label, at); err != nil {
		return Redeemed{}, err
	}
	if err := tx.Commit(); err != nil {
		return Redeemed{}, err
	}
	return Redeemed{Token: token, Role: role, Label: label}, nil
}

// Session says who a token belongs to, or nil if it is unknown or retired.
func (s *Store) Session(token, kind string) (*Session, error) {
	if token == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Session
	err := s.db.QueryRow("SELECT id, role, kind, label FROM tokens WHERE hash = ? AND kind = ? AND revoked_at IS NULL",
		sha([]byte(token)), kind).Scan(&out.ID, &out.Role, &out.Kind, &out.Label)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec("UPDATE tokens SET last_seen_at = ? WHERE id = ?", s.stamp(), out.ID); err != nil {
		return nil, err
	}
	return &out, nil
}

// Revoke retires one token by its number.
func (s *Store) Revoke(tokenID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	done, err := s.db.Exec("UPDATE tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", s.stamp(), tokenID)
	if err != nil {
		return false, err
	}
	changed, err := done.RowsAffected()
	return changed > 0, err
}

// SignOut retires one token by its value.
func (s *Store) SignOut(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("UPDATE tokens SET revoked_at = ? WHERE hash = ? AND revoked_at IS NULL", s.stamp(), sha([]byte(token)))
	return err
}

// --------------------------------------------------------------- register

// Latest is the newest revision, or nil if no desk has sent a register.
func (s *Store) Latest() (*Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest()
}

func (s *Store) latest() (*Revision, error) {
	var out Revision
	err := s.db.QueryRow("SELECT revision, sha256, bytes, synced_at FROM revisions ORDER BY revision DESC LIMIT 1").
		Scan(&out.Revision, &out.SHA256, &out.Bytes, &out.SyncedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) fileOf(revision int64) string {
	return filepath.Join(s.registers, fmt.Sprintf("register-%08d.db", revision))
}

// LatestFile is the latest register's file, or "" if no desk has sent one or
// its file is gone.
func (s *Store) LatestFile() (string, *Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latestFile()
}

func (s *Store) latestFile() (string, *Revision, error) {
	revision, err := s.latest()
	if err != nil || revision == nil {
		return "", nil, err
	}
	path := s.fileOf(revision.Revision)
	if _, err := os.Stat(path); err != nil {
		return "", nil, nil
	}
	return path, revision, nil
}

// PutRegister accepts a register from the writing desk.
//
// base is the revision the desk last agreed with the server on (0 for none).
// If the server has moved on since, the copy is refused: accepting it would
// silently discard cheques another desk recorded.
func (s *Store) PutRegister(data []byte, base, tokenID int64) (*Revision, error) {
	if len(data) > MaxRegisterBytes {
		return nil, refuse(413, "That file is too large to be a register.")
	}
	if len(data) < 100 || !bytes.Equal(data[:16], sqliteMagic) {
		return nil, refuse(400, "That is not a register file.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	latest, err := s.latest()
	if err != nil {
		return nil, err
	}
	held := int64(0)
	if latest != nil {
		held = latest.Revision
	}
	if held != base {
		return nil, staleRegister(latest)
	}
	digest := sha(data)
	// The same register again: nothing to record.
	if latest != nil && latest.SHA256 == digest {
		return latest, nil
	}

	incoming := filepath.Join(s.registers, "incoming-"+hex.EncodeToString(randomBytes(6))+".db")
	if err := os.WriteFile(incoming, data, 0o644); err != nil {
		return nil, err
	}
	defer removeWithSidecars(incoming)
	if err := inspect(incoming); err != nil {
		return nil, err
	}
	syncedAt := s.stamp()
	done, err := s.db.Exec("INSERT INTO revisions (sha256, bytes, synced_at, token_id) VALUES (?, ?, ?, ?)",
		digest, len(data), syncedAt, tokenID)
	if err != nil {
		return nil, err
	}
	revision, err := done.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := os.Rename(incoming, s.fileOf(revision)); err != nil {
		return nil, err
	}
	if err := s.publish(revision); err != nil {
		return nil, err
	}
	s.prune()
	return &Revision{Revision: revision, SHA256: digest, Bytes: int64(len(data)), SyncedAt: syncedAt}, nil
}

func removeWithSidecars(path string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(path + suffix)
	}
}

// inspect refuses anything that is not an intact cheque register.
func inspect(path string) error {
	refusal := refuse(400, "That file is damaged or is not a cheque register. Nothing was replaced.")
	db, err := sql.Open("sqlite", sqliteURI(path, "mode=ro"))
	if err != nil {
		return refusal
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var check string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
		return refusal
	}
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return refusal
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return refusal
		}
		found[name] = true
	}
	if rows.Err() != nil || !found["cheques"] || !found["entities"] {
		return refusal
	}
	return nil
}

// publish puts a revision where the report pages read it. Moved into place whole.
func (s *Store) publish(revision int64) error {
	next := s.CurrentPath + ".next"
	if err := copyFile(s.fileOf(revision), next); err != nil {
		return err
	}
	for _, sidecar := range []string{"-wal", "-shm", "-journal"} {
		_ = os.Remove(s.CurrentPath + sidecar)
	}
	return os.Rename(next, s.CurrentPath)
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Republish makes sure the report pages have the latest register after a restart.
func (s *Store) Republish() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, revision, err := s.latestFile()
	if err != nil || revision == nil {
		return err
	}
	return s.publish(revision.Revision)
}

var registerFile = regexp.MustCompile(`^register-\d+\.db$`)

// prune keeps the most recent copies. The rows stay as the history of what was
// sent and when.
func (s *Store) prune() {
	entries, err := os.ReadDir(s.registers)
	if err != nil {
		return
	}
	var files []string
	for _, entry := range entries {
		if registerFile.MatchString(entry.Name()) {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	for i := 0; i < len(files)-keepRevisions; i++ {
		_ = os.Remove(filepath.Join(s.registers, files[i]))
	}
	// Uploads abandoned by a crash.
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "incoming-") {
			continue
		}
		info, err := entry.Info()
		if err == nil && s.now().Sub(info.ModTime()) > time.Hour {
			_ = os.Remove(filepath.Join(s.registers, entry.Name()))
		}
	}
}
