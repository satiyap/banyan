// The Banyan server, and a desk syncing with it. Every register here is
// invented: created empty by the desk's own code, with made-up payees.
import { mkdtempSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { beforeEach, expect, test } from 'vitest'
import { Database } from '../server/db'
import { readConnection } from '../server/connection'
import { applyPendingRestore, snapshot, Sync } from '../server/sync'
import { createBackend } from './app'

const ROOT = resolve(__dirname, '..', '..')
const ADMIN = 'test-admin-token-0123456789abcdef0123456789abcdef'
const work = (name: string) => mkdtempSync(join(tmpdir(), `banyan-backend-${name}-`))

let backend: ReturnType<typeof createBackend>
let clock: number

beforeEach(() => {
  clock = Date.parse('2026-11-12T10:00:00Z')
  backend = createBackend(
    {
      dataDir: work('server'),
      staticDir: join(ROOT, 'static'),
      templatesDir: join(ROOT, 'templates'),
      calPath: join(ROOT, 'cheque-calibration.json'),
      adminToken: ADMIN,
      secureCookies: true,
      name: 'Invented Association'
    },
    { now: () => new Date((clock += 1000)) }
  )
})

const call = (path: string, init: RequestInit = {}) => backend.app.request(`https://banyan.test${path}`, init)
const asJson = (body: unknown, headers: Record<string, string> = {}): RequestInit => ({
  method: 'POST',
  headers: { 'content-type': 'application/json', ...headers },
  body: JSON.stringify(body)
})
const bearer = (token: string) => ({ authorization: `Bearer ${token}` })

async function issue(role: 'write' | 'read', label = ''): Promise<string> {
  const reply = await call('/admin/keys', asJson({ role, label }, bearer(ADMIN)))
  expect(reply.status).toBe(201)
  return ((await reply.json()) as { key: string }).key
}

async function deskToken(role: 'write' | 'read' = 'write'): Promise<string> {
  const reply = await call('/api/v1/redeem', asJson({ key: await issue(role) }))
  expect(reply.status).toBe(200)
  return ((await reply.json()) as { token: string }).token
}

/** An invented register with the given payees, as bytes. */
function register(...payees: string[]): Buffer {
  const path = join(work('register'), 'register.db')
  const db = new Database(path)
  for (const payee of payees) db.createEntity(payee)
  return snapshot(path)
}

const put = (token: string, data: Uint8Array, base: number) =>
  call('/api/v1/register', {
    method: 'PUT',
    headers: { ...bearer(token), 'x-base-revision': String(base) },
    body: new Uint8Array(data)
  })

// --------------------------------------------------------------------- keys

test('issuing keys needs the admin token', async () => {
  expect((await call('/admin/keys', asJson({ role: 'read' }))).status).toBe(401)
  expect((await call('/admin/keys', asJson({ role: 'read' }, bearer('wrong')))).status).toBe(401)
  expect((await call('/admin/keys', asJson({ role: 'owner' }, bearer(ADMIN)))).status).toBe(400)
  const listed = (await (await call('/admin/access', { headers: bearer(ADMIN) })).json()) as { keys: object[] }
  expect(listed.keys).toEqual([])
})

test('a key works once, however it is typed', async () => {
  const key = await issue('write', 'treasurer')
  expect(key).toMatch(/^([A-Z2-9]{5}-){4}[A-Z2-9]{5}$/)
  const typed = key.toLowerCase().replaceAll('-', ' ')
  const first = await call('/api/v1/redeem', asJson({ key: typed }))
  expect(await first.json()).toMatchObject({ role: 'write', label: 'treasurer' })
  const second = await call('/api/v1/redeem', asJson({ key }))
  expect(second.status).toBe(401)
})

test('a key expires after a week unused', async () => {
  const key = await issue('read')
  clock += 8 * 864e5
  const reply = await call('/api/v1/redeem', asJson({ key }))
  expect(reply.status).toBe(401)
  expect(((await reply.json()) as { detail: string }).detail).toMatch(/expired/)
})

test('a read key offered to a desk is turned away without being used up', async () => {
  const key = await issue('read')
  const refused = await call('/api/v1/redeem', asJson({ key, want: 'write' }))
  expect(refused.status).toBe(403)
  expect((await call('/api/v1/redeem', asJson({ key }))).status).toBe(200)
})

test('wrong keys are slowed down', async () => {
  for (let i = 0; i < 8; i++) expect((await call('/api/v1/redeem', asJson({ key: `wrong-${i}` }))).status).toBe(401)
  expect((await call('/api/v1/redeem', asJson({ key: await issue('read') }))).status).toBe(429)
})

test('a short admin token is refused, and wrong ones are slowed down', async () => {
  const config = {
    dataDir: work('weak'),
    staticDir: join(ROOT, 'static'),
    templatesDir: join(ROOT, 'templates'),
    calPath: join(ROOT, 'cheque-calibration.json'),
    adminToken: 'short',
    secureCookies: true
  }
  expect(() => createBackend(config)).toThrow(/too short/)

  for (let i = 0; i < 8; i++) expect((await call('/admin/access', { headers: bearer(`guess-${i}`) })).status).toBe(401)
  // Even the right token waits once the address has guessed too often.
  expect((await call('/admin/access', { headers: bearer(ADMIN) })).status).toBe(429)
})

test('the admin routes do not exist without an admin token', async () => {
  const bare = createBackend({
    dataDir: work('bare'),
    staticDir: join(ROOT, 'static'),
    templatesDir: join(ROOT, 'templates'),
    calPath: join(ROOT, 'cheque-calibration.json'),
    adminToken: '',
    secureCookies: true
  })
  const reply = await bare.app.request('https://banyan.test/admin/keys', asJson({ role: 'write' }, bearer('')))
  expect(reply.status).toBe(404)
  bare.close()
})

// ----------------------------------------------------------------- register

test('the register is numbered, and a desk that has fallen behind is refused', async () => {
  const token = await deskToken()
  expect(await (await call('/api/v1/register/status', { headers: bearer(token) })).json()).toEqual({ role: 'write', latest: null })

  const one = register('Invented Electricals')
  const first = (await (await put(token, one, 0)).json()) as { latest: { revision: number } }
  expect(first.latest.revision).toBe(1)

  // The same register again is not a new copy.
  expect(((await (await put(token, one, 1)).json()) as { latest: { revision: number } }).latest.revision).toBe(1)

  const two = register('Invented Electricals', 'Made-up Plumbing')
  expect(((await (await put(token, two, 1)).json()) as { latest: { revision: number } }).latest.revision).toBe(2)

  // Built on revision 1, which is no longer the latest.
  const stale = await put(token, register('Somebody Else'), 1)
  expect(stale.status).toBe(409)
  expect(((await stale.json()) as { latest: { revision: number } }).latest.revision).toBe(2)

  const back = await call('/api/v1/register', { headers: bearer(token) })
  expect(back.headers.get('x-revision')).toBe('2')
  expect(Buffer.from(await back.arrayBuffer()).equals(two)).toBe(true)
})

test('only an intact cheque register is accepted', async () => {
  const token = await deskToken()
  expect((await put(token, Buffer.from('not a database at all'.repeat(10)), 0)).status).toBe(400)
  const damaged = Buffer.from(register('Invented Electricals'))
  damaged.fill(0xff, 100, 4000)
  expect((await put(token, damaged, 0)).status).toBe(400)
  expect((await call('/api/v1/register', { headers: bearer(token) })).status).toBe(404)
})

test('a read token can neither send nor fetch the register', async () => {
  const writer = await deskToken()
  await put(writer, register('Invented Electricals'), 0)
  const reader = await deskToken('read')
  expect((await put(reader, register('Other'), 1)).status).toBe(403)
  expect((await call('/api/v1/register', { headers: bearer(reader) })).status).toBe(403)
  expect((await call('/api/v1/register', {})).status).toBe(401)
})

test('connecting a new desk retires the one before it', async () => {
  const old = await deskToken()
  const taken = await deskToken()
  expect((await call('/api/v1/register/status', { headers: bearer(old) })).status).toBe(401)
  expect((await call('/api/v1/register/status', { headers: bearer(taken) })).status).toBe(200)
})

// ------------------------------------------------------------------ members

async function signIn(key: string): Promise<{ status: number; location: string; cookie: string }> {
  const reply = await call('/login', {
    method: 'POST',
    headers: { 'content-type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({ key }).toString()
  })
  return {
    status: reply.status,
    location: reply.headers.get('location') ?? '',
    cookie: (reply.headers.get('set-cookie') ?? '').split(';')[0]
  }
}

test('members sign in with a key and see reports, and nothing else', async () => {
  const stranger = await call('/')
  expect([stranger.status, stranger.headers.get('location')]).toEqual([302, '/login'])
  expect((await call('/api/reports')).status).toBe(401)
  expect(await (await call('/login')).text()).toContain('Banyan reports')

  expect(await signIn('no-such-key')).toMatchObject({ status: 303, location: '/login?error=invalid', cookie: '' })

  const session = await signIn(await issue('read', 'flat 12'))
  expect(session).toMatchObject({ status: 303, location: '/' })
  expect(session.cookie).toMatch(/^banyan_reader=[0-9a-f]{64}$/)
  const cookie = { cookie: session.cookie }

  const home = await (await call('/', { headers: cookie })).text()
  expect(home).toContain('/static/js/reader-boot.js')
  expect(home).not.toContain('desk.js')
  const version = /name="desk-version" content="([^"]+)"/.exec(home)![1]
  const page = { ...cookie, 'x-desk-version': version }

  expect((await call('/api/reports', { headers: page })).status).toBe(200)
  expect((await call('/api/reports/monthly', { headers: page })).status).toBe(200)
  expect((await call('/api/reports/entities.csv', { headers: cookie })).status).toBe(200)
  expect(await (await call('/api/reader', { headers: cookie })).json()).toEqual({ role: 'read', latest: null })

  // The rest of the desk is not there for a member, whatever the method.
  for (const path of ['/api/health', '/api/cheques', '/api/desk', '/api/entities', '/api/reports/monthly.pdf', '/api/connection', '/setup']) {
    expect((await call(path, { headers: page })).status, path).toBe(404)
  }
  expect((await call('/api/entities', asJson({ payee_name: 'X' }, page))).status).toBe(404)
  expect((await call('/api/v1/register', { headers: bearer(session.cookie.split('=')[1]) })).status).toBe(401)

  await call('/logout', { method: 'POST', headers: cookie })
  expect((await call('/api/reports', { headers: page })).status).toBe(401)
})

test('the reports are drawn from the latest register received', async () => {
  const cookie = { cookie: (await signIn(await issue('read'))).cookie }
  const token = await deskToken()
  const path = join(work('register'), 'register.db')
  const db = new Database(path)
  const entity = db.createEntity('Invented Electricals')
  db.addCheque({
    cheque_no: '000123',
    payee: 'Invented Electricals',
    entity_id: entity,
    amount_paise: 1234500,
    amount_words: 'Twelve Thousand Three Hundred Forty Five Only',
    cheque_date: '2026-10-05',
    memo: '',
    status: 'logged',
    cal_version: 1
  })
  await put(token, snapshot(path), 0)

  const report = (await (await call('/api/reports?fy=2026', { headers: cookie })).json()) as { total_paise: number }
  expect(JSON.stringify(report)).toContain('Invented Electricals')
  expect(await (await call('/api/reader', { headers: cookie })).json()).toMatchObject({ latest: { revision: 1 } })
})

// --------------------------------------------------------------- desk sync

test('anyone may ask what the server is, and learns nothing about the register', async () => {
  const info = await (await call('/api/v1/info')).json()
  expect(info).toEqual({
    service: 'banyan',
    name: 'Invented Association',
    api: 1,
    features: { register_sync: true, restore: true, member_reports: true },
    sign_in: '/login'
  })
})

test('a desk checks the address is a Banyan server before spending a key on it', async () => {
  const a = desk('a')
  expect(await a.sync.discover('https://banyan.test/')).toMatchObject({ name: 'Invented Association', api_url: 'https://banyan.test' })

  // Somewhere that answers, but is not a Banyan server: the key never leaves the desk.
  const key = await issue('write')
  const sent: string[] = []
  const elsewhere = new Sync(a.paths, ((url: string, init?: RequestInit) => {
    sent.push(`${init?.method ?? 'GET'} ${url}`)
    return Promise.resolve(new Response('<html>hello</html>', { status: 200 }))
  }) as typeof fetch)
  await expect(elsewhere.connect('https://example.org', key)).rejects.toThrow(/no Banyan server at that address/)
  expect(sent).toEqual(['GET https://example.org/api/v1/info'])

  // A server that speaks a newer API than this desk knows.
  const newer = new Sync(a.paths, (() =>
    Promise.resolve(Response.json({ service: 'banyan', name: 'X', api: 99, features: { register_sync: true } }))) as typeof fetch)
  await expect(newer.connect('https://example.org', key)).rejects.toThrow(/newer than this app/)

  // The key was never used up, and the name the server gave is remembered.
  await a.sync.connect('https://banyan.test', key)
  expect(readConnection(a.paths.connectionPath)).toMatchObject({ server_name: 'Invented Association' })
})

function desk(name: string) {
  const dir = work(name)
  const paths = { dbPath: join(dir, 'banyan.db'), connectionPath: join(dir, 'connection.json') }
  const db = new Database(paths.dbPath)
  const fetcher = ((url: string, init?: RequestInit) => backend.app.request(url, init)) as typeof fetch
  return { paths, db, sync: new Sync(paths, fetcher) }
}

test('a desk sends its register after connecting and after each change', async () => {
  const a = desk('a')
  expect(a.sync.status()).toMatchObject({ connected: false, synced_at: '' })
  await a.sync.flush() // not connected: nothing happens
  expect(backend.store.latest()).toBeNull()

  const status = await a.sync.connect('https://banyan.test', await issue('write'))
  expect(status).toMatchObject({ connected: true, revision: 1, pending: false, error: '', choice: null })
  // The key is spent; what is kept is the token.
  expect(readConnection(a.paths.connectionPath)).toMatchObject({ status: 'connected', api_url: 'https://banyan.test' })
  expect(readFileSync(a.paths.connectionPath, 'utf8')).not.toContain('user_key')

  await a.sync.flush() // unchanged: no new copy
  expect(backend.store.latest()!.revision).toBe(1)

  a.db.createEntity('Invented Electricals')
  expect((await a.sync.flush()).revision).toBe(2)
})

test('a wrong key connects nothing, and nothing is saved', async () => {
  const a = desk('a')
  await expect(a.sync.connect('https://banyan.test', 'WRONG-WRONG')).rejects.toThrow(/not valid/)
  expect(readConnection(a.paths.connectionPath)).toEqual({ status: 'unset' })
})

test('a read key makes the app reports-only: it never sends its register and cannot fetch one', async () => {
  // The treasurer's desk has sent a register.
  const treasurer = desk('treasurer')
  await treasurer.sync.connect('https://banyan.test', await issue('write'))
  treasurer.db.createEntity('Invented Electricals')
  await treasurer.sync.flush()
  const before = backend.store.latest()!.revision

  const a = desk('reader')
  a.db.createEntity('On The Reader Mac Only')
  const status = await a.sync.connect('https://banyan.test', await issue('read'))
  expect(status).toMatchObject({ connected: true, read_only: true, pending: false, choice: null, revision: null })
  expect(a.sync.readOnly()).toBe(true)
  expect(readConnection(a.paths.connectionPath)).toMatchObject({ role: 'read' })

  // Nothing it does reaches the server's register, and the treasurer is not retired.
  a.sync.schedule()
  await a.sync.flush()
  await a.sync.overwrite()
  expect(backend.store.latest()!.revision).toBe(before)
  await expect(a.sync.fetchForRestore()).rejects.toThrow()
  expect((await treasurer.sync.flush()).retired).toBe(false)

  // It can read the reports, and only the reports.
  expect((await a.sync.read('/api/v1/reports')).status).toBe(200)
  expect((await a.sync.read('/api/v1/reports/monthly')).status).toBe(200)
  expect((await a.sync.read('/api/v1/reports/entities.csv')).status).toBe(200)
  expect((await a.sync.read('/api/v1/reports/monthly.pdf')).status).toBe(404)
  expect((await a.sync.read('/api/v1/register')).status).toBe(403)
  expect((await call('/api/v1/reports')).status).toBe(401)
})

test('resetting the key keeps the address unless told to forget it, and never the token', async () => {
  const a = desk('a')
  await a.sync.connect('https://banyan.test', await issue('write'))
  expect(a.sync.disconnect()).toMatchObject({ connected: false })
  expect(readConnection(a.paths.connectionPath)).toEqual({
    status: 'skipped',
    api_url: 'https://banyan.test',
    server_name: 'Invented Association'
  })
  // A second reset still remembers it; asking to forget does not.
  a.sync.disconnect()
  expect(readConnection(a.paths.connectionPath)).toMatchObject({ api_url: 'https://banyan.test' })
  a.sync.disconnect(true)
  expect(readConnection(a.paths.connectionPath)).toEqual({ status: 'skipped' })
})

test('an unreachable server is reported and the desk keeps its changes pending', async () => {
  const a = desk('a')
  await a.sync.connect('https://banyan.test', await issue('write'))
  const offline = new Sync(a.paths, (() => Promise.reject(new Error('offline'))) as typeof fetch)
  a.db.createEntity('Invented Electricals')
  const status = await offline.flush()
  expect(status).toMatchObject({ pending: true, revision: 1 })
  expect(status.error).toMatch(/could not be reached/)
  // Back online: it catches up.
  expect(await a.sync.flush()).toMatchObject({ pending: false, revision: 2, error: '' })
})

test('handing over: the new desk restores, and the old desk is retired', async () => {
  const a = desk('a')
  await a.sync.connect('https://banyan.test', await issue('write'))
  a.db.createEntity('Invented Electricals')
  await a.sync.flush()

  // A second Mac with a fresh, different register.
  const b = desk('b')
  const status = await b.sync.connect('https://banyan.test', await issue('write'))
  // It does not send and does not fetch: the treasurer is asked.
  expect(status.choice).toMatchObject({ revision: 2 })
  expect(backend.store.latest()!.revision).toBe(2)

  // The old desk can no longer write to the server.
  a.db.createEntity('Written After Handover')
  expect(await a.sync.flush()).toMatchObject({ retired: true, pending: true })
  expect(backend.store.latest()!.revision).toBe(2)

  // The treasurer chooses the server's register. It goes in at the next start.
  await b.sync.fetchForRestore()
  expect(b.db.listEntities()).toEqual([])
  expect(applyPendingRestore(b.paths)).toBe(true)
  expect(applyPendingRestore(b.paths)).toBe(false)
  const restored = new Database(b.paths.dbPath)
  expect(restored.listEntities().map((e) => e.payee_name)).toEqual(['Invented Electricals'])

  const after = await new Sync(b.paths, ((url: string, init?: RequestInit) => backend.app.request(url, init)) as typeof fetch).flush()
  expect(after).toMatchObject({ choice: null, error: '', pending: false })
  restored.createEntity('Made-up Plumbing')
  const next = await new Sync(b.paths, ((url: string, init?: RequestInit) => backend.app.request(url, init)) as typeof fetch).flush()
  expect(next.revision).toBeGreaterThan(2)
  expect(next.choice).toBeNull()
})

test('the treasurer can choose this desk’s register over the server’s', async () => {
  const a = desk('a')
  await a.sync.connect('https://banyan.test', await issue('write'))
  const b = desk('b')
  b.db.createEntity('The Right One')
  expect((await b.sync.connect('https://banyan.test', await issue('write'))).choice).not.toBeNull()
  const status = await b.sync.overwrite()
  expect(status).toMatchObject({ choice: null, pending: false, revision: 2 })
})

// The sign-in page once kept class names its stylesheet had dropped, and went
// out unstyled. A page may only use classes its own stylesheets define.
test.each(['login.html', 'setup.html', 'reader.html'])('%s uses only classes its stylesheets define', (name) => {
  const dir = join(ROOT, 'static')
  const html = readFileSync(join(dir, name), 'utf8')
  const css = [...html.matchAll(/href="\/static\/([\w-]+\.css)"/g)].map((m) => readFileSync(join(dir, m[1]), 'utf8')).join('\n')
  const used = new Set([...html.matchAll(/class="([^"]+)"/g)].flatMap((m) => m[1].split(/\s+/)).filter(Boolean))
  // Hooks for scripts and bare wrappers, with no style of their own.
  const hooks = new Set(['js-skip', 'ico', 'set'])
  const missing = [...used].filter((c) => !hooks.has(c) && !new RegExp(`\\.${c}(?![\\w-])`).test(css))
  expect(missing).toEqual([])
})
