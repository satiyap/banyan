/**
 * The register against its recorded answers.
 *
 * tests/golden/db.json holds scripted scenarios: every return value, every
 * exception, and the final contents and schema of every file left behind.
 * Here the same scenarios are replayed against Database with the same clock,
 * and everything must come out identical.
 */

import { copyFileSync, mkdtempSync, readdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { fileURLToPath } from 'node:url'

import { afterAll, describe, expect, it } from 'vitest'

import {
  Database,
  DatabaseError,
  IntegrityError,
  KeyError,
  LookupError,
  MigrationError,
  OperationalError,
  ValueError,
  incrementNo,
  type BatchChanges,
  type BatchItem,
  type BookChanges,
  type ChequeChanges,
  type EntityFields,
  type EventOptions,
  type ListChequesOptions,
  type NewBook,
  type NewCheque
} from './db'
import { ValueError as SharedValueError } from './pycompat'

const ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const GOLDEN = join(ROOT, 'tests', 'golden')

interface Op {
  method: string
  args: unknown[]
  kwargs: Record<string, unknown>
  result?: unknown
  error?: { type: string; message: string }
}

interface Dump {
  schema: Array<{ type: string; name: string; tbl_name: string; sql: string | null }>
  user_version: number
  tables: Record<string, { columns: string[]; rows: Array<Record<string, unknown>> }>
}

interface Snapshot {
  files: string[]
  dumps: Record<string, Dump>
}

interface Scenario {
  name: string
  note: string
  setup_sql: string
  start_from: string
  clock_base: string
  clock_calls: number
  ops: Op[]
  final: Snapshot
}

const fixture = JSON.parse(readFileSync(join(GOLDEN, 'db.json'), 'utf8')) as { scenarios: Scenario[] }

const tmp = mkdtempSync(join(tmpdir(), 'banyan-db-test-'))
afterAll(() => rmSync(tmp, { recursive: true, force: true }))

function folderFor(name: string): string {
  return mkdtempSync(join(tmp, `${name}-`))
}

// ------------------------------------------------------ reading the recording
/** Each method as the recording names it (snake_case, with keyword arguments), and how it is called here. */
const CALLS: Record<string, (db: Database, a: unknown[], k: Record<string, unknown>) => unknown> = {
  add_cheque: (db, _a, k) => db.addCheque(k as NewCheque),
  update_cheque: (db, a, k) => db.updateCheque(a[0] as number, k as ChequeChanges),
  settle: (db, a) => db.settle(new Map(a[0] as Array<[number, string]>), a[1] as string | undefined),
  void: (db, a) => db.void(a[0] as number, a[1] as string),
  cheque_no_used: (db, a) => db.chequeNoUsed(a[0] as string),
  create_batch: (db, a) => db.createBatch(a[0] as BatchItem[], a[1] as string, a[2] as string),
  get_batch: (db, a) => db.getBatch(a[0] as number),
  open_batch: (db) => db.openBatch(),
  move_batch: (db, a, k) =>
    db.moveBatch(a[0] as number, a[1] as string | string[], a[2] as string, k as BatchChanges),
  save_batch_items: (db, a) => db.saveBatchItems(a[0] as number, a[1] as BatchItem[], a[2] as string),
  check_entity: (db, a) => db.checkEntity(a[0] as number | null, a[1] as string),
  list_entities: (db) => db.listEntities(),
  get_entity: (db, a) => db.getEntity(a[0] as number),
  create_entity: (db, a, k) => db.createEntity(a[0] as string, k as EntityFields),
  update_entity: (db, a, k) => db.updateEntity(a[0] as number, k as EntityFields),
  move_cheques: (db, a) => db.moveCheques(a[0] as number[], a[1] as number),
  delete_entity: (db, a) => db.deleteEntity(a[0] as number),
  add_event: (db, a, k) => db.addEvent(a[0] as string, a[1] as string | undefined, k as EventOptions),
  events_about: (db, a) => db.eventsAbout(a[0] as string),
  cheque_events: (db, a) => db.chequeEvents(a[0] as string[]),
  list_books: (db) => db.listBooks(),
  get_book: (db, a) => db.getBook(a[0] as number),
  add_book: (db, _a, k) => db.addBook(k as unknown as NewBook),
  update_book: (db, a, k) => db.updateBook(a[0] as number, k as unknown as BookChanges),
  list_blank_leaves: (db) => db.listBlankLeaves(),
  add_blank_leaves: (db, a) => db.addBlankLeaves(a[0] as string[], a[1] as string),
  used_numbers: (db) => db.usedNumbers(),
  get_cheque: (db, a) => db.getCheque(a[0] as number),
  list_cheques: (db, _a, k) => db.listCheques(k as ListChequesOptions),
  issued_cheques: (db) => db.issuedCheques(),
  events_for: (db, a) => db.eventsFor(a[0] as number),
  entity_meta: (db) => db.entityMeta(),
  pending_jobs: (db) => db.pendingJobs(),
  next_cheque_no: (db, a) => db.nextChequeNo(a[0] as string | undefined)
}

/** The TypeScript class that stands for each Python exception. */
const RECORDED_NAME = new Map<unknown, string>([
  [ValueError, 'ValueError'],
  [LookupError, 'LookupError'],
  [KeyError, 'KeyError'],
  [IntegrityError, 'IntegrityError'],
  [OperationalError, 'OperationalError'],
  [MigrationError, 'RuntimeError']
])

/** A return value as the fixture records it: None, a sorted set, a dict with string keys. */
function normalise(value: unknown): unknown {
  if (value === undefined || value === null) return null
  if (value instanceof Set) return [...value].sort()
  if (value instanceof Map) return Object.fromEntries([...value].map(([k, v]) => [String(k), normalise(v)]))
  if (Array.isArray(value)) return value.map(normalise)
  if (typeof value === 'object') {
    return Object.fromEntries(Object.entries(value as Record<string, unknown>).map(([k, v]) => [k, normalise(v)]))
  }
  return value
}

function dump(path: string): Dump {
  const raw = new DatabaseSync(path)
  try {
    const rows = (sql: string): Array<Record<string, unknown>> =>
      raw
        .prepare(sql)
        .all()
        .map((r) => ({ ...r }))
    const schema = rows('SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY name') as Dump['schema']
    const tables: Dump['tables'] = {}
    const names = schema.filter((r) => r.type === 'table').map((r) => r.name)
    for (const name of names.sort()) {
      tables[name] = {
        columns: rows(`PRAGMA table_info("${name}")`).map((r) => r.name as string),
        rows: rows(`SELECT * FROM "${name}" ORDER BY rowid`)
      }
    }
    return { schema, user_version: rows('PRAGMA user_version')[0].user_version as number, tables }
  } finally {
    raw.close()
  }
}

function snapshot(folder: string): Snapshot {
  const files = readdirSync(folder)
    .filter((name) => !/-(wal|shm|journal)$/.test(name))
    .sort()
  return { files, dumps: Object.fromEntries(files.map((name) => [name, dump(join(folder, name))])) }
}

/** The recording's clock: a fixed moment, one second later each time it is read. */
function steppingClock(base: string): { now: () => Date; calls: () => number } {
  const start = Date.parse(base.slice(0, 19) + 'Z') + Number(base.slice(20, 23))
  let calls = 0
  return { now: () => new Date(start + 1000 * calls++), calls: () => calls }
}

function replay(scenario: Scenario): { folder: string; path: string } {
  const folder = folderFor(scenario.name)
  const path = join(folder, 'register.db')
  if (scenario.start_from) copyFileSync(join(GOLDEN, scenario.start_from), path)
  if (scenario.setup_sql) {
    // As Python's sqlite3 opens it: foreign keys are not enforced unless asked for.
    const raw = new DatabaseSync(path, { enableForeignKeyConstraints: false })
    raw.exec(scenario.setup_sql)
    raw.close()
  }
  const clock = steppingClock(scenario.clock_base)
  let db: Database | undefined
  scenario.ops.forEach((op, i) => {
    let outcome: { result: unknown } | { error: { type: string; message: string } }
    try {
      if (op.method === '__open__') {
        db = undefined
        db = new Database(path, { now: clock.now })
        outcome = { result: null }
      } else if (op.method === 'increment_no') {
        outcome = { result: incrementNo(op.args[0] as string) }
      } else if (!db) {
        outcome = { error: { type: 'NotOpen', message: 'the register did not open' } }
      } else {
        outcome = { result: normalise(CALLS[op.method](db, op.args, op.kwargs)) }
      }
    } catch (error) {
      const type = RECORDED_NAME.get((error as Error).constructor)
      if (!type) throw error
      outcome = { error: { type, message: (error as Error).message } }
    }
    const expected = op.error ? { error: op.error } : { result: op.result }
    expect(outcome, `${scenario.name} #${i} ${op.method}(${JSON.stringify([op.args, op.kwargs])})`).toStrictEqual(
      expected
    )
  })
  expect(clock.calls(), 'clock readings').toBe(scenario.clock_calls)
  return { folder, path }
}

// --------------------------------------------------------------- the tests
describe('replaying the recorded scenarios', () => {
  it('has scenarios to replay', () => {
    expect(fixture.scenarios.length).toBeGreaterThanOrEqual(15)
  })

  it.each(fixture.scenarios.map((s) => [s.name, s] as const))('%s', (_name, scenario) => {
    const { folder } = replay(scenario)
    const after = snapshot(folder)
    expect(after.files).toStrictEqual(scenario.final.files)
    for (const file of scenario.final.files) {
      const expected = scenario.final.dumps[file]
      const actual = after.dumps[file]
      expect(actual.schema, `${file} schema`).toStrictEqual(expected.schema)
      expect(actual.user_version, `${file} user_version`).toBe(expected.user_version)
      expect(Object.keys(actual.tables), `${file} tables`).toStrictEqual(Object.keys(expected.tables))
      for (const [table, want] of Object.entries(expected.tables)) {
        expect(actual.tables[table].columns, `${file} ${table} columns`).toStrictEqual(want.columns)
        expect(actual.tables[table].rows, `${file} ${table} rows`).toStrictEqual(want.rows)
      }
    }
  })

  it('covers every public method', () => {
    const internal = new Set([
      'constructor',
      'utcnow',
      'backupPath',
      'backupTo',
      'addColumns',
      'migrate',
      'connect',
      'entityFor',
      'matchEntity',
      'checkNames'
    ])
    const camel = (name: string): string => name.replace(/_([a-z])/g, (_m, c: string) => c.toUpperCase())
    const publicMethods = Object.getOwnPropertyNames(Database.prototype).filter((name) => !internal.has(name))
    expect(Object.keys(CALLS).map(camel).sort()).toStrictEqual(publicMethods.sort())
    const replayed = new Set(fixture.scenarios.flatMap((s) => s.ops.map((op) => camel(op.method))))
    for (const name of publicMethods) expect(replayed, name).toContain(name)
    expect(replayed).toContain('incrementNo')
  })
})

describe('one file, either desk', () => {
  const emptyScenario = fixture.scenarios.find((s) => s.name === 'empty')!

  it('creates the schema of the recorded empty register', () => {
    const path = join(folderFor('fresh'), 'register.db')
    new Database(path)
    const ours = dump(path)
    const theirs = dump(copyGolden('register-empty.db'))
    expect(ours.schema).toStrictEqual(theirs.schema)
    expect(ours.user_version).toBe(theirs.user_version)
    expect(ours.tables).toStrictEqual(theirs.tables)
    expect(ours).toStrictEqual(emptyScenario.final.dumps['register.db'])
    expect(journalMode(path)).toBe('wal')
    expect(journalMode(copyGolden('register-empty.db'))).toBe('wal')
    expect(readdirSync(join(path, '..'))).toStrictEqual(['register.db'])
  })

  it('opens a register written by an earlier desk, leaves it unchanged, and works in it', () => {
    const path = copyGolden('register-sample.db')
    const before = dump(path)
    const db = new Database(path)
    expect(dump(path)).toStrictEqual(before)
    expect(readdirSync(join(path, '..'))).toStrictEqual(['register.db'])

    const cheques = db.listCheques()
    expect(cheques.map((c) => c.cheque_no)).toStrictEqual(['000604', '000605', '000603', '000602', '000601'])
    expect(db.listEntities().map((e) => e.entity_name || e.payee_name)).toContain('Lift AMC')
    expect(db.openBatch()?.items).toHaveLength(2)
    expect(db.usedNumbers()).toStrictEqual(new Set(['000601', '000602', '000603', '000604', '000605', '000606']))

    // Cheque 5 awaits its verdict; cheque 3 failed and was voided.
    expect(db.getCheque(5)?.status).toBe('queued')
    expect(db.settle({ 3: 'printed', 5: 'printed' }, 'looks right')).toStrictEqual([5])
    expect(db.settle({ 5: 'failed' })).toStrictEqual([])
    expect(db.getCheque(5)?.status).toBe('printed')
    expect(db.getCheque(3)?.status).toBe('void')

    const id = db.addCheque({
      cheque_no: db.nextChequeNo(),
      payee: 'Greenleaf Gardening',
      amount_paise: 1250000,
      amount_words: 'Rupees Invented Amount Only',
      cheque_date: '2031-05-01',
      status: 'queued'
    })
    expect(id).toBe(6)
    const added = db.getCheque(id)!
    expect(added.cheque_no).toBe('000606')
    expect(added.entity_id).toBe(1)
    expect(added.entity_name).toBe('Garden upkeep')
    expect(added.created_at).toMatch(/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\+00:00$/)
    expect(db.void(id, 'pulled from the tray')).toBe(true)
    expect(db.void(id, 'again')).toBe(false)
    expect(db.settle({ [id]: 'printed' })).toStrictEqual([])
    expect(db.eventsFor(id).map((e) => e.kind)).toStrictEqual(['created', 'void'])
    expect(() => db.addBlankLeaves(['000606'], 'torn')).toThrow(ValueError)
    expect(dump(path).schema).toStrictEqual(before.schema)
  })

  function copyGolden(name: string): string {
    const path = join(folderFor(name), 'register.db')
    copyFileSync(join(GOLDEN, name), path)
    return path
  }

  function journalMode(path: string): unknown {
    const raw = new DatabaseSync(path)
    try {
      return raw.prepare('PRAGMA journal_mode').get()?.journal_mode
    } finally {
      raw.close()
    }
  }
})

describe('the rules of the register', () => {
  function open(name: string): Database {
    return new Database(join(folderFor(name), 'register.db'))
  }

  function addCheque(db: Database, status: string, extra: Partial<NewCheque> = {}): number {
    return db.addCheque({
      cheque_no: '',
      payee: 'Greenleaf Gardening',
      amount_paise: 1250000,
      amount_words: 'Rupees Invented Amount Only',
      cheque_date: '2031-04-05',
      status,
      ...extra
    })
  }

  it('gives a verdict only to a cheque awaiting one', () => {
    const db = open('verdicts')
    const ids = Object.fromEntries(
      ['queued', 'printed', 'failed', 'void', 'logged'].map((status) => [status, addCheque(db, status)])
    )
    const all = new Map(Object.values(ids).map((id) => [id, 'printed']))
    expect(db.settle(all)).toStrictEqual([ids.queued])
    expect(db.getCheque(ids.failed)?.status).toBe('failed')
    expect(db.getCheque(ids.void)?.status).toBe('void')
    expect(db.getCheque(ids.logged)?.status).toBe('logged')
    expect(db.settle(all)).toStrictEqual([])
    expect(db.eventsFor(ids.failed).map((e) => e.kind)).toStrictEqual(['created'])
  })

  it('lets only one of two desks settle the same cheque', () => {
    const db = open('race')
    const other = new Database(db.path)
    const id = addCheque(db, 'queued')
    expect(db.settle({ [id]: 'printed' })).toStrictEqual([id])
    expect(other.settle({ [id]: 'failed' })).toStrictEqual([])
    expect(other.void(id, 'stopped')).toBe(true)
    expect(db.void(id, 'stopped twice')).toBe(false)
    expect(db.getCheque(id)?.status).toBe('void')
  })

  it('settles in the order given: a Map as built, a plain object by ascending id', () => {
    const db = open('order')
    const [a, b, c] = [addCheque(db, 'queued'), addCheque(db, 'queued'), addCheque(db, 'queued')]
    expect(
      db.settle(
        new Map([
          [c, 'printed'],
          [a, 'failed']
        ])
      )
    ).toStrictEqual([c, a])
    expect(db.settle({ [b]: 'printed' })).toStrictEqual([b])
  })

  it('never deletes a cheque and records every change', () => {
    const db = open('append')
    const id = addCheque(db, 'queued')
    db.updateCheque(id, { job_id: 'Desk_Laser-1', event: 'queued', detail: 'job Desk_Laser-1' })
    db.settle({ [id]: 'failed' }, 'feed jammed')
    db.void(id, 'leaf destroyed')
    expect(db.listCheques().map((c) => [c.id, c.status])).toStrictEqual([[id, 'void']])
    expect(db.eventsFor(id).map((e) => e.kind)).toStrictEqual(['created', 'queued', 'confirmed-failed', 'void'])
    expect(db.deleteEntity(db.getCheque(id)!.entity_id!)).toBe(false)
    expect(Object.getOwnPropertyNames(Database.prototype).filter((n) => /cheque/i.test(n) && /delete|remove/i.test(n)))
      .toStrictEqual([])
  })

  it('rolls a refused write back whole', () => {
    const db = open('rollback')
    expect(() => addCheque(db, 'nonsense', { payee: 'Nobody Plumbing' })).toThrow(IntegrityError)
    expect(db.listEntities()).toStrictEqual([])
    expect(() => db.addBlankLeaves(['000901', '000901'], 'twice')).toThrow(IntegrityError)
    expect(db.listBlankLeaves()).toStrictEqual([])
    expect(db.eventsAbout('leaf:000901')).toStrictEqual([])
  })

  it('returns plain rows with number ids, and money as whole paise', () => {
    const db = open('rows')
    const id = addCheque(db, 'logged', { amount_paise: 123456789012 })
    const row = db.getCheque(id)!
    expect(Object.getPrototypeOf(row)).toBe(Object.prototype)
    expect(typeof row.id).toBe('number')
    expect(row.amount_paise).toBe(123456789012)
    expect(Number.isInteger(row.amount_paise)).toBe(true)
    expect(db.getCheque(999)).toBeNull()
    expect(db.getEntity(999)).toBeNull()
    expect(db.getBook(999)).toBeNull()
    expect(db.getBatch(999)).toBeNull()
    expect(db.openBatch()).toBeNull()
  })

  it('stores whole numbers as integers and booleans as 1 and 0', () => {
    const db = open('binding')
    const entity = db.createEntity('Meridian Lifts', { ac_payee: false, seal_required: true, notes: 7 as never })
    const row = db.getEntity(entity)!
    expect([row.ac_payee, row.seal_required, row.notes]).toStrictEqual([0, 1, '7'])
    const id = addCheque(db, 'logged', { cheque_no: 41 as never })
    expect(db.getCheque(id)?.cheque_no).toBe('41')
  })

  it('stamps time as UTC whole seconds with +00:00, truncated, not rounded', () => {
    const path = join(folderFor('clock'), 'register.db')
    const db = new Database(path, { now: () => new Date('2031-12-31T23:59:59.999Z') })
    db.addEvent('note')
    expect(db.eventsAbout('')[0].at).toBe('2031-12-31T23:59:59+00:00')
  })

  it('names its errors as the recordings do', () => {
    const db = open('errors')
    expect(ValueError).toBe(SharedValueError)
    expect(new KeyError("'payee'")).toBeInstanceOf(LookupError)
    expect(new IntegrityError('x')).toBeInstanceOf(DatabaseError)
    expect(new OperationalError('x')).toBeInstanceOf(DatabaseError)
    expect(() => db.updateEntity(99, { notes: 'x' })).toThrow(LookupError)
    expect(() => db.addCheque({ amount_paise: 1 } as unknown as NewCheque)).toThrow(KeyError)
    expect(() => db.updateCheque(99, { event: 'edited' })).toThrow(IntegrityError)
    expect(() => db.updateCheque(1, { colour: 'blue' })).toThrow(OperationalError)
    db.createEntity('Meridian Lifts')
    expect(() => db.createEntity('Meridian Lifts')).toThrow(ValueError)
  })
})
