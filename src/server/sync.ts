// Keeps the Banyan server's copy of the register level with this desk's.
//
// There is no separate backup. After every change to the register the whole
// file is sent to the server, which numbers each copy it accepts. The desk
// remembers the last number the two agreed on and sends it with the next
// copy, so the server can refuse a desk that has fallen behind another.
//
// Whenever the desk and the server disagree about whose register is the
// newer, nothing is sent and nothing is fetched. The treasurer is asked.
import { createHash, randomBytes } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { cleanApiUrl, cleanUserKey, ConnectionError, readConnection, saveConnection, skipConnection } from './connection'

export interface Revision {
  revision: number
  sha256: string
  bytes: number
  synced_at: string
}

/** The last copy the desk and the server agreed on. */
interface Agreed {
  api_url: string
  revision: number
  sha256: string
  synced_at: string
}

export interface SyncStatus {
  connected: boolean
  /** True for a reports-only app: connected, but with no register of its own to keep level. */
  read_only: boolean
  api_url: string
  /** The last time the server took this desk's register, or the desk took the server's. */
  synced_at: string
  revision: number | null
  /** True while this desk holds changes the server has not accepted. */
  pending: boolean
  /** Why the last attempt failed, in words for the treasurer. Empty when it worked. */
  error: string
  /** Set when the server holds a register this desk did not start from. The treasurer must choose. */
  choice: Revision | null
  /** True when the server says another desk has taken over. This desk can no longer send. */
  retired: boolean
}

export interface SyncPaths {
  dbPath: string
  connectionPath: string
}

/** The newest desk-to-server API this desk understands. See API_VERSION in src/backend/app.ts. */
export const DESK_API_VERSION = 1

/** What a Banyan server says about itself at /api/v1/info. */
export interface ServerInfo {
  name: string
  api: number
  features: Record<string, boolean>
}

const TIMEOUT_MS = 20_000
const RETRY_MS = 60_000
const DEBOUNCE_MS = 400

const sha = (data: Uint8Array): string => createHash('sha256').update(data).digest('hex')

/** A consistent copy of the register as it is right now, taken without disturbing it. */
export function snapshot(dbPath: string): Buffer {
  const copy = `${dbPath}.sync-${randomBytes(4).toString('hex')}`
  const db = new DatabaseSync(dbPath, { readOnly: true })
  try {
    db.exec('PRAGMA busy_timeout = 5000')
    db.prepare('VACUUM INTO ?').run(copy)
    return readFileSync(copy)
  } finally {
    db.close()
    rmSync(copy, { force: true })
  }
}

const restorePath = (dbPath: string): string => `${dbPath}.restore`
const agreedPath = (connectionPath: string): string => join(dirname(connectionPath), 'sync.json')

/**
 * Put a fetched register in place. Runs when the server starts, before the
 * register is opened, because the file cannot be swapped under an open desk.
 *
 * The register being replaced is kept beside it, never deleted.
 */
export function applyPendingRestore(paths: SyncPaths, now: () => Date = () => new Date()): boolean {
  const incoming = restorePath(paths.dbPath)
  const note = `${incoming}.json`
  if (!existsSync(incoming) || !existsSync(note)) return false
  const agreed = JSON.parse(readFileSync(note, 'utf8')) as Agreed
  if (existsSync(paths.dbPath)) {
    const stamp = now().toISOString().replace(/[:.]/g, '-')
    renameSync(paths.dbPath, `${paths.dbPath}.before-restore-${stamp}`)
  }
  for (const sidecar of ['-wal', '-shm', '-journal']) rmSync(paths.dbPath + sidecar, { force: true })
  renameSync(incoming, paths.dbPath)
  writeFileSync(agreedPath(paths.connectionPath), JSON.stringify(agreed, null, 2))
  rmSync(note, { force: true })
  return true
}

export class Sync {
  private error = ''
  private choice: Revision | null = null
  private retired = false
  private pending = false
  private running: Promise<void> | null = null
  private again = false
  private timer: NodeJS.Timeout | undefined

  constructor(
    private readonly paths: SyncPaths,
    private readonly fetcher: typeof fetch = fetch
  ) {}

  /** The connection, whichever kind of key made it. */
  private anyConnection() {
    const c = readConnection(this.paths.connectionPath)
    return c.status === 'connected' ? c : null
  }

  /** The connection, if it is a desk's. A reports-only app holds no register to send. */
  private connection() {
    const c = this.anyConnection()
    return c?.role === 'write' ? c : null
  }

  /** True when this app was connected with a read key: it shows reports and nothing else. */
  readOnly(): boolean {
    return this.anyConnection()?.role === 'read'
  }

  /** Fetch something a reader may see from the Banyan server, as that reader. */
  async read(path: string): Promise<Response> {
    const c = this.anyConnection()
    if (!c) throw new ConnectionError('This app is not connected to the server.')
    return this.call(c.api_url, path, { headers: { authorization: `Bearer ${c.token}` } })
  }

  private agreed(apiUrl: string): Agreed | null {
    try {
      const saved = JSON.parse(readFileSync(agreedPath(this.paths.connectionPath), 'utf8')) as Agreed
      return saved.api_url === apiUrl && Number.isInteger(saved.revision) ? saved : null
    } catch {
      return null
    }
  }

  private agree(apiUrl: string, latest: Revision, localSha: string): void {
    const agreed: Agreed = { api_url: apiUrl, revision: latest.revision, sha256: localSha, synced_at: latest.synced_at }
    mkdirSync(dirname(this.paths.connectionPath), { recursive: true })
    writeFileSync(agreedPath(this.paths.connectionPath), JSON.stringify(agreed, null, 2))
    this.error = ''
    this.choice = null
    this.pending = false
  }

  status(): SyncStatus {
    const c = this.connection()
    const agreed = c ? this.agreed(c.api_url) : null
    const any = this.anyConnection()
    return {
      connected: any !== null,
      read_only: any?.role === 'read',
      api_url: any?.api_url ?? '',
      synced_at: agreed?.synced_at ?? '',
      revision: agreed?.revision ?? null,
      pending: c !== null && this.pending,
      error: c ? this.error : '',
      choice: c ? this.choice : null,
      retired: c !== null && this.retired
    }
  }

  private async call(apiUrl: string, path: string, init: RequestInit = {}): Promise<Response> {
    try {
      return await this.fetcher(`${apiUrl}${path}`, { ...init, signal: AbortSignal.timeout(TIMEOUT_MS) })
    } catch {
      throw new ConnectionError('The Banyan server could not be reached. Check the internet connection.')
    }
  }

  private static async detail(reply: Response): Promise<string> {
    const body = (await reply.json().catch(() => null)) as { detail?: unknown } | null
    return typeof body?.detail === 'string' ? body.detail : `The server answered ${reply.status}.`
  }

  /**
   * Ask an address whether it is a Banyan server this desk can work with.
   * Nothing is sent but the question, so a mistyped address costs no key.
   */
  async discover(apiUrlInput: unknown): Promise<ServerInfo & { api_url: string }> {
    const apiUrl = cleanApiUrl(apiUrlInput)
    const notOne = new ConnectionError('There is no Banyan server at that address. Check the API URL.')
    const reply = await this.call(apiUrl, '/api/v1/info')
    if (!reply.ok) throw notOne
    const info = (await reply.json().catch(() => null)) as { service?: unknown; name?: unknown; api?: unknown; features?: unknown } | null
    if (info?.service !== 'banyan' || typeof info.api !== 'number') throw notOne
    if (info.api > DESK_API_VERSION) {
      throw new ConnectionError('That server is newer than this app. Update Banyan, then connect again.')
    }
    const features = (info.features ?? {}) as Record<string, boolean>
    if (!features.register_sync) throw new ConnectionError('That server does not take a register from a desk.')
    return { api_url: apiUrl, name: typeof info.name === 'string' ? info.name.slice(0, 80) : '', api: info.api, features }
  }

  /** Redeem a key for this desk and remember the token it buys. */
  async connect(apiUrlInput: unknown, keyInput: unknown): Promise<SyncStatus> {
    const key = cleanUserKey(keyInput)
    // Find out what is at the address before a single-use key is sent to it.
    const info = await this.discover(apiUrlInput)
    const apiUrl = info.api_url
    const reply = await this.call(apiUrl, '/api/v1/redeem', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ key })
    })
    if (!reply.ok) throw new ConnectionError(await Sync.detail(reply))
    // The key decides what this app is: the cheque desk, or a reports-only reader.
    const { token, role } = (await reply.json()) as { token: string; role: 'write' | 'read' }
    saveConnection(this.paths.connectionPath, apiUrl, token, info.name, role === 'read' ? 'read' : 'write')
    this.retired = false
    this.error = ''
    this.choice = null
    await this.flush()
    return this.status()
  }

  /**
   * Forget the key. The register on this desk is untouched.
   *
   * The server's address is kept unless `forgetServer` is set, so a new key
   * can be entered without finding the server again.
   */
  disconnect(forgetServer = false): SyncStatus {
    const known = readConnection(this.paths.connectionPath)
    const remember =
      !forgetServer && known.status !== 'unset' && known.api_url
        ? { api_url: known.api_url, server_name: known.server_name ?? '' }
        : undefined
    skipConnection(this.paths.connectionPath, remember)
    this.error = ''
    this.choice = null
    this.retired = false
    this.pending = false
    return this.status()
  }

  /** Note that the register may have changed, and send it shortly. */
  schedule(): void {
    if (!this.connection()) return
    this.pending = true
    clearTimeout(this.timer)
    this.timer = setTimeout(() => void this.flush(), DEBOUNCE_MS)
    this.timer.unref()
  }

  /** Send the register now if the server does not have it, and wait for the answer. */
  async flush(): Promise<SyncStatus> {
    clearTimeout(this.timer)
    if (this.running) {
      // A send is under way with an older copy: go round once more after it.
      this.again = true
      await this.running
    } else {
      this.running = this.push(false).finally(() => (this.running = null))
      await this.running
    }
    if (this.again) {
      this.again = false
      return this.flush()
    }
    if (this.error && !this.choice && !this.retired && this.connection()) {
      this.timer = setTimeout(() => void this.flush(), RETRY_MS)
      this.timer.unref()
    }
    return this.status()
  }

  /** Replace the server's register with this desk's. Only ever after the treasurer chose it. */
  async overwrite(): Promise<SyncStatus> {
    await this.running
    await this.push(true)
    return this.status()
  }

  private async push(force: boolean): Promise<void> {
    const c = this.connection()
    if (!c || !existsSync(this.paths.dbPath)) return
    try {
      const data = snapshot(this.paths.dbPath)
      const digest = sha(data)
      const agreed = this.agreed(c.api_url)
      if (!force && agreed?.sha256 === digest) {
        // Nothing has changed since the server last took it.
        this.pending = false
        this.error = ''
        return
      }
      this.pending = true
      const auth = { authorization: `Bearer ${c.token}` }

      let base = agreed?.revision
      if (force || base === undefined) {
        // No shared starting point yet: see what the server holds first.
        const reply = await this.call(c.api_url, '/api/v1/register/status', { headers: auth })
        if (!reply.ok) return this.refused(reply)
        const { latest } = (await reply.json()) as { latest: Revision | null }
        if (latest && latest.sha256 === digest) return this.agree(c.api_url, latest, digest)
        if (latest && !force) {
          this.choice = latest
          this.error = ''
          return
        }
        base = latest?.revision ?? 0
      }

      const reply = await this.call(c.api_url, '/api/v1/register', {
        method: 'PUT',
        headers: { ...auth, 'content-type': 'application/vnd.sqlite3', 'x-base-revision': String(base) },
        body: new Uint8Array(data)
      })
      if (reply.status === 409) {
        const { latest } = (await reply.json()) as { latest: Revision | null }
        this.choice = latest
        this.error = ''
        return
      }
      if (!reply.ok) return this.refused(reply)
      const { latest } = (await reply.json()) as { latest: Revision }
      this.agree(c.api_url, latest, digest)
    } catch (exc) {
      this.error = exc instanceof ConnectionError ? exc.message : `The register could not be sent: ${(exc as Error).message}`
    }
  }

  private async refused(reply: Response): Promise<void> {
    this.error = await Sync.detail(reply)
    // 401: the server no longer knows this desk. Another desk has taken over.
    if (reply.status === 401) this.retired = true
  }

  /**
   * Fetch the server's register and leave it beside this desk's, to be put in
   * place when the app restarts. Returns once it is safely on disk.
   */
  async fetchForRestore(): Promise<Revision> {
    const c = this.connection()
    if (!c) throw new ConnectionError('This desk is not connected to the server.')
    await this.running
    const reply = await this.call(c.api_url, '/api/v1/register', { headers: { authorization: `Bearer ${c.token}` } })
    if (!reply.ok) throw new ConnectionError(await Sync.detail(reply))
    const data = new Uint8Array(await reply.arrayBuffer())
    const digest = sha(data)
    if (digest !== reply.headers.get('x-sha256')) {
      throw new ConnectionError('The register arrived damaged. Nothing on this desk was changed. Try again.')
    }
    const latest: Revision = {
      revision: Number(reply.headers.get('x-revision')),
      sha256: digest,
      bytes: data.length,
      synced_at: reply.headers.get('x-synced-at') ?? new Date().toISOString()
    }
    const incoming = restorePath(this.paths.dbPath)
    const agreed: Agreed = { api_url: c.api_url, revision: latest.revision, sha256: digest, synced_at: latest.synced_at }
    writeFileSync(incoming, data)
    writeFileSync(`${incoming}.json`, JSON.stringify(agreed, null, 2))
    return latest
  }
}
