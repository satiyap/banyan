// Package db is SQLite storage for the cheque register.
//
// The register is the treasurer's record of what was physically issued, so rows
// are append-mostly: a cheque is never deleted, only voided, and every state
// change is written to an events table alongside it.
//
// Amounts are stored as integer paise. Dates are ISO strings (YYYY-MM-DD) and
// timestamps are UTC ISO-8601, so ordering is lexicographic and portable.
//
// The SQL text, the schema and the stored value formats are fixed: a register
// written by any earlier version of the desk opens unchanged, and the
// recordings in tests/golden hold them in place. The register was first kept
// by a Python program, which is why values are stored the way Python's
// sqlite3 and json modules write them.
package db

const entitiesTable = `
-- One row per organisation or person the association pays.
--
-- Two names, and the difference matters. payee_name is the only field that ever
-- reaches a cheque: it is printed on the Pay line and must match the bank
-- account exactly. entity_name is what the treasurer calls them, and never
-- leaves this app. Everything else is recorded so the desk remembers how this
-- entity is normally paid.
--
-- Several entities may print the same payee_name: one vendor paid for two
-- different things is two rhythms, not one. A cheque therefore belongs to an
-- entity by id, never by its printed name. The entity name is what tells
-- same-name entities apart, so that pair is unique.
CREATE TABLE IF NOT EXISTS entities (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    payee_name    TEXT NOT NULL,
    entity_name   TEXT NOT NULL DEFAULT '',
    ac_payee      INTEGER NOT NULL DEFAULT 1,
    seal_required INTEGER NOT NULL DEFAULT 0,
    category      TEXT NOT NULL DEFAULT '',
    notes         TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    -- 'one_off' for someone paid once: kept out of the rhythm and grouped
    -- together in reports. These last three come last, in the order older
    -- registers gain them by ALTER, so a rebuild can copy rows with SELECT *.
    kind          TEXT NOT NULL DEFAULT 'regular',
    date_rule     TEXT NOT NULL DEFAULT 'today',
    remind_days   TEXT NOT NULL DEFAULT '3,1,0',
    -- For kind 'per_cheque' only: text each new cheque's payee starts from,
    -- which the treasurer completes or overwrites per leaf.
    payee_prefix  TEXT NOT NULL DEFAULT ''
);
`

const schema = `
CREATE TABLE IF NOT EXISTS cheques (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    cheque_no    TEXT    NOT NULL DEFAULT '',
    payee        TEXT    NOT NULL,
    amount_paise INTEGER NOT NULL CHECK (amount_paise > 0),
    amount_words TEXT    NOT NULL,
    cheque_date  TEXT    NOT NULL,
    memo         TEXT    NOT NULL DEFAULT '',
    status       TEXT    NOT NULL CHECK (status IN
                     ('queued','printed','failed','void','logged')),
    job_id       TEXT    NOT NULL DEFAULT '',
    printer      TEXT    NOT NULL DEFAULT '',
    pdf_path     TEXT    NOT NULL DEFAULT '',
    cal_version  INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL,
    entity_id    INTEGER REFERENCES entities(id),
    batch_id     INTEGER REFERENCES batches(id)
);
CREATE INDEX IF NOT EXISTS idx_cheques_payee ON cheques(payee);
CREATE INDEX IF NOT EXISTS idx_cheques_date  ON cheques(cheque_date);

CREATE TABLE IF NOT EXISTS events (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    cheque_id INTEGER REFERENCES cheques(id),
    kind      TEXT NOT NULL,
    detail    TEXT NOT NULL DEFAULT '',
    at        TEXT NOT NULL,
    subject   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_events_cheque ON events(cheque_id);

-- A run of cheques printed together. Its first cheque always prints alone and
-- is confirmed before the rest are sent, so a misaligned feed costs one leaf,
-- not the whole stack. items holds the drafted cheques as JSON until each is
-- printed; a printed one gains its cheque_id there and a row in cheques.
CREATE TABLE IF NOT EXISTS batches (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    status     TEXT    NOT NULL CHECK (status IN
                   ('review','first_sent','first_ok','rest_sent','done','cancelled')),
    items      TEXT    NOT NULL,
    next_no    TEXT    NOT NULL DEFAULT '',
    printer    TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

` + entitiesTable + `
-- A physical cheque book: the range of leaf numbers printed in it. Leaves are
-- used in order, so how far a book has got is the highest number of it in the
-- register; nothing about a book is stored on the cheques themselves, which
-- is why adding a book never rewrites a register row. position orders the
-- shelf: the book in use is the first one, by position, with leaves left.
CREATE TABLE IF NOT EXISTS cheque_books (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    first_no     TEXT    NOT NULL,
    last_no      TEXT    NOT NULL,
    received_on  TEXT    NOT NULL DEFAULT '',
    position     INTEGER NOT NULL DEFAULT 0,
    -- The 80%-used alert: '' (not answered), 'ordered', or 'snoozed' until
    -- alert_until. Answering it is an event like any other change.
    alert_state  TEXT    NOT NULL DEFAULT '',
    alert_until  TEXT    NOT NULL DEFAULT '',
    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL
);

-- A numbered leaf that was used up without becoming a cheque: torn, spoiled
-- by hand, lost. A cheque row needs a payee and an amount, which a blank leaf
-- does not have, so it is recorded here instead — and counts as used, so its
-- number is never printed on again.
CREATE TABLE IF NOT EXISTS blank_leaves (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    cheque_no  TEXT    NOT NULL UNIQUE,
    reason     TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL
);
`

// Indexes on columns that a register created before entity_id existed only
// gains in migrate, so they are created after it.
const indexes = `
CREATE INDEX IF NOT EXISTS idx_cheques_entity ON cheques(entity_id);
CREATE INDEX IF NOT EXISTS idx_cheques_batch ON cheques(batch_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_entities_names
    ON entities(payee_name, entity_name COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS idx_cheques_no ON cheques(cheque_no);
`

type addedColumn struct{ table, column, ddl string }

// Columns added after the first release. Each is nullable or defaulted, so
// adding one rewrites no row, and a desk from before it — the version on the
// server, say — still reads and writes the register as it always did: it
// simply never mentions the new column.
var addedColumns = []addedColumn{
	{"cheques", "batch_id", "INTEGER REFERENCES batches(id)"},
	{"entities", "kind", "TEXT NOT NULL DEFAULT 'regular'"},
	// How this entity's cheques are dated: 'today', or post-dated to a day of
	// the month — 'next:1' is the 1st of next month, 'this:25' the coming 25th.
	{"entities", "date_rule", "TEXT NOT NULL DEFAULT 'today'"},
	// Days before a post-dated cheque's date to remind, comma-separated; 0 is
	// the day itself.
	{"entities", "remind_days", "TEXT NOT NULL DEFAULT '3,1,0'"},
	// What an event is about when it is not one cheque: 'batch:14', 'book:2',
	// 'leaf:004412', 'test'. Empty for a cheque's own events.
	{"events", "subject", "TEXT NOT NULL DEFAULT ''"},
	{"entities", "payee_prefix", "TEXT NOT NULL DEFAULT ''"},
	// The fixed report head (reports.HEADS); category stays a free-text note.
	{"entities", "head", "TEXT NOT NULL DEFAULT ''"},
}

// Columns added after desk v2 shipped. A register that already went through
// that migration has its pre-desk-v2 copy, so these take a copy of their own.
var afterDeskV2 = map[string]bool{"payee_prefix": true, "head": true}

// PerCheque is the kind of an entity whose payee changes on every cheque (GST,
// paid to a fresh CRN each time). Its payee_name is never printed and never
// matched against a leaf.
const PerCheque = "per_cheque"

// Issued lists the statuses that count as "this cheque leaf was actually used".
var Issued = []string{"queued", "printed", "logged"}

// OpenBatch lists the stages of a batch that is still in progress.
var OpenBatch = []string{"review", "first_sent", "first_ok", "rest_sent"}

// EntityFieldNames lists the columns of an entity that can be set.
var EntityFieldNames = []string{
	"payee_name",
	"entity_name",
	"ac_payee",
	"seal_required",
	"category",
	"notes",
	"kind",
	"date_rule",
	"remind_days",
	"payee_prefix",
	"head",
}

// How long a write waits for another connection to finish.
const busyTimeoutMS = 10000

const eventSQL = "INSERT INTO events (cheque_id, kind, detail, at) VALUES (?,?,?,?)"
