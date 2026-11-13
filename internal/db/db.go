package db

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/satiyap/banyan/internal/pycompat"
)

// Database is the register in one SQLite file.
type Database struct {
	path string
	now  func() time.Time
}

// Open opens the register at path, creating it, its folder and any missing
// table or column, and bringing an older register up to date.
func Open(path string, options Options) (*Database, error) {
	d := &Database{path: path, now: options.Now}
	if d.now == nil {
		d.now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return nil, translate(err)
	}
	if err := d.addColumns(); err != nil {
		return nil, err
	}
	if err := d.migrate(); err != nil {
		return nil, err
	}
	if err := d.do(func(con *conn) error { return con.exec(indexes) }); err != nil {
		return nil, err
	}
	return d, nil
}

// Path is the register's file.
func (d *Database) Path() string { return d.path }

// utcnow is the time now as the register stores it: UTC, whole seconds,
// '+00:00' — Python's datetime.now(timezone.utc).isoformat(timespec='seconds').
func (d *Database) utcnow() string {
	return d.now().UTC().Format("2006-01-02T15:04:05") + "+00:00"
}

// backupPath is a copy of the register beside it, as '<file name>.<tag>'.
func (d *Database) backupPath(tag string) string {
	return filepath.Join(filepath.Dir(d.path), filepath.Base(d.path)+"."+tag)
}

// backupTo copies the whole register to a new file. VACUUM INTO writes the
// same rows and schema as Python's online backup, in one statement.
func (d *Database) backupTo(con *conn, backup string) error {
	return con.exec("VACUUM INTO ?", backup)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func names(cells []any) []string {
	out := make([]string, len(cells))
	for i, cell := range cells {
		out[i], _ = cell.(string)
	}
	return out
}

func columnsOf(con *conn, table string) ([]string, error) {
	res, err := con.all("PRAGMA table_info(" + table + ")")
	return names(res.column("name")), err
}

// addColumns creates any missing table and adds any missing column, in place.
//
// Everything added is a new table or a defaulted column, so no existing
// row is rewritten. Even so, the first time a register is changed this
// way a copy is taken beside it: this file is the only record of which
// leaves were used.
func (d *Database) addColumns() error {
	tables, err := connect(d, func(con *conn) ([]string, error) {
		res, err := con.all("SELECT name FROM sqlite_master WHERE type = 'table'")
		return names(res.column("name")), err
	})
	if err != nil {
		return err
	}
	has := func(name string) bool { return slices.Contains(tables, name) }
	missingTables := len(tables) > 0 && !(has("cheque_books") && has("blank_leaves"))

	missing := func(con *conn) ([]addedColumn, error) {
		var out []addedColumn
		for _, added := range addedColumns {
			columns, err := columnsOf(con, added.table)
			if err != nil {
				return nil, err
			}
			if !slices.Contains(columns, added.column) {
				out = append(out, added)
			}
		}
		return out, nil
	}

	todo, err := connect(d, func(con *conn) ([]addedColumn, error) {
		if err := con.exec(schema); err != nil {
			return nil, err
		}
		return missing(con)
	})
	if err != nil {
		return err
	}
	if (len(todo) > 0 || missingTables) && has("cheques") {
		later := !missingTables && !slices.ContainsFunc(todo, func(a addedColumn) bool { return !afterDeskV2[a.column] })
		tag := "pre-desk-v2"
		if later {
			tag = "pre-" + strings.ReplaceAll(todo[0].column, "_", "-")
		}
		backup := d.backupPath(tag)
		if !exists(backup) {
			if err := d.do(func(con *conn) error { return d.backupTo(con, backup) }); err != nil {
				return err
			}
		}
	}
	return d.do(func(con *conn) error {
		todo, err := missing(con)
		if err != nil {
			return err
		}
		for _, added := range todo {
			if err := con.exec("ALTER TABLE " + added.table + " ADD COLUMN " + added.column + " " + added.ddl); err != nil {
				return err
			}
		}
		return nil
	})
}

// migrate brings a register from before entity_id up to date, in place.
//
// Each step runs only when needed: add cheques.entity_id, rebuild entities
// without the old UNIQUE(payee_name), and link every cheque to the entity
// its payee named. Before this change payee_name was unique, so that link
// is unambiguous. A copy of the file is taken first — this is the only
// record of which leaves were used, and a table rebuild is not something
// to trust blind.
func (d *Database) migrate() error {
	type state struct{ hasColumn, oldUnique, unlinked bool }
	st, err := connect(d, func(con *conn) (state, error) {
		var st state
		columns, err := columnsOf(con, "cheques")
		if err != nil {
			return st, err
		}
		st.hasColumn = slices.Contains(columns, "entity_id")
		indexList, err := con.all("PRAGMA index_list(entities)")
		if err != nil {
			return st, err
		}
		origins := indexList.column("origin")
		for i, name := range names(indexList.column("name")) {
			if origins[i] != "u" {
				continue
			}
			info, err := con.all(`PRAGMA index_info("` + name + `")`)
			if err != nil {
				return st, err
			}
			if on := names(info.column("name")); len(on) == 1 && on[0] == "payee_name" {
				st.oldUnique = true
				break
			}
		}
		if st.hasColumn {
			row, err := con.get("SELECT 1 FROM cheques WHERE entity_id IS NULL LIMIT 1")
			if err != nil {
				return st, err
			}
			st.unlinked = row != nil
		}
		return st, nil
	})
	if err != nil {
		return err
	}
	if st.hasColumn && !st.oldUnique && !st.unlinked {
		return nil
	}

	backup := d.backupPath("pre-entity-id")
	if !st.hasColumn && !exists(backup) {
		if err := d.do(func(con *conn) error { return d.backupTo(con, backup) }); err != nil {
			return err
		}
	}

	// Foreign keys off for the rebuild: the pragma cannot change inside a
	// transaction, and dropping a parent table with them on would cascade.
	// The rebuild follows SQLite's create-copy-drop-rename order; renaming
	// the old table aside instead would repoint cheques' reference at it.
	con, err := openConn(d.path, true)
	if err != nil {
		return translate(err)
	}
	defer con.close()
	if err := con.exec(fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeoutMS)); err != nil {
		return translate(err)
	}
	rebuild := func() error {
		if err := con.exec("PRAGMA foreign_keys = OFF"); err != nil {
			return err
		}
		if err := con.exec("BEGIN IMMEDIATE"); err != nil {
			return err
		}
		if !st.hasColumn {
			if err := con.exec("ALTER TABLE cheques ADD COLUMN entity_id INTEGER REFERENCES entities(id)"); err != nil {
				return err
			}
		}
		if st.oldUnique {
			if err := con.exec(strings.Replace(entitiesTable, "IF NOT EXISTS entities", "entities_new", 1)); err != nil {
				return err
			}
			if _, err := con.run("INSERT INTO entities_new SELECT * FROM entities"); err != nil {
				return err
			}
			if err := con.exec("DROP TABLE entities"); err != nil {
				return err
			}
			if err := con.exec("ALTER TABLE entities_new RENAME TO entities"); err != nil {
				return err
			}
		}
		now := d.utcnow()
		if _, err := con.run(
			"INSERT INTO entities (payee_name, entity_name, created_at, updated_at) "+
				"SELECT DISTINCT payee, payee, ?, ? FROM cheques c WHERE entity_id IS NULL "+
				"AND NOT EXISTS (SELECT 1 FROM entities e WHERE e.payee_name = c.payee)",
			now, now,
		); err != nil {
			return err
		}
		// Only where the printed name picks out exactly one entity; anything
		// else is left for the treasurer to assign rather than guessed.
		if _, err := con.run(
			"UPDATE cheques SET entity_id = (SELECT e.id FROM entities e " +
				"WHERE e.payee_name = cheques.payee) WHERE entity_id IS NULL AND " +
				"(SELECT COUNT(*) FROM entities e WHERE e.payee_name = cheques.payee) = 1",
		); err != nil {
			return err
		}
		bad, err := con.all("PRAGMA foreign_key_check")
		if err != nil {
			return err
		}
		if len(bad.rows) > 0 {
			return MigrationError(fmt.Sprintf("register migration broke %d references", len(bad.rows)))
		}
		return con.exec("COMMIT")
	}
	if err := rebuild(); err != nil {
		con.exec("ROLLBACK")
		return translate(err)
	}
	return nil
}

// connect runs fn on a connection of its own: commit when it returns, roll
// back when it fails, close either way.
func connect[T any](d *Database, fn func(con *conn) (T, error)) (T, error) {
	var zero T
	con, err := openConn(d.path, false)
	if err != nil {
		return zero, translate(err)
	}
	defer con.close()
	for _, pragma := range []string{
		fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeoutMS),
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
	} {
		if err := con.exec(pragma); err != nil {
			return zero, translate(err)
		}
	}
	out, err := fn(con)
	if err == nil {
		err = con.commit()
	}
	if err != nil {
		con.rollback()
		return zero, translate(err)
	}
	return out, nil
}

// do is connect for work that returns nothing.
func (d *Database) do(fn func(con *conn) error) error {
	_, err := connect(d, func(con *conn) (struct{}, error) { return struct{}{}, fn(con) })
	return err
}

func marks(n int, separator string) string {
	return strings.TrimSuffix(strings.Repeat("?"+separator, n), separator)
}

func sets(columns []string) []string {
	out := make([]string, len(columns))
	for i, column := range columns {
		out[i] = column + " = ?"
	}
	return out
}

func anys[T any](values []T) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

// -- writes ----------------------------------------------------------

// AddCheque records a new cheque and its 'created' event, and returns its id.
func (d *Database) AddCheque(cheque NewCheque) (int64, error) {
	now := d.utcnow()
	row := cheque.clone()
	if !row.Has("created_at") {
		row.Set("created_at", now)
	}
	if !row.Has("updated_at") {
		row.Set("updated_at", now)
	}
	return connect(d, func(con *conn) (int64, error) {
		chosen, _ := row.Get("entity_id")
		payee, ok := row.Get("payee")
		if !ok {
			return 0, KeyError("'payee'")
		}
		oneOff, _ := row.Get("one_off")
		row.Delete("one_off")
		kind := "regular"
		if truthy(oneOff) {
			kind = "one_off"
		}
		entity, err := d.entityFor(con, chosen, payee, now, kind)
		if err != nil {
			return 0, err
		}
		row.Set("entity_id", entity)
		cur, err := con.run(
			"INSERT INTO cheques ("+strings.Join(row.names(), ", ")+") VALUES ("+marks(len(row), ", ")+")",
			row.values()...,
		)
		if err != nil {
			return 0, err
		}
		status, _ := row.Get("status")
		if status == nil {
			status = ""
		}
		if _, err := con.run(eventSQL, cur.lastrowid, "created", "status="+text(status), now); err != nil {
			return 0, err
		}
		return cur.lastrowid, nil
	})
}

// UpdateCheque changes columns of a cheque and records an event with them.
func (d *Database) UpdateCheque(chequeID int64, options ChequeChanges) error {
	changes := options.Columns.clone()
	// An event alone is still a change worth recording.
	if len(changes) == 0 && options.Event == "" {
		return nil
	}
	now := d.utcnow()
	changes.Set("updated_at", now)
	return d.do(func(con *conn) error {
		_, err := con.run(
			"UPDATE cheques SET "+strings.Join(sets(changes.names()), ", ")+" WHERE id = ?",
			append(changes.values(), chequeID)...,
		)
		if err != nil {
			return err
		}
		if options.Event != "" {
			_, err = con.run(eventSQL, chequeID, options.Event, options.Detail, now)
		}
		return err
	})
}

// Settle moves queued cheques to printed or failed, all in one transaction.
//
// The status is checked in the same statement that changes it, so two
// verdicts arriving together — a double click, or a void racing a
// confirm — cannot both land: whichever is second finds nothing still
// queued. Returns the ids that actually moved.
//
// Outcomes are applied in the order given.
func (d *Database) Settle(outcomes []Outcome, detail string) ([]int64, error) {
	now := d.utcnow()
	return connect(d, func(con *conn) ([]int64, error) {
		moved := []int64{}
		for _, o := range outcomes {
			cur, err := con.run(
				"UPDATE cheques SET status = ?, updated_at = ? WHERE id = ? AND status = 'queued'",
				o.Outcome, now, o.ChequeID,
			)
			if err != nil {
				return nil, err
			}
			if cur.rowcount != 0 {
				if _, err := con.run(eventSQL, o.ChequeID, "confirmed-"+o.Outcome, detail, now); err != nil {
					return nil, err
				}
				moved = append(moved, o.ChequeID)
			}
		}
		return moved, nil
	})
}

// Void voids a cheque unless it already is — checked and changed together.
func (d *Database) Void(chequeID int64, reason string) (bool, error) {
	now := d.utcnow()
	return connect(d, func(con *conn) (bool, error) {
		cur, err := con.run(
			"UPDATE cheques SET status = 'void', updated_at = ? WHERE id = ? AND status != 'void'",
			now, chequeID,
		)
		if err != nil {
			return false, err
		}
		if cur.rowcount != 0 {
			if _, err := con.run(eventSQL, chequeID, "void", reason, now); err != nil {
				return false, err
			}
		}
		return cur.rowcount != 0, nil
	})
}

// ChequeNoUsed reports whether a leaf number is already in the register. Any
// status counts: a misprinted or voided leaf was still physically used up.
func (d *Database) ChequeNoUsed(chequeNo string) (bool, error) {
	return connect(d, func(con *conn) (bool, error) {
		row, err := con.get(
			"SELECT 1 FROM cheques WHERE cheque_no = ? UNION ALL "+
				"SELECT 1 FROM blank_leaves WHERE cheque_no = ? LIMIT 1",
			chequeNo, chequeNo,
		)
		return row != nil, err
	})
}

// -- batches ----------------------------------------------------------

// CreateBatch starts a batch in review and returns its id. Only one batch may
// be in progress.
func (d *Database) CreateBatch(items []BatchItem, nextNo, printer string) (int64, error) {
	now := d.utcnow()
	return connect(d, func(con *conn) (int64, error) {
		open, err := con.get("SELECT 1 FROM batches WHERE status IN ("+marks(len(OpenBatch), ",")+")", anys(OpenBatch)...)
		if err != nil {
			return 0, err
		}
		if open != nil {
			return 0, ValueError("another batch is still in progress — finish or cancel it first")
		}
		cur, err := con.run(
			"INSERT INTO batches (status, items, next_no, printer, created_at, updated_at) VALUES (?,?,?,?,?,?)",
			"review", pyJSONDumps(items), nextNo, printer, now, now,
		)
		return cur.lastrowid, err
	})
}

// GetBatch is one batch, or nil.
func (d *Database) GetBatch(batchID int64) (*BatchRow, error) {
	row, err := connect(d, func(con *conn) (*batchStored, error) {
		return one[batchStored](con, "SELECT * FROM batches WHERE id = ?", batchID)
	})
	if err != nil || row == nil {
		return nil, err
	}
	out := &BatchRow{
		ID: row.ID, Status: row.Status, NextNo: row.NextNo, Printer: row.Printer,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if err := json.Unmarshal([]byte(row.Items), &out.Items); err != nil {
		return nil, &pycompat.Error{Type: "SyntaxError", Message: err.Error()}
	}
	return out, nil
}

// OpenBatch is the batch in progress, or nil.
func (d *Database) OpenBatch() (*BatchRow, error) {
	row, err := connect(d, func(con *conn) ([]any, error) {
		return con.get(
			"SELECT id FROM batches WHERE status IN ("+marks(len(OpenBatch), ",")+") ORDER BY id DESC LIMIT 1",
			anys(OpenBatch)...,
		)
	})
	if err != nil || row == nil {
		return nil, err
	}
	id, _ := row[0].(int64)
	return d.GetBatch(id)
}

// MoveBatch changes a batch's stage only if it is still where the caller saw it.
//
// Every step of a batch claims its stage this way before touching the
// printer, so a second click on the same button finds the stage already
// taken and sends nothing.
func (d *Database) MoveBatch(batchID int64, fromStatus []string, toStatus string, changes BatchChanges) (bool, error) {
	columns := changes.clone()
	if items, ok := columns.Get("items"); ok {
		columns.Set("items", pyJSONDumps(items))
	}
	assignments := append(sets(columns.names()), "status = ?", "updated_at = ?")
	return connect(d, func(con *conn) (bool, error) {
		params := append(columns.values(), toStatus, d.utcnow(), batchID)
		cur, err := con.run(
			"UPDATE batches SET "+strings.Join(assignments, ", ")+
				" WHERE id = ? AND status IN ("+marks(len(fromStatus), ",")+")",
			append(params, anys(fromStatus)...)...,
		)
		return cur.rowcount != 0, err
	})
}

// SaveBatchItems rewrites a batch's items and the next leaf number.
func (d *Database) SaveBatchItems(batchID int64, items []BatchItem, nextNo string) error {
	return d.do(func(con *conn) error {
		_, err := con.run(
			"UPDATE batches SET items = ?, next_no = ?, updated_at = ? WHERE id = ?",
			pyJSONDumps(items), nextNo, d.utcnow(), batchID,
		)
		return err
	})
}

// entityFor is the entity a new cheque belongs to.
//
// A chosen entity must print exactly the name on the cheque, or the
// register would credit a payment to someone the leaf does not name.
// Without a choice, the printed name decides — and when several entities
// share it, nothing is guessed: the cheque is refused until one is picked.
// Writing to a name nobody has yet creates its entity, so the list of
// entities is never out of step with the register.
func (d *Database) entityFor(con *conn, entityID, payee any, now, kind string) (any, error) {
	found, err := d.matchEntity(con, entityID, payee)
	if err != nil || found != nil {
		return found, err
	}
	cur, err := con.run(
		"INSERT INTO entities (payee_name, entity_name, kind, created_at, updated_at) VALUES (?,?,?,?,?)",
		payee, payee, kind, now, now,
	)
	return cur.lastrowid, err
}

// matchEntity is the existing entity a cheque to this payee belongs to, nil
// when it would be a new one, or ValueError when the choice is wrong or missing.
func (d *Database) matchEntity(con *conn, entityID, payee any) (any, error) {
	if entityID != nil {
		e, err := con.get("SELECT payee_name, kind, payee_prefix FROM entities WHERE id = ?", entityID)
		if err != nil {
			return nil, err
		}
		if e == nil {
			return nil, ValueError("that entity no longer exists")
		}
		payeeName, kind, payeePrefix := e[0], e[1], e[2]
		if kind == PerCheque {
			if truthy(payeePrefix) && same(payee, payeePrefix) {
				return nil, ValueError("type the rest of the payee after %s", repr(payeePrefix))
			}
			return entityID, nil
		}
		if !same(payeeName, payee) {
			return nil, ValueError("that entity prints as %s, not %s", repr(payeeName), repr(payee))
		}
		return entityID, nil
	}
	res, err := con.all("SELECT id, entity_name FROM entities WHERE payee_name = ? AND kind != ?", payee, PerCheque)
	if err != nil {
		return nil, err
	}
	if len(res.rows) > 1 {
		entityNames := make([]string, len(res.rows))
		for i, r := range res.rows {
			entityNames[i] = text(or(r[1], "(unnamed)"))
		}
		return nil, ValueError(
			"%d entities print as %s (%s); choose which one this cheque is for",
			len(res.rows), repr(payee), strings.Join(entityNames, ", "),
		)
	}
	if len(res.rows) == 1 {
		return res.rows[0][0], nil
	}
	return nil, nil
}

// CheckEntity returns ValueError if a cheque to this payee could not be
// recorded. A nil entityID is no choice of entity.
func (d *Database) CheckEntity(entityID *int64, payee string) error {
	return d.do(func(con *conn) error {
		var chosen any
		if entityID != nil {
			chosen = *entityID
		}
		_, err := d.matchEntity(con, chosen, payee)
		return err
	})
}

// -- entities ---------------------------------------------------------

// ListEntities is every entity, by the name the treasurer knows it by.
func (d *Database) ListEntities() ([]EntityRow, error) {
	return connect(d, func(con *conn) ([]EntityRow, error) {
		return list[EntityRow](con,
			"SELECT * FROM entities ORDER BY "+
				"CASE WHEN entity_name = '' THEN payee_name ELSE entity_name END "+
				"COLLATE NOCASE, id")
	})
}

// GetEntity is one entity, or nil.
func (d *Database) GetEntity(entityID int64) (*EntityRow, error) {
	return connect(d, func(con *conn) (*EntityRow, error) {
		return one[EntityRow](con, "SELECT * FROM entities WHERE id = ?", entityID)
	})
}

// checkNames refuses a second entity with the same two names. Same-name
// entities are told apart only by their entity name, so two that agree on
// both would be indistinguishable at the desk.
func (d *Database) checkNames(con *conn, payeeName, entityName, entityID any) error {
	clash, err := con.get(
		"SELECT 1 FROM entities WHERE payee_name = ? AND entity_name = ? COLLATE NOCASE AND id IS NOT ?",
		payeeName, entityName, entityID,
	)
	if err != nil {
		return err
	}
	if clash != nil {
		return ValueError(
			"an entity called %s already prints as %s — give this one a different entity name",
			repr(or(entityName, "(unnamed)")), repr(payeeName),
		)
	}
	return nil
}

// cleanFields keeps the entity columns that were given a value.
func cleanFields(fields EntityFields, skip string) Fields {
	clean := Fields{}
	for _, f := range fields {
		if slices.Contains(EntityFieldNames, f.Name) && f.Name != skip && f.Value != nil {
			clean.Set(f.Name, f.Value)
		}
	}
	return clean
}

// CreateEntity adds an entity that prints payeeName and returns its id.
func (d *Database) CreateEntity(payeeName string, fields EntityFields) (int64, error) {
	now := d.utcnow()
	clean := cleanFields(fields, "payee_name")
	if !clean.Has("entity_name") {
		clean.Set("entity_name", "")
	}
	return connect(d, func(con *conn) (int64, error) {
		entityName, _ := clean.Get("entity_name")
		if err := d.checkNames(con, payeeName, entityName, nil); err != nil {
			return 0, err
		}
		row := append(Fields{{"payee_name", payeeName}}, clean...)
		row = append(row, Field{"created_at", now}, Field{"updated_at", now})
		cur, err := con.run(
			"INSERT INTO entities ("+strings.Join(row.names(), ", ")+") VALUES ("+marks(len(row), ", ")+")",
			row.values()...,
		)
		return cur.lastrowid, err
	})
}

// UpdateEntity edits one entity. Returns whether its printed name changed.
//
// Changing the printed name carries this entity's cheques with it, and
// only this entity's: another entity printing the old name keeps its own
// history. Each cheque touched gets an event, since its payee text changed.
// A payee-per-cheque entity prints no name of its own, so nothing is
// carried, and it cannot become a fixed-name entity while its cheques
// disagree with that name.
func (d *Database) UpdateEntity(entityID int64, fields EntityFields) (bool, error) {
	now := d.utcnow()
	clean := cleanFields(fields, "")
	return connect(d, func(con *conn) (bool, error) {
		old, err := one[EntityRow](con, "SELECT * FROM entities WHERE id = ?", entityID)
		if err != nil {
			return false, err
		}
		if old == nil {
			return false, LookupError("no such entity")
		}
		given := func(name, was string) any {
			if v, ok := clean.Get(name); ok {
				return v
			}
			return was
		}
		payeeName := given("payee_name", old.PayeeName)
		if err := d.checkNames(con, payeeName, given("entity_name", old.EntityName), entityID); err != nil {
			return false, err
		}
		if len(clean) == 0 {
			return false, nil
		}
		kind := given("kind", old.Kind)
		if old.Kind == PerCheque && kind != PerCheque {
			row, err := con.get(
				"SELECT COUNT(DISTINCT payee) n FROM cheques WHERE entity_id = ? AND payee != ?",
				entityID, payeeName,
			)
			if err != nil {
				return false, err
			}
			if other, _ := row[0].(int64); other != 0 {
				plural := ""
				if other != 1 {
					plural = "s"
				}
				return false, ValueError(
					"its cheques print %d payee%s other than %s, so it stays payee per cheque",
					other, plural, repr(payeeName),
				)
			}
		}
		_, err = con.run(
			"UPDATE entities SET "+strings.Join(sets(clean.names()), ", ")+", updated_at = ? WHERE id = ?",
			append(clean.values(), now, entityID)...,
		)
		if err != nil {
			return false, err
		}
		renamed := !same(payeeName, old.PayeeName) && old.Kind != PerCheque && kind != PerCheque
		if renamed {
			_, err := con.run(
				"INSERT INTO events (cheque_id, kind, detail, at) SELECT id, ?, ?, ? FROM cheques WHERE entity_id = ?",
				"payee-renamed", old.PayeeName+" → "+text(payeeName), now, entityID,
			)
			if err != nil {
				return false, err
			}
			_, err = con.run("UPDATE cheques SET payee = ?, updated_at = ? WHERE entity_id = ?", payeeName, now, entityID)
			if err != nil {
				return false, err
			}
		}
		return renamed, nil
	})
}

// MoveCheques counts cheques against another entity, all or none.
//
// The leaf is unchanged — payee, amount, date and status stay as issued —
// so a cheque may only move to an entity that would have printed it: one
// with the same payee name, or a payee-per-cheque entity. Each move is
// guarded on the entity the cheque was read under and writes an event.
// Returns the ids that moved; those already there are skipped.
func (d *Database) MoveCheques(chequeIDs []int64, entityID int64) ([]int64, error) {
	now := d.utcnow()
	return connect(d, func(con *conn) ([]int64, error) {
		target, err := one[EntityRow](con, "SELECT * FROM entities WHERE id = ?", entityID)
		if err != nil {
			return nil, err
		}
		if target == nil {
			return nil, LookupError("no such entity")
		}
		res, err := con.all(
			"SELECT c.id, c.payee, c.entity_id, e.entity_name, e.payee_name "+
				"FROM cheques c LEFT JOIN entities e ON e.id = c.entity_id "+
				"WHERE c.id IN ("+marks(len(chequeIDs), ",")+")",
			anys(chequeIDs)...,
		)
		if err != nil {
			return nil, err
		}
		distinct := map[int64]bool{}
		for _, id := range chequeIDs {
			distinct[id] = true
		}
		if len(res.rows) != len(distinct) {
			return nil, LookupError("some of those cheques are not in the register")
		}
		to := target.EntityName
		if to == "" {
			to = target.PayeeName
		}
		for _, r := range res.rows {
			if payee := r[1]; target.Kind != PerCheque && !same(target.PayeeName, payee) {
				return nil, ValueError(
					"%s prints as %s, but a leaf being moved says %s "+
						"— cheques can only move to an entity that prints the same name, "+
						"or one whose payee changes per cheque",
					repr(to), repr(target.PayeeName), repr(payee),
				)
			}
		}
		moved := []int64{}
		for _, r := range res.rows {
			id, _ := r[0].(int64)
			from, entityName, payeeName := r[2], r[3], r[4]
			if same(from, entityID) {
				continue
			}
			cur, err := con.run(
				"UPDATE cheques SET entity_id = ?, updated_at = ? WHERE id = ? AND entity_id IS ?",
				entityID, now, id, from,
			)
			if err != nil {
				return nil, err
			}
			if cur.rowcount != 1 {
				return nil, ValueError("a cheque changed while it was being moved — reload and try again")
			}
			was := text(or(or(entityName, payeeName), "—"))
			if _, err := con.run(eventSQL, id, "entity-moved", "entity: "+was+" → "+to, now); err != nil {
				return nil, err
			}
			moved = append(moved, id)
		}
		return moved, nil
	})
}

// DeleteEntity removes an entity that has never been paid.
//
// One that has is left alone: the register refers to it, and deleting it
// would leave cheques pointing at an entity the desk no longer knows.
func (d *Database) DeleteEntity(entityID int64) (bool, error) {
	return connect(d, func(con *conn) (bool, error) {
		row, err := con.get("SELECT COUNT(*) c FROM cheques WHERE entity_id = ?", entityID)
		if err != nil {
			return false, err
		}
		if used, _ := row[0].(int64); used != 0 {
			return false, nil
		}
		_, err = con.run("DELETE FROM entities WHERE id = ?", entityID)
		return err == nil, err
	})
}

// -- events not about one cheque -------------------------------------

// AddEvent records an event that is about a subject, a cheque, or nothing.
func (d *Database) AddEvent(kind, detail string, options EventOptions) error {
	return d.do(func(con *conn) error {
		var chequeID any
		if options.ChequeID != nil {
			chequeID = *options.ChequeID
		}
		_, err := con.run(
			"INSERT INTO events (cheque_id, kind, detail, at, subject) VALUES (?,?,?,?,?)",
			chequeID, kind, detail, d.utcnow(), options.Subject,
		)
		return err
	})
}

// EventsAbout is the events recorded about a subject, oldest first.
func (d *Database) EventsAbout(subject string) ([]EventRow, error) {
	return connect(d, func(con *conn) ([]EventRow, error) {
		return list[EventRow](con, "SELECT * FROM events WHERE subject = ? ORDER BY id ASC", subject)
	})
}

// ChequeEvents is every cheque's events of the given kinds, oldest first.
func (d *Database) ChequeEvents(kinds []string) ([]EventRow, error) {
	return connect(d, func(con *conn) ([]EventRow, error) {
		return list[EventRow](con,
			"SELECT * FROM events WHERE kind IN ("+marks(len(kinds), ",")+") AND cheque_id IS NOT NULL ORDER BY id ASC",
			anys(kinds)...,
		)
	})
}

// -- cheque books -------------------------------------------------------

// ListBooks is the shelf, in order.
func (d *Database) ListBooks() ([]BookRow, error) {
	return connect(d, func(con *conn) ([]BookRow, error) {
		return list[BookRow](con, "SELECT * FROM cheque_books ORDER BY position, id")
	})
}

// GetBook is one cheque book, or nil.
func (d *Database) GetBook(bookID int64) (*BookRow, error) {
	return connect(d, func(con *conn) (*BookRow, error) {
		return one[BookRow](con, "SELECT * FROM cheque_books WHERE id = ?", bookID)
	})
}

// position is a shelf edge, 0 when the shelf is empty.
func position(cell any) int64 {
	n, _ := cell.(int64)
	return n
}

// AddBook shelves a book, behind the others or in front of them. The ranges
// have been checked by the caller against every other book and the
// register; this only records the result.
func (d *Database) AddBook(book NewBook) (int64, error) {
	now := d.utcnow()
	return connect(d, func(con *conn) (int64, error) {
		edge, err := con.get("SELECT MIN(position) lo, MAX(position) hi FROM cheque_books")
		if err != nil {
			return 0, err
		}
		at := position(edge[1]) + 1
		if book.Front {
			at = position(edge[0]) - 1
		}
		cur, err := con.run(
			"INSERT INTO cheque_books (name, first_no, last_no, received_on, position, "+
				"created_at, updated_at) VALUES (?,?,?,?,?,?,?)",
			book.Name, book.FirstNo, book.LastNo, book.ReceivedOn, at, now, now,
		)
		if err != nil {
			return 0, err
		}
		_, err = con.run(
			"INSERT INTO events (cheque_id, kind, detail, at, subject) VALUES (NULL,?,?,?,?)",
			"book-added", book.Detail, now, fmt.Sprintf("book:%d", cur.lastrowid),
		)
		return cur.lastrowid, err
	})
}

// UpdateBook changes columns of a book and records the event that says why.
func (d *Database) UpdateBook(bookID int64, options BookChanges) error {
	changes := options.Columns.clone()
	now := d.utcnow()
	if options.Front {
		lo, err := connect(d, func(con *conn) (int64, error) {
			row, err := con.get("SELECT MIN(position) lo FROM cheque_books")
			if err != nil {
				return 0, err
			}
			return position(row[0]), nil
		})
		if err != nil {
			return err
		}
		changes.Set("position", lo-1)
	}
	assignments := append(sets(changes.names()), "updated_at = ?")
	return d.do(func(con *conn) error {
		_, err := con.run(
			"UPDATE cheque_books SET "+strings.Join(assignments, ", ")+" WHERE id = ?",
			append(changes.values(), now, bookID)...,
		)
		if err != nil {
			return err
		}
		_, err = con.run(
			"INSERT INTO events (cheque_id, kind, detail, at, subject) VALUES (NULL,?,?,?,?)",
			options.Kind, options.Detail, now, fmt.Sprintf("book:%d", bookID),
		)
		return err
	})
}

// -- blank leaves -------------------------------------------------------

// ListBlankLeaves is every leaf used up without a cheque.
func (d *Database) ListBlankLeaves() ([]BlankLeafRow, error) {
	return connect(d, func(con *conn) ([]BlankLeafRow, error) {
		return list[BlankLeafRow](con, "SELECT * FROM blank_leaves ORDER BY id")
	})
}

// AddBlankLeaves records leaves used up without a cheque. A number already in
// the register, in either table, is refused and nothing is written —
// checked inside the same transaction as the inserts.
func (d *Database) AddBlankLeaves(numbers []string, reason string) ([]string, error) {
	now := d.utcnow()
	err := d.do(func(con *conn) error {
		var taken []string
		for _, n := range numbers {
			row, err := con.get(
				"SELECT 1 FROM cheques WHERE cheque_no = ? UNION ALL SELECT 1 FROM blank_leaves WHERE cheque_no = ?",
				n, n,
			)
			if err != nil {
				return err
			}
			if row != nil {
				taken = append(taken, n)
			}
		}
		if len(taken) > 0 {
			return ValueError("already in the register: %s", strings.Join(taken, ", "))
		}
		for _, n := range numbers {
			if _, err := con.run("INSERT INTO blank_leaves (cheque_no, reason, created_at) VALUES (?,?,?)", n, reason, now); err != nil {
				return err
			}
			if _, err := con.run(
				"INSERT INTO events (cheque_id, kind, detail, at, subject) VALUES (NULL,?,?,?,?)",
				"leaf-void", reason, now, "leaf:"+n,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return numbers, nil
}

// UsedNumbers is every leaf number the register knows, in any status.
func (d *Database) UsedNumbers() (map[string]bool, error) {
	return connect(d, func(con *conn) (map[string]bool, error) {
		res, err := con.all("SELECT cheque_no FROM cheques WHERE cheque_no != '' UNION SELECT cheque_no FROM blank_leaves")
		if err != nil {
			return nil, err
		}
		out := map[string]bool{}
		for _, r := range res.rows {
			out[text(r[0])] = true
		}
		return out, nil
	})
}

// -- reads -----------------------------------------------------------

// GetCheque is one cheque, or nil.
func (d *Database) GetCheque(chequeID int64) (*ChequeWithEntity, error) {
	return connect(d, func(con *conn) (*ChequeWithEntity, error) {
		return one[ChequeWithEntity](con,
			"SELECT c.*, COALESCE(e.entity_name, '') AS entity_name FROM cheques c "+
				"LEFT JOIN entities e ON e.id = c.entity_id WHERE c.id = ?",
			chequeID,
		)
	})
}

// ListCheques is the register, newest cheque date first.
func (d *Database) ListCheques(options ListChequesOptions) ([]ChequeWithEntity, error) {
	var where []string
	var params []any
	if options.Status != "" {
		where = append(where, "c.status = ?")
		params = append(params, options.Status)
	}
	if options.Payee != "" {
		where = append(where, "c.payee = ?")
		params = append(params, options.Payee)
	}
	if options.EntityID != nil {
		where = append(where, "c.entity_id = ?")
		params = append(params, *options.EntityID)
	}
	if options.Search != "" {
		where = append(where, "(c.payee LIKE ? OR e.entity_name LIKE ? OR c.cheque_no LIKE ? OR c.memo LIKE ?)")
		for i := 0; i < 4; i++ {
			params = append(params, "%"+options.Search+"%")
		}
	}
	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}
	limit := int64(500)
	if options.Limit != nil {
		limit = *options.Limit
	}
	query := "SELECT c.*, COALESCE(e.entity_name, '') AS entity_name " +
		"FROM cheques c LEFT JOIN entities e ON e.id = c.entity_id " + clause + " " +
		"ORDER BY c.cheque_date DESC, c.id DESC LIMIT ? OFFSET ?"
	return connect(d, func(con *conn) ([]ChequeWithEntity, error) {
		return list[ChequeWithEntity](con, query, append(params, limit, options.Offset)...)
	})
}

// IssuedCheques is every cheque that consumed a leaf, oldest first — the
// basis of the frequency analysis. Void and failed prints are excluded
// because they represent no payment.
func (d *Database) IssuedCheques() ([]ChequeRow, error) {
	return connect(d, func(con *conn) ([]ChequeRow, error) {
		return list[ChequeRow](con,
			"SELECT * FROM cheques WHERE status IN ("+marks(len(Issued), ",")+") ORDER BY cheque_date ASC, id ASC",
			anys(Issued)...,
		)
	})
}

// EventsFor is one cheque's events, oldest first.
func (d *Database) EventsFor(chequeID int64) ([]EventRow, error) {
	return connect(d, func(con *conn) ([]EventRow, error) {
		return list[EventRow](con, "SELECT * FROM events WHERE cheque_id = ? ORDER BY id ASC", chequeID)
	})
}

// EntityMeta is what the frequency analysis needs to know of each entity, by entity id.
func (d *Database) EntityMeta() (map[int64]EntityMeta, error) {
	type row struct {
		ID int64 `json:"id"`
		EntityMeta
	}
	rows, err := connect(d, func(con *conn) ([]row, error) {
		return list[row](con, "SELECT id, entity_name, category, notes, kind FROM entities")
	})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]EntityMeta, len(rows))
	for _, r := range rows {
		out[r.ID] = r.EntityMeta
	}
	return out, nil
}

// PendingJobs is the queued cheques that were handed to a printer.
func (d *Database) PendingJobs() ([]ChequeRow, error) {
	return connect(d, func(con *conn) ([]ChequeRow, error) {
		return list[ChequeRow](con, "SELECT * FROM cheques WHERE status = 'queued' AND job_id != ''")
	})
}

// NextChequeNo suggests the next cheque number by incrementing the last one
// used, or current when it is given.
func (d *Database) NextChequeNo(current string) (string, error) {
	if current == "" {
		row, err := connect(d, func(con *conn) ([]any, error) {
			return con.get("SELECT cheque_no FROM cheques WHERE cheque_no != '' ORDER BY id DESC LIMIT 1")
		})
		if err != nil {
			return "", err
		}
		if row != nil {
			current = text(row[0])
		}
	}
	return IncrementNo(current), nil
}

var trailingDigits = regexp.MustCompile(`^([^\n]*?)(\p{Nd}+)$`)

// digitValue is the value of a decimal digit in any script, as Python's int()
// reads it. Every script's digits are a run of code points starting at its zero.
func digitValue(r rune) byte {
	if r >= '0' && r <= '9' {
		return byte(r - '0')
	}
	start := r
	for unicode.Is(unicode.Nd, start-1) {
		start--
	}
	return byte((r - start) % 10)
}

// IncrementNo is the leaf after this one: 000123 -> 000124, keeping any
// prefix and the zero padding. Empty when the number does not end in digits.
func IncrementNo(current string) string {
	m := trailingDigits.FindStringSubmatch(pycompat.Strip(current))
	if m == nil {
		return ""
	}
	ascii := make([]byte, 0, len(m[2]))
	for _, r := range m[2] {
		ascii = append(ascii, '0'+digitValue(r))
	}
	next, _ := new(big.Int).SetString(string(ascii), 10)
	out := next.Add(next, big.NewInt(1)).String()
	if pad := utf8.RuneCountInString(m[2]) - len(out); pad > 0 {
		out = strings.Repeat("0", pad) + out
	}
	return m[1] + out
}
