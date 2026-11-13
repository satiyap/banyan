/**
 * SQLite storage for the cheque register.
 *
 * The register is the treasurer's record of what was physically issued, so rows
 * are append-mostly: a cheque is never deleted, only voided, and every state
 * change is written to an events table alongside it.
 *
 * Amounts are stored as integer paise. Dates are ISO strings (YYYY-MM-DD) and
 * timestamps are UTC ISO-8601, so ordering is lexicographic and portable.
 *
 * The SQL text, the schema and the stored value formats are fixed: a register
 * written by any earlier version of the desk opens unchanged, and the
 * recordings in tests/golden hold them in place. The register was first kept
 * by a Python program, which is why values are stored the way Python's
 * sqlite3 and json modules write them.
 */

import { existsSync, mkdirSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
import { DatabaseSync, type SQLInputValue, type SQLOutputValue } from 'node:sqlite'

import { pyRepr, pyStrip, ValueError } from './pycompat'

const ENTITIES_TABLE = `
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

const SCHEMA =
  `
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

` +
  ENTITIES_TABLE +
  `
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
const INDEXES = `
CREATE INDEX IF NOT EXISTS idx_cheques_entity ON cheques(entity_id);
CREATE INDEX IF NOT EXISTS idx_cheques_batch ON cheques(batch_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_entities_names
    ON entities(payee_name, entity_name COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS idx_cheques_no ON cheques(cheque_no);
`

// Columns added after the first release, as [table, column, definition]. Each
// is nullable or defaulted, so adding one rewrites no row, and a desk from
// before it — the version on the server, say — still reads and writes the
// register as it always did: it simply never mentions the new column.
const ADDED_COLUMNS: ReadonlyArray<readonly [table: string, column: string, ddl: string]> = [
  ['cheques', 'batch_id', 'INTEGER REFERENCES batches(id)'],
  ['entities', 'kind', "TEXT NOT NULL DEFAULT 'regular'"],
  // How this entity's cheques are dated: 'today', or post-dated to a day of
  // the month — 'next:1' is the 1st of next month, 'this:25' the coming 25th.
  ['entities', 'date_rule', "TEXT NOT NULL DEFAULT 'today'"],
  // Days before a post-dated cheque's date to remind, comma-separated; 0 is
  // the day itself.
  ['entities', 'remind_days', "TEXT NOT NULL DEFAULT '3,1,0'"],
  // What an event is about when it is not one cheque: 'batch:14', 'book:2',
  // 'leaf:004412', 'test'. Empty for a cheque's own events.
  ['events', 'subject', "TEXT NOT NULL DEFAULT ''"],
  ['entities', 'payee_prefix', "TEXT NOT NULL DEFAULT ''"],
  // The fixed report head (reports.HEADS); category stays a free-text note.
  ['entities', 'head', "TEXT NOT NULL DEFAULT ''"]
]

// Columns added after desk v2 shipped. A register that already went through
// that migration has its pre-desk-v2 copy, so these take a copy of their own.
const AFTER_DESK_V2: ReadonlySet<string> = new Set(['payee_prefix', 'head'])

// An entity whose payee changes on every cheque (GST, paid to a fresh CRN each
// time). Its payee_name is never printed and never matched against a leaf.
export const PER_CHEQUE = 'per_cheque'

// Statuses that count as "this cheque leaf was actually used".
export const ISSUED = ['queued', 'printed', 'logged'] as const

// How long a write waits for another connection to finish.
const BUSY_TIMEOUT_MS = 10000

// ---------------------------------------------------------------- errors
// Named after the Python exceptions the recordings in tests/golden expect,
// with the same messages; the HTTP layer maps each to a status. ValueError —
// the request is understood but the register refuses it — is the one every
// module shares.
export { ValueError }

/** Python LookupError: the row asked for is not in the register. */
export class LookupError extends Error {
  override name = 'LookupError'
}

/** Python KeyError (a LookupError there too): a required field was not given. */
export class KeyError extends LookupError {
  override name = 'KeyError'
}

/** Python sqlite3.DatabaseError: anything SQLite itself refused. */
export class DatabaseError extends Error {
  override name = 'DatabaseError'
  /** SQLite's extended result code, when the engine supplied one. */
  errcode?: number
}

/** Python sqlite3.IntegrityError: a CHECK, NOT NULL, UNIQUE or FOREIGN KEY constraint failed. */
export class IntegrityError extends DatabaseError {
  override name = 'IntegrityError'
}

/** Python sqlite3.OperationalError: bad SQL (an unknown column, say), a locked or unreadable file. */
export class OperationalError extends DatabaseError {
  override name = 'OperationalError'
}

/** Python RuntimeError from the register migration: the rebuild left dangling references. */
export class MigrationError extends Error {
  override name = 'MigrationError'
}

// SQLite primary result codes that Python's sqlite3 raises as OperationalError.
const OPERATIONAL_CODES: ReadonlySet<number> = new Set([1, 3, 4, 5, 6, 8, 9, 10, 13, 14, 15, 16, 17])

function translate(error: unknown): unknown {
  if (!(error instanceof Error) || (error as { code?: unknown }).code !== 'ERR_SQLITE_ERROR') return error
  const errcode = Number((error as { errcode?: unknown }).errcode)
  const primary = errcode & 0xff
  const out =
    primary === 19 || primary === 20
      ? new IntegrityError(error.message)
      : OPERATIONAL_CODES.has(primary)
        ? new OperationalError(error.message)
        : new DatabaseError(error.message)
  out.errcode = errcode
  out.cause = error
  return out
}

// ------------------------------------------------------------------ rows
/** A value a column can be given. Booleans are stored as 1 and 0, as Python stores them. */
export type SqlValue = string | number | bigint | boolean | null

export type ChequeStatus = 'queued' | 'printed' | 'failed' | 'void' | 'logged'
export type BatchStatus = 'review' | 'first_sent' | 'first_ok' | 'rest_sent' | 'done' | 'cancelled'

export interface ChequeRow {
  id: number
  cheque_no: string
  payee: string
  amount_paise: number
  amount_words: string
  cheque_date: string
  memo: string
  status: ChequeStatus
  job_id: string
  printer: string
  pdf_path: string
  cal_version: number
  created_at: string
  updated_at: string
  entity_id: number | null
  batch_id: number | null
}

/** A cheque as getCheque and listCheques return it: with its entity's name, '' when it has none. */
export interface ChequeWithEntity extends ChequeRow {
  entity_name: string
}

export interface EntityRow {
  id: number
  payee_name: string
  entity_name: string
  ac_payee: number
  seal_required: number
  category: string
  notes: string
  created_at: string
  updated_at: string
  kind: string
  date_rule: string
  remind_days: string
  payee_prefix: string
  head: string
}

export interface EventRow {
  id: number
  cheque_id: number | null
  kind: string
  detail: string
  at: string
  subject: string
}

/** One drafted cheque of a batch, as the HTTP layer stores it. */
export type BatchItem = Record<string, unknown>

export interface BatchRow {
  id: number
  status: BatchStatus
  items: BatchItem[]
  next_no: string
  printer: string
  created_at: string
  updated_at: string
}

export interface BookRow {
  id: number
  name: string
  first_no: string
  last_no: string
  received_on: string
  position: number
  alert_state: string
  alert_until: string
  created_at: string
  updated_at: string
}

export interface BlankLeafRow {
  id: number
  cheque_no: string
  reason: string
  created_at: string
}

export interface EntityMeta {
  category: string
  notes: string
  entity_name: string
  kind: string
}

/** Columns of a new cheque, by column name. one_off is not a column: it decides the kind of a new entity. */
export interface NewCheque {
  payee: string
  entity_id?: number | null
  one_off?: boolean | null
  [column: string]: SqlValue | undefined
}

/** Column changes for a cheque, plus the event to record with them. */
export interface ChequeChanges {
  event?: string
  detail?: string
  [column: string]: SqlValue | undefined
}

export interface ListChequesOptions {
  status?: string
  payee?: string
  search?: string
  entity_id?: number | null
  limit?: number
  offset?: number
}

/** Fields of an entity. Unknown keys and null values are ignored. */
export interface EntityFields {
  payee_name?: string | null
  entity_name?: string | null
  ac_payee?: number | boolean | null
  seal_required?: number | boolean | null
  category?: string | null
  notes?: string | null
  kind?: string | null
  date_rule?: string | null
  remind_days?: string | null
  payee_prefix?: string | null
  head?: string | null
  [other: string]: SqlValue | undefined
}

export interface NewBook {
  name: string
  first_no: string
  last_no: string
  received_on: string
  front: boolean
  detail: string
}

/** The event to record for a book, plus its column changes; front: true moves it to the front of the shelf. */
export interface BookChanges {
  kind: string
  detail: string
  front?: boolean
  [column: string]: SqlValue | undefined
}

/** Column changes made with a batch's stage move; items is stored as JSON. */
export interface BatchChanges {
  items?: BatchItem[]
  [column: string]: SqlValue | BatchItem[] | undefined
}

export interface EventOptions {
  subject?: string
  cheque_id?: number | null
}

export interface DatabaseOptions {
  /** The clock. Only tests replace it. */
  now?: () => Date
}

// --------------------------------------------------------------- helpers
type Row = Record<string, SQLOutputValue>

/** node:sqlite hands back null-prototype rows; callers get plain objects, with every integer a number. */
function plain<T>(row: Row): T {
  const out: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(row)) out[key] = typeof value === 'bigint' ? Number(value) : value
  return out as T
}

/** Bind a value the way Python's sqlite3 does: True and False as 1 and 0, and a whole number as an INTEGER
 *  (node:sqlite would bind every number as a REAL, which a TEXT column stores as '5.0'). */
function bind(value: unknown): SQLInputValue {
  if (typeof value === 'boolean') return value ? 1n : 0n
  if (typeof value === 'number' && Number.isSafeInteger(value)) return BigInt(value)
  return value as SQLInputValue
}

/** The keyword arguments actually given: a key left undefined was not passed. */
function given<V>(fields: Record<string, V | undefined>): Record<string, V> {
  const out: Record<string, V> = {}
  for (const [key, value] of Object.entries(fields)) if (value !== undefined) out[key] = value
  return out
}

function marksFor(values: readonly unknown[]): string {
  return values.map(() => '?').join(',')
}

function pyJsonString(text: string): string {
  let out = '"'
  for (let i = 0; i < text.length; i++) {
    const ch = text[i]
    const code = text.charCodeAt(i)
    if (ch === '"') out += '\\"'
    else if (ch === '\\') out += '\\\\'
    else if (ch === '\n') out += '\\n'
    else if (ch === '\r') out += '\\r'
    else if (ch === '\t') out += '\\t'
    else if (ch === '\b') out += '\\b'
    else if (ch === '\f') out += '\\f'
    else if (code < 0x20 || code > 0x7e) out += '\\u' + code.toString(16).padStart(4, '0')
    else out += ch
  }
  return out + '"'
}

/** Python's json.dumps with its defaults — ', ' and ': ' separators, non-ASCII escaped — so the items column
 *  holds the same text whichever desk wrote it. */
function pyJsonDumps(value: unknown): string {
  if (value === null || value === undefined) return 'null'
  if (value === true) return 'true'
  if (value === false) return 'false'
  if (typeof value === 'string') return pyJsonString(value)
  if (typeof value === 'bigint') return value.toString()
  if (typeof value === 'number') {
    if (Number.isNaN(value)) return 'NaN'
    if (!Number.isFinite(value)) return value > 0 ? 'Infinity' : '-Infinity'
    return String(value)
  }
  if (Array.isArray(value)) return '[' + value.map(pyJsonDumps).join(', ') + ']'
  const pairs = Object.entries(value as Record<string, unknown>)
    .filter(([, v]) => v !== undefined)
    .map(([k, v]) => pyJsonString(k) + ': ' + pyJsonDumps(v))
  return '{' + pairs.join(', ') + '}'
}

interface Written {
  rowcount: number
  lastrowid: number
}

/**
 * One connection, behaving as Python's sqlite3 does by default: reads run on
 * their own, and the first INSERT, UPDATE or DELETE opens a transaction that
 * lasts until commit or rollback. With manual set nothing is opened
 * implicitly (Python's isolation_level=None).
 */
class Connection {
  private inTransaction = false

  constructor(
    private readonly raw: DatabaseSync,
    private readonly manual = false
  ) {}

  /** Run SQL that takes no parameters and is not a row change: a script, a PRAGMA, DDL. */
  exec(sql: string): void {
    this.raw.exec(sql)
  }

  all<T = Row>(sql: string, params: readonly unknown[] = []): T[] {
    return (this.raw.prepare(sql).all(...params.map(bind)) as Row[]).map((row) => plain<T>(row))
  }

  get<T = Row>(sql: string, params: readonly unknown[] = []): T | undefined {
    const row = this.raw.prepare(sql).get(...params.map(bind)) as Row | undefined
    return row === undefined ? undefined : plain<T>(row)
  }

  /** An INSERT, UPDATE or DELETE. */
  run(sql: string, params: readonly unknown[] = []): Written {
    if (!this.manual && !this.inTransaction) {
      this.raw.exec('BEGIN')
      this.inTransaction = true
    }
    const result = this.raw.prepare(sql).run(...params.map(bind))
    return { rowcount: Number(result.changes), lastrowid: Number(result.lastInsertRowid) }
  }

  commit(): void {
    if (!this.inTransaction) return
    this.raw.exec('COMMIT')
    this.inTransaction = false
  }

  rollback(): void {
    if (!this.inTransaction) return
    this.inTransaction = false
    this.raw.exec('ROLLBACK')
  }
}

const EVENT_SQL = 'INSERT INTO events (cheque_id, kind, detail, at) VALUES (?,?,?,?)'

export class Database {
  static readonly OPEN_BATCH: readonly string[] = ['review', 'first_sent', 'first_ok', 'rest_sent']

  static readonly ENTITY_FIELDS: readonly string[] = [
    'payee_name',
    'entity_name',
    'ac_payee',
    'seal_required',
    'category',
    'notes',
    'kind',
    'date_rule',
    'remind_days',
    'payee_prefix',
    'head'
  ]

  readonly path: string
  private readonly now: () => Date

  constructor(path: string, options: DatabaseOptions = {}) {
    this.path = path
    this.now = options.now ?? (() => new Date())
    mkdirSync(dirname(this.path), { recursive: true })
    this.addColumns()
    this.migrate()
    this.connect((con) => con.exec(INDEXES))
  }

  /** The time now as the register stores it: UTC, whole seconds, '+00:00' — Python's
   *  datetime.now(timezone.utc).isoformat(timespec='seconds'). */
  private utcnow(): string {
    return this.now().toISOString().slice(0, 19) + '+00:00'
  }

  /** A copy of the register beside it, as `<file name>.<tag>`. */
  private backupPath(tag: string): string {
    return join(dirname(this.path), `${basename(this.path)}.${tag}`)
  }

  /** Copy the whole register to a new file. Python uses the online backup API, which node:sqlite only
   *  offers asynchronously; VACUUM INTO writes the same rows and schema in one synchronous statement. */
  private backupTo(con: Connection, backup: string): void {
    con.all('VACUUM INTO ?', [backup])
  }

  /**
   * Create any missing table and add any missing column, in place.
   *
   * Everything added is a new table or a defaulted column, so no existing
   * row is rewritten. Even so, the first time a register is changed this
   * way a copy is taken beside it: this file is the only record of which
   * leaves were used.
   */
  private addColumns(): void {
    const tables = this.connect(
      (con) =>
        new Set(con.all<{ name: string }>("SELECT name FROM sqlite_master WHERE type = 'table'").map((r) => r.name))
    )
    const missingTables = tables.size > 0 && !(tables.has('cheque_books') && tables.has('blank_leaves'))

    const missing = (con: Connection): Array<readonly [string, string, string]> =>
      ADDED_COLUMNS.filter(
        ([table, column]) =>
          !con.all<{ name: string }>(`PRAGMA table_info(${table})`).some((r) => r.name === column)
      )

    const todo = this.connect((con) => {
      con.exec(SCHEMA)
      return missing(con)
    })
    if ((todo.length > 0 || missingTables) && tables.has('cheques')) {
      const later = !missingTables && todo.every(([, column]) => AFTER_DESK_V2.has(column))
      const tag = later ? 'pre-' + todo[0][1].replaceAll('_', '-') : 'pre-desk-v2'
      const backup = this.backupPath(tag)
      if (!existsSync(backup)) this.connect((con) => this.backupTo(con, backup))
    }
    this.connect((con) => {
      for (const [table, column, ddl] of missing(con)) con.exec(`ALTER TABLE ${table} ADD COLUMN ${column} ${ddl}`)
    })
  }

  /**
   * Bring a register from before entity_id up to date, in place.
   *
   * Each step runs only when needed: add cheques.entity_id, rebuild entities
   * without the old UNIQUE(payee_name), and link every cheque to the entity
   * its payee named. Before this change payee_name was unique, so that link
   * is unambiguous. A copy of the file is taken first — this is the only
   * record of which leaves were used, and a table rebuild is not something
   * to trust blind.
   */
  private migrate(): void {
    const { hasColumn, oldUnique, unlinked } = this.connect((con) => {
      const hasColumn = con.all<{ name: string }>('PRAGMA table_info(cheques)').some((r) => r.name === 'entity_id')
      const oldUnique = con.all<{ name: string; origin: string }>('PRAGMA index_list(entities)').some((r) => {
        if (r.origin !== 'u') return false
        const columns = con.all<{ name: string }>(`PRAGMA index_info("${r.name}")`).map((c) => c.name)
        return columns.length === 1 && columns[0] === 'payee_name'
      })
      const unlinked =
        hasColumn && con.get('SELECT 1 FROM cheques WHERE entity_id IS NULL LIMIT 1') !== undefined
      return { hasColumn, oldUnique, unlinked }
    })
    if (hasColumn && !oldUnique && !unlinked) return

    const backup = this.backupPath('pre-entity-id')
    if (!hasColumn && !existsSync(backup)) this.connect((con) => this.backupTo(con, backup))

    // Foreign keys off for the rebuild: the pragma cannot change inside a
    // transaction, and dropping a parent table with them on would cascade.
    // The rebuild follows SQLite's create-copy-drop-rename order; renaming
    // the old table aside instead would repoint cheques' reference at it.
    const raw = new DatabaseSync(this.path)
    raw.exec(`PRAGMA busy_timeout = ${BUSY_TIMEOUT_MS}`)
    const con = new Connection(raw, true)
    try {
      try {
        con.exec('PRAGMA foreign_keys = OFF')
        con.exec('BEGIN IMMEDIATE')
        if (!hasColumn) {
          con.exec('ALTER TABLE cheques ADD COLUMN entity_id INTEGER REFERENCES entities(id)')
        }
        if (oldUnique) {
          con.exec(ENTITIES_TABLE.replace('IF NOT EXISTS entities', 'entities_new'))
          con.run('INSERT INTO entities_new SELECT * FROM entities')
          con.exec('DROP TABLE entities')
          con.exec('ALTER TABLE entities_new RENAME TO entities')
        }
        const now = this.utcnow()
        con.run(
          'INSERT INTO entities (payee_name, entity_name, created_at, updated_at) ' +
            'SELECT DISTINCT payee, payee, ?, ? FROM cheques c WHERE entity_id IS NULL ' +
            'AND NOT EXISTS (SELECT 1 FROM entities e WHERE e.payee_name = c.payee)',
          [now, now]
        )
        // Only where the printed name picks out exactly one entity; anything
        // else is left for the treasurer to assign rather than guessed.
        con.run(
          'UPDATE cheques SET entity_id = (SELECT e.id FROM entities e ' +
            'WHERE e.payee_name = cheques.payee) WHERE entity_id IS NULL AND ' +
            '(SELECT COUNT(*) FROM entities e WHERE e.payee_name = cheques.payee) = 1'
        )
        const bad = con.all('PRAGMA foreign_key_check')
        if (bad.length) throw new MigrationError(`register migration broke ${bad.length} references`)
        con.exec('COMMIT')
      } catch (error) {
        con.exec('ROLLBACK')
        throw error
      }
    } catch (error) {
      throw translate(error)
    } finally {
      raw.close()
    }
  }

  /**
   * Run fn on a connection of its own:
   * commit when it returns, roll back when it throws, close either way.
   */
  private connect<T>(fn: (con: Connection) => T): T {
    const raw = new DatabaseSync(this.path)
    try {
      raw.exec(`PRAGMA busy_timeout = ${BUSY_TIMEOUT_MS}`)
      raw.exec('PRAGMA foreign_keys = ON')
      raw.exec('PRAGMA journal_mode = WAL')
      const con = new Connection(raw)
      try {
        const out = fn(con)
        con.commit()
        return out
      } catch (error) {
        con.rollback()
        throw error
      }
    } catch (error) {
      throw translate(error)
    } finally {
      raw.close()
    }
  }

  // -- writes ----------------------------------------------------------
  addCheque(cheque: NewCheque): number {
    const now = this.utcnow()
    const row: Record<string, SqlValue> = given(cheque)
    if (!('created_at' in row)) row.created_at = now
    if (!('updated_at' in row)) row.updated_at = now
    return this.connect((con) => {
      const chosen = (row.entity_id ?? null) as number | null
      if (!('payee' in row)) throw new KeyError("'payee'")
      const payee = row.payee as string
      const oneOff = row.one_off ?? false
      delete row.one_off
      row.entity_id = this.entityFor(con, chosen, payee, now, oneOff ? 'one_off' : 'regular')
      const cols = Object.keys(row).join(', ')
      const marks = Object.keys(row)
        .map(() => '?')
        .join(', ')
      const cur = con.run(`INSERT INTO cheques (${cols}) VALUES (${marks})`, Object.values(row))
      const chequeId = cur.lastrowid
      con.run(EVENT_SQL, [chequeId, 'created', `status=${row.status ?? ''}`, now])
      return chequeId
    })
  }

  updateCheque(chequeId: number, options: ChequeChanges = {}): void {
    const { event = '', detail = '', ...rest } = options
    const changes: Record<string, SqlValue> = given(rest)
    // An event alone is still a change worth recording.
    if (Object.keys(changes).length === 0 && !event) return
    const now = this.utcnow()
    changes.updated_at = now
    const sets = Object.keys(changes)
      .map((k) => `${k} = ?`)
      .join(', ')
    this.connect((con) => {
      con.run(`UPDATE cheques SET ${sets} WHERE id = ?`, [...Object.values(changes), chequeId])
      if (event) con.run(EVENT_SQL, [chequeId, event, detail, now])
    })
  }

  /**
   * Move queued cheques to printed or failed, all in one transaction.
   *
   * The status is checked in the same statement that changes it, so two
   * verdicts arriving together — a double click, or a void racing a
   * confirm — cannot both land: whichever is second finds nothing still
   * queued. Returns the ids that actually moved.
   *
   * Outcomes are applied in the order given. A Map keeps the order it was
   * built in; a plain object lists its ids in
   * ascending order.
   */
  settle(outcomes: ReadonlyMap<number, string> | Record<number, string>, detail = ''): number[] {
    const now = this.utcnow()
    const pairs: Array<[number, string]> =
      outcomes instanceof Map
        ? [...outcomes.entries()]
        : Object.entries(outcomes as Record<number, string>).map(([id, outcome]) => [Number(id), outcome])
    const moved: number[] = []
    this.connect((con) => {
      for (const [chequeId, outcome] of pairs) {
        const cur = con.run("UPDATE cheques SET status = ?, updated_at = ? WHERE id = ? AND status = 'queued'", [
          outcome,
          now,
          chequeId
        ])
        if (cur.rowcount) {
          con.run(EVENT_SQL, [chequeId, `confirmed-${outcome}`, detail, now])
          moved.push(chequeId)
        }
      }
    })
    return moved
  }

  /** Void a cheque unless it already is — checked and changed together. */
  void(chequeId: number, reason: string): boolean {
    const now = this.utcnow()
    return this.connect((con) => {
      const cur = con.run("UPDATE cheques SET status = 'void', updated_at = ? WHERE id = ? AND status != 'void'", [
        now,
        chequeId
      ])
      if (cur.rowcount) con.run(EVENT_SQL, [chequeId, 'void', reason, now])
      return Boolean(cur.rowcount)
    })
  }

  /** Whether a leaf number is already in the register. Any status counts:
   *  a misprinted or voided leaf was still physically used up. */
  chequeNoUsed(chequeNo: string): boolean {
    return this.connect(
      (con) =>
        con.get(
          'SELECT 1 FROM cheques WHERE cheque_no = ? UNION ALL ' +
            'SELECT 1 FROM blank_leaves WHERE cheque_no = ? LIMIT 1',
          [chequeNo, chequeNo]
        ) !== undefined
    )
  }

  // -- batches ----------------------------------------------------------
  createBatch(items: BatchItem[], nextNo: string, printer: string): number {
    const now = this.utcnow()
    return this.connect((con) => {
      const marks = marksFor(Database.OPEN_BATCH)
      if (con.get(`SELECT 1 FROM batches WHERE status IN (${marks})`, Database.OPEN_BATCH)) {
        throw new ValueError('another batch is still in progress — finish or cancel it first')
      }
      const cur = con.run(
        'INSERT INTO batches (status, items, next_no, printer, created_at, updated_at) VALUES (?,?,?,?,?,?)',
        ['review', pyJsonDumps(items), nextNo, printer, now, now]
      )
      return cur.lastrowid
    })
  }

  getBatch(batchId: number): BatchRow | null {
    const row = this.connect((con) =>
      con.get<Omit<BatchRow, 'items'> & { items: string }>('SELECT * FROM batches WHERE id = ?', [batchId])
    )
    if (!row) return null
    return { ...row, items: JSON.parse(row.items) as BatchItem[] }
  }

  openBatch(): BatchRow | null {
    const marks = marksFor(Database.OPEN_BATCH)
    const row = this.connect((con) =>
      con.get<{ id: number }>(
        `SELECT id FROM batches WHERE status IN (${marks}) ORDER BY id DESC LIMIT 1`,
        Database.OPEN_BATCH
      )
    )
    return row ? this.getBatch(row.id) : null
  }

  /**
   * Change a batch's stage only if it is still where the caller saw it.
   *
   * Every step of a batch claims its stage this way before touching the
   * printer, so a second click on the same button finds the stage already
   * taken and sends nothing.
   */
  moveBatch(
    batchId: number,
    fromStatus: string | readonly string[],
    toStatus: string,
    changes: BatchChanges = {}
  ): boolean {
    const froms = typeof fromStatus === 'string' ? [fromStatus] : fromStatus
    const columns: Record<string, unknown> = given(changes)
    if ('items' in columns) columns.items = pyJsonDumps(columns.items)
    const sets = [...Object.keys(columns).map((k) => `${k} = ?`), 'status = ?', 'updated_at = ?'].join(', ')
    const marks = marksFor(froms)
    return this.connect((con) => {
      const cur = con.run(`UPDATE batches SET ${sets} WHERE id = ? AND status IN (${marks})`, [
        ...Object.values(columns),
        toStatus,
        this.utcnow(),
        batchId,
        ...froms
      ])
      return Boolean(cur.rowcount)
    })
  }

  saveBatchItems(batchId: number, items: BatchItem[], nextNo: string): void {
    this.connect((con) => {
      con.run('UPDATE batches SET items = ?, next_no = ?, updated_at = ? WHERE id = ?', [
        pyJsonDumps(items),
        nextNo,
        this.utcnow(),
        batchId
      ])
    })
  }

  /**
   * The entity a new cheque belongs to.
   *
   * A chosen entity must print exactly the name on the cheque, or the
   * register would credit a payment to someone the leaf does not name.
   * Without a choice, the printed name decides — and when several entities
   * share it, nothing is guessed: the cheque is refused until one is picked.
   * Writing to a name nobody has yet creates its entity, so the list of
   * entities is never out of step with the register.
   */
  private entityFor(con: Connection, entityId: number | null, payee: string, now: string, kind = 'regular'): number {
    const found = this.matchEntity(con, entityId, payee)
    if (found !== null) return found
    const cur = con.run(
      'INSERT INTO entities (payee_name, entity_name, kind, created_at, updated_at) VALUES (?,?,?,?,?)',
      [payee, payee, kind, now, now]
    )
    return cur.lastrowid
  }

  /** The existing entity a cheque to this payee belongs to, null when it
   *  would be a new one, or ValueError when the choice is wrong or missing. */
  private matchEntity(con: Connection, entityId: number | null, payee: string): number | null {
    if (entityId !== null) {
      const e = con.get<{ payee_name: string; kind: string; payee_prefix: string }>(
        'SELECT payee_name, kind, payee_prefix FROM entities WHERE id = ?',
        [entityId]
      )
      if (!e) throw new ValueError('that entity no longer exists')
      if (e.kind === PER_CHEQUE) {
        if (e.payee_prefix && payee === e.payee_prefix) {
          throw new ValueError(`type the rest of the payee after ${pyRepr(e.payee_prefix)}`)
        }
        return entityId
      }
      if (e.payee_name !== payee) {
        throw new ValueError(`that entity prints as ${pyRepr(e.payee_name)}, not ${pyRepr(payee)}`)
      }
      return entityId
    }
    const rows = con.all<{ id: number; entity_name: string }>(
      'SELECT id, entity_name FROM entities WHERE payee_name = ? AND kind != ?',
      [payee, PER_CHEQUE]
    )
    if (rows.length > 1) {
      const names = rows.map((r) => r.entity_name || '(unnamed)').join(', ')
      throw new ValueError(
        `${rows.length} entities print as ${pyRepr(payee)} (${names}); choose which one this cheque is for`
      )
    }
    return rows.length ? rows[0].id : null
  }

  /** Throw ValueError if a cheque to this payee could not be recorded. */
  checkEntity(entityId: number | null | undefined, payee: string): void {
    this.connect((con) => {
      this.matchEntity(con, entityId ?? null, payee)
    })
  }

  // -- entities ---------------------------------------------------------
  listEntities(): EntityRow[] {
    return this.connect((con) =>
      con.all<EntityRow>(
        'SELECT * FROM entities ORDER BY ' +
          "CASE WHEN entity_name = '' THEN payee_name ELSE entity_name END " +
          'COLLATE NOCASE, id'
      )
    )
  }

  getEntity(entityId: number): EntityRow | null {
    return this.connect((con) => con.get<EntityRow>('SELECT * FROM entities WHERE id = ?', [entityId]) ?? null)
  }

  /** Same-name entities are told apart only by their entity name, so two
   *  that agree on both would be indistinguishable at the desk. */
  private checkNames(con: Connection, payeeName: string, entityName: string, entityId: number | null = null): void {
    const clash = con.get(
      'SELECT 1 FROM entities WHERE payee_name = ? AND entity_name = ? COLLATE NOCASE AND id IS NOT ?',
      [payeeName, entityName, entityId]
    )
    if (clash) {
      throw new ValueError(
        `an entity called ${pyRepr(entityName || '(unnamed)')} already prints as ` +
          `${pyRepr(payeeName)} — give this one a different entity name`
      )
    }
  }

  createEntity(payeeName: string, fields: EntityFields = {}): number {
    const now = this.utcnow()
    const clean: Record<string, SqlValue> = {}
    for (const [k, v] of Object.entries(fields)) {
      if (Database.ENTITY_FIELDS.includes(k) && k !== 'payee_name' && v !== null && v !== undefined) clean[k] = v
    }
    if (!('entity_name' in clean)) clean.entity_name = ''
    return this.connect((con) => {
      this.checkNames(con, payeeName, clean.entity_name as string)
      const row: Record<string, SqlValue> = { payee_name: payeeName, ...clean, created_at: now, updated_at: now }
      const marks = Object.keys(row)
        .map(() => '?')
        .join(', ')
      const cur = con.run(`INSERT INTO entities (${Object.keys(row).join(', ')}) VALUES (${marks})`, Object.values(row))
      return cur.lastrowid
    })
  }

  /**
   * Edit one entity. Returns whether its printed name changed.
   *
   * Changing the printed name carries this entity's cheques with it, and
   * only this entity's: another entity printing the old name keeps its own
   * history. Each cheque touched gets an event, since its payee text changed.
   * A payee-per-cheque entity prints no name of its own, so nothing is
   * carried, and it cannot become a fixed-name entity while its cheques
   * disagree with that name.
   */
  updateEntity(entityId: number, fields: EntityFields = {}): boolean {
    const now = this.utcnow()
    const clean: Record<string, SqlValue> = {}
    for (const [k, v] of Object.entries(fields)) {
      if (Database.ENTITY_FIELDS.includes(k) && v !== null && v !== undefined) clean[k] = v
    }
    return this.connect((con) => {
      const old = con.get<EntityRow>('SELECT * FROM entities WHERE id = ?', [entityId])
      if (!old) throw new LookupError('no such entity')
      const payeeName = 'payee_name' in clean ? (clean.payee_name as string) : old.payee_name
      this.checkNames(
        con,
        payeeName,
        'entity_name' in clean ? (clean.entity_name as string) : old.entity_name,
        entityId
      )
      if (Object.keys(clean).length === 0) return false
      const kind = 'kind' in clean ? (clean.kind as string) : old.kind
      if (old.kind === PER_CHEQUE && kind !== PER_CHEQUE) {
        const other = con.get<{ n: number }>(
          'SELECT COUNT(DISTINCT payee) n FROM cheques WHERE entity_id = ? AND payee != ?',
          [entityId, payeeName]
        )!.n
        if (other) {
          throw new ValueError(
            `its cheques print ${other} payee${other !== 1 ? 's' : ''} ` +
              `other than ${pyRepr(payeeName)}, so it stays payee per cheque`
          )
        }
      }
      const sets = Object.keys(clean)
        .map((k) => `${k} = ?`)
        .join(', ')
      con.run(`UPDATE entities SET ${sets}, updated_at = ? WHERE id = ?`, [...Object.values(clean), now, entityId])
      const renamed = payeeName !== old.payee_name && old.kind !== PER_CHEQUE && kind !== PER_CHEQUE
      if (renamed) {
        con.run(
          'INSERT INTO events (cheque_id, kind, detail, at) SELECT id, ?, ?, ? FROM cheques WHERE entity_id = ?',
          ['payee-renamed', `${old.payee_name} → ${payeeName}`, now, entityId]
        )
        con.run('UPDATE cheques SET payee = ?, updated_at = ? WHERE entity_id = ?', [payeeName, now, entityId])
      }
      return renamed
    })
  }

  /**
   * Count cheques against another entity, all or none.
   *
   * The leaf is unchanged — payee, amount, date and status stay as issued —
   * so a cheque may only move to an entity that would have printed it: one
   * with the same payee name, or a payee-per-cheque entity. Each move is
   * guarded on the entity the cheque was read under and writes an event.
   * Returns the ids that moved; those already there are skipped.
   */
  moveCheques(chequeIds: number[], entityId: number): number[] {
    const now = this.utcnow()
    return this.connect((con) => {
      const target = con.get<EntityRow>('SELECT * FROM entities WHERE id = ?', [entityId])
      if (!target) throw new LookupError('no such entity')
      const marks = marksFor(chequeIds)
      const rows = con.all<{
        id: number
        payee: string
        entity_id: number | null
        entity_name: string | null
        payee_name: string | null
      }>(
        'SELECT c.id, c.payee, c.entity_id, e.entity_name, e.payee_name ' +
          'FROM cheques c LEFT JOIN entities e ON e.id = c.entity_id ' +
          `WHERE c.id IN (${marks})`,
        chequeIds
      )
      if (rows.length !== new Set(chequeIds).size) {
        throw new LookupError('some of those cheques are not in the register')
      }
      for (const r of rows) {
        if (target.kind !== PER_CHEQUE && target.payee_name !== r.payee) {
          throw new ValueError(
            `${pyRepr(target.entity_name || target.payee_name)} prints as ` +
              `${pyRepr(target.payee_name)}, but a leaf being moved says ${pyRepr(r.payee)} ` +
              '— cheques can only move to an entity that prints the same name, ' +
              'or one whose payee changes per cheque'
          )
        }
      }
      const to = target.entity_name || target.payee_name
      const moved: number[] = []
      for (const r of rows) {
        if (r.entity_id === entityId) continue
        const cur = con.run('UPDATE cheques SET entity_id = ?, updated_at = ? WHERE id = ? AND entity_id IS ?', [
          entityId,
          now,
          r.id,
          r.entity_id
        ])
        if (cur.rowcount !== 1) {
          throw new ValueError('a cheque changed while it was being moved — reload and try again')
        }
        const was = r.entity_name || r.payee_name || '—'
        con.run(EVENT_SQL, [r.id, 'entity-moved', `entity: ${was} → ${to}`, now])
        moved.push(r.id)
      }
      return moved
    })
  }

  /**
   * Remove an entity that has never been paid.
   *
   * One that has is left alone: the register refers to it, and deleting it
   * would leave cheques pointing at an entity the desk no longer knows.
   */
  deleteEntity(entityId: number): boolean {
    return this.connect((con) => {
      const used = con.get<{ c: number }>('SELECT COUNT(*) c FROM cheques WHERE entity_id = ?', [entityId])!.c
      if (used) return false
      con.run('DELETE FROM entities WHERE id = ?', [entityId])
      return true
    })
  }

  // -- events not about one cheque -------------------------------------
  addEvent(kind: string, detail = '', options: EventOptions = {}): void {
    const { subject = '', cheque_id = null } = options
    this.connect((con) => {
      con.run('INSERT INTO events (cheque_id, kind, detail, at, subject) VALUES (?,?,?,?,?)', [
        cheque_id,
        kind,
        detail,
        this.utcnow(),
        subject
      ])
    })
  }

  eventsAbout(subject: string): EventRow[] {
    return this.connect((con) =>
      con.all<EventRow>('SELECT * FROM events WHERE subject = ? ORDER BY id ASC', [subject])
    )
  }

  chequeEvents(kinds: readonly string[]): EventRow[] {
    const marks = marksFor(kinds)
    return this.connect((con) =>
      con.all<EventRow>(
        `SELECT * FROM events WHERE kind IN (${marks}) AND cheque_id IS NOT NULL ORDER BY id ASC`,
        kinds
      )
    )
  }

  // -- cheque books -------------------------------------------------------
  listBooks(): BookRow[] {
    return this.connect((con) => con.all<BookRow>('SELECT * FROM cheque_books ORDER BY position, id'))
  }

  getBook(bookId: number): BookRow | null {
    return this.connect((con) => con.get<BookRow>('SELECT * FROM cheque_books WHERE id = ?', [bookId]) ?? null)
  }

  /** Shelve a book, behind the others or in front of them. The ranges
   *  have been checked by the caller against every other book and the
   *  register; this only records the result. */
  addBook(book: NewBook): number {
    const { name, first_no, last_no, received_on, front, detail } = book
    const now = this.utcnow()
    return this.connect((con) => {
      const edge = con.get<{ lo: number | null; hi: number | null }>(
        'SELECT MIN(position) lo, MAX(position) hi FROM cheque_books'
      )!
      const position = front ? (edge.lo || 0) - 1 : (edge.hi || 0) + 1
      const cur = con.run(
        'INSERT INTO cheque_books (name, first_no, last_no, received_on, position, ' +
          'created_at, updated_at) VALUES (?,?,?,?,?,?,?)',
        [name, first_no, last_no, received_on, position, now, now]
      )
      const bookId = cur.lastrowid
      con.run('INSERT INTO events (cheque_id, kind, detail, at, subject) VALUES (NULL,?,?,?,?)', [
        'book-added',
        detail,
        now,
        `book:${bookId}`
      ])
      return bookId
    })
  }

  updateBook(bookId: number, options: BookChanges): void {
    const { kind, detail, front = false, ...rest } = options
    const changes: Record<string, SqlValue> = given(rest)
    const now = this.utcnow()
    if (front) {
      const lo = this.connect((con) => con.get<{ lo: number | null }>('SELECT MIN(position) lo FROM cheque_books')!.lo)
      changes.position = (lo || 0) - 1
    }
    const sets = [...Object.keys(changes).map((k) => `${k} = ?`), 'updated_at = ?'].join(', ')
    this.connect((con) => {
      con.run(`UPDATE cheque_books SET ${sets} WHERE id = ?`, [...Object.values(changes), now, bookId])
      con.run('INSERT INTO events (cheque_id, kind, detail, at, subject) VALUES (NULL,?,?,?,?)', [
        kind,
        detail,
        now,
        `book:${bookId}`
      ])
    })
  }

  // -- blank leaves -------------------------------------------------------
  listBlankLeaves(): BlankLeafRow[] {
    return this.connect((con) => con.all<BlankLeafRow>('SELECT * FROM blank_leaves ORDER BY id'))
  }

  /** Record leaves used up without a cheque. A number already in the
   *  register, in either table, is refused and nothing is written —
   *  checked inside the same transaction as the inserts. */
  addBlankLeaves(numbers: string[], reason: string): string[] {
    const now = this.utcnow()
    this.connect((con) => {
      const taken = numbers.filter((n) =>
        con.get(
          'SELECT 1 FROM cheques WHERE cheque_no = ? UNION ALL SELECT 1 FROM blank_leaves WHERE cheque_no = ?',
          [n, n]
        )
      )
      if (taken.length) throw new ValueError(`already in the register: ${taken.join(', ')}`)
      for (const n of numbers) {
        con.run('INSERT INTO blank_leaves (cheque_no, reason, created_at) VALUES (?,?,?)', [n, reason, now])
        con.run('INSERT INTO events (cheque_id, kind, detail, at, subject) VALUES (NULL,?,?,?,?)', [
          'leaf-void',
          reason,
          now,
          `leaf:${n}`
        ])
      }
    })
    return numbers
  }

  /** Every leaf number the register knows, in any status. */
  usedNumbers(): Set<string> {
    return this.connect(
      (con) =>
        new Set(
          con
            .all<{ cheque_no: string }>(
              "SELECT cheque_no FROM cheques WHERE cheque_no != '' UNION SELECT cheque_no FROM blank_leaves"
            )
            .map((r) => r.cheque_no)
        )
    )
  }

  // -- reads -----------------------------------------------------------
  getCheque(chequeId: number): ChequeWithEntity | null {
    return this.connect(
      (con) =>
        con.get<ChequeWithEntity>(
          "SELECT c.*, COALESCE(e.entity_name, '') AS entity_name FROM cheques c " +
            'LEFT JOIN entities e ON e.id = c.entity_id WHERE c.id = ?',
          [chequeId]
        ) ?? null
    )
  }

  listCheques(options: ListChequesOptions = {}): ChequeWithEntity[] {
    const { status = '', payee = '', search = '', entity_id = null, limit = 500, offset = 0 } = options
    const where: string[] = []
    const params: unknown[] = []
    if (status) {
      where.push('c.status = ?')
      params.push(status)
    }
    if (payee) {
      where.push('c.payee = ?')
      params.push(payee)
    }
    if (entity_id !== null) {
      where.push('c.entity_id = ?')
      params.push(entity_id)
    }
    if (search) {
      where.push('(c.payee LIKE ? OR e.entity_name LIKE ? OR c.cheque_no LIKE ? OR c.memo LIKE ?)')
      for (let i = 0; i < 4; i++) params.push(`%${search}%`)
    }
    const clause = where.length ? `WHERE ${where.join(' AND ')}` : ''
    const sql =
      "SELECT c.*, COALESCE(e.entity_name, '') AS entity_name " +
      `FROM cheques c LEFT JOIN entities e ON e.id = c.entity_id ${clause} ` +
      'ORDER BY c.cheque_date DESC, c.id DESC LIMIT ? OFFSET ?'
    return this.connect((con) => con.all<ChequeWithEntity>(sql, [...params, limit, offset]))
  }

  /** Every cheque that consumed a leaf, oldest first — the basis of the
   *  frequency analysis. Void and failed prints are excluded because they
   *  represent no payment. */
  issuedCheques(): ChequeRow[] {
    const marks = marksFor(ISSUED)
    return this.connect((con) =>
      con.all<ChequeRow>(`SELECT * FROM cheques WHERE status IN (${marks}) ORDER BY cheque_date ASC, id ASC`, ISSUED)
    )
  }

  eventsFor(chequeId: number): EventRow[] {
    return this.connect((con) =>
      con.all<EventRow>('SELECT * FROM events WHERE cheque_id = ? ORDER BY id ASC', [chequeId])
    )
  }

  /** What the frequency analysis needs to know of each entity, by entity id. */
  entityMeta(): Map<number, EntityMeta> {
    const rows = this.connect((con) =>
      con.all<EntityMeta & { id: number }>('SELECT id, entity_name, category, notes, kind FROM entities')
    )
    return new Map(
      rows.map((r) => [r.id, { category: r.category, notes: r.notes, entity_name: r.entity_name, kind: r.kind }])
    )
  }

  pendingJobs(): ChequeRow[] {
    return this.connect((con) =>
      con.all<ChequeRow>("SELECT * FROM cheques WHERE status = 'queued' AND job_id != ''")
    )
  }

  /** Suggest the next cheque number by incrementing the last one used. */
  nextChequeNo(current = ''): string {
    if (!current) {
      const row = this.connect((con) =>
        con.get<{ cheque_no: string }>("SELECT cheque_no FROM cheques WHERE cheque_no != '' ORDER BY id DESC LIMIT 1")
      )
      current = row ? row.cheque_no : ''
    }
    return incrementNo(current)
  }
}

const DECIMAL_DIGIT = /^\p{Nd}$/u

/** The value of a decimal digit in any script, as Python's int() reads it. Every script's digits are a run
 *  of code points starting at its zero. */
function digitValue(ch: string): number {
  const code = ch.codePointAt(0)!
  if (code >= 0x30 && code <= 0x39) return code - 0x30
  let start = code
  while (DECIMAL_DIGIT.test(String.fromCodePoint(start - 1))) start--
  return (code - start) % 10
}

/** The leaf after this one: 000123 -> 000124, keeping any prefix and the
 *  zero padding. Empty when the number does not end in digits. */
export function incrementNo(current: string): string {
  const m = /^([^\n]*?)(\p{Nd}+)$/u.exec(pyStrip(current))
  if (!m) return ''
  const digits = Array.from(m[2])
  const next = BigInt(digits.map(digitValue).join('')) + 1n
  return m[1] + next.toString().padStart(digits.length, '0')
}
