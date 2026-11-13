/**
 * Banyan cheque desk — HTTP API and static frontend.
 *
 * Prints onto genuine bank-issued SBI leaves. Two rules shape the whole design:
 *
 *   1. Nothing reaches the printer that has not been rendered at the measured
 *      geometry and had its page box verified at 1:1.
 *   2. A cheque is not recorded as issued because a job was queued. The print
 *      system accepting a job says nothing about whether toner landed correctly
 *      on a hand-fed leaf, so the register waits for the treasurer to confirm
 *      the physical result.
 *
 * The page in static/ is written against these routes: every path, status
 * code, response key and error message is part of that contract, and the
 * recorded sessions in tests/golden/http.json hold each one in place.
 */

import { createHash, timingSafeEqual } from 'node:crypto'
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, realpathSync, statSync } from 'node:fs'
import { basename, extname, join, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

import { Hono } from 'hono'
import type { Context } from 'hono'
import { getCookie } from 'hono/cookie'
import type { z } from 'zod'

import { AmountError, parseAmount } from './amount'
import { type Calibration, CalibrationError, loadCalibration, PAGE_H_MM, PAGE_W_MM } from './calibration'
import { type Config, SESSION_COOKIE } from './config'
import { ConnectionError, describeConnection, readConnection } from './connection'
import { Sync } from './sync'
import * as dating from './dating'
import { type BatchItem, type BatchRow, type ChequeWithEntity, Database, incrementNo, LookupError } from './db'
import * as frequency from './frequency'
import { rendererInfo } from './host'
import * as leaves from './leaves'
import {
  AccountFor,
  BatchCreate,
  BatchFirstOutcome,
  BatchRestOutcome,
  BatchStep,
  BatchTestPrint,
  BookAlertAnswer,
  BookInput,
  BookUpdate,
  ChequeEditRequest,
  ChequeInput,
  ChequeTestPrint,
  type ComposeResponse,
  composeResponse,
  ConfirmRequest,
  EntityInput,
  type ErrorDetail,
  fields,
  IssueRequest,
  MoveChequesRequest,
  NOT_SENT,
  RemindAnswer,
  TestPrintRequest,
  tidy,
  validate,
  VoidRequest
} from './models'
import * as printing from './printing'
import { addDays, cmpStr, dayNumber, floorDiv, isoDate, parseIsoDate, pyMod, pyRepr, ValueError } from './pycompat'
import { RenderError, renderPdf, type RenderResult, resultAsDict } from './render'
import * as reportPdf from './reportPdf'
import * as reports from './reports'

const VERSION_HEADER = 'X-Desk-Version'

type Json = Record<string, unknown>

/** FastAPI's HTTPException: answered as `{"detail": ...}` with this status. */
class HTTPException extends Error {
  constructor(
    readonly status: number,
    readonly detail: string,
    readonly headers: Record<string, string> = {}
  ) {
    super(detail)
  }
}

/** FastAPI's RequestValidationError: answered as 422 with the list of what was wrong. */
class RequestValidationError extends Error {
  constructor(readonly errors: ErrorDetail[]) {
    super('request validation failed')
  }
}

function json(content: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(content), { status, headers: { 'content-type': 'application/json', ...headers } })
}

// ------------------------------------------------- Python formatting, mirrored
/** `f'{paise // 100}.{paise % 100:02d}'` */
const rupee = (paise: number): string => `${floorDiv(paise, 100)}.${String(pyMod(paise, 100)).padStart(2, '0')}`

/**
 * Python's `{x:.Nf}`. toFixed rounds the number's exact value, as Python does,
 * except on an exact tie, where Python goes to the even digit and toFixed goes
 * up. A binary number sits exactly on a tie only when x * 2^(N+1) is an odd integer.
 */
function fixed(x: number, digits: number): string {
  const n = x * 2 ** (digits + 1)
  if (Number.isInteger(n) && n % 2 !== 0) {
    const lower = Math.floor(x * 10 ** digits)
    return ((lower % 2 === 0 ? lower : lower + 1) / 10 ** digits).toFixed(digits)
  }
  return x.toFixed(digits)
}

const pad = (n: number, width: number): string => String(n).padStart(width, '0')

function localToday(): string {
  const now = new Date()
  return isoDate(now.getFullYear(), now.getMonth() + 1, now.getDate())
}

/** One row of `csv.writer(quoting=csv.QUOTE_ALL, lineterminator='\r\n')`. */
function csvRow(cells: readonly unknown[]): string {
  return (
    cells.map((cell) => `"${(cell === null || cell === undefined ? '' : String(cell)).replaceAll('"', '""')}"`).join(',') +
    '\r\n'
  )
}

/** A cell starting with = + - or @ is executed as a formula by Excel and
 * Numbers. Prefixing with an apostrophe keeps it as text. */
function csvSafe(value: unknown): string {
  const s = value === null || value === undefined ? '' : String(value)
  return ['=', '+', '-', '@'].includes(s.slice(0, 1)) ? "'" + s : s
}

/** urllib's unquote: %XX becomes its byte, anything else is left as written. */
function unquote(text: string): string {
  if (!text.includes('%')) return text
  const bytes: number[] = []
  for (let i = 0; i < text.length; ) {
    const hex = text.slice(i + 1, i + 3)
    if (text[i] === '%' && /^[0-9a-fA-F]{2}$/.test(hex)) {
      bytes.push(parseInt(hex, 16))
      i += 3
    } else {
      const cp = text.codePointAt(i) as number
      const ch = String.fromCodePoint(cp)
      bytes.push(...Buffer.from(ch, 'utf8'))
      i += ch.length
    }
  }
  return Buffer.from(bytes).toString('utf8')
}

// ------------------------------------------------------------------ files
// The media type of each kind of file the desk serves. Anything it cannot
// name goes out as text/plain.
const MEDIA_TYPES: Record<string, string> = {
  '.html': 'text/html',
  '.htm': 'text/html',
  '.css': 'text/css',
  '.js': 'text/javascript',
  '.mjs': 'text/javascript',
  '.json': 'application/json',
  '.txt': 'text/plain',
  '.csv': 'text/csv',
  '.xml': 'text/xml',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.ico': 'image/vnd.microsoft.icon',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.ttf': 'font/ttf',
  '.otf': 'font/otf',
  '.pdf': 'application/pdf',
  '.wasm': 'application/wasm'
}

function isFile(path: string): boolean {
  try {
    return statSync(path).isFile()
  } catch {
    return false
  }
}

/** Starlette's FileResponse: the file, its type, and the validators a browser caches by. */
function fileResponse(
  path: string,
  options: { mediaType?: string; filename?: string; request?: Request } = {}
): Response {
  const stat = statSync(path)
  let type = options.mediaType ?? MEDIA_TYPES[extname(path).toLowerCase()] ?? 'text/plain'
  if (type.startsWith('text/')) type += '; charset=utf-8'
  const headers: Record<string, string> = {
    'content-type': type,
    'content-length': String(stat.size),
    'last-modified': new Date(Math.floor(stat.mtimeMs / 1000) * 1000).toUTCString(),
    etag: `"${createHash('md5').update(`${stat.mtimeMs}-${stat.size}`).digest('hex')}"`
  }
  if (options.filename) headers['content-disposition'] = `attachment; filename="${options.filename}"`

  // A browser that already holds this copy is told so instead of being sent it again.
  const request = options.request
  if (request) {
    const noneMatch = request.headers.get('if-none-match')
    const since = request.headers.get('if-modified-since')
    const unchanged = noneMatch
      ? noneMatch.split(',').some((tag) => tag.trim().replace(/^W\//, '') === headers.etag)
      : since !== null && Date.parse(since) >= Date.parse(headers['last-modified'])
    if (unchanged) {
      const { 'content-type': _type, 'content-length': _length, ...validators } = headers
      return new Response(null, { status: 304, headers: validators })
    }
    if (request.method === 'HEAD') return new Response(null, { status: 200, headers })
  }
  return new Response(readFileSync(path), { status: 200, headers })
}

function filesUnder(dir: string, prefix = ''): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const rel = prefix ? `${prefix}/${entry.name}` : entry.name
    if (entry.isDirectory()) out.push(...filesUnder(join(dir, entry.name), rel))
    else if (entry.isFile()) out.push(rel)
  }
  return out
}

/**
 * A fingerprint of the page and the API it was written against.
 *
 * Computed rather than hand-bumped, so it cannot be forgotten: any release that
 * changes the frontend or the API gets a new one, and a tab still holding the
 * previous page is refused and reloads instead of calling an API it no
 * longer matches — which once turned every entity edit into a duplicate.
 *
 * What is hashed is the frontend files, the print template, and the server's
 * own code: the built server is one file,
 * so hashing the file this code is running from covers all of it.
 */
function deskVersion(config: Config): string {
  const h = createHash('sha256')
  const add = (label: string, path: string): void => {
    h.update(label)
    h.update(readFileSync(path))
  }
  const folder = (name: string, dir: string, suffixes: string[]): void => {
    for (const rel of filesUnder(dir).sort(cmpStr)) {
      if (basename(rel) !== 'leaf-photo.jpg' && suffixes.includes(extname(rel))) add(`${name}/${rel}`, join(dir, rel))
    }
  }
  folder('static', config.staticDir, ['.js', '.css', '.html'])
  folder('templates', config.templatesDir, ['.tpl'])
  try {
    const self = fileURLToPath(import.meta.url)
    if (isFile(self)) add('server', self)
  } catch {
    // Where this code's own file cannot be named, the frontend and template still count.
  }
  return h.digest('hex').slice(0, 12)
}

// ---------------------------------------------------------------- requests
/** What a route reads from its request. Path and query problems are collected,
 * and reported together with the body's. */
class Req {
  private readonly errors: ErrorDetail[] = []

  constructor(
    readonly raw: Request,
    private readonly params: Record<string, string>,
    private readonly search: URLSearchParams
  ) {}

  private take<T>(schema: z.ZodType<T>, input: unknown, loc: (string | number)[]): T {
    const result = validate(schema, input, loc)
    if (result.ok) return result.value
    this.errors.push(...result.errors)
    return undefined as T
  }

  /** A path parameter. Only read the value after check(). */
  path<T>(name: string, schema: z.ZodType<T>): T {
    return this.take(schema, this.params[name], ['path', name])
  }

  /** A query parameter; when sent twice the last one counts. */
  query<T>(name: string, schema: z.ZodType<T>): T {
    const sent = this.search.getAll(name)
    return this.take(schema, sent.length ? sent[sent.length - 1] : NOT_SENT, ['query', name])
  }

  /** The JSON body, validated. Unparseable JSON is reported at once, on its own. */
  async body<T>(schema: z.ZodType<T>): Promise<T> {
    const text = Buffer.from(await this.raw.arrayBuffer()).toString('utf8')
    let input: unknown = null
    if (text) {
      const type = (this.raw.headers.get('content-type') ?? '').split(';')[0].trim().toLowerCase()
      if (/^application\/(?:.+\+)?json$/.test(type)) {
        try {
          input = JSON.parse(text)
        } catch (exc) {
          const message = (exc as Error).message
          const at = /position (\d+)/.exec(message)
          throw new RequestValidationError([
            {
              type: 'json_invalid',
              loc: ['body', at ? Number(at[1]) : text.length],
              msg: 'JSON decode error',
              input: {},
              ctx: { error: message }
            }
          ])
        }
      } else {
        // Not declared as JSON, so it is not read as JSON: the model is handed the text.
        input = text
      }
    }
    if (input === null) {
      this.errors.push({ type: 'missing', loc: ['body'], msg: 'Field required', input: null })
      return undefined as T
    }
    return this.take(schema, input, ['body'])
  }

  /** Refuse the request if anything read so far was wrong. */
  check(): void {
    if (this.errors.length) throw new RequestValidationError(this.errors)
  }
}

type Handler = (rq: Req) => Response | Promise<Response>

interface Route {
  /** Null for the static mount, which answers every method itself. */
  methods: string[] | null
  regex: RegExp
  handler: Handler
}

const id = fields.int()
const optionalInt = fields.int({ default: null, nullable: true })

export function createApp(
  config: Config,
  options: {
    /** The desk's calendar date, 'YYYY-MM-DD'. Only tests replace it. */
    today?: () => string
    /** The register's clock. Only tests replace it. */
    now?: () => Date
    /** Called once a register fetched from the server is on disk, waiting for a restart to be put in place. */
    onRestoreReady?: () => void
  } = {}
): { app: Hono; sync: Sync; version: string; page: (name?: string) => string; close(): void } {
  const db = new Database(config.dbPath, options.now ? { now: options.now } : {})
  const state: { cal: Calibration; calMtime: number | null; calError: string } = {
    cal: loadCalibration(config.calPath), // fail fast: no geometry, no app
    calMtime: null,
    calError: ''
  }
  mkdirSync(config.pdfDir, { recursive: true })

  const DESK_VERSION = deskVersion(config)
  const renderPaths = { templatesDir: config.templatesDir, staticDir: config.staticDir, chrome: config.chrome }

  /** Today's date, wherever a route needs it. */
  const today = (): string => options.today?.() ?? localToday()

  /**
   * Re-read the calibration file when it changes on disk.
   *
   * Measurements are edited by hand between test prints; picking the change up
   * without a restart keeps that loop tight. A broken edit keeps the last good
   * copy and is surfaced through /api/health rather than crashing the app.
   */
  function cal(): Calibration {
    const current = state.cal
    try {
      if (statSync(config.calPath).mtimeMs !== state.calMtime) {
        state.cal = loadCalibration(config.calPath)
        state.calMtime = statSync(config.calPath).mtimeMs
        state.calError = ''
      }
    } catch (exc) {
      const code = (exc as NodeJS.ErrnoException).code
      if (exc instanceof CalibrationError) state.calError = exc.message
      else if (code === 'ENOENT') state.calError = `[Errno 2] No such file or directory: ${pyRepr(config.calPath)}`
      else if (typeof code === 'string') state.calError = (exc as Error).message
      else throw exc
      return current
    }
    return state.cal
  }

  // ---------------------------------------------------------------- helpers
  function rowToDict<T extends { amount_paise: number }>(row: T): T & { amount_plain: string } {
    return { ...row, amount_plain: rupee(row.amount_paise) }
  }

  /** The shape the renderer and the browser preview both consume. */
  function chequePayload(data: ChequeInput, amountWords: string, amountFigures: string, testPattern = false): Json {
    return {
      payee: data.payee,
      cheque_date: data.cheque_date,
      amount_words: amountWords,
      amount_figures: amountFigures,
      crossing: data.crossing,
      strike_order: data.strike_order,
      star_fill: data.star_fill,
      test_pattern: testPattern
    }
  }

  function compose(data: ChequeInput): ComposeResponse {
    let amount
    try {
      amount = parseAmount(data.amount)
    } catch (exc) {
      if (exc instanceof AmountError) return composeResponse({ ok: false, error: exc.message })
      throw exc
    }

    const warnings = [...cal().warnings]
    if (data.cheque_date < today()) warnings.push('This date is in the past.')
    if (dayNumber(parseIsoDate(data.cheque_date)) - dayNumber(parseIsoDate(today())) > 90) {
      warnings.push(
        'This date is more than three months ahead; banks treat ' +
          'a cheque as stale after three months from its date.'
      )
    }

    const [year, month, day] = parseIsoDate(data.cheque_date)
    return composeResponse({
      ok: true,
      amount_paise: amount.paiseTotal,
      amount_plain: amount.plain,
      amount_words: amount.words,
      amount_figures: amount.figures,
      date_digits: `${pad(day, 2)}${pad(month, 2)}${pad(year, 4)}`,
      warnings,
      cheque: chequePayload(data, amount.words, amount.figures)
    })
  }

  async function renderFor(payload: Json, name: string): Promise<RenderResult> {
    try {
      return await renderPdf(cal(), payload, join(config.pdfDir, name), renderPaths)
    } catch (exc) {
      if (exc instanceof RenderError) throw new HTTPException(500, exc.message)
      throw exc
    }
  }

  // ----------------------------------------------------------------- routes
  const routes: Route[] = []

  /** Declare a route. Paths are written as FastAPI wrote them: `{name}` is one path segment. */
  function route(methods: string[] | null, path: string, handler: Handler): void {
    const pattern = path
      .split(/(\{\w+\})/)
      .map((part) =>
        part.startsWith('{') ? `(?<${part.slice(1, -1)}>[^/]+)` : part.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
      )
      .join('')
    routes.push({ methods, regex: new RegExp(`^${pattern}$`), handler })
  }
  const get = (path: string, handler: Handler): void => route(['GET'], path, handler)
  const post = (path: string, handler: Handler): void => route(['POST'], path, handler)
  const put = (path: string, handler: Handler): void => route(['PUT'], path, handler)
  const patch = (path: string, handler: Handler): void => route(['PATCH'], path, handler)
  const del = (path: string, handler: Handler): void => route(['DELETE'], path, handler)

  get('/api/health', async () => {
    const c = cal()
    const renderer = rendererInfo(config.chrome)

    const printers = await printing.listPrinters(true)
    // The queue the next cheque would go to. A queue existing is not enough:
    // the print system calls a switched-off printer "idle".
    const target = printers.find((p) => p.is_default) ?? printers[0] ?? null
    const printerOk = target !== null && target.online !== false
    return json({
      ok: renderer.ok && printerOk && !state.calError,
      calibration: { version: c.version, source: basename(c.source), warnings: c.warnings, error: state.calError },
      renderer,
      printing: { cups: printing.cupsAvailable(), printers },
      page_mm: { w: PAGE_W_MM, h: PAGE_H_MM },
      db: config.dbPath,
      desk_version: DESK_VERSION
    })
  })

  get('/api/calibration', () => json(cal().asDict()))

  get('/api/printers', async () => json({ printers: await printing.listPrinters(true) }))

  get('/api/next-cheque-no', (rq) => {
    const after = rq.query('after', fields.str({ default: '' }))
    rq.check()
    return json({ cheque_no: db.nextChequeNo(after) })
  })

  /** Validate and expand an amount without touching the register or printer. */
  post('/api/compose', async (rq) => {
    const data = await rq.body(ChequeInput)
    rq.check()
    return json(compose(data))
  })

  get('/api/cheques', (rq) => {
    const status = rq.query('status', fields.str({ default: '' }))
    const payee = rq.query('payee', fields.str({ default: '' }))
    const search = rq.query('search', fields.str({ default: '' }))
    const entityId = rq.query('entity_id', optionalInt)
    const limit = rq.query('limit', fields.int({ default: 500, le: 2000 }))
    const offset = rq.query('offset', fields.int({ default: 0 }))
    rq.check()
    const rows = db.listCheques({ status, payee, search, entity_id: entityId, limit, offset })
    return json({ cheques: rows.map(rowToDict) })
  })

  get('/api/cheques/{cheque_id}', async (rq) => {
    const chequeId = rq.path('cheque_id', id)
    rq.check()
    const row = db.getCheque(chequeId)
    if (!row) throw new HTTPException(404, 'no such cheque')
    const out: Json = { ...rowToDict(row) }
    out.events = db.eventsFor(chequeId)
    if (row.status === 'queued' && row.job_id) out.job_state = await printing.jobState(row.job_id)
    return json(out)
  })

  /** Write a cheque: render it, send it to the printer, and record it as
   * awaiting confirmation. Recording as 'printed' happens only when the
   * treasurer confirms the physical result. */
  post('/api/cheques', async (rq) => {
    const req = await rq.body(IssueRequest)
    rq.check()
    const composed = compose(req)
    if (!composed.ok) throw new HTTPException(400, composed.error)

    if (req.action === 'log') {
      let chequeId: number
      try {
        chequeId = db.addCheque({
          cheque_no: req.cheque_no,
          payee: req.payee,
          entity_id: req.entity_id,
          one_off: req.one_off,
          amount_paise: composed.amount_paise,
          amount_words: composed.amount_words,
          cheque_date: req.cheque_date,
          memo: req.memo,
          status: 'logged',
          cal_version: cal().version
        })
      } catch (exc) {
        if (exc instanceof ValueError) throw new HTTPException(409, exc.message)
        throw exc
      }
      return json(
        { id: chequeId, status: 'logged', message: 'Recorded as handwritten. Nothing was sent to the printer.' },
        201
      )
    }

    if (!req.confirm) throw new HTTPException(400, 'printing onto a cheque leaf needs an explicit confirmation')

    let printer = ''
    try {
      printer = await printing.resolvePrinter(req.printer)
    } catch (exc) {
      if (exc instanceof printing.PrintError) throw new HTTPException(400, exc.message)
      throw exc
    }

    const payload = chequePayload(req, composed.amount_words, composed.amount_figures)
    // Recorded before rendering, so an ambiguous payee stops here — before
    // anything reaches the printer.
    let chequeId: number
    try {
      chequeId = db.addCheque({
        cheque_no: req.cheque_no,
        payee: req.payee,
        entity_id: req.entity_id,
        one_off: req.one_off,
        amount_paise: composed.amount_paise,
        amount_words: composed.amount_words,
        cheque_date: req.cheque_date,
        memo: req.memo,
        status: 'queued',
        printer,
        cal_version: cal().version
      })
    } catch (exc) {
      if (exc instanceof ValueError) throw new HTTPException(409, exc.message)
      throw exc
    }

    let result: RenderResult
    try {
      result = await renderPdf(cal(), payload, join(config.pdfDir, `cheque-${pad(chequeId, 6)}.pdf`), renderPaths)
    } catch (exc) {
      if (!(exc instanceof RenderError)) throw exc
      // Nothing reached the printer, so the leaf is unused — but the row must
      // not stay 'queued', where it would count as issued and could later be
      // confirmed as printed.
      db.updateCheque(chequeId, { status: 'failed', event: 'render-failed', detail: exc.message })
      throw new HTTPException(500, exc.message)
    }
    db.updateCheque(chequeId, {
      pdf_path: result.pdf_path,
      event: 'rendered',
      detail: `${fixed(result.page_w_mm, 1)}x${fixed(result.page_h_mm, 1)}mm`
    })
    const jobId = await submit(chequeId, result.pdf_path, printer)
    return json(
      {
        id: chequeId,
        status: 'queued',
        job_id: jobId,
        printer,
        pdf: `/api/cheques/${chequeId}/pdf`,
        page: resultAsDict(result),
        message: 'Sent to the printer. Confirm the leaf once it comes out.'
      },
      201
    )
  })

  /** Hand one rendered leaf to the print system and record the job, or record why not. */
  async function submit(chequeId: number, pdf: string, printer: string, chequeNo = ''): Promise<string> {
    let jobId: string
    try {
      jobId = await printing.submit(pdf, printer, PAGE_W_MM, PAGE_H_MM, `cheque ${chequeId}`)
    } catch (exc) {
      if (!(exc instanceof printing.PrintError)) throw exc
      db.updateCheque(chequeId, { status: 'failed', event: 'print-failed', detail: exc.message })
      throw new HTTPException(502, `could not send to the printer: ${exc.message}`)
    }
    const changes = chequeNo ? { cheque_no: chequeNo } : {}
    db.updateCheque(chequeId, {
      job_id: jobId,
      event: 'queued',
      detail: chequeNo ? `${jobId} on leaf ${chequeNo}` : jobId,
      ...changes
    })
    return jobId
  }

  /** A misprint's job may still be held by a sleeping printer; left alone it
   * would print onto whatever leaf is loaded next. */
  async function cancelQuietly(jobId: string): Promise<void> {
    if (jobId) {
      try {
        await printing.cancel(jobId)
      } catch {
        // best effort — the verdict stands either way
      }
    }
  }

  post('/api/cheques/{cheque_id}/confirm', async (rq) => {
    const chequeId = rq.path('cheque_id', id)
    const req = await rq.body(ConfirmRequest)
    rq.check()
    const row = db.getCheque(chequeId)
    if (!row) throw new HTTPException(404, 'no such cheque')
    if (row.batch_id !== null) {
      throw new HTTPException(409, 'this cheque is part of a batch — confirm it from the batch on the Write page')
    }
    // Only a cheque still awaiting its verdict can be confirmed. Letting a
    // misprint be flipped to 'printed' would quietly turn a wasted leaf back
    // into a payment in the register and in the frequency analysis; correcting
    // a mistake there is a void, which leaves a trail.
    if (!db.settle(new Map([[chequeId, req.outcome]]), req.detail).length) {
      const current = (db.getCheque(chequeId) as ChequeWithEntity).status
      throw new HTTPException(409, `this cheque is already recorded as ${current}`)
    }
    if (req.outcome === 'failed') await cancelQuietly(row.job_id)
    return json({ id: chequeId, status: req.outcome })
  })

  /**
   * Correct the cheque number or the note on a register entry.
   *
   * Both are bookkeeping, not the payment itself, so they can be fixed after the
   * leaf has gone out. Every change writes an event recording what it was
   * before — a register that can be edited without a trail is not a record.
   */
  patch('/api/cheques/{cheque_id}', async (rq) => {
    const chequeId = rq.path('cheque_id', id)
    const req = await rq.body(ChequeEditRequest)
    rq.check()
    const row = db.getCheque(chequeId)
    if (!row) throw new HTTPException(404, 'no such cheque')

    const changes: Record<string, string | number> = {}
    const detail: string[] = []
    if (req.cheque_no !== null && req.cheque_no !== row.cheque_no) {
      changes.cheque_no = req.cheque_no
      detail.push(`no: ${row.cheque_no || '—'} → ${req.cheque_no || '—'}`)
    }
    if (req.memo !== null && req.memo !== row.memo) {
      changes.memo = req.memo
      detail.push(`note: ${row.memo || '—'} → ${req.memo || '—'}`)
    }
    if (req.entity_id !== null && req.entity_id !== row.entity_id) {
      const target = db.getEntity(req.entity_id)
      if (!target) throw new HTTPException(404, 'no such entity')
      if (target.kind !== 'per_cheque' && target.payee_name !== row.payee) {
        throw new HTTPException(
          409,
          `that entity prints as ${pyRepr(target.payee_name)}, but this leaf ` +
            `says ${pyRepr(row.payee)} — a cheque can only move between ` +
            'entities that print the same name, or to one whose payee ' +
            'changes per cheque'
        )
      }
      changes.entity_id = req.entity_id
      detail.push(`entity: ${row.entity_name || '—'} → ${target.entity_name || '—'}`)
    }

    if (!Object.keys(changes).length) return json({ id: chequeId, changed: false })
    db.updateCheque(chequeId, { event: 'edited', detail: detail.join('; '), ...changes })
    return json({ id: chequeId, changed: true, detail: detail.join('; ') })
  })

  /**
   * Count several register entries against one entity, all or none.
   *
   * For tidying the register — say, folding GST cheques that each became their
   * own entity into one payee-per-cheque entity. Only which entity a cheque is
   * counted against changes; each move writes an event.
   */
  post('/api/cheques/move', async (rq) => {
    const req = await rq.body(MoveChequesRequest)
    rq.check()
    let moved: number[]
    try {
      moved = db.moveCheques(req.cheque_ids, req.entity_id)
    } catch (exc) {
      if (exc instanceof LookupError) throw new HTTPException(404, exc.message)
      if (exc instanceof ValueError) throw new HTTPException(409, exc.message)
      throw exc
    }
    return json({ moved })
  })

  post('/api/cheques/{cheque_id}/void', async (rq) => {
    const chequeId = rq.path('cheque_id', id)
    const req = await rq.body(VoidRequest)
    rq.check()
    const row = db.getCheque(chequeId)
    if (!row) throw new HTTPException(404, 'no such cheque')
    if (!db.void(chequeId, req.reason)) throw new HTTPException(409, 'this cheque is already void')
    if (row.status === 'queued') await cancelQuietly(row.job_id)
    return json({ id: chequeId, status: 'void' })
  })

  get('/api/cheques/{cheque_id}/pdf', (rq) => {
    const chequeId = rq.path('cheque_id', id)
    rq.check()
    const row = db.getCheque(chequeId)
    if (!row) throw new HTTPException(404, 'no such cheque')
    if (!row.pdf_path || !isFile(row.pdf_path)) throw new HTTPException(404, 'no PDF was kept for this cheque')
    return fileResponse(row.pdf_path, { mediaType: 'application/pdf', filename: `cheque-${pad(chequeId, 6)}.pdf` })
  })

  /**
   * Render the diagnostic sheet, and print it only when asked to.
   *
   * This is the sheet that gets held against a real leaf; it must never be run
   * on the leaf itself.
   */
  post('/api/test-print', async (rq) => {
    const req = await rq.body(TestPrintRequest)
    rq.check()
    const payload = {
      payee: '',
      cheque_date: '',
      amount_words: '',
      amount_figures: '',
      crossing: false,
      strike_order: false,
      star_fill: false,
      test_pattern: req.pattern === 'test'
    }
    const result = await renderFor(payload, `${req.pattern}-sheet.pdf`)
    const out: Json = { page: resultAsDict(result), printed: false, pdf: '/api/test-sheet.pdf' }
    if (req.confirm) {
      const printer = await printing.resolvePrinter(req.printer)
      out.job_id = await printing.submit(result.pdf_path, printer, PAGE_W_MM, PAGE_H_MM, 'cheque test sheet')
      out.printer = printer
      out.printed = true
    }
    return json(out)
  })

  get('/api/test-sheet.pdf', () => {
    const path = join(config.pdfDir, 'test-sheet.pdf')
    if (!isFile(path)) throw new HTTPException(404, 'render a test sheet first')
    return fileResponse(path, { mediaType: 'application/pdf', filename: 'cheque-test-sheet.pdf' })
  })

  // ---------------------------------------------------------------- batches
  // A batch prints its first cheque alone and waits for the treasurer to say it
  // came out right before sending the rest, so a bad feed or a calibration slip
  // costs one leaf rather than the whole stack. Each step claims the batch's
  // stage before it touches the printer: a second click on the same button finds
  // the stage already moved on and sends nothing.

  const BATCH_FIELDS = [
    'payee', 'entity_id', 'amount', 'cheque_date', 'memo', 'crossing', 'strike_order', 'star_fill', 'one_off'
  ] as const

  /** `item[key]`: a stored batch item without a key the code relies on is a fault, not a default. */
  function need(item: BatchItem, key: string): unknown {
    if (!(key in item)) throw new Error(`KeyError: '${key}'`)
    return item[key]
  }

  function itemInput(item: BatchItem): ChequeInput {
    const given: Json = {}
    for (const k of BATCH_FIELDS) if (k in item) given[k] = item[k]
    const result = ChequeInput.safeParse(given)
    if (!result.success) throw new Error('a stored batch item is not a valid cheque')
    return result.data
  }

  /** The batch as the Write page shows it: every cheque with its words, the
   * leaf it is or will be printed on, and what the next step asks for. */
  function batchView(batch: BatchRow): Json {
    const items: Json[] = []
    let number = batch.next_no
    batch.items.forEach((item, i) => {
      const composed = compose(itemInput(item))
      const row = item.cheque_id ? db.getCheque(item.cheque_id as number) : null
      let leaf: string, status: string
      if (row) {
        leaf = row.cheque_no
        status = row.status
      } else {
        leaf = number
        status = 'draft'
        number = incrementNo(number)
      }
      const shown: Json = { index: i }
      for (const k of BATCH_FIELDS) shown[k] = item[k] ?? null
      items.push({
        ...shown,
        amount_paise: composed.amount_paise,
        amount_words: composed.amount_words,
        amount_figures: composed.amount_figures,
        warnings: composed.warnings,
        cheque_no: leaf,
        status,
        cheque_id: item.cheque_id ?? null,
        entity_name: row
          ? row.entity_name
          : entityName(need(item, 'entity_id') as number | null, need(item, 'payee') as string)
      })
    })
    const drafts = items.filter((it) => it.status === 'draft')
    return {
      id: batch.id,
      status: batch.status,
      printer: batch.printer,
      items,
      count: items.length,
      total_paise: items.reduce((sum, it) => sum + (it.amount_paise as number), 0),
      to_print: drafts.length,
      first_leaf: drafts.length ? drafts[0].cheque_no : '',
      last_leaf: drafts.length ? drafts[drafts.length - 1].cheque_no : ''
    }
  }

  /** What the desk calls the entity a draft cheque is for. */
  function entityName(entityId: number | null, payee: string): string {
    if (entityId !== null) {
      const e = db.getEntity(entityId)
      return e ? e.entity_name : ''
    }
    const matches = db.listEntities().filter((e) => e.payee_name === payee)
    return matches.length === 1 ? matches[0].entity_name : ''
  }

  function batchOr404(batchId: number): BatchRow {
    const batch = db.getBatch(batchId)
    if (!batch) throw new HTTPException(404, 'no such batch')
    return batch
  }

  /** Cheques that still need a leaf: never printed, or misprinted. They go
   * back to the Write page as drafts for a new batch. */
  function leftovers(batch: BatchRow): Json[] {
    const out: Json[] = []
    for (const item of batch.items) {
      const row = item.cheque_id ? db.getCheque(item.cheque_id as number) : null
      if (row === null || row.status === 'failed') {
        const draft: Json = {}
        for (const k of BATCH_FIELDS) if (k in item) draft[k] = item[k]
        out.push(draft)
      }
    }
    return out
  }

  get('/api/batches/open', () => {
    const batch = db.openBatch()
    return json({ batch: batch ? batchView(batch) : null })
  })

  /** Check every cheque and fix the leaf numbers. Nothing is printed. */
  post('/api/batches', async (rq) => {
    const req = await rq.body(BatchCreate)
    rq.check()
    const problems: string[] = []
    let number = req.first_cheque_no
    req.items.forEach((item, index) => {
      const i = index + 1
      const composed = compose(item)
      if (!composed.ok) problems.push(`Cheque ${i} (${item.payee}): ${composed.error}`)
      try {
        db.checkEntity(item.entity_id, item.payee)
      } catch (exc) {
        if (!(exc instanceof ValueError)) throw exc
        problems.push(`Cheque ${i}: ${exc.message}`)
      }
      if (db.chequeNoUsed(number)) problems.push(`Leaf ${number} is already in the register.`)
      number = incrementNo(number)
    })
    if (problems.length) throw new HTTPException(400, problems.join(' '))
    let printer: string
    try {
      printer = await printing.resolvePrinter(req.printer)
    } catch (exc) {
      if (exc instanceof printing.PrintError) throw new HTTPException(400, exc.message)
      throw exc
    }
    // Stored in the model's own field order, as Pydantic dumped them.
    const items: BatchItem[] = req.items.map((item) => ({
      payee: item.payee,
      entity_id: item.entity_id,
      one_off: item.one_off,
      amount: item.amount,
      cheque_date: item.cheque_date,
      memo: item.memo,
      crossing: item.crossing,
      strike_order: item.strike_order,
      star_fill: item.star_fill,
      cheque_id: null
    }))
    let batchId: number
    try {
      batchId = db.createBatch(items, req.first_cheque_no, printer)
    } catch (exc) {
      if (exc instanceof ValueError) throw new HTTPException(409, exc.message)
      throw exc
    }
    return json(batchView(db.getBatch(batchId) as BatchRow), 201)
  })

  /**
   * Render every leaf first, then send them in feed order.
   *
   * Rendering all of them before sending any means a render failure leaves the
   * tray untouched. Sending stops at the first failure: the leaves behind it
   * are pre-numbered, so carrying on would put a cheque on the wrong leaf.
   * Each leaf takes its number only once the print system has accepted it.
   */
  async function printItems(batch: BatchRow, indexes: number[]): Promise<string[]> {
    const rendered: Array<[number, BatchItem, ComposeResponse, RenderResult]> = []
    for (const i of indexes) {
      const item = batch.items[i]
      const composed = compose(itemInput(item))
      const payload = chequePayload(itemInput(item), composed.amount_words, composed.amount_figures)
      let result: RenderResult
      try {
        result = await renderPdf(
          cal(),
          payload,
          join(config.pdfDir, `batch-${pad(batch.id, 4)}-${pad(i, 2)}.pdf`),
          renderPaths
        )
      } catch (exc) {
        if (!(exc instanceof RenderError)) throw exc
        throw new HTTPException(500, `cheque ${i + 1} could not be rendered, so nothing was sent: ${exc.message}`)
      }
      rendered.push([i, item, composed, result])
    }

    const sent: string[] = []
    let number = batch.next_no
    try {
      for (const [i, item, composed, result] of rendered) {
        if (db.chequeNoUsed(number)) {
          throw new HTTPException(409, `leaf ${number} is already in the register — nothing further was sent`)
        }
        const chequeId = db.addCheque({
          cheque_no: '',
          payee: need(item, 'payee') as string,
          entity_id: need(item, 'entity_id') as number | null,
          one_off: (item.one_off ?? false) as boolean,
          amount_paise: composed.amount_paise,
          amount_words: composed.amount_words,
          cheque_date: need(item, 'cheque_date') as string,
          memo: need(item, 'memo') as string,
          status: 'queued',
          printer: batch.printer,
          cal_version: cal().version,
          batch_id: batch.id,
          pdf_path: result.pdf_path
        })
        db.updateCheque(chequeId, {
          event: 'rendered',
          detail:
            `${fixed(result.page_w_mm, 1)}x${fixed(result.page_h_mm, 1)}mm, ` + `batch ${batch.id} cheque ${i + 1}`
        })
        item.cheque_id = chequeId
        try {
          await submit(chequeId, result.pdf_path, batch.printer, number)
        } catch (exc) {
          // The print system refused it, so the leaf is still blank: the row
          // stays as a failed attempt with no leaf number, and the cheque goes
          // back to being a draft.
          if (exc instanceof HTTPException) item.cheque_id = null
          throw exc
        }
        sent.push(number)
        number = incrementNo(number)
      }
    } finally {
      // Whatever happened, the batch records which cheques now have a row
      // and which leaf comes next.
      db.saveBatchItems(batch.id, batch.items, number)
    }
    return sent
  }

  post('/api/batches/{batch_id}/print-first', async (rq) => {
    const batchId = rq.path('batch_id', id)
    const req = await rq.body(BatchStep)
    rq.check()
    if (!req.confirm) throw new HTTPException(400, 'printing onto a cheque leaf needs an explicit confirmation')
    const batch = batchOr404(batchId)
    const first = batch.items.findIndex((it) => !it.cheque_id)
    if (first === -1 || !db.moveBatch(batchId, 'review', 'first_sent')) {
      throw new HTTPException(409, 'this batch is not waiting to print its first cheque')
    }
    let sent: string[]
    try {
      sent = await printItems(batch, [first])
    } catch (exc) {
      if (exc instanceof HTTPException) db.moveBatch(batchId, 'first_sent', 'review')
      throw exc
    }
    return json({ sent, batch: batchView(db.getBatch(batchId) as BatchRow) })
  })

  post('/api/batches/{batch_id}/first-outcome', async (rq) => {
    const batchId = rq.path('batch_id', id)
    const req = await rq.body(BatchFirstOutcome)
    rq.check()
    const batch = batchOr404(batchId)
    if (batch.status !== 'first_sent') throw new HTTPException(409, 'this batch is not waiting on its first cheque')
    const queued = batch.items
      .filter((it) => it.cheque_id && (db.getCheque(it.cheque_id as number) as ChequeWithEntity).status === 'queued')
      .map((it) => it.cheque_id as number)
    if (queued.length !== 1) throw new HTTPException(409, 'could not find the first cheque awaiting its verdict')
    const chequeId = queued[0]
    db.settle(new Map([[chequeId, req.outcome]]), req.detail || `batch ${batchId}, first leaf`)
    if (req.outcome === 'printed') {
      db.moveBatch(batchId, 'first_sent', 'first_ok')
    } else {
      await cancelQuietly((db.getCheque(chequeId) as ChequeWithEntity).job_id)
      // The spoiled leaf keeps its number in the register; the cheque itself
      // goes back to the front of the batch to be printed on the next leaf.
      for (const item of batch.items) if (item.cheque_id === chequeId) item.cheque_id = null
      db.moveBatch(batchId, 'first_sent', 'review', { items: batch.items })
    }
    return json({ batch: batchView(db.getBatch(batchId) as BatchRow) })
  })

  post('/api/batches/{batch_id}/print-rest', async (rq) => {
    const batchId = rq.path('batch_id', id)
    const req = await rq.body(BatchStep)
    rq.check()
    if (!req.confirm) throw new HTTPException(400, 'printing onto cheque leaves needs an explicit confirmation')
    const batch = batchOr404(batchId)
    const rest = batch.items.flatMap((it, i) => (it.cheque_id ? [] : [i]))
    if (!rest.length) throw new HTTPException(409, 'every cheque in this batch has already been printed')
    if (!db.moveBatch(batchId, 'first_ok', 'rest_sent')) {
      throw new HTTPException(409, 'the first cheque must be confirmed as printed before the rest are sent')
    }
    let sent: string[]
    try {
      sent = await printItems(batch, rest)
    } catch (exc) {
      if (exc instanceof HTTPException) {
        const now = (db.getBatch(batchId) as BatchRow).items
        if (!rest.some((i) => now[i].cheque_id)) db.moveBatch(batchId, 'rest_sent', 'first_ok') // nothing went out
      }
      throw exc
    }
    return json({ sent, batch: batchView(db.getBatch(batchId) as BatchRow) })
  })

  /** One click for the whole stack: everything printed, except any listed. */
  post('/api/batches/{batch_id}/rest-outcome', async (rq) => {
    const batchId = rq.path('batch_id', id)
    const req = await rq.body(BatchRestOutcome)
    rq.check()
    let batch = batchOr404(batchId)
    if (batch.status !== 'rest_sent') {
      throw new HTTPException(409, 'this batch is not waiting on its remaining cheques')
    }
    const queued = new Set(
      batch.items
        .filter((it) => it.cheque_id && (db.getCheque(it.cheque_id as number) as ChequeWithEntity).status === 'queued')
        .map((it) => it.cheque_id as number)
    )
    if (req.misprinted.some((cid) => !queued.has(cid))) {
      throw new HTTPException(
        400,
        'some of the cheques marked as misprinted are not awaiting a verdict in this batch'
      )
    }
    // Outcomes are applied in ascending order of id, deliberately: the recorded
    // sessions depend on it.
    const outcomes = new Map<number, string>()
    for (const cid of [...queued].sort((a, b) => a - b)) {
      outcomes.set(cid, req.misprinted.includes(cid) ? 'failed' : 'printed')
    }
    db.settle(outcomes, req.detail || `batch ${batchId}`)
    for (const cid of req.misprinted) await cancelQuietly((db.getCheque(cid) as ChequeWithEntity).job_id)
    db.moveBatch(batchId, 'rest_sent', 'done')
    batch = db.getBatch(batchId) as BatchRow
    return json({ batch: batchView(batch), leftovers: leftovers(batch) })
  })

  /** Stop a batch that has nothing at the printer. Printed cheques stay in the
   * register; the rest come back as drafts. */
  post('/api/batches/{batch_id}/cancel', (rq) => {
    const batchId = rq.path('batch_id', id)
    rq.check()
    batchOr404(batchId)
    if (!db.moveBatch(batchId, ['review', 'first_ok'], 'cancelled')) {
      throw new HTTPException(409, 'a leaf from this batch is waiting for its verdict — confirm it before cancelling')
    }
    return json({
      batch: batchView(db.getBatch(batchId) as BatchRow),
      leftovers: leftovers(db.getBatch(batchId) as BatchRow)
    })
  })

  get('/api/frequency', () => {
    const rows = db.issuedCheques()
    const rhythms = frequency.analyse(rows, today(), db.entityMeta())
    return json({
      payees: rhythms.map((r) => r.asDict()),
      summary: frequency.summarise(rhythms, rows, today()),
      today: today()
    })
  })

  /** Every entity, with what the register knows about paying it. */
  function listEntities(): { entities: Json[] } {
    const rows = db.listEntities()
    const issued = db.issuedCheques()

    const stats = new Map<number | null, { count: number; total_paise: number; last_date: string }>()
    const payees = new Map<number | null, string[]>()
    for (const c of [...issued].reverse()) {
      let seen = payees.get(c.entity_id)
      if (!seen) payees.set(c.entity_id, (seen = []))
      if (!seen.includes(c.payee)) seen.push(c.payee)
    }
    for (const c of issued) {
      let s = stats.get(c.entity_id)
      if (!s) stats.set(c.entity_id, (s = { count: 0, total_paise: 0, last_date: '' }))
      s.count += 1
      s.total_paise += Math.trunc(Number(c.amount_paise))
      if (cmpStr(c.cheque_date, s.last_date) > 0) s.last_date = c.cheque_date
    }

    const now = today()
    return {
      entities: rows.map((row) => ({
        ...row,
        ac_payee: Boolean(row.ac_payee),
        seal_required: Boolean(row.seal_required),
        ...(stats.get(row.id) ?? { count: 0, total_paise: 0, last_date: '' }),
        rule_date: dating.ruleDate(row.date_rule, now),
        // A payee-per-cheque entity's recent payees, newest first, offered as
        // suggestions when its next cheque is written.
        payees: row.kind === 'per_cheque' ? (payees.get(row.id) ?? []).slice(0, 8) : []
      }))
    }
  }

  get('/api/entities', () => json(listEntities()))

  function entityFields(req: EntityInput) {
    return {
      entity_name: req.entity_name,
      ac_payee: Number(req.ac_payee),
      seal_required: Number(req.seal_required),
      category: req.category,
      notes: req.notes,
      kind: req.kind,
      date_rule: req.date_rule,
      remind_days: req.remind_days,
      payee_prefix: req.payee_prefix,
      head: req.head
    }
  }

  post('/api/entities', async (rq) => {
    const req = await rq.body(EntityInput)
    rq.check()
    let entityId: number
    try {
      entityId = db.createEntity(req.payee_name, entityFields(req))
    } catch (exc) {
      if (exc instanceof ValueError) throw new HTTPException(409, exc.message)
      throw exc
    }
    return json({ id: entityId }, 201)
  })

  put('/api/entities/{entity_id}', async (rq) => {
    const entityId = rq.path('entity_id', id)
    const req = await rq.body(EntityInput)
    rq.check()
    let renamed: boolean
    try {
      renamed = db.updateEntity(entityId, { payee_name: req.payee_name, ...entityFields(req) })
    } catch (exc) {
      if (exc instanceof LookupError) throw new HTTPException(404, exc.message)
      if (exc instanceof ValueError) throw new HTTPException(409, exc.message)
      throw exc
    }
    return json({ id: entityId, renamed })
  })

  del('/api/entities/{entity_id}', (rq) => {
    const entityId = rq.path('entity_id', id)
    rq.check()
    if (!db.deleteEntity(entityId)) {
      throw new HTTPException(409, 'this entity has cheques in the register, so it cannot be removed')
    }
    return json({ deleted: entityId })
  })

  function report(fy: number | null, quarter: number | null): reports.Report {
    if (quarter !== null && ![1, 2, 3, 4].includes(quarter)) {
      throw new HTTPException(400, 'quarter must be 1, 2, 3 or 4')
    }
    const year = fy !== null ? fy : reports.fyOf(today())
    if (!(2000 <= year && year <= 2100)) throw new HTTPException(400, 'that financial year is out of range')
    return reports.build(db.issuedCheques(), db.listEntities(), year, quarter || null, today())
  }

  /** Each month under each expense head: one financial year, or the last N months. */
  function monthly(fy: number | null, last: number | null): reports.MonthlyReport {
    if (last !== null && !(1 <= last && last <= 24)) {
      throw new HTTPException(400, 'last must be between 1 and 24 months')
    }
    if (fy !== null && !(2000 <= fy && fy <= 2100)) {
      throw new HTTPException(400, 'that financial year is out of range')
    }
    return reports.monthly(db.issuedCheques(), db.listEntities(), fy, last, today())
  }

  get('/api/reports/monthly', (rq) => {
    const fy = rq.query('fy', optionalInt)
    const last = rq.query('last', optionalInt)
    rq.check()
    return json(monthly(fy, last))
  })

  /** The monthly report as an A4 PDF, for a committee meeting or the file. */
  get('/api/reports/monthly.pdf', async (rq) => {
    const fy = rq.query('fy', optionalInt)
    const last = rq.query('last', optionalInt)
    rq.check()
    const d = monthly(fy, last)
    const period = d.last
      ? `last-${d.last}-months`
      : 'fy-' + (reports.fyLabel(d.fy as number).split(/\s+/).pop() as string).replace('–', '-')
    const name = `banyan-monthly-${period}.pdf`
    let path: string
    try {
      path = await reportPdf.render(
        d,
        join(mkdtempSync(join(config.pdfDir, 'banyan-report-')), name),
        join(config.staticDir, 'fonts'),
        config.chrome
      )
    } catch (exc) {
      if (exc instanceof RenderError) throw new HTTPException(500, `the report could not be rendered: ${exc.message}`)
      throw exc
    }
    return fileResponse(path, { mediaType: 'application/pdf', filename: name })
  })

  /** Every report for a financial year, or one quarter of it (Q1 = Apr–Jun). */
  get('/api/reports', (rq) => {
    const fy = rq.query('fy', optionalInt)
    const quarter = rq.query('quarter', optionalInt)
    rq.check()
    return json(report(fy, quarter))
  })

  const REPORT_CSV: Record<string, string[]> = {
    entities: [
      'Entity', 'Prints as', 'Kind', 'Head', 'Cheques', 'Total (INR)', 'Share %',
      'Average (INR)', 'Largest (INR)', 'Previous period (INR)', 'Last paid'
    ],
    categories: ['Head', 'Entities', 'Cheques', 'Total (INR)', 'Share %', 'Previous period (INR)'],
    payees: ['Payee (as printed)', 'Entities', 'Cheques', 'Total (INR)', 'Share %'],
    cheques: ['Cheque no', 'Cheque date', 'Payee', 'Entity', 'Head', 'Amount (INR)', 'Memo', 'Status']
  }

  function csvResponse(text: string, filename: string): Response {
    return new Response(text, {
      status: 200,
      headers: {
        'content-type': 'text/csv; charset=utf-8',
        'content-disposition': `attachment; filename="${filename}"`
      }
    })
  }

  /** One report as a spreadsheet, with its period breakdown as extra columns.
   * Every text cell a person typed is neutralised against formula execution. */
  get('/api/reports/{kind}.csv', (rq) => {
    const kind = rq.path('kind', fields.str({}))
    const fy = rq.query('fy', optionalInt)
    const quarter = rq.query('quarter', optionalInt)
    rq.check()
    if (!Object.hasOwn(REPORT_CSV, kind)) throw new HTTPException(404, 'no such report')
    const data = report(fy, quarter)
    const head = REPORT_CSV[kind]
    const cols = data.columns.map((c) => c.label)
    let buf = ''
    const pct = (f: number): string => fixed(f * 100, 1)

    if (kind === 'cheques') {
      buf += csvRow(head)
      const meta = new Map(db.listEntities().map((e) => [e.id, e]))
      for (const r of db.issuedCheques()) {
        if (!(cmpStr(data.start, r.cheque_date) <= 0 && cmpStr(r.cheque_date, data.end) <= 0)) continue
        const e = r.entity_id === null ? undefined : meta.get(r.entity_id)
        buf += csvRow([
          csvSafe(r.cheque_no), r.cheque_date, csvSafe(r.payee),
          csvSafe(e ? e.entity_name : ''),
          csvSafe(reports.headOf(e ?? null)),
          rupee(r.amount_paise), csvSafe(r.memo), r.status
        ])
      }
    } else {
      buf += csvRow(kind !== 'payees' ? [...head, ...cols] : head)
      const kinds: Record<string, string> = { one_off: 'One-off', per_cheque: 'Payee per cheque' }
      const rows: Array<{ cells: unknown[]; columns?: number[] }> =
        kind === 'entities'
          ? data.entities.map((s) => ({
              cells: [
                csvSafe(s.entity_name), csvSafe(s.payee_name),
                Object.hasOwn(kinds, s.kind) ? kinds[s.kind] : 'Regular',
                csvSafe(s.category), s.count, rupee(s.paise),
                pct(s.share), rupee(s.average_paise), rupee(s.largest_paise),
                rupee(s.previous_paise), s.last_date
              ],
              columns: s.columns
            }))
          : kind === 'categories'
            ? data.categories.map((s) => ({
                cells: [
                  csvSafe(s.category), s.entity_count, s.count,
                  rupee(s.paise), pct(s.share), rupee(s.previous_paise)
                ],
                columns: s.columns
              }))
            : data.payees.map((s) => ({
                cells: [csvSafe(s.payee), csvSafe(s.entities.join('; ')), s.count, rupee(s.paise), pct(s.share)]
              }))
      for (const row of rows) {
        buf += csvRow(kind !== 'payees' ? [...row.cells, ...(row.columns as number[]).map(rupee)] : row.cells)
      }
      if (kind !== 'payees') {
        const total =
          kind === 'entities'
            ? ['Total', '', '', '', data.totals.count, rupee(data.totals.paise)]
            : ['Total', data.totals.entity_count, data.totals.count, rupee(data.totals.paise)]
        buf += csvRow([
          ...total,
          ...Array<string>(head.length - total.length).fill(''),
          ...data.column_totals.map(rupee)
        ])
      }
    }

    const slug = data.label.replace('FY ', 'FY').replace('–', '-').split(' (')[0].replaceAll(' ', '-')
    return csvResponse(buf, `banyan-${kind}-${slug}.csv`)
  })

  /** Every column a person can type into is neutralised against spreadsheet
   * formula execution, and quoted so a comma cannot shift the row. */
  get('/api/export.csv', () => {
    const rows = db.listCheques({ limit: 100000 })
    let buf = csvRow([
      'Cheque no', 'Cheque date', 'Payee', 'Entity', 'Amount (INR)',
      'Amount in words', 'Memo', 'Status', 'Printer', 'Calibration', 'Recorded at'
    ])
    for (const r of rows) {
      buf += csvRow([
        csvSafe(r.cheque_no), r.cheque_date, csvSafe(r.payee),
        csvSafe(r.entity_name),
        rupee(r.amount_paise),
        csvSafe(r.amount_words), csvSafe(r.memo), r.status,
        csvSafe(r.printer), r.cal_version, r.created_at
      ])
    }
    return csvResponse(buf, 'banyan-cheque-register.csv')
  })

  // ---------------------------------------------------------------- the desk
  // Everything below is read-mostly and additive: it groups what the endpoints
  // above already record, and the few writes it makes are events, cheque books
  // and blank leaves. None of it renders, numbers or settles a cheque.

  function books(now: string): leaves.BooksView {
    return leaves.booksView(
      db.listBooks().map((b) => ({ ...b })),
      db.listCheques({ limit: 100000 }),
      db.listBlankLeaves(),
      now
    )
  }

  function reminders(now: string): dating.Reminder[] {
    const entities = new Map(db.listEntities().map((e) => [e.id, { ...e }]))
    const events = db.chequeEvents(['remind-ack', 'remind-snooze']).map((e) => ({ ...e, cheque_id: e.cheque_id as number }))
    return dating.reminders(
      db.issuedCheques().map((c) => ({ ...c })),
      entities,
      events,
      now
    )
  }

  /** One call for the Desk: who is due, what is at the printer, which
   * post-dated cheques are coming up, and how the cheque book is doing. */
  get('/api/desk', () => {
    const now = today()
    const issued = db.issuedCheques()
    const rhythms = frequency.analyse(issued, now, db.entityMeta())
    const queued = db.listCheques({ status: 'queued' }).map(rowToDict)
    const batch = db.openBatch()
    const since = addDays(now, -100)
    return json({
      today: now,
      entities: listEntities().entities,
      rhythms: rhythms.map((r) => r.asDict()),
      summary: frequency.summarise(rhythms, issued, now),
      awaiting: queued.filter((c) => c.batch_id === null),
      batch: batch ? batchView(batch) : null,
      recent: issued
        .filter((c) => cmpStr(c.cheque_date, since) >= 0)
        .map((c) => ({
          id: c.id,
          entity_id: c.entity_id,
          payee: c.payee,
          cheque_no: c.cheque_no,
          cheque_date: c.cheque_date,
          amount_paise: c.amount_paise,
          status: c.status
        })),
      reminders: reminders(now),
      books: books(now)
    })
  })

  /** The last cheques issued to one entity, oldest first, and their median. */
  get('/api/entities/{entity_id}/history', (rq) => {
    const entityId = rq.path('entity_id', id)
    const limit = rq.query('limit', fields.int({ default: 8, ge: 1, le: 60 }))
    rq.check()
    if (!db.getEntity(entityId)) throw new HTTPException(404, 'no such entity')
    const rows = db
      .listCheques({ entity_id: entityId, limit: 500 })
      .filter((r) => ['queued', 'printed', 'logged'].includes(r.status))
      .map(rowToDict)
      .slice(0, limit)
    rows.reverse()
    const amounts = rows.map((r) => r.amount_paise).sort((a, b) => a - b)
    const mid = Math.floor(amounts.length / 2)
    const median = amounts.length
      ? amounts.length % 2
        ? amounts[mid]
        : floorDiv(amounts[mid - 1] + amounts[mid], 2)
      : 0
    return json({ cheques: rows, median_paise: median })
  })

  /** Every leaf, newest first, with gaps and duplicate numbers worked out
   * here so the page never has to guess. */
  get('/api/register', () => {
    const now = today()
    const cheques = db.listCheques({ limit: 100000 }).map((r) => ({ ...r }))
    const blanks = db.listBlankLeaves().map((b) => ({ ...b }))
    const view = leaves.registerView(
      cheques,
      blanks,
      db.listBooks().map((b) => ({ ...b })),
      now
    )
    return json({ ...view, books: books(now), today: now })
  })

  /** Whether a leaf number is already used, and by what: asked before a
   * single cheque is sent, so a leaf number is never written twice. */
  get('/api/leaves/{cheque_no}', (rq) => {
    const chequeNo = rq.path('cheque_no', fields.str({}))
    rq.check()
    const number = tidy(chequeNo)
    const rows = db
      .listCheques({ limit: 100000 })
      .filter((r) => r.cheque_no === number)
      .map(rowToDict)
    const blank = db.listBlankLeaves().filter((b) => b.cheque_no === number)
    return json({
      cheque_no: number,
      used: Boolean(rows.length || blank.length),
      cheques: rows,
      blank,
      events: db.eventsAbout(`leaf:${number}`)
    })
  })

  /** Events about a batch, a book or a blank leaf rather than one cheque. */
  get('/api/events', (rq) => {
    const subject = rq.query('subject', fields.str({ min: 1, max: 60 }))
    rq.check()
    return json({ events: db.eventsAbout(subject) })
  })

  /** Record leaves that were used but never reached the register: written
   * by hand, or spoiled blank. Every number is checked before anything is
   * written, and each one gets its event. */
  post('/api/register/account-for', async (rq) => {
    const req = await rq.body(AccountFor)
    rq.check()
    const used = db.usedNumbers()
    const numbers = req.items.map((it) => it.cheque_no)
    const clash = [
      ...new Set(numbers.filter((n) => used.has(n) || numbers.filter((other) => other === n).length > 1))
    ].sort(cmpStr)
    if (clash.length) throw new HTTPException(409, `already in the register: ${clash.join(', ')}`)
    const logged: Array<[(typeof req.items)[number], ComposeResponse]> = []
    for (const it of req.items) {
      if (it.as !== 'logged') continue
      if (!it.payee || !it.amount || !it.cheque_date) {
        throw new HTTPException(400, `leaf ${it.cheque_no}: a handwritten cheque needs its payee, amount and date`)
      }
      const composed = compose({
        payee: it.payee,
        entity_id: null,
        one_off: false,
        amount: it.amount,
        cheque_date: it.cheque_date,
        cheque_no: '',
        memo: '',
        crossing: true,
        strike_order: false,
        star_fill: false
      })
      if (!composed.ok) throw new HTTPException(400, `leaf ${it.cheque_no}: ${composed.error}`)
      try {
        db.checkEntity(it.entity_id, it.payee)
      } catch (exc) {
        if (exc instanceof ValueError) throw new HTTPException(409, `leaf ${it.cheque_no}: ${exc.message}`)
        throw exc
      }
      logged.push([it, composed])
    }
    const voids = req.items.filter((it) => it.as === 'void')
    if (voids.length) {
      try {
        db.addBlankLeaves(
          voids.map((it) => it.cheque_no),
          [...new Set(voids.map((it) => it.reason || 'used up blank'))].sort(cmpStr).join('; ')
        )
      } catch (exc) {
        if (exc instanceof ValueError) throw new HTTPException(409, exc.message)
        throw exc
      }
    }
    const ids: number[] = []
    for (const [it, composed] of logged) {
      ids.push(
        db.addCheque({
          cheque_no: it.cheque_no,
          payee: it.payee,
          entity_id: it.entity_id,
          amount_paise: composed.amount_paise,
          amount_words: composed.amount_words,
          cheque_date: it.cheque_date as string,
          memo: it.memo || it.reason,
          status: 'logged',
          cal_version: cal().version
        })
      )
    }
    return json({ logged: ids, voided: voids.map((it) => it.cheque_no) })
  })

  /** Put cheques on plain paper through the real render and the real
   * printer call. No leaf number, no register row, no stage: one event each. */
  async function plainPaper(payloads: Array<[string, Json]>, printer: string, subject: string): Promise<Json[]> {
    const rendered: Array<[string, RenderResult]> = []
    for (const [i, [label, payload]] of payloads.entries()) {
      rendered.push([label, await renderFor(payload, `plain-${pad(i, 2)}.pdf`)])
    }
    const out: Json[] = []
    for (const [label, result] of rendered) {
      let jobId: string
      try {
        jobId = await printing.submit(result.pdf_path, printer, PAGE_W_MM, PAGE_H_MM, 'plain-paper test')
      } catch (exc) {
        if (exc instanceof printing.PrintError) {
          throw new HTTPException(502, `could not send to the printer: ${exc.message}`)
        }
        throw exc
      }
      db.addEvent('test-print', `${label} on plain paper, ${jobId}`, { subject })
      out.push({ label, job_id: jobId, page: resultAsDict(result) })
    }
    return out
  }

  /** The run's first cheque (or every one) on plain paper, before any leaf. */
  post('/api/batches/{batch_id}/test-print', async (rq) => {
    const batchId = rq.path('batch_id', id)
    const req = await rq.body(BatchTestPrint)
    rq.check()
    if (!req.confirm) throw new HTTPException(400, 'sending to the printer needs an explicit confirmation')
    const batch = batchOr404(batchId)
    if (batch.status !== 'review') throw new HTTPException(409, 'the plain-paper test comes before the first leaf')
    let drafts = batch.items.flatMap((it, i) => (it.cheque_id ? [] : [[i, it] as [number, BatchItem]]))
    if (req.which === 'first') drafts = drafts.slice(0, 1)
    const payloads: Array<[string, Json]> = []
    for (const [i, item] of drafts) {
      const composed = compose(itemInput(item))
      payloads.push([
        `cheque ${i + 1} (${need(item, 'payee')})`,
        chequePayload(itemInput(item), composed.amount_words, composed.amount_figures)
      ])
    }
    return json({ sent: await plainPaper(payloads, batch.printer, `batch:${batchId}`) })
  })

  /** One cheque on plain paper, before it goes on a leaf. */
  post('/api/test-print/cheque', async (rq) => {
    const req = await rq.body(ChequeTestPrint)
    rq.check()
    if (!req.confirm) throw new HTTPException(400, 'sending to the printer needs an explicit confirmation')
    const composed = compose(req)
    if (!composed.ok) throw new HTTPException(400, composed.error)
    let printer: string
    try {
      printer = await printing.resolvePrinter(req.printer)
    } catch (exc) {
      if (exc instanceof printing.PrintError) throw new HTTPException(400, exc.message)
      throw exc
    }
    const payload = chequePayload(req, composed.amount_words, composed.amount_figures)
    return json({ sent: await plainPaper([[req.payee, payload]], printer, 'test') })
  })

  get('/api/books', () => json(books(today())))

  /** Shelve a cheque book. Refused if any of its numbers is in another book,
   * or already in the register unless it is said to be partly used. */
  post('/api/books', async (rq) => {
    const req = await rq.body(BookInput)
    rq.check()
    const shelf = db.listBooks().map((b) => ({ ...b }))
    if (shelf.some((b) => reports.casefold(b.name) === reports.casefold(req.name))) {
      throw new HTTPException(409, `there is already a book called ${pyRepr(req.name)}`)
    }
    let info: leaves.NewBookCheck
    try {
      info = leaves.checkNewBook(req.first_no, req.last_no, shelf, db.usedNumbers(), req.already_used)
    } catch (exc) {
      if (exc instanceof ValueError) throw new HTTPException(409, exc.message)
      throw exc
    }
    const bookId = db.addBook({
      name: req.name,
      first_no: req.first_no,
      last_no: req.last_no,
      received_on: req.received_on ? req.received_on : '',
      front: req.start === 'now',
      detail:
        `${req.first_no}–${req.last_no}, ${info.count} leaves` +
        (info.in_register ? `, ${info.in_register} already used` : '')
    })
    return json({ id: bookId, ...info, books: books(today()) }, 201)
  })

  patch('/api/books/{book_id}', async (rq) => {
    const bookId = rq.path('book_id', id)
    const req = await rq.body(BookUpdate)
    rq.check()
    const book = db.getBook(bookId)
    if (!book) throw new HTTPException(404, 'no such book')
    if (req.name && req.name !== book.name) {
      db.updateBook(bookId, { kind: 'book-renamed', detail: `${book.name} → ${req.name}`, name: req.name })
    }
    if (req.use_now) {
      db.updateBook(bookId, { kind: 'book-use-now', detail: 'moved to the front of the shelf', front: true })
    }
    return json(books(today()))
  })

  post('/api/books/{book_id}/alert', async (rq) => {
    const bookId = rq.path('book_id', id)
    const req = await rq.body(BookAlertAnswer)
    rq.check()
    if (!db.getBook(bookId)) throw new HTTPException(404, 'no such book')
    if (req.answer === 'ordered') {
      db.updateBook(bookId, {
        kind: 'book-alert',
        detail: 'a new book has been ordered',
        alert_state: 'ordered',
        alert_until: ''
      })
    } else {
      const until = addDays(today(), req.days)
      db.updateBook(bookId, {
        kind: 'book-alert',
        detail: `remind again on ${until}`,
        alert_state: 'snoozed',
        alert_until: until
      })
    }
    return json(books(today()))
  })

  get('/api/reminders', () => json({ reminders: reminders(today()), today: today() }))

  /** Answer a post-dated cheque's reminder. An event, and only an event:
   * the cheque's status and date are untouched. */
  post('/api/cheques/{cheque_id}/remind-ack', async (rq) => {
    const chequeId = rq.path('cheque_id', id)
    const req = await rq.body(RemindAnswer)
    rq.check()
    const now = today()
    const current = reminders(now).find((r) => r.cheque_id === chequeId)
    if (current === undefined || current.stage === null) {
      throw new HTTPException(409, 'this cheque has no reminder to answer')
    }
    if (req.answer === 'ack') {
      db.updateCheque(chequeId, { event: 'remind-ack', detail: `stage ${current.stage}d: funds are arranged` })
    } else {
      const until = addDays(now, req.days)
      db.updateCheque(chequeId, { event: 'remind-snooze', detail: `until ${until}` })
    }
    return json({ reminders: reminders(now) })
  })

  // ---------------------------------------------------------------- frontend

  // The preview backdrop is a photograph of a real leaf with the account, the
  // account name and the MICR band blacked out, the anti-copy patch blurred and
  // the resolution halved. A copy ships with the app; one kept with the desk's
  // data is served in preference to it, and the request 404s (the frontend then
  // draws its guide) when neither exists. This route is
  // declared before the /static mount so it takes precedence over it.
  get('/static/leaf-photo.jpg', () => {
    for (const candidate of [join(config.dataStaticDir, 'leaf-photo.jpg'), join(config.staticDir, 'leaf-photo.jpg')]) {
      if (isFile(candidate)) return fileResponse(candidate)
    }
    throw new HTTPException(404, 'no leaf photo prepared')
  })

  /** The frontend's files. A path that climbs out of the folder is simply not found. */
  function staticFile(rq: Req, path: string): Response {
    if (!['GET', 'HEAD'].includes(rq.raw.method)) throw new HTTPException(405, 'Method Not Allowed')
    let full: string
    let root: string
    try {
      root = realpathSync(config.staticDir)
      full = realpathSync(join(root, ...path.split('/')))
    } catch {
      throw new HTTPException(404, 'Not Found')
    }
    if (!full.startsWith(root + sep) || !isFile(full)) throw new HTTPException(404, 'Not Found')
    return fileResponse(full, { request: rq.raw })
  }

  /**
   * The page, stamped with the version it belongs to.
   *
   * Its scripts and stylesheets are addressed with ?v=<version>, so a new
   * release is a URL the browser has never cached — normal caching stays on,
   * and a reload after an update cannot pick up the previous app.js.
   */
  function indexHtml(page = 'index.html'): string {
    const html = readFileSync(join(config.staticDir, page), 'utf8')
    return html
      .replace(/((?:src|href)="\/static\/[^"?]+)"/g, `$1?v=${DESK_VERSION}"`)
      .replace('<head>', `<head>\n<meta name="desk-version" content="${DESK_VERSION}">`)
  }

  // The desk's page gets one more script as it is served: the way into Settings.
  const INDEX_HTML = indexHtml().replace(
    '</body>',
    `<script src="/static/js/desk-extras.js?v=${DESK_VERSION}"></script>\n</body>`
  )

  // Connected with a read key, the app is the members' report pages and nothing else.
  let readerHtml = ''
  get('/', () => {
    const html = sync.readOnly() ? (readerHtml ||= indexHtml('reader.html')) : INDEX_HTML
    return new Response(html, { status: 200, headers: { 'content-type': 'text/html; charset=utf-8' } })
  })

  // ------------------------------------------------------- server connection
  // The desktop app asks once, on first launch, for
  // the API URL and user key of the Banyan server, and can be told to skip.
  // Nothing is sent to that server from here yet: these routes only keep
  // what was entered.
  let setupHtml = ''

  get('/setup', () => {
    try {
      setupHtml ||= indexHtml('setup.html')
    } catch {
      throw new HTTPException(404, 'Not Found')
    }
    return new Response(setupHtml, { status: 200, headers: { 'content-type': 'text/html; charset=utf-8' } })
  })

  const sync = new Sync({ dbPath: config.dbPath, connectionPath: config.connectionPath })
  const connectionNow = () => ({
    ...describeConnection(readConnection(config.connectionPath)),
    sync: sync.status(),
    desk_version: DESK_VERSION
  })
  /** Run something that talks to the Banyan server, answering its refusals in words. */
  const withServer = async (work: () => Promise<unknown>): Promise<Response> => {
    try {
      await work()
    } catch (exc) {
      if (exc instanceof ConnectionError) throw new HTTPException(400, exc.message)
      throw exc
    }
    return json(connectionNow())
  }

  get('/api/connection', () => json(connectionNow()))

  // Redeems the key with the server, which spends it, and keeps the token.
  put('/api/connection', async (rq) => {
    const sent = (await rq.raw.json().catch(() => null)) as { api_url?: unknown; user_key?: unknown } | null
    return withServer(() => sync.connect(sent?.api_url, sent?.user_key))
  })

  // Is there a Banyan server at this address? Asked before a key is typed.
  post('/api/connection/check', async (rq) => {
    const sent = (await rq.raw.json().catch(() => null)) as { api_url?: unknown } | null
    try {
      const info = await sync.discover(sent?.api_url)
      return json({ ok: true, name: info.name, api_url: info.api_url })
    } catch (exc) {
      if (exc instanceof ConnectionError) throw new HTTPException(400, exc.message)
      throw exc
    }
  })

  // Forgets the key. The server's address is kept for next time unless the
  // page asks for that to be forgotten too.
  post('/api/connection/skip', async (rq) => {
    const sent = (await rq.raw.json().catch(() => null)) as { forget_server?: unknown } | null
    sync.disconnect(sent?.forget_server === true)
    return json(connectionNow())
  })

  // Send the register now and wait for the answer. The app calls this before it quits.
  post('/api/sync/flush', () => withServer(() => sync.flush()))

  // The treasurer chose this desk's register over the server's.
  post('/api/sync/overwrite', async (rq) => {
    const sent = (await rq.raw.json().catch(() => null)) as { confirm?: unknown } | null
    if (sent?.confirm !== true) throw new HTTPException(400, 'replacing the server’s register needs an explicit confirmation')
    return withServer(() => sync.overwrite())
  })

  // The treasurer chose the server's register over this desk's. It is fetched
  // now and put in place when the app restarts; the register it replaces is kept.
  post('/api/sync/restore', async (rq) => {
    const sent = (await rq.raw.json().catch(() => null)) as { confirm?: unknown } | null
    if (sent?.confirm !== true) throw new HTTPException(400, 'replacing this desk’s register needs an explicit confirmation')
    const reply = await withServer(() => sync.fetchForRestore())
    options.onRestoreReady?.()
    return reply
  })

  // ----------------------------------------------------------------- serving
  const app = new Hono()

  // Inside the
  // desktop app only the window that was handed the session cookie may talk
  // to the server. Before everything else, pages and files included.
  app.use('*', async (c, next) => {
    if (config.token) {
      const digest = (text: string): Buffer => createHash('sha256').update(text).digest()
      const sent = getCookie(c, SESSION_COOKIE) ?? ''
      if (!timingSafeEqual(digest(sent), digest(config.token))) return json({ detail: 'Not authorised' }, 401)
    }
    await next()
  })

  /**
   * Turn away API calls from a page older than this server.
   *
   * A page that sends a different version is out of date. One that sends none
   * predates versioning, so its writes are refused too; plain reads without a
   * version stay open for the health probe, the CSV and PDF links, and curl.
   * Nothing a refused request asked for is done.
   */
  app.use('*', async (c, next) => {
    if (requestPath(c).startsWith('/api/')) {
      const sent = c.req.raw.headers.get(VERSION_HEADER)
      if (sent !== DESK_VERSION && (sent !== null || !['GET', 'HEAD'].includes(c.req.raw.method))) {
        return json(
          {
            detail: 'The desk has been updated since this page was opened. Reload the page — nothing was saved.',
            reload: true,
            desk_version: DESK_VERSION
          },
          409
        )
      }
    }
    await next()
  })

  // A reports-only app (connected with a read key) has no desk behind it. Its
  // report pages are answered by the Banyan server, and every other part of
  // the desk's API is closed, so nothing here can be written or printed and
  // the register on this Mac, if there is one, is neither shown nor touched.
  app.use('*', async (c, next) => {
    const path = requestPath(c)
    if (!path.startsWith('/api/') || !sync.readOnly()) return next()
    if (/^\/api\/(connection|sync)\b/.test(path)) return next()
    const reading = ['GET', 'HEAD'].includes(c.req.raw.method)
    try {
      if (reading && path === '/api/reader') {
        const reply = await sync.read('/api/v1/register/status')
        const body = (await reply.json().catch(() => ({}))) as { latest?: unknown; detail?: string }
        if (!reply.ok) return json({ app: true, latest: null, error: body.detail ?? `The server answered ${reply.status}.` })
        return json({ app: true, role: 'read', latest: body.latest ?? null })
      }
      if (reading && /^\/api\/reports(\/|$)/.test(path)) {
        const query = /\?([^#]*)/.exec(c.req.raw.url)?.[1]
        const reply = await sync.read(`/api/v1${path.slice(4)}${query ? `?${query}` : ''}`)
        const headers: Record<string, string> = { 'cache-control': 'no-store' }
        for (const name of ['content-type', 'content-disposition']) {
          const value = reply.headers.get(name)
          if (value) headers[name] = value
        }
        return new Response(reply.body, { status: reply.status, headers })
      }
    } catch (exc) {
      if (exc instanceof ConnectionError) return json({ detail: exc.message }, 502)
      throw exc
    }
    return json({ detail: 'This app is connected with a read key. It shows reports only.' }, 403)
  })

  // Every change to the register goes to the Banyan server straight away.
  app.use('*', async (c, next) => {
    await next()
    const path = requestPath(c)
    const wrote = !['GET', 'HEAD', 'OPTIONS'].includes(c.req.raw.method) && c.res.status < 400
    if (wrote && path.startsWith('/api/') && !/^\/api\/(connection|sync)\b/.test(path)) sync.schedule()
  })

  const STATIC_MOUNT = /^\/static\/(.*)$/s

  /** Find the route as Starlette does: the first whose path and method both
   * match; failing that, 405 from the first whose path matches; else 404. */
  app.all('*', async (c) => {
    const request = c.req.raw
    const path = requestPath(c)
    const query = new URLSearchParams(/\?([^#]*)/.exec(request.url)?.[1] ?? '')
    try {
      let wrongMethod: Route | null = null
      for (const r of routes) {
        const m = r.regex.exec(path)
        if (!m) continue
        if (r.methods && !r.methods.includes(request.method)) {
          wrongMethod ??= r
          continue
        }
        return await r.handler(new Req(request, m.groups ?? {}, query))
      }
      const inStatic = STATIC_MOUNT.exec(path)
      if (inStatic) return staticFile(new Req(request, {}, query), inStatic[1])
      if (wrongMethod) {
        throw new HTTPException(405, 'Method Not Allowed', { allow: (wrongMethod.methods as string[]).join(', ') })
      }
      // A stray or missing final slash is sent to the address that exists, whatever the method.
      if (path !== '/') {
        const other = path.endsWith('/') ? path.replace(/\/+$/, '') : path + '/'
        if (routes.some((r) => r.regex.test(other)) || STATIC_MOUNT.test(other)) {
          const [, origin, rawPath, rest] = /^([a-z][a-z0-9+.-]*:\/\/[^/?#]*)([^?#]*)(\?[^#]*)?/i.exec(request.url) ?? []
          const location = origin + (path.endsWith('/') ? rawPath.replace(/\/+$/, '') : rawPath + '/') + (rest ?? '')
          return new Response(null, { status: 307, headers: { location } })
        }
      }
      throw new HTTPException(404, 'Not Found')
    } catch (exc) {
      if (exc instanceof HTTPException) return json({ detail: exc.detail }, exc.status, exc.headers)
      if (exc instanceof RequestValidationError) return json({ detail: exc.errors }, 422)
      if (exc instanceof CalibrationError) return json({ detail: exc.message }, 500)
      throw exc
    }
  })

  // Anything a route did not expect. The Python answered these the same way.
  app.onError((exc) => {
    console.error(exc)
    return new Response('Internal Server Error', { status: 500, headers: { 'content-type': 'text/plain; charset=utf-8' } })
  })

  // Catch up on anything that did not reach the server before the last quit.
  sync.schedule()

  return {
    app,
    sync,
    /** The fingerprint pages send back with every API call. */
    version: DESK_VERSION,
    /** A page from the frontend folder, stamped with that fingerprint. */
    page: indexHtml,
    close(): void {
      // The register opens a connection for each call and closes it again,
      // so there is nothing held open to release.
    }
  }
}

/** The request's path exactly as sent, with %XX decoded. */
function requestPath(c: Context): string {
  const raw = /^[a-z][a-z0-9+.-]*:\/\/[^/?#]*([^?#]*)/i.exec(c.req.raw.url)?.[1] ?? ''
  return unquote(raw || '/')
}
