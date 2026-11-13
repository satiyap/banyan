// The Banyan server: the one place outside the treasurer's Mac that holds the
// register.
//
// It does three things.
//   1. Turns single-use keys into tokens (`/api/v1/redeem`, `/login`).
//   2. Takes the register from the writing desk after every change, and hands
//      it back to a desk that needs to restore (`/api/v1/register`).
//   3. Shows members the reports, read-only, in a browser (`/`).
//
// The report pages and their numbers are the desk's own: the same frontend
// files and the same report code, run against the latest register received.
// Nothing here can write a cheque, and nothing here can print.
import { timingSafeEqual, createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { Hono, type Context } from 'hono'
import { deleteCookie, getCookie, setCookie } from 'hono/cookie'
import { createApp as createDesk } from '../server/app'
import type { Config as DeskConfig } from '../server/config'
import { setPrintingDisabled } from '../server/printing'
import { MAX_REGISTER_BYTES, StaleRegister, Store, StoreError, type Role, type Session } from './store'

export interface BackendConfig {
  /** Where keys, tokens and registers are kept. */
  dataDir: string
  /** The desk's frontend files, print template and calibration, shipped with the server. */
  staticDir: string
  templatesDir: string
  calPath: string
  /** Lets its holder issue keys. With none set, the admin routes do not exist. */
  adminToken: string
  /** False only for tests and local runs over plain http. */
  secureCookies: boolean
  /** What this server calls itself to an app that asks, e.g. the association's name. */
  name?: string
}

/**
 * The version of the desk-to-server API. Raised only when a change would
 * break a desk built against the previous number; a desk refuses to connect
 * to a server that speaks a newer one than it knows.
 */
export const API_VERSION = 1

const READER_COOKIE = 'banyan_reader'
const COOKIE_DAYS = 180

/** What a signed-in member may fetch from the desk's own API. Reports, and nothing that writes. */
const READER_API = [/^\/api\/reports$/, /^\/api\/reports\/monthly$/, /^\/api\/reports\/[a-z]+\.csv$/]

const json = (body: unknown, status = 200, headers: Record<string, string> = {}): Response =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json', 'cache-control': 'no-store', ...headers }
  })

const same = (a: string, b: string): boolean => {
  const digest = (text: string): Buffer => createHash('sha256').update(text).digest()
  return timingSafeEqual(digest(a), digest(b))
}

const bearer = (c: Context): string => /^Bearer\s+(.+)$/i.exec(c.req.header('authorization') ?? '')?.[1]?.trim() ?? ''

/** Slows down guessing at keys: a handful of wrong tries per address, then a wait. */
class Attempts {
  private readonly failures = new Map<string, number[]>()
  constructor(
    private readonly limit = 8,
    private readonly windowMs = 10 * 60_000,
    private readonly now: () => number = Date.now
  ) {}
  private recent(who: string): number[] {
    const since = this.now() - this.windowMs
    const kept = (this.failures.get(who) ?? []).filter((at) => at > since)
    if (kept.length) this.failures.set(who, kept)
    else this.failures.delete(who)
    return kept
  }
  blocked(who: string): boolean {
    return this.recent(who).length >= this.limit
  }
  failed(who: string): void {
    this.failures.set(who, [...this.recent(who), this.now()])
  }
}

/** The shortest admin token the server will run with: 32 characters, as `openssl rand -hex 32` gives twice over. */
export const MIN_ADMIN_TOKEN = 32

export function createBackend(config: BackendConfig, options: { now?: () => Date } = {}) {
  if (config.adminToken && config.adminToken.length < MIN_ADMIN_TOKEN) {
    throw new Error(`BANYAN_ADMIN_TOKEN is too short to be safe. Use at least ${MIN_ADMIN_TOKEN} random characters: openssl rand -hex 32`)
  }
  const store = new Store(config.dataDir, options.now)
  store.republish()

  // The desk's own app, pointed at the latest register and reachable only
  // through the short list of report routes below.
  setPrintingDisabled(true)
  const deskConfig: DeskConfig = {
    dbPath: store.currentPath,
    calPath: config.calPath,
    pdfDir: `${config.dataDir}/tmp`,
    staticDir: config.staticDir,
    templatesDir: config.templatesDir,
    dataStaticDir: `${config.dataDir}/no-such-folder`,
    connectionPath: `${config.dataDir}/no-such-folder/connection.json`,
    chrome: '',
    token: '',
    printDisabled: true,
    port: 0
  }
  const desk = createDesk(deskConfig, options.now ? { now: options.now } : {})
  const page = (name: string): Response =>
    new Response(desk.page(name), {
      headers: { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' }
    })

  const attempts = new Attempts()
  const caller = (c: Context): string =>
    c.req.header('cf-connecting-ip') ?? c.req.header('x-forwarded-for')?.split(',')[0].trim() ?? 'direct'

  const app = new Hono()

  app.use('*', async (c, next) => {
    await next()
    c.res.headers.set('x-content-type-options', 'nosniff')
    c.res.headers.set('x-frame-options', 'DENY')
    c.res.headers.set('referrer-policy', 'no-referrer')
  })

  app.onError((exc) => {
    if (exc instanceof StaleRegister) return json({ detail: exc.message, latest: exc.latest }, 409)
    if (exc instanceof StoreError) return json({ detail: exc.message }, exc.status)
    console.error(exc)
    return json({ detail: 'The server could not do that.' }, 500)
  })

  // Cheap, and depends on nothing but this process being up.
  app.get('/healthz', () => json({ ok: true }))

  // Discovery. An app that has been given an address asks here whether it is
  // a Banyan server at all, and what it can do, before spending a key on it.
  // Open to anyone, so it says nothing about the register or who uses it.
  app.get('/api/v1/info', () =>
    json({
      service: 'banyan',
      name: config.name || 'Banyan server',
      api: API_VERSION,
      features: { register_sync: true, restore: true, member_reports: true },
      sign_in: '/login'
    })
  )

  // ----------------------------------------------------------------- keys

  /** Redeem a key, counting wrong tries against the caller. */
  function redeem(c: Context, key: string, kind: 'desk' | 'web', want?: Role) {
    const who = caller(c)
    if (attempts.blocked(who)) throw new StoreError('Too many wrong keys. Wait ten minutes and try again.', 429)
    try {
      return store.redeem(key, kind, want)
    } catch (exc) {
      if (exc instanceof StoreError && exc.status === 401) attempts.failed(who)
      throw exc
    }
  }

  // The desk exchanges its key for a token. It asks for a write key by name,
  // so a read key typed into a desk by mistake is not used up.
  app.post('/api/v1/redeem', async (c) => {
    const sent = (await c.req.json().catch(() => null)) as { key?: unknown; want?: unknown } | null
    const want = sent?.want === 'write' || sent?.want === 'read' ? sent.want : undefined
    const { token, role, label } = redeem(c, typeof sent?.key === 'string' ? sent.key : '', 'desk', want)
    return json({ token, role, label })
  })

  // ------------------------------------------------------------- the desk

  function deskSession(c: Context): Session {
    const session = store.session(bearer(c), 'desk')
    if (!session) throw new StoreError('This desk is not signed in to the server, or has been replaced by another desk.', 401)
    return session
  }
  function writer(c: Context): Session {
    const session = deskSession(c)
    if (session.role !== 'write') throw new StoreError('Only the treasurer’s desk can do that.', 403)
    return session
  }

  app.get('/api/v1/register/status', (c) => {
    const session = deskSession(c)
    return json({ role: session.role, latest: store.latest() })
  })

  app.put('/api/v1/register', async (c) => {
    const session = writer(c)
    const base = Number(c.req.header('x-base-revision') ?? 'NaN')
    if (!Number.isInteger(base) || base < 0) throw new StoreError('X-Base-Revision is missing.', 400)
    if (Number(c.req.header('content-length') ?? 0) > MAX_REGISTER_BYTES) {
      throw new StoreError('That file is too large to be a register.', 413)
    }
    const data = new Uint8Array(await c.req.arrayBuffer())
    return json({ latest: store.putRegister(data, base, session.id) })
  })

  // Reports for an app connected with a key of either kind. The same figures
  // members see in a browser, and the same short list: reports, nothing else.
  app.get('/api/v1/reports/*', (c) => forwardReport(c))
  app.get('/api/v1/reports', (c) => forwardReport(c))
  function forwardReport(c: Context): Response | Promise<Response> {
    deskSession(c)
    const url = new URL(c.req.url)
    url.pathname = url.pathname.replace(/^\/api\/v1\//, '/api/')
    if (!READER_API.some((allowed) => allowed.test(url.pathname))) return json({ detail: 'Not Found' }, 404)
    return desk.app.fetch(new Request(url, { headers: { accept: c.req.header('accept') ?? '*/*' } }))
  }

  // Restoring a desk. The whole register leaves the server only for the writer.
  app.get('/api/v1/register', (c) => {
    writer(c)
    const latest = store.latestFile()
    if (!latest) throw new StoreError('The server has no register yet.', 404)
    return new Response(readFileSync(latest.path), {
      headers: {
        'content-type': 'application/vnd.sqlite3',
        'cache-control': 'no-store',
        'x-revision': String(latest.revision.revision),
        'x-sha256': latest.revision.sha256,
        'x-synced-at': latest.revision.synced_at
      }
    })
  })

  // ---------------------------------------------------------------- admin
  // Issuing keys. There is no admin page yet: these are called with curl.

  const admin = new Hono()
  admin.use('*', async (c, next) => {
    if (!config.adminToken) return json({ detail: 'Not Found' }, 404)
    // Guessing at the admin token is slowed down the same way as guessing at keys.
    const who = `admin:${caller(c)}`
    if (attempts.blocked(who)) return json({ detail: 'Too many wrong tries. Wait ten minutes.' }, 429)
    if (!same(bearer(c), config.adminToken)) {
      attempts.failed(who)
      return json({ detail: 'Not authorised' }, 401)
    }
    await next()
  })
  admin.post('/keys', async (c) => {
    const sent = (await c.req.json().catch(() => null)) as { role?: unknown; label?: unknown } | null
    return json(store.issueKey(sent?.role as Role, typeof sent?.label === 'string' ? sent.label.slice(0, 80) : ''), 201)
  })
  admin.get('/access', () => json(store.listAccess()))
  admin.post('/tokens/:id/revoke', (c) => json({ revoked: store.revoke(Number(c.req.param('id'))) }))
  app.route('/admin', admin)

  // ------------------------------------------------------------- members

  const reader = (c: Context): Session | null => store.session(getCookie(c, READER_COOKIE) ?? '', 'web')

  app.get('/login', (c) => (reader(c) ? c.redirect('/') : page('login.html')))

  app.post('/login', async (c) => {
    const form = await c.req.parseBody().catch(() => ({}) as Record<string, unknown>)
    let token: string
    try {
      token = redeem(c, typeof form.key === 'string' ? form.key : '', 'web').token
    } catch (exc) {
      if (!(exc instanceof StoreError)) throw exc
      const why = exc.status === 429 ? 'wait' : /expired/.test(exc.message) ? 'expired' : 'invalid'
      return c.redirect(`/login?error=${why}`, 303)
    }
    setCookie(c, READER_COOKIE, token, {
      httpOnly: true,
      secure: config.secureCookies,
      sameSite: 'Lax',
      path: '/',
      maxAge: COOKIE_DAYS * 86400
    })
    return c.redirect('/', 303)
  })

  app.post('/logout', (c) => {
    const token = getCookie(c, READER_COOKIE)
    if (token) store.signOut(token)
    deleteCookie(c, READER_COOKIE, { path: '/' })
    return c.redirect('/login', 303)
  })

  app.get('/', (c) => (reader(c) ? page('reader.html') : c.redirect('/login')))

  // When the register the reports are drawn from was last received.
  app.get('/api/reader', (c) => {
    const session = reader(c)
    if (!session) return json({ detail: 'Not signed in' }, 401)
    return json({ role: session.role, latest: store.latest() })
  })

  // Styles, scripts and fonts are the same for everyone and hold no data.
  app.get('/static/*', (c) => {
    if (/leaf-photo/.test(c.req.path)) return json({ detail: 'Not Found' }, 404)
    return desk.app.fetch(c.req.raw)
  })

  app.get('/api/*', (c) => {
    if (!reader(c)) return json({ detail: 'Not signed in', sign_in: true }, 401)
    if (!READER_API.some((allowed) => allowed.test(c.req.path))) return json({ detail: 'Not Found' }, 404)
    return desk.app.fetch(c.req.raw)
  })

  app.all('*', () => json({ detail: 'Not Found' }, 404))

  return {
    app,
    store,
    close(): void {
      desk.close()
      store.close()
    }
  }
}
