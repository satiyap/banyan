// What the Banyan server keeps: login keys, the tokens they were redeemed
// for, and every copy of the register a desk has sent.
//
// A key is single-use. Redeeming it yields a long-lived token and spends the
// key. There are two privileges: `write` (the treasurer's desk) and `read`
// (everyone else, who see reports). Only one desk may write at a time:
// redeeming a write key for a desk retires the desk that held it before.
//
// The register arrives whole. Each accepted copy is a numbered revision, and a
// desk must say which revision it started from, so a desk that has fallen
// behind can never overwrite a newer register.
import { createHash, randomBytes } from 'node:crypto'
import { copyFileSync, existsSync, mkdirSync, readdirSync, renameSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

export type Role = 'write' | 'read'
/** Where a token is used: the desktop app, or a browser reading reports. */
export type Kind = 'desk' | 'web'

export interface Session {
  id: number
  role: Role
  kind: Kind
  label: string
}

export interface Revision {
  revision: number
  sha256: string
  bytes: number
  synced_at: string
}

export class StoreError extends Error {
  constructor(
    message: string,
    readonly status: number
  ) {
    super(message)
  }
}

/** The desk sent a register built on a revision that is no longer the latest. */
export class StaleRegister extends StoreError {
  constructor(readonly latest: Revision | null) {
    super('The server holds a newer register than the one this desk started from. Nothing was replaced.', 409)
  }
}

const SCHEMA = `
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

const KEY_DAYS = 7
const KEEP_REVISIONS = 200
export const MAX_REGISTER_BYTES = 64 * 1024 * 1024
const SQLITE_MAGIC = Buffer.from('SQLite format 3\0', 'latin1')
// No 0/O or 1/I/L: a key is read off a screen and typed.
const KEY_ALPHABET = 'ABCDEFGHJKMNPQRSTUVWXYZ23456789'

const sha = (data: string | Uint8Array): string => createHash('sha256').update(data).digest('hex')

/** A key as it is compared: case and the dashes between groups do not matter. */
const normalKey = (key: string): string => key.toUpperCase().replace(/[^A-Z0-9]/g, '')

function newKey(): string {
  const bytes = randomBytes(25)
  let out = ''
  for (let i = 0; i < 25; i++) {
    out += KEY_ALPHABET[bytes[i] % KEY_ALPHABET.length]
    if (i % 5 === 4 && i < 24) out += '-'
  }
  return out
}

export class Store {
  private readonly db: DatabaseSync
  private readonly registers: string
  /** The latest register, kept under a fixed name for the report pages to read. */
  readonly currentPath: string

  constructor(
    readonly dir: string,
    private readonly now: () => Date = () => new Date()
  ) {
    this.registers = join(dir, 'registers')
    mkdirSync(this.registers, { recursive: true })
    this.currentPath = join(dir, 'current.db')
    this.db = new DatabaseSync(join(dir, 'server.db'))
    this.db.exec('PRAGMA busy_timeout = 5000')
    this.db.exec(SCHEMA)
  }

  close(): void {
    this.db.close()
  }

  private stamp(): string {
    return this.now().toISOString()
  }

  // ------------------------------------------------------------------- keys

  /** A new single-use key. Returned once: only its hash is kept. */
  issueKey(role: Role, label = ''): { key: string; role: Role; label: string; expires_at: string } {
    if (role !== 'write' && role !== 'read') throw new StoreError('role must be "write" or "read"', 400)
    const key = newKey()
    const expires = new Date(this.now().getTime() + KEY_DAYS * 864e5).toISOString()
    this.db
      .prepare('INSERT INTO keys (hash, role, label, created_at, expires_at) VALUES (?, ?, ?, ?, ?)')
      .run(sha(normalKey(key)), role, label, this.stamp(), expires)
    return { key, role, label, expires_at: expires }
  }

  /** Every key and token, without anything that could be used to sign in. */
  listAccess(): { keys: unknown[]; tokens: unknown[] } {
    return {
      keys: this.db.prepare('SELECT id, role, label, created_at, expires_at, redeemed_at FROM keys ORDER BY id').all(),
      tokens: this.db
        .prepare('SELECT id, role, kind, label, created_at, last_seen_at, revoked_at FROM tokens ORDER BY id')
        .all()
    }
  }

  /**
   * Spend a key and return the token it buys.
   *
   * `want` lets the desk ask for a write key specifically: a read key offered
   * to a desk is turned away without being spent.
   */
  redeem(key: string, kind: Kind, want?: Role): { token: string; role: Role; label: string } {
    const refusal = new StoreError('That key is not valid, or it has already been used.', 401)
    const row = this.db.prepare('SELECT * FROM keys WHERE hash = ?').get(sha(normalKey(String(key ?? '')))) as
      | { id: number; role: Role; label: string; expires_at: string; redeemed_at: string | null }
      | undefined
    if (!row || row.redeemed_at) throw refusal
    if (row.expires_at < this.stamp()) throw new StoreError('That key has expired. Ask for a new one.', 401)
    if (want && row.role !== want) {
      throw new StoreError(`That is a ${row.role} key. This needs a ${want} key. It has not been used up.`, 403)
    }
    const token = randomBytes(32).toString('hex')
    const at = this.stamp()
    this.db.exec('BEGIN IMMEDIATE')
    try {
      this.db.prepare('UPDATE keys SET redeemed_at = ? WHERE id = ?').run(at, row.id)
      // One desk writes at a time. The desk that held the pen before is retired.
      if (row.role === 'write' && kind === 'desk') {
        this.db
          .prepare("UPDATE tokens SET revoked_at = ? WHERE role = 'write' AND kind = 'desk' AND revoked_at IS NULL")
          .run(at)
      }
      this.db
        .prepare('INSERT INTO tokens (hash, role, kind, label, created_at) VALUES (?, ?, ?, ?, ?)')
        .run(sha(token), row.role, kind, row.label, at)
      this.db.exec('COMMIT')
    } catch (exc) {
      this.db.exec('ROLLBACK')
      throw exc
    }
    return { token, role: row.role, label: row.label }
  }

  /** Who a token belongs to, or null if it is unknown or retired. */
  session(token: string, kind: Kind): Session | null {
    if (!token) return null
    const row = this.db
      .prepare('SELECT id, role, kind, label FROM tokens WHERE hash = ? AND kind = ? AND revoked_at IS NULL')
      .get(sha(token), kind) as Session | undefined
    if (!row) return null
    this.db.prepare('UPDATE tokens SET last_seen_at = ? WHERE id = ?').run(this.stamp(), row.id)
    return { id: row.id, role: row.role, kind: row.kind, label: row.label }
  }

  revoke(tokenId: number): boolean {
    const done = this.db
      .prepare('UPDATE tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL')
      .run(this.stamp(), tokenId)
    return Number(done.changes) > 0
  }

  /** Retire one token by its value: signing out. */
  signOut(token: string): void {
    this.db.prepare('UPDATE tokens SET revoked_at = ? WHERE hash = ? AND revoked_at IS NULL').run(this.stamp(), sha(token))
  }

  // --------------------------------------------------------------- register

  latest(): Revision | null {
    const row = this.db
      .prepare('SELECT revision, sha256, bytes, synced_at FROM revisions ORDER BY revision DESC LIMIT 1')
      .get() as Revision | undefined
    return row ? { revision: row.revision, sha256: row.sha256, bytes: row.bytes, synced_at: row.synced_at } : null
  }

  private fileOf(revision: number): string {
    return join(this.registers, `register-${String(revision).padStart(8, '0')}.db`)
  }

  /** The latest register's file, or null if no desk has sent one. */
  latestFile(): { path: string; revision: Revision } | null {
    const revision = this.latest()
    if (!revision) return null
    const path = this.fileOf(revision.revision)
    return existsSync(path) ? { path, revision } : null
  }

  /**
   * Accept a register from the writing desk.
   *
   * `base` is the revision the desk last agreed with the server on (0 for
   * none). If the server has moved on since, the copy is refused: accepting it
   * would silently discard cheques another desk recorded.
   */
  putRegister(data: Uint8Array, base: number, tokenId: number): Revision {
    if (data.length > MAX_REGISTER_BYTES) throw new StoreError('That file is too large to be a register.', 413)
    if (data.length < 100 || !Buffer.from(data.subarray(0, 16)).equals(SQLITE_MAGIC)) {
      throw new StoreError('That is not a register file.', 400)
    }
    const latest = this.latest()
    if ((latest?.revision ?? 0) !== base) throw new StaleRegister(latest)
    const digest = sha(data)
    // The same register again: nothing to record.
    if (latest && latest.sha256 === digest) return latest

    const incoming = join(this.registers, `incoming-${randomBytes(6).toString('hex')}.db`)
    writeFileSync(incoming, data)
    try {
      this.inspect(incoming)
      const synced_at = this.stamp()
      const done = this.db
        .prepare('INSERT INTO revisions (sha256, bytes, synced_at, token_id) VALUES (?, ?, ?, ?)')
        .run(digest, data.length, synced_at, tokenId)
      const revision = Number(done.lastInsertRowid)
      renameSync(incoming, this.fileOf(revision))
      this.publish(revision)
      this.prune()
      return { revision, sha256: digest, bytes: data.length, synced_at }
    } finally {
      rmSync(incoming, { force: true })
    }
  }

  /** Refuse anything that is not an intact cheque register. */
  private inspect(path: string): void {
    let db: DatabaseSync | undefined
    try {
      db = new DatabaseSync(path, { readOnly: true })
      const check = db.prepare('PRAGMA integrity_check').get() as { integrity_check?: string } | undefined
      if (check?.integrity_check !== 'ok') throw new Error('damaged')
      const tables = db.prepare("SELECT name FROM sqlite_master WHERE type = 'table'").all() as { name: string }[]
      for (const needed of ['cheques', 'entities']) {
        if (!tables.some((t) => t.name === needed)) throw new Error('not a register')
      }
    } catch {
      throw new StoreError('That file is damaged or is not a cheque register. Nothing was replaced.', 400)
    } finally {
      db?.close()
    }
  }

  /** Put a revision where the report pages read it. Moved into place whole. */
  private publish(revision: number): void {
    const next = `${this.currentPath}.next`
    copyFileSync(this.fileOf(revision), next)
    for (const sidecar of ['-wal', '-shm', '-journal']) rmSync(this.currentPath + sidecar, { force: true })
    renameSync(next, this.currentPath)
  }

  /** Make sure the report pages have the latest register after a restart. */
  republish(): void {
    const latest = this.latestFile()
    if (latest) this.publish(latest.revision.revision)
  }

  /** Keep the most recent copies. The rows stay as the history of what was sent and when. */
  private prune(): void {
    const files = readdirSync(this.registers)
      .filter((name) => /^register-\d+\.db$/.test(name))
      .sort()
    for (const name of files.slice(0, Math.max(0, files.length - KEEP_REVISIONS))) {
      rmSync(join(this.registers, name), { force: true })
    }
    // Uploads abandoned by a crash.
    for (const name of readdirSync(this.registers)) {
      if (name.startsWith('incoming-') && this.now().getTime() - statSync(join(this.registers, name)).mtimeMs > 36e5) {
        rmSync(join(this.registers, name), { force: true })
      }
    }
  }
}
