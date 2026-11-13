/**
 * The HTTP layer against its recorded answers.
 *
 * tests/golden/http.json holds scripted sessions: every request with the
 * response the desk gave. Here each session is replayed against the server, on a fresh temporary register, with the same frozen
 * date, the same stepping clock and the same time zone, and every response
 * must come out the same: status, content type, download name, JSON (values
 * and key order), CSV text.
 *
 * Nothing here can reach a printer: PATH is an empty folder for the whole
 * test process, so lp, lpstat, ippfind and cancel cannot be found, and
 * printing is also switched off in the printing module.
 */

import { createHash } from 'node:crypto'
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, utimesSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { delimiter, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { Hono } from 'hono'
import { afterAll, describe, expect, it, vi } from 'vitest'

import { createApp } from './app'
import { type Config, SESSION_COOKIE } from './config'
import { type BatchItem, type ChequeChanges, Database, type NewCheque } from './db'
import { rendererInfo } from './host'
import * as printing from './printing'
import { cupsAvailable, setPrintingDisabled } from './printing'

const ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))

interface RecordedRequest {
  method: string
  path: string
  version: 'ok' | 'wrong' | 'none'
  body?: unknown
  raw?: string
  content_type?: string | null
  headers?: Record<string, string>
}

interface RecordedResponse {
  status: number
  content_type: string
  content_disposition?: string
  /** Where a redirect points, without the host. */
  location?: string
  json?: unknown
  text?: string
  sha256?: string
  pdf?: true
  page?: true
  empty?: true
}

type Step =
  | { request: RecordedRequest; response: RecordedResponse; ignore?: string[]; note?: string }
  | { setup: 'db'; method: string; args: unknown[]; kwargs: Record<string, unknown>; result: unknown }
  | { setup: 'write_cal'; text: string | null }
  | { setup: 'write_pdf'; name: string }

interface Session {
  name: string
  note: string
  needs_renderer: boolean
  /** A queue name the session pretends is configured; '' for none. Submitting is still impossible. */
  stub_printer: string
  steps: Step[]
}

interface Fixture {
  today: string
  clock_base: string
  tz: string
  cal_name: string
  requests: number
  sessions: Session[]
}

const fixture = JSON.parse(readFileSync(join(ROOT, 'tests', 'golden', 'http.json'), 'utf8')) as Fixture

// ------------------------------------------------------------ safety setup
// Before any request: no print system can be found, and the time zone is the
// one the sessions were recorded in (the register's timestamps are read as local dates).
const WORK = mkdtempSync(join(tmpdir(), 'banyan-http-test-'))
const EMPTY_BIN = join(WORK, 'empty-bin')
mkdirSync(EMPTY_BIN)
process.env.PATH = EMPTY_BIN
process.env.TZ = fixture.tz
delete process.env.CHEQUE_PRINTER
setPrintingDisabled(true)

afterAll(() => rmSync(WORK, { recursive: true, force: true }))

// The Python refuses a leaf with "CUPS is not available on this machine". Here
// the same request is refused one line earlier, by the switch this test turns
// on as a second safeguard, and says so in its own words.
const RECORDED_REFUSAL = 'CUPS is not available on this machine'
const SWITCHED_OFF = 'printing is switched off in this build, so nothing was sent to the printer'

const WRONG_VERSION = 'feedfacecafe'
const FAKE_PDF = '%PDF-1.4\n% an invented placeholder, not a cheque\n%%EOF\n'

function configFor(folder: string, extra: Partial<Config> = {}): Config {
  mkdirSync(folder, { recursive: true })
  mkdirSync(join(folder, 'static'), { recursive: true })
  const calPath = join(folder, fixture.cal_name)
  copyFileSync(join(ROOT, 'cheque-calibration.json'), calPath)
  return {
    dbPath: join(folder, 'register.db'),
    calPath,
    pdfDir: join(folder, 'pdf'),
    staticDir: join(ROOT, 'static'),
    templatesDir: join(ROOT, 'templates'),
    dataStaticDir: join(folder, 'static'),
    connectionPath: join(folder, 'connection.json'),
    chrome: '',
    token: '',
    printDisabled: true,
    port: 0,
    ...extra
  }
}

/** Starts at the recorded moment and steps one second per reading, as the recording's did. */
function steppingClock(): () => Date {
  const base = Date.parse(fixture.clock_base)
  let calls = 0
  return () => new Date(base + 1000 * calls++)
}

async function deskVersionOf(app: Hono): Promise<string> {
  const res = await app.request('/api/health')
  return ((await res.json()) as { desk_version: string }).desk_version
}

/** Fill a recorded value's placeholders in for this run. */
function fill<T>(value: T, subs: Array<[string, string]>): T {
  if (value === undefined) return value
  let text = JSON.stringify(value)
  for (const [from, to] of subs) text = text.replaceAll(from, JSON.stringify(to).slice(1, -1))
  return JSON.parse(text) as T
}

function drop(value: unknown, dotted: string): void {
  const keys = dotted.split('.')
  let at = value as Record<string, unknown> | undefined
  for (const key of keys.slice(0, -1)) at = at?.[key] as Record<string, unknown> | undefined
  if (at) delete at[keys[keys.length - 1]]
}

function setupDb(db: Database, method: string, args: unknown[], kwargs: Record<string, unknown>): unknown {
  switch (method) {
    case 'add_cheque':
      return db.addCheque(kwargs as NewCheque)
    case 'update_cheque':
      return db.updateCheque(args[0] as number, kwargs as ChequeChanges)
    case 'create_batch':
      return db.createBatch(args[0] as BatchItem[], args[1] as string, args[2] as string)
    case 'move_batch':
      return db.moveBatch(args[0] as number, args[1] as string | string[], args[2] as string)
    case 'save_batch_items':
      return db.saveBatchItems(args[0] as number, args[1] as BatchItem[], args[2] as string)
    default:
      throw new Error(`the replay does not know the register call ${method}`)
  }
}

/** Rewrite the calibration file and make sure its modification time moved. */
function writeCal(path: string, text: string): void {
  const before = statSync(path).mtimeMs
  writeFileSync(path, text)
  let bump = 0
  while (statSync(path).mtimeMs === before) {
    bump += 1
    utimesSync(path, before / 1000 + bump, before / 1000 + bump)
  }
}

async function replay(session: Session): Promise<{ replayed: number; failures: string[] }> {
  const folder = join(WORK, 'sessions', session.name)
  const config = configFor(folder)
  const calGood = readFileSync(config.calPath, 'utf8')
  const now = steppingClock()
  const { app, close } = createApp(config, { today: () => fixture.today, now })
  // Shares the app's clock, as the setup calls did when the session was recorded.
  const db = new Database(config.dbPath, { now })
  const version = await deskVersionOf(app)
  const subs: Array<[string, string]> = [
    ['{PDF_DIR}', config.pdfDir],
    ['{DESK_VERSION}', version],
    [RECORDED_REFUSAL, SWITCHED_OFF]
  ]
  const failures: string[] = []
  let replayed = 0

  for (const [index, step] of session.steps.entries()) {
    if ('setup' in step) {
      if (step.setup === 'db') {
        const result = setupDb(db, step.method, fill(step.args, subs), fill(step.kwargs, subs))
        if (JSON.stringify(result ?? null) !== JSON.stringify(step.result)) {
          failures.push(`#${index} setup ${step.method}: returned ${JSON.stringify(result)}, recorded ${JSON.stringify(step.result)}`)
        }
      } else if (step.setup === 'write_cal') {
        writeCal(config.calPath, step.text ?? calGood)
      } else {
        writeFileSync(join(config.pdfDir, step.name), FAKE_PDF)
      }
      continue
    }

    const { request, response: want } = step
    const headers: Record<string, string> = { ...(request.headers ?? {}) }
    if (request.version === 'ok') headers['X-Desk-Version'] = version
    if (request.version === 'wrong') headers['X-Desk-Version'] = WRONG_VERSION
    let body: string | undefined
    if (request.raw !== undefined) body = request.raw
    else if (request.body !== undefined) body = JSON.stringify(fill(request.body, subs))
    if (body !== undefined && request.content_type) headers['content-type'] = request.content_type

    const res = await app.request(request.path, { method: request.method, headers, body })
    replayed += 1
    const label = `#${index} ${request.method} ${request.path}${request.body !== undefined ? ' ' + JSON.stringify(request.body).slice(0, 120) : ''}`
    const fail = (what: string): void => void failures.push(`${label}\n    ${what}`)

    if (res.status !== want.status) {
      fail(`status ${res.status}, recorded ${want.status}: ${(await res.text()).slice(0, 300)}`)
      continue
    }
    const type = res.headers.get('content-type') ?? ''
    if (type !== want.content_type) fail(`content-type ${type}, recorded ${want.content_type}`)
    const disposition = res.headers.get('content-disposition') ?? undefined
    if (disposition !== want.content_disposition) {
      fail(`content-disposition ${disposition}, recorded ${want.content_disposition}`)
    }

    const location = res.headers.get('location')?.replace(/^http:\/\/[^/]+/, '')
    if (location !== want.location) fail(`redirected to ${location}, recorded ${want.location}`)

    if (want.empty) {
      const length = (await res.arrayBuffer()).byteLength
      if (length) fail(`a body of ${length} bytes where the recording has none`)
    } else if (want.json !== undefined) {
      const expected = fill(want.json, subs)
      const got = (await res.json()) as unknown
      for (const key of step.ignore ?? []) {
        drop(expected, key)
        drop(got, key)
      }
      if (want.status === 422 && isJsonInvalid(expected)) {
        // Unparseable JSON: where the parser gave up, and its own words for why, are the engine's.
        const [e] = (expected as { detail: Array<Record<string, unknown>> }).detail
        const [g] = (got as { detail: Array<Record<string, unknown>> }).detail
        if (g?.type !== e.type || g?.msg !== e.msg || (g?.loc as unknown[])?.[0] !== 'body') {
          fail(`422 ${JSON.stringify(g)}, recorded ${JSON.stringify(e)}`)
        }
      } else if (JSON.stringify(got) !== JSON.stringify(expected)) {
        // Same text means same values in the same key order.
        fail(`JSON differs\n    got    ${firstDifference(JSON.stringify(got), JSON.stringify(expected))}`)
      }
    } else if (want.text !== undefined) {
      const got = await res.text()
      const expected = fill(want.text, subs)
      if (got !== expected) fail(`text differs\n    got    ${firstDifference(got, expected)}`)
    } else if (want.sha256 !== undefined) {
      const got = createHash('sha256').update(Buffer.from(await res.arrayBuffer())).digest('hex')
      if (got !== want.sha256) fail('the file served is not the file recorded: if it was changed on purpose, put its new sha256 in tests/golden/http.json')
    } else if (want.pdf) {
      const bytes = Buffer.from(await res.arrayBuffer())
      if (bytes.subarray(0, 4).toString('latin1') !== '%PDF') fail('not a PDF')
    } else if (want.page) {
      if (!(await res.text()).includes('<meta name="desk-version"')) fail('the page is not stamped')
    }
  }
  close()
  return { replayed, failures }
}

function isJsonInvalid(expected: unknown): boolean {
  const detail = (expected as { detail?: Array<{ type?: string }> }).detail
  return Array.isArray(detail) && detail.length === 1 && detail[0].type === 'json_invalid'
}

/** Both strings from a little before where they part, for a readable failure. */
function firstDifference(got: string, expected: string): string {
  let i = 0
  while (i < got.length && i < expected.length && got[i] === expected[i]) i++
  const from = Math.max(0, i - 80)
  return `…${got.slice(from, i + 160)}\n    recorded …${expected.slice(from, i + 160)}`
}

describe('recorded HTTP sessions', () => {
  it('cannot find a print system', () => {
    expect(process.env.PATH).toBe(EMPTY_BIN)
    expect(process.env.PATH?.split(delimiter)).toEqual([EMPTY_BIN])
    expect(cupsAvailable()).toBe(false)
  })

  it('covers at least 250 requests', () => {
    const count = fixture.sessions.flatMap((s) => s.steps).filter((s) => 'request' in s).length
    expect(count).toBe(fixture.requests)
    expect(count).toBeGreaterThanOrEqual(250)
  })

  const renderer = rendererInfo()
  for (const session of fixture.sessions) {
    const requests = session.steps.filter((s) => 'request' in s).length
    const skip = session.needs_renderer && !renderer.ok
    const title = `${session.name}: ${session.note} (${requests} requests)`
    it.skipIf(skip)(
      skip ? `${title} — SKIPPED: rendering a PDF needs an installed Google Chrome (${renderer.path})` : title,
      async () => {
        // The look-up of a queue name was stood in for the same way when the session was recorded.
        const stub = session.stub_printer
          ? vi.spyOn(printing, 'resolvePrinter').mockImplementation(async (preferred = '') => preferred || session.stub_printer)
          : null
        const { replayed, failures } = await replay(session).finally(() => stub?.mockRestore())
        expect(cupsAvailable()).toBe(false)
        expect(failures.join('\n\n'), `${failures.length} of ${replayed} responses differ from the recording`).toBe('')
        expect(replayed).toBe(requests)
      },
      session.needs_renderer ? 180_000 : 60_000
    )
  }
})

describe('the session cookie', () => {
  const token = 'an-invented-session-token'
  const { app } = createApp(configFor(join(WORK, 'token'), { token }))
  const cookie = `${SESSION_COOKIE}=${token}`

  it('refuses the page, its files and the API without it', async () => {
    for (const path of ['/', '/static/base.css', '/api/health', '/api/cheques', '/nope']) {
      const res = await app.request(path)
      expect(res.status, path).toBe(401)
      expect(res.headers.get('content-type')).toBe('application/json')
      expect(await res.json()).toEqual({ detail: 'Not authorised' })
    }
  })

  it('refuses a wrong or empty value, and a write, before anything else is looked at', async () => {
    for (const bad of [`${SESSION_COOKIE}=wrong`, `${SESSION_COOKIE}=`, `other=${token}`, `${SESSION_COOKIE}=${token}x`]) {
      expect((await app.request('/api/health', { headers: { cookie: bad } })).status, bad).toBe(401)
    }
    const res = await app.request('/api/entities', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ payee_name: 'Greenleaf Gardening' })
    })
    expect(res.status).toBe(401) // not the 409 a page without a version would get
    expect(((await (await app.request('/api/entities', { headers: { cookie } })).json()) as { entities: unknown[] }).entities).toEqual([])
  })

  it('lets everything through with it', async () => {
    for (const path of ['/', '/static/base.css', '/api/health', '/api/cheques']) {
      expect((await app.request(path, { headers: { cookie } })).status, path).toBe(200)
    }
    expect((await app.request('/nope', { headers: { cookie: `a=b; ${cookie}; c=d` } })).status).toBe(404)
  })

  it('asks for nothing when no token is configured', async () => {
    const open = createApp(configFor(join(WORK, 'no-token'))).app
    expect((await open.request('/api/health')).status).toBe(200)
    expect((await open.request('/')).status).toBe(200)
  })
})

describe('the page and its files', () => {
  const folder = join(WORK, 'static-files')
  const config = configFor(folder)
  const { app } = createApp(config)

  it('stamps the page with the desk version', async () => {
    const version = await deskVersionOf(app)
    expect(version).toMatch(/^[0-9a-f]{12}$/)
    const res = await app.request('/')
    expect(res.status).toBe(200)
    expect(res.headers.get('content-type')).toBe('text/html; charset=utf-8')
    const html = await res.text()
    expect(html).toContain(`<head>\n<meta name="desk-version" content="${version}">`)
    expect(html.match(/<meta name="desk-version"/g)).toHaveLength(1)

    // Every /static/ address in the page carries the version, and nothing else changed.
    const source = readFileSync(join(ROOT, 'static', 'index.html'), 'utf8')
    const addresses = [...source.matchAll(/(?:src|href)="(\/static\/[^"?]+)"/g)].map((m) => m[1])
    expect(addresses.length).toBeGreaterThan(3)
    for (const address of addresses) expect(html).toContain(`"${address}?v=${version}"`)
    // The one addition the desktop app makes: a script that adds the way into Settings.
    const extras = '<script src="/static/js/desk-extras.js"></script>\n'
    const unstamped = html.replaceAll(`?v=${version}`, '').replace(`\n<meta name="desk-version" content="${version}">`, '')
    expect(unstamped.split(extras)).toHaveLength(2)
    expect(unstamped.replace(extras, '')).toBe(source)

    // The stamped addresses are served.
    const css = await app.request(`${addresses.find((a) => a.endsWith('.css'))}?v=${version}`)
    expect(css.status).toBe(200)
    expect(css.headers.get('content-type')).toBe('text/css; charset=utf-8')
  })

  it('gives the same version for the same files, and a new one when a file changes', async () => {
    const copy = join(folder, 'frontend')
    mkdirSync(join(copy, 'js'), { recursive: true })
    for (const f of ['index.html', 'base.css', 'js/boot.js']) copyFileSync(join(ROOT, 'static', f), join(copy, f))
    const versionOf = (dir: string): Promise<string> =>
      deskVersionOf(createApp(configFor(join(folder, 'v'), { staticDir: dir })).app)
    const before = await versionOf(copy)
    expect(await versionOf(copy)).toBe(before)
    writeFileSync(join(copy, 'js', 'boot.js'), readFileSync(join(copy, 'js', 'boot.js'), 'utf8') + '\n// changed\n')
    expect(await versionOf(copy)).not.toBe(before)
    // The leaf photo is data, not a release: it never changes the version.
    const changed = await versionOf(copy)
    writeFileSync(join(copy, 'leaf-photo.jpg'), 'not a real photograph')
    expect(await versionOf(copy)).toBe(changed)
  })

  it('serves files with their types and lets the browser revalidate', async () => {
    const types: Record<string, string> = {
      '/static/cheque-layout.js': 'text/javascript; charset=utf-8',
      '/static/favicon.svg': 'image/svg+xml',
      '/static/fonts/geist-sans-latin-400-normal.woff2': 'font/woff2',
      '/static/index.html': 'text/html; charset=utf-8'
    }
    for (const [path, type] of Object.entries(types)) {
      const res = await app.request(path)
      expect(res.status, path).toBe(200)
      expect(res.headers.get('content-type'), path).toBe(type)
      const bytes = Buffer.from(await res.arrayBuffer())
      expect(bytes.equals(readFileSync(join(ROOT, path.slice(1))))).toBe(true)
      expect(res.headers.get('content-length')).toBe(String(bytes.length))
    }
    const first = await app.request('/static/base.css')
    const etag = first.headers.get('etag') as string
    expect(etag).toMatch(/^"[0-9a-f]{32}"$/)
    const again = await app.request('/static/base.css', { headers: { 'if-none-match': etag } })
    expect(again.status).toBe(304)
    expect((await again.arrayBuffer()).byteLength).toBe(0)
    const since = await app.request('/static/base.css', {
      headers: { 'if-modified-since': first.headers.get('last-modified') as string }
    })
    expect(since.status).toBe(304)
    expect((await app.request('/static/base.css', { headers: { 'if-none-match': '"something-else"' } })).status).toBe(200)
  })

  it('refuses to climb out of the static folder', async () => {
    writeFileSync(join(folder, 'secret.txt'), 'outside the static folder')
    const outside = [
      '/static/../package.json',
      '/static/..%2Fpackage.json',
      '/static/%2e%2e/package.json',
      '/static/%2e%2e%2fpackage.json',
      '/static/js/../../package.json',
      '/static/js/..%2f..%2fpackage.json',
      '/static//etc/passwd',
      '/static/%2Fetc%2Fpasswd',
      '/static/..',
      '/static/js/%00',
      '/static/..%5cpackage.json'
    ]
    for (const path of outside) {
      // As a browser or the test client sends it: the URL parser has already tidied the dots away.
      const tidied = await app.request(path)
      expect(tidied.status, path).toBe(path === '/static/..' ? 200 : 404) // '/static/..' is simply '/'
      if (tidied.status === 404) expect(await tidied.text(), path).not.toContain('"name"')
      // And exactly as written, the way a hand-made request reaches the server.
      const request = new Request('http://localhost/')
      Object.defineProperty(request, 'url', { value: `http://localhost${path}` })
      const raw = await app.fetch(request)
      expect(raw.status, `${path} (as written)`).toBe(404)
      expect(await raw.text(), path).not.toContain('"name"')
    }
    // The same request shape does reach a file that is inside the folder.
    const inside = new Request('http://localhost/')
    Object.defineProperty(inside, 'url', { value: 'http://localhost/static/js/../base.css' })
    expect((await app.fetch(inside)).status).toBe(200)
    for (const path of ['/static/', '/static/js', '/static/fonts/']) {
      expect((await app.request(path)).status, path).toBe(404)
    }
    expect((await app.request('/static')).status).toBe(307) // to /static/, which is not a file
  })

  it('ships the redacted leaf photo, and prefers the one kept with the data', async () => {
    // The shipped backdrop has the account, the account name and the MICR band
    // blacked out, the anti-copy patch blurred, and half the resolution of the
    // photograph it came from. Its hash is pinned so a different photograph cannot replace
    // it unnoticed: look at any new one before changing this.
    const shipped = readFileSync(join(ROOT, 'static', 'leaf-photo.jpg'))
    expect(createHash('sha256').update(shipped).digest('hex')).toBe(
      '3bb42345fe75719730cf5cc368cffc6064c72adb5fb2b38d52f3e0df6aaaa53f'
    )
    const packaged = await app.request('/static/leaf-photo.jpg')
    expect(packaged.status).toBe(200)
    expect(packaged.headers.get('content-type')).toBe('image/jpeg')
    expect(Buffer.from(await packaged.arrayBuffer()).equals(shipped)).toBe(true)

    // An invented stand-in, not a photograph of anything.
    writeFileSync(join(config.dataStaticDir, 'leaf-photo.jpg'), 'stand-in bytes')
    const found = await app.request('/static/leaf-photo.jpg')
    expect(found.status).toBe(200)
    expect(found.headers.get('content-type')).toBe('image/jpeg')
    expect(await found.text()).toBe('stand-in bytes')
    rmSync(join(config.dataStaticDir, 'leaf-photo.jpg'))
    const again = await app.request('/static/leaf-photo.jpg')
    expect(Buffer.from(await again.arrayBuffer()).equals(shipped)).toBe(true)
  })
})

describe('starting up', () => {
  it('fails at once when the calibration cannot be loaded', () => {
    const config = configFor(join(WORK, 'no-cal'))
    rmSync(config.calPath)
    expect(() => createApp(config)).toThrow(/not found — cannot print without measured geometry/)
    writeFileSync(config.calPath, '{ not json')
    expect(() => createApp(config)).toThrow(/is not valid JSON/)
  })

  it('uses the machine date when none is injected', async () => {
    const { app } = createApp(configFor(join(WORK, 'today')))
    const now = new Date()
    const local = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`
    expect(((await (await app.request('/api/reminders')).json()) as { today: string }).today).toBe(local)
  })
})
