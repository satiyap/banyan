package desksync

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// This Mac's register when a key is added: what it holds, whether the server
// already has it, and how it is set aside. A register is never deleted: when
// a Mac takes the server's register, or becomes a reports-only app, its own
// is moved beside the new one under a dated name, and the leaves it used are
// carried into the register it takes, so no leaf number is offered twice.

// Agreement is the last copy this desk and the server at apiUrl agreed on.
func (s *Sync) Agreement(apiUrl string) (revision int64, digest string, ok bool) {
	a := s.agreed(apiUrl)
	if a == nil {
		return 0, "", false
	}
	return a.revision, a.sha256, true
}

// EverAgreed is true when this desk's register matches a copy some server took.
func (s *Sync) EverAgreed() bool {
	raw, err := os.ReadFile(agreedPath(s.paths.ConnectionPath))
	if err != nil {
		return false
	}
	var saved struct {
		Sha256 string `json:"sha256"`
	}
	if json.Unmarshal(raw, &saved) != nil || saved.Sha256 == "" {
		return false
	}
	data, err := Snapshot(s.paths.DbPath)
	return err == nil && sha(data) == saved.Sha256
}

// LeavesIn is every leaf number a register file has used, in any status,
// including leaves used up without a cheque.
func LeavesIn(dbPath string) ([]string, error) {
	if !exists(dbPath) {
		return nil, nil
	}
	// Opened for writing although only read: a set-aside file may still have
	// its write-ahead log beside it, which a read-only open cannot take in.
	db, err := sql.Open("sqlite", sqliteURI(dbPath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	// A register from before blank leaves were kept has no table for them.
	tables := map[string]bool{}
	names, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return nil, err
	}
	for names.Next() {
		var n string
		if err := names.Scan(&n); err != nil {
			names.Close()
			return nil, err
		}
		tables[n] = true
	}
	names.Close()
	var parts []string
	hasColumn := func(table string) bool {
		var one int
		return db.QueryRow("SELECT 1 FROM pragma_table_info(?) WHERE name = 'cheque_no'", table).Scan(&one) == nil
	}
	if tables["cheques"] && hasColumn("cheques") {
		parts = append(parts, "SELECT cheque_no FROM cheques WHERE cheque_no != ''")
	}
	if tables["blank_leaves"] && hasColumn("blank_leaves") {
		parts = append(parts, "SELECT cheque_no FROM blank_leaves")
	}
	if len(parts) == 0 {
		return nil, nil
	}
	rows, err := db.Query(strings.Join(parts, " UNION "))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out, rows.Err()
}

// CarryPath holds the leaf numbers a set-aside register used, waiting to be
// recorded in the register that replaced it.
func CarryPath(dbPath string) string { return dbPath + ".carry-leaves.json" }

func writeCarry(dbPath, from string) error {
	leaves, err := LeavesIn(from)
	if err != nil || len(leaves) == 0 {
		return err
	}
	raw, err := json.Marshal(struct {
		From   string   `json:"from"`
		Leaves []string `json:"leaves"`
	}{filepath.Base(from), leaves})
	if err != nil {
		return err
	}
	return os.WriteFile(CarryPath(dbPath), raw, 0o666)
}

// ReadCarry is the leaves waiting to be recorded, and the file they came from.
func ReadCarry(dbPath string) (from string, leaves []string) {
	raw, err := os.ReadFile(CarryPath(dbPath))
	if err != nil {
		return "", nil
	}
	var saved struct {
		From   string   `json:"from"`
		Leaves []string `json:"leaves"`
	}
	if json.Unmarshal(raw, &saved) != nil {
		return "", nil
	}
	return saved.From, saved.Leaves
}

// setAside moves the register and its sidecars to a dated name beside it.
func setAside(dbPath, why string, now time.Time) (string, error) {
	if !exists(dbPath) {
		return "", nil
	}
	stamp := strings.NewReplacer(":", "-", ".", "-").Replace(isoTime(now))
	aside := dbPath + ".before-" + why + "-" + stamp
	if err := os.Rename(dbPath, aside); err != nil {
		return "", err
	}
	// The write-ahead log can hold the latest changes: it goes with the file,
	// so the set-aside copy opens complete.
	for _, sidecar := range []string{"-wal", "-shm", "-journal"} {
		if exists(dbPath + sidecar) {
			if err := os.Rename(dbPath+sidecar, aside+sidecar); err != nil {
				return "", err
			}
		}
	}
	return aside, nil
}

// --------------------------------------------------- reports-only: set aside

func setAsidePath(dbPath string) string { return dbPath + ".set-aside" }

// RequestSetAside asks for the register to be set aside at the next start,
// for a Mac becoming a reports-only app. The open register cannot be moved
// from under the running desk.
func RequestSetAside(dbPath string) error {
	return os.WriteFile(setAsidePath(dbPath), []byte("read\n"), 0o666)
}

// ApplyPendingSetAside runs at start, before the register is opened.
func ApplyPendingSetAside(dbPath string, now func() time.Time) (bool, error) {
	if now == nil {
		now = time.Now
	}
	if !exists(setAsidePath(dbPath)) {
		return false, nil
	}
	if _, err := setAside(dbPath, "read", now()); err != nil {
		return false, err
	}
	return true, remove(setAsidePath(dbPath))
}

// ------------------------------------------------------------ bring it back

// SetAside is a register this Mac put aside when it became a reports-only app.
type SetAside struct {
	Name string `json:"name"`
	At   string `json:"at"`
}

// ListSetAside is every register set aside for a read key, newest first.
func ListSetAside(dbPath string) []SetAside {
	matches, _ := filepath.Glob(dbPath + ".before-read-*")
	var out []SetAside
	for _, m := range matches {
		if strings.HasSuffix(m, "-wal") || strings.HasSuffix(m, "-shm") || strings.HasSuffix(m, "-journal") {
			continue
		}
		at := ""
		if info, err := os.Stat(m); err == nil {
			at = isoTime(info.ModTime())
		}
		out = append(out, SetAside{Name: filepath.Base(m), At: at})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

func bringBackPath(dbPath string) string { return dbPath + ".bring-back" }

// RequestBringBack asks for a set-aside register to be put back at the next start.
func RequestBringBack(dbPath, name string) error {
	for _, s := range ListSetAside(dbPath) {
		if s.Name == name {
			return os.WriteFile(bringBackPath(dbPath), []byte(name), 0o666)
		}
	}
	return errors.New("there is no register set aside by that name")
}

// ApplyPendingBringBack runs at start, before the register is opened. The
// register it replaces is set aside in its turn, never deleted.
func ApplyPendingBringBack(dbPath string, now func() time.Time) (bool, error) {
	if now == nil {
		now = time.Now
	}
	raw, err := os.ReadFile(bringBackPath(dbPath))
	if err != nil {
		return false, nil
	}
	name := strings.TrimSpace(string(raw))
	from := filepath.Join(filepath.Dir(dbPath), name)
	if !strings.HasPrefix(name, filepath.Base(dbPath)+".before-read-") || strings.ContainsAny(name, `/\`) || !exists(from) {
		return false, remove(bringBackPath(dbPath))
	}
	if _, err := setAside(dbPath, "bring-back", now()); err != nil {
		return false, err
	}
	if err := os.Rename(from, dbPath); err != nil {
		return false, err
	}
	return true, remove(bringBackPath(dbPath))
}
