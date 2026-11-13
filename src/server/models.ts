/**
 * Request and response shapes for the API.
 *
 * A bad request is answered with HTTP 422 and a list of what was wrong, in
 * the shape and wording Pydantic gives (the API was first declared as
 * Pydantic models). The page reads the `msg` of each entry, and the recorded
 * sessions in tests/golden/http.json fix the checks, their order and wording.
 *
 * Each model is a zod schema. The fields are not zod's own string/number
 * checks, because Pydantic reads input differently from zod in ways the page
 * can see: it accepts "1" and 1.0 for an integer and "yes" for a boolean,
 * counts string length in code points, reports every wrong field rather than
 * the first, and words its errors its own way. So every field is a small
 * transform that follows Pydantic's rules and raises Pydantic's error, and
 * zod does what it is good at: walking objects and lists, collecting the
 * issues with their paths, and giving each model a TypeScript type.
 */

import { z } from 'zod'

import { daysInMonth, isoDate, PY_WS, pyStrip, ValueError } from './pycompat'

// ------------------------------------------------------------ the 422 body
/** One entry of FastAPI's `{"detail": [...]}` for a request that failed validation. */
export interface ErrorDetail {
  type: string
  loc: (string | number)[]
  msg: string
  input: unknown
  ctx?: Record<string, unknown>
}

/** A key the request did not send. Fields see this instead of `undefined`, which zod treats specially. */
const MISSING: unique symbol = Symbol('missing')

interface Refinement {
  issues: unknown[]
}

/** Record one Pydantic error on the zod context and stop validating this field. */
function fail(ctx: Refinement, type: string, msg: string, input: unknown, extra?: Record<string, unknown>): never {
  ctx.issues.push({ code: 'custom', message: msg, input, params: { type, input: { value: input }, ctx: extra } })
  return z.NEVER
}

const required = (ctx: Refinement): never => fail(ctx, 'missing', 'Field required', MISSING)

/** Turn zod's issues into FastAPI's `detail` list. `root` is what was validated; `loc` is where it sits. */
export function errorDetails(error: z.ZodError, loc: (string | number)[], root: unknown): ErrorDetail[] {
  return error.issues.map((issue) => {
    const params = (issue as { params?: { type: string; input: { value: unknown }; ctx?: Record<string, unknown> } })
      .params
    const path = issue.path as (string | number)[]
    if (!params) {
      // Not one of ours: zod itself objected. Nothing here should reach this.
      return { type: 'value_error', loc: [...loc, ...path], msg: issue.message, input: null }
    }
    let input = params.input.value
    if (params.type === 'missing') {
      // Pydantic shows the object the field is missing from.
      let parent: unknown = root
      for (const key of path.slice(0, -1)) parent = (parent as Record<string | number, unknown>)?.[key]
      input = path.length ? parent : null
    }
    const detail: ErrorDetail = { type: params.type, loc: [...loc, ...path], msg: issue.message, input }
    if (params.ctx) detail.ctx = params.ctx
    return detail
  })
}

export type Validated<T> = { ok: true; value: T } | { ok: false; errors: ErrorDetail[] }

/** Validate one value (a body, or a single path or query parameter) against a schema. */
export function validate<T>(schema: z.ZodType<T>, input: unknown, loc: (string | number)[]): Validated<T> {
  const result = schema.safeParse(input)
  if (result.success) return { ok: true, value: result.data }
  return { ok: false, errors: errorDetails(result.error, loc, input) }
}

// ------------------------------------------------------------------ fields
const WS_RE = new RegExp(`[${PY_WS}]+`)

/** `' '.join(v.split())`: runs of whitespace become one space, the ends are trimmed. */
export function tidy(v: string): string {
  return v.split(WS_RE).filter(Boolean).join(' ')
}

const codePoints = (s: string): number => {
  let n = 0
  for (const _ of s) n++
  return n
}

const plural = (n: number, word: string): string => `${n} ${word}${n === 1 ? '' : 's'}`

interface StrOptions<D> {
  min?: number
  max?: number
  /** Pydantic matches with Rust's regex crate: `\d` is any Unicode digit and `$` is the very end. */
  pattern?: { source: string; re: RegExp }
  /** What a request that leaves the field out gets. Absent: the field is required. */
  default?: D
  nullable?: boolean
  /** The field validators, in the order the model declares them. They run only on a value that passed. */
  after?: Array<(v: string) => string>
}

function str(options: StrOptions<string> & { nullable?: false }): z.ZodType<string>
function str(options: StrOptions<string | null> & { nullable: true }): z.ZodType<string | null>
function str(options: StrOptions<string | null>): z.ZodType<string | null> {
  return z.unknown().transform((v, ctx): string | null => {
    if (v === MISSING) return 'default' in options ? (options.default as string | null) : required(ctx)
    if (v === null && options.nullable) return null
    if (typeof v !== 'string') return fail(ctx, 'string_type', 'Input should be a valid string', v)
    const length = codePoints(v)
    if (options.min !== undefined && length < options.min) {
      return fail(ctx, 'string_too_short', `String should have at least ${plural(options.min, 'character')}`, v, {
        min_length: options.min
      })
    }
    if (options.max !== undefined && length > options.max) {
      return fail(ctx, 'string_too_long', `String should have at most ${plural(options.max, 'character')}`, v, {
        max_length: options.max
      })
    }
    if (options.pattern && !options.pattern.re.test(v)) {
      return fail(ctx, 'string_pattern_mismatch', `String should match pattern '${options.pattern.source}'`, v, {
        pattern: options.pattern.source
      })
    }
    let out = v
    for (const check of options.after ?? []) {
      try {
        out = check(out)
      } catch (exc) {
        if (!(exc instanceof ValueError)) throw exc
        return fail(ctx, 'value_error', `Value error, ${exc.message}`, v, { error: {} })
      }
    }
    return out
  })
}

const INT_TEXT_RE = /^[+-]?[0-9]+(?:_[0-9]+)*(?:\.0+)?$/

/** Pydantic's lax integer: 3, 3.0, true, "3", " 3 ", "3.0", "1_000"; nothing with a fraction. */
function laxInt(v: unknown, ctx: Refinement): number {
  if (typeof v === 'boolean') return v ? 1 : 0
  if (typeof v === 'number') {
    if (!Number.isFinite(v)) return fail(ctx, 'finite_number', 'Input should be a finite number', v)
    if (!Number.isInteger(v)) {
      return fail(ctx, 'int_from_float', 'Input should be a valid integer, got a number with a fractional part', v)
    }
    return v
  }
  if (typeof v === 'string') {
    const text = pyStrip(v)
    if (!INT_TEXT_RE.test(text)) {
      return fail(ctx, 'int_parsing', 'Input should be a valid integer, unable to parse string as an integer', v)
    }
    return Number(text.replace(/\.0+$/, '').replaceAll('_', ''))
  }
  return fail(ctx, 'int_type', 'Input should be a valid integer', v)
}

interface IntOptions<D> {
  ge?: number
  le?: number
  default?: D
  nullable?: boolean
}

function int(options?: IntOptions<number> & { nullable?: false }): z.ZodType<number>
function int(options: IntOptions<number | null> & { nullable: true }): z.ZodType<number | null>
function int(options: IntOptions<number | null> = {}): z.ZodType<number | null> {
  return z.unknown().transform((v, ctx): number | null => {
    if (v === MISSING) return 'default' in options ? (options.default as number | null) : required(ctx)
    if (v === null && options.nullable) return null
    const n = laxInt(v, ctx)
    if (options.ge !== undefined && n < options.ge) {
      return fail(ctx, 'greater_than_equal', `Input should be greater than or equal to ${options.ge}`, v, {
        ge: options.ge
      })
    }
    if (options.le !== undefined && n > options.le) {
      return fail(ctx, 'less_than_equal', `Input should be less than or equal to ${options.le}`, v, { le: options.le })
    }
    return n
  })
}

const TRUE_WORDS = new Set(['1', 't', 'true', 'on', 'y', 'yes'])
const FALSE_WORDS = new Set(['0', 'f', 'false', 'off', 'n', 'no'])

/** Pydantic's lax boolean: true, 1, 1.0, "true", "yes", "on", "t", "y", "1" and their opposites. */
function bool(options: { default?: boolean } = {}): z.ZodType<boolean> {
  return z.unknown().transform((v, ctx): boolean => {
    if (v === MISSING) return 'default' in options ? (options.default as boolean) : required(ctx)
    if (typeof v === 'boolean') return v
    const unclear = (): never =>
      fail(ctx, 'bool_parsing', 'Input should be a valid boolean, unable to interpret input', v)
    if (typeof v === 'number') {
      if (v === 0) return false
      if (v === 1) return true
      return Number.isInteger(v) ? unclear() : fail(ctx, 'bool_type', 'Input should be a valid boolean', v)
    }
    if (typeof v === 'string') {
      const word = v.toLowerCase()
      if (TRUE_WORDS.has(word)) return true
      if (FALSE_WORDS.has(word)) return false
      return unclear()
    }
    return fail(ctx, 'bool_type', 'Input should be a valid boolean', v)
  })
}

function literal<const T extends string>(values: readonly T[], options?: { default?: T; nullable?: false }): z.ZodType<T>
function literal<const T extends string>(
  values: readonly T[],
  options: { default?: T | null; nullable: true }
): z.ZodType<T | null>
function literal<const T extends string>(
  values: readonly T[],
  options: { default?: T | null; nullable?: boolean } = {}
): z.ZodType<T | null> {
  const quoted = values.map((v) => `'${v}'`)
  const expected = quoted.length === 1 ? quoted[0] : `${quoted.slice(0, -1).join(', ')} or ${quoted[quoted.length - 1]}`
  return z.unknown().transform((v, ctx): T | null => {
    if (v === MISSING) return 'default' in options ? (options.default as T | null) : required(ctx)
    if (v === null && options.nullable) return null
    if (typeof v === 'string' && (values as readonly string[]).includes(v)) return v as T
    return fail(ctx, 'literal_error', `Input should be ${expected}`, v, { expected })
  })
}

// -- dates ------------------------------------------------------------------
// Pydantic reads a date with the speedate library: 'YYYY-MM-DD', or a datetime
// whose time is exactly midnight, or a Unix timestamp that falls on midnight.
// What follows reproduces its answers and its wording for everything a date
// field is likely to be sent; see the port notes for the corners it leaves.

const SECONDS_PER_DAY = 86_400
/** speedate reads a number above this as milliseconds rather than seconds. */
const MS_WATERSHED = 20_000_000_000

function dateProblem(ctx: Refinement, v: unknown, error: string): never {
  return fail(ctx, 'date_from_datetime_parsing', `Input should be a valid date or datetime, ${error}`, v, { error })
}

const inexact = (ctx: Refinement, v: unknown): never =>
  fail(ctx, 'date_from_datetime_inexact', 'Datetimes provided to dates should have zero time - e.g. be exact dates', v)

function dateFromTimestamp(n: number, v: unknown, ctx: Refinement): string {
  const seconds = Math.abs(n) > MS_WATERSHED ? n / 1000 : n
  const t = new Date(seconds * 1000)
  if (Number.isNaN(t.getTime()) || t.getUTCFullYear() > 9999) {
    return dateProblem(ctx, v, 'dates after 9999 are not supported as unix timestamps')
  }
  if (t.getUTCFullYear() < 1) return dateProblem(ctx, v, 'dates before 0000 are not supported as unix timestamps')
  if (seconds % SECONDS_PER_DAY !== 0) return inexact(ctx, v)
  return isoDate(t.getUTCFullYear(), t.getUTCMonth() + 1, t.getUTCDate())
}

const isDigits = (s: string): boolean => /^[0-9]+$/.test(s)

/** The time after a date's separator. Returns whether it is exactly midnight. */
function midnight(time: string, v: unknown, ctx: Refinement): boolean {
  if (time.length < 5) return dateProblem(ctx, v, 'input is too short')
  if (!isDigits(time.slice(0, 2))) return dateProblem(ctx, v, 'invalid character in hour')
  if (time[2] !== ':') return dateProblem(ctx, v, 'invalid time separator, expected `:`')
  if (!isDigits(time.slice(3, 5))) return dateProblem(ctx, v, 'invalid character in minute')
  const hour = Number(time.slice(0, 2))
  const minute = Number(time.slice(3, 5))
  if (hour > 23) return dateProblem(ctx, v, 'hour value is outside expected range of 0-23')
  if (minute > 59) return dateProblem(ctx, v, 'minute value is outside expected range of 0-59')
  let rest = time.slice(5)
  let second = 0
  let fraction = ''
  if (rest.startsWith(':')) {
    if (rest.length < 3) return dateProblem(ctx, v, 'input is too short')
    if (!isDigits(rest.slice(1, 3))) return dateProblem(ctx, v, 'invalid character in second')
    second = Number(rest.slice(1, 3))
    if (second > 59) return dateProblem(ctx, v, 'second value is outside expected range of 0-59')
    rest = rest.slice(3)
    const m = /^[.,]([0-9]+)/.exec(rest)
    if (m) {
      fraction = m[1]
      rest = rest.slice(m[0].length)
    }
  }
  if (rest && !/^(?:[zZ]|[+-][0-9]{2}(?::?[0-9]{2})?)$/.test(rest)) {
    return dateProblem(ctx, v, 'unexpected extra characters at the end of the input')
  }
  return hour === 0 && minute === 0 && second === 0 && !/[1-9]/.test(fraction)
}

function dateFromText(v: string, ctx: Refinement): string {
  // A string of digits is a timestamp, as a number would be.
  if (/^[+-]?[0-9]+(?:\.[0-9]+)?$/.test(v) && !/^[0-9]{4}-/.test(v)) return dateFromTimestamp(Number(v), v, ctx)
  if (v.length < 10) return dateProblem(ctx, v, 'input is too short')
  if (!isDigits(v.slice(0, 4))) return dateProblem(ctx, v, 'invalid character in year')
  if (v[4] !== '-') return dateProblem(ctx, v, 'invalid date separator, expected `-`')
  if (!isDigits(v.slice(5, 7))) return dateProblem(ctx, v, 'invalid character in month')
  if (v[7] !== '-') return dateProblem(ctx, v, 'invalid date separator, expected `-`')
  if (!isDigits(v.slice(8, 10))) return dateProblem(ctx, v, 'invalid character in day')
  const year = Number(v.slice(0, 4))
  const month = Number(v.slice(5, 7))
  const day = Number(v.slice(8, 10))
  if (month < 1 || month > 12) return dateProblem(ctx, v, 'month value is outside expected range of 1-12')
  if (day < 1 || day > daysInMonth(year, month)) return dateProblem(ctx, v, 'day value is outside expected range')
  if (v.length > 10) {
    if (!'Tt_ '.includes(v[10])) {
      return dateProblem(ctx, v, 'invalid datetime separator, expected `T`, `t`, `_` or space')
    }
    if (!midnight(v.slice(11), v, ctx)) return inexact(ctx, v)
  }
  if (year === 0) {
    const error = 'year 0 is out of range'
    return fail(ctx, 'date_parsing', `Input should be a valid date in the format YYYY-MM-DD, ${error}`, v, { error })
  }
  return isoDate(year, month, day)
}

/** A calendar date, carried as 'YYYY-MM-DD' ). */
function date(options?: { nullable?: false }): z.ZodType<string>
function date(options: { default?: string | null; nullable: true }): z.ZodType<string | null>
function date(options: { default?: string | null; nullable?: boolean } = {}): z.ZodType<string | null> {
  return z.unknown().transform((v, ctx): string | null => {
    if (v === MISSING) return 'default' in options ? (options.default as string | null) : required(ctx)
    if (v === null && options.nullable) return null
    if (typeof v === 'string') return dateFromText(v, ctx)
    if (typeof v === 'number') return dateFromTimestamp(v, v, ctx)
    return fail(ctx, 'date_type', 'Input should be a valid date', v)
  })
}

interface ListOptions {
  min?: number
  max: number
  /** True when a request may leave the list out and get an empty one. */
  optional?: boolean
}

/** A list. Too many items is reported on its own; too few only once every item has passed. */
function list<T>(item: z.ZodType<T>, options: ListOptions): z.ZodType<T[]> {
  return z
    .unknown()
    .transform((v, ctx): unknown[] => {
      if (v === MISSING) return options.optional ? [] : required(ctx)
      if (!Array.isArray(v)) return fail(ctx, 'list_type', 'Input should be a valid list', v)
      if (v.length > options.max) {
        return fail(
          ctx,
          'too_long',
          `List should have at most ${plural(options.max, 'item')} after validation, not ${v.length}`,
          v,
          { field_type: 'List', max_length: options.max, actual_length: v.length }
        )
      }
      return v
    })
    .pipe(z.array(item))
    .transform((v, ctx): T[] => {
      if (options.min !== undefined && v.length < options.min) {
        return fail(
          ctx,
          'too_short',
          `List should have at least ${plural(options.min, 'item')} after validation, not ${v.length}`,
          v,
          { field_type: 'List', min_length: options.min, actual_length: v.length }
        )
      }
      return v
    }) as unknown as z.ZodType<T[]>
}

/** A model: an object whose fields are checked in the order given, all of them, unknown keys ignored. */
function model<S extends Record<string, z.ZodType>>(shape: S): z.ZodType<{ [K in keyof S]: z.output<S[K]> }> {
  const keys = Object.keys(shape)
  return z
    .unknown()
    .transform((v, ctx): Record<string, unknown> => {
      if (typeof v !== 'object' || v === null || Array.isArray(v)) {
        return fail(
          ctx,
          'model_attributes_type',
          'Input should be a valid dictionary or object to extract fields from',
          v
        )
      }
      const given = v as Record<string, unknown>
      const filled: Record<string, unknown> = {}
      for (const key of keys) filled[key] = Object.hasOwn(given, key) ? given[key] : MISSING
      return filled
    })
    .pipe(z.object(shape)) as unknown as z.ZodType<{ [K in keyof S]: z.output<S[K]> }>
}

// ------------------------------------------------------------ field validators
function payeeNotBlank(v: string): string {
  if (!v) throw new ValueError('enter who the cheque is for')
  return v
}

function payeeNameNotBlank(v: string): string {
  if (!v) throw new ValueError('the payee name is what gets printed — it cannot be blank')
  return v
}

// Characters `str.isdigit()` accepts beyond the decimal digits: superscripts,
// circled digits and the like (Unicode Numeric_Type=Digit).
const DIGIT_ONLY =
  '\\u{B2}\\u{B3}\\u{B9}\\u{1369}-\\u{1371}\\u{19DA}\\u{2070}\\u{2074}-\\u{2079}\\u{2080}-\\u{2089}' +
  '\\u{2460}-\\u{2468}\\u{2474}-\\u{247C}\\u{2488}-\\u{2490}\\u{24EA}\\u{24F5}-\\u{24FD}\\u{24FF}' +
  '\\u{2776}-\\u{277E}\\u{2780}-\\u{2788}\\u{278A}-\\u{2792}\\u{10A40}-\\u{10A43}\\u{10E60}-\\u{10E68}' +
  '\\u{11052}-\\u{1105A}\\u{1F100}-\\u{1F10A}'
const ENDS_IN_DIGIT_RE = new RegExp(`[\\p{Nd}${DIGIT_ONLY}]$`, 'u')

function numbered(v: string): string {
  v = pyStrip(v)
  if (!ENDS_IN_DIGIT_RE.test(v)) {
    throw new ValueError(
      'the first cheque number must end in digits, so the following leaves can be numbered from it'
    )
  }
  return v
}

const DATE_RULE = '^(today|(next|this):([1-9]|[12]\\d|3[01]))$'
const REMIND_DAYS = '^(\\d{1,2}(,\\d{1,2}){0,3})?$'
const unicodeDigits = (source: string): RegExp => new RegExp(source.replaceAll('\\d', '\\p{Nd}'), 'u')

// ------------------------------------------------------------------ models
const chequeInputShape = {
  payee: str({ min: 1, max: 120, after: [tidy, payeeNotBlank] }),
  // Which entity this payment is for. Needed only when several entities
  // print the same payee name; otherwise the name alone decides.
  entity_id: int({ default: null, nullable: true }),
  // When the payee is new: record them as a one-off rather than a regular.
  one_off: bool({ default: false }),
  amount: str({ min: 1, max: 32 }),
  cheque_date: date(),
  // Collapse whitespace so "Sunrise   Services" and "Sunrise Services"
  // are one payee in the frequency analysis rather than two.
  cheque_no: str({ default: '', max: 40, after: [tidy] }),
  memo: str({ default: '', max: 200, after: [tidy] }),
  crossing: bool({ default: true }),
  strike_order: bool({ default: false }),
  star_fill: bool({ default: false })
}

/** What the treasurer types in to write one cheque. */
export const ChequeInput = model(chequeInputShape)
export type ChequeInput = z.infer<typeof ChequeInput>

/** A cheque to print, or to record as already written. */
export const IssueRequest = model({
  ...chequeInputShape,
  action: literal(['print', 'log'], { default: 'print' }),
  printer: str({ default: '' }),
  confirm: bool({ default: false }) // printing a real leaf requires an explicit yes
})
export type IssueRequest = z.infer<typeof IssueRequest>

export const ConfirmRequest = model({
  outcome: literal(['printed', 'failed']),
  detail: str({ default: '', max: 200 })
})
export type ConfirmRequest = z.infer<typeof ConfirmRequest>

export const VoidRequest = model({
  reason: str({ default: '', max: 200 })
})
export type VoidRequest = z.infer<typeof VoidRequest>

/**
 * A correction to a register entry.
 *
 * Only fields that are bookkeeping rather than payment. The payee,
 * amount, date and status describe what was actually issued — changing those
 * after the fact would make the register disagree with the leaf in someone's
 * hand, so they are corrected by voiding and re-issuing instead.
 */
export const ChequeEditRequest = model({
  cheque_no: str({ default: null, nullable: true, max: 40, after: [tidy] }),
  memo: str({ default: null, nullable: true, max: 200, after: [tidy] }),
  // Moving a cheque between entities that print the same name: the leaf
  // is unchanged, only which of them the desk counts it against.
  entity_id: int({ default: null, nullable: true })
})
export type ChequeEditRequest = z.infer<typeof ChequeEditRequest>

/**
 * An organisation or person the association pays.
 *
 * Only payee_name is ever printed. The rest is the desk's own record of how
 * this entity is normally paid.
 */
export const EntityInput = model({
  payee_name: str({ min: 1, max: 120, after: [tidy, payeeNameNotBlank] }),
  entity_name: str({ default: '', max: 100, after: [tidy] }),
  ac_payee: bool({ default: true }),
  seal_required: bool({ default: false }),
  category: str({ default: '', max: 60, after: [tidy] }),
  notes: str({ default: '', max: 500 }),
  // The report head. Left out, kept: a page from before heads were fixed.
  head: literal(['', 'Salary', 'Utilities', 'AMC', 'Misc', 'GST / Tax'], { default: null, nullable: true }),
  // 'per_cheque': the payee is typed on each cheque (GST to a fresh CRN), so
  // payee_name is the entity's own label and never reaches a leaf.
  kind: literal(['regular', 'one_off', 'per_cheque'], { default: 'regular' }),
  // What a per_cheque entity's payee starts from in the Tray. Left out, kept.
  payee_prefix: str({ default: null, nullable: true, max: 60, after: [tidy] }),
  // Left out, these keep what the entity has: a page from before they
  // existed must not reset them by saving an unrelated edit.
  date_rule: str({ default: null, nullable: true, pattern: { source: DATE_RULE, re: unicodeDigits(DATE_RULE) } }),
  remind_days: str({ default: null, nullable: true, pattern: { source: REMIND_DAYS, re: unicodeDigits(REMIND_DAYS) } })
})
export type EntityInput = z.infer<typeof EntityInput>

export const TestPrintRequest = model({
  printer: str({ default: '' }),
  confirm: bool({ default: false }),
  pattern: literal(['test', 'blank'], { default: 'test' })
})
export type TestPrintRequest = z.infer<typeof TestPrintRequest>

/** What /api/compose answers. Every key is always present, in this order. */
export interface ComposeResponse {
  ok: boolean
  amount_paise: number
  amount_plain: string
  amount_words: string
  amount_figures: string
  date_digits: string
  warnings: string[]
  error: string
  cheque: Record<string, unknown>
}

export function composeResponse(given: Partial<ComposeResponse> & { ok: boolean }): ComposeResponse {
  return {
    ok: given.ok,
    amount_paise: given.amount_paise ?? 0,
    amount_plain: given.amount_plain ?? '',
    amount_words: given.amount_words ?? '',
    amount_figures: given.amount_figures ?? '',
    date_digits: given.date_digits ?? '',
    warnings: given.warnings ?? [],
    error: given.error ?? '',
    cheque: given.cheque ?? {}
  }
}

/**
 * A run of cheques to review and then print, first one alone.
 *
 * Each item's own cheque_no is ignored: leaves are numbered in the order they
 * feed, from first_cheque_no, and only when they are actually printed — so a
 * misprinted first leaf moves every later number on by one, as it should.
 */
export const BatchCreate = model({
  items: list(ChequeInput, { min: 1, max: 50 }),
  first_cheque_no: str({ min: 1, max: 40, after: [numbered] }),
  printer: str({ default: '' })
})
export type BatchCreate = z.infer<typeof BatchCreate>

export const BatchStep = model({
  confirm: bool({ default: false }) // sending leaves to the printer needs an explicit yes
})
export type BatchStep = z.infer<typeof BatchStep>

export const BatchFirstOutcome = model({
  outcome: literal(['printed', 'failed']),
  detail: str({ default: '', max: 200 })
})
export type BatchFirstOutcome = z.infer<typeof BatchFirstOutcome>

/** The verdict on everything after the first leaf. Empty means every leaf
 * printed correctly — the one-click case. */
export const BatchRestOutcome = model({
  misprinted: list(int(), { max: 50, optional: true }),
  detail: str({ default: '', max: 200 })
})
export type BatchRestOutcome = z.infer<typeof BatchRestOutcome>

/** One cheque on plain paper: the real render and printer settings, but
 * no leaf number and nothing in the register. */
export const ChequeTestPrint = model({
  ...chequeInputShape,
  printer: str({ default: '' }),
  confirm: bool({ default: false })
})
export type ChequeTestPrint = z.infer<typeof ChequeTestPrint>

export const BatchTestPrint = model({
  which: literal(['first', 'all'], { default: 'first' }),
  confirm: bool({ default: false })
})
export type BatchTestPrint = z.infer<typeof BatchTestPrint>

/** A cheque book, by the numbers printed on its first and last leaves. */
export const BookInput = model({
  name: str({ min: 1, max: 40, after: [tidy] }),
  first_no: str({ min: 1, max: 40, after: [tidy] }),
  last_no: str({ min: 1, max: 40, after: [tidy] }),
  received_on: date({ default: null, nullable: true }),
  // 'after': behind every book already on the shelf. 'now': in front of
  // them, so it is used next even if another book has leaves left.
  start: literal(['after', 'now'], { default: 'after' }),
  // A book some of whose leaves are already in the register: the one in use
  // when books were first set up. Without this, an overlap is refused.
  already_used: bool({ default: false })
})
export type BookInput = z.infer<typeof BookInput>

export const BookUpdate = model({
  name: str({ default: null, nullable: true, min: 1, max: 40 }),
  use_now: bool({ default: false })
})
export type BookUpdate = z.infer<typeof BookUpdate>

export const BookAlertAnswer = model({
  answer: literal(['ordered', 'snooze']),
  days: int({ default: 3, ge: 1, le: 30 })
})
export type BookAlertAnswer = z.infer<typeof BookAlertAnswer>

export const RemindAnswer = model({
  answer: literal(['ack', 'snooze']),
  days: int({ default: 1, ge: 1, le: 30 })
})
export type RemindAnswer = z.infer<typeof RemindAnswer>

/**
 * One leaf the register has no record of: written by hand (with what
 * was written on it) or used up blank.
 *
 * The field is `as`, which is what the request sends.
 */
export const AccountForItem = model({
  cheque_no: str({ min: 1, max: 40, after: [tidy] }),
  as: literal(['logged', 'void']),
  reason: str({ default: '', max: 200 }),
  payee: str({ default: '', max: 120, after: [tidy] }),
  entity_id: int({ default: null, nullable: true }),
  amount: str({ default: '', max: 32 }),
  cheque_date: date({ default: null, nullable: true }),
  memo: str({ default: '', max: 200, after: [tidy] })
})
export type AccountForItem = z.infer<typeof AccountForItem>

export const AccountFor = model({
  items: list(AccountForItem, { min: 1, max: 50 })
})
export type AccountFor = z.infer<typeof AccountFor>

/** Count register entries against another entity. The leaves are unchanged. */
export const MoveChequesRequest = model({
  cheque_ids: list(int(), { min: 1, max: 500 }),
  entity_id: int()
})
export type MoveChequesRequest = z.infer<typeof MoveChequesRequest>

// ------------------------------------------------- path and query parameters
/** The field kinds, for the parameters a route reads from its path and query string. */
export const fields = { str, int, bool, date, literal }

/** Hands a field "not sent", so its default applies or it reports that it is required. */
export const NOT_SENT: unknown = MISSING
