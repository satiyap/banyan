/**
 * Small pieces of Python behaviour that amount, calibration, leaves and dating
 * depend on and that JavaScript does differently by default.
 *
 * The desk's rules were first written in Python, and the recordings in
 * tests/golden hold these modules to the answers that code gave. Nothing here
 * is app logic. Each helper stands in for a Python built-in whose JavaScript
 * namesake gives another answer on some input: `\s` and `\d` cover different characters, strings sort by UTF-16 unit
 * rather than code point, `repr()` has no equivalent, and so on.
 */

/** Python's ValueError, by name: what the modules throw when an input is refused. */
export class ValueError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'ValueError'
  }
}

/** The characters Python's `str.isspace()`, `str.strip()` and `\s` treat as whitespace
 * (regex character-class source). JS `\s` adds U+FEFF and lacks U+001C-U+001F and U+0085. */
export const PY_WS = '\\t-\\r\\x1c-\\x20\\x85\\xa0\\u1680\\u2000-\\u200a\\u2028\\u2029\\u202f\\u205f\\u3000'

const STRIP_RE = new RegExp(`^[${PY_WS}]+|[${PY_WS}]+$`, 'g')
const NUMERIC_WS = PY_WS.replace('\\x1c-\\x20', ' ')
const NUMERIC_STRIP_RE = new RegExp(`^[${NUMERIC_WS}]+|[${NUMERIC_WS}]+$`, 'g')
const ND_RE = /^\p{Nd}$/u
const UNPRINTABLE_RE = /^[\p{C}\p{Z}]$/u

/** `str.strip()` with no argument. */
export function pyStrip(s: string): string {
  return s.replace(STRIP_RE, '')
}

/** Python compares strings by code point; JS `<` compares UTF-16 units, which
 * orders astral characters before U+E000-U+FFFF. Returns -1, 0 or 1. */
export function cmpStr(a: string, b: string): number {
  const n = Math.min(a.length, b.length)
  for (let i = 0; i < n; i++) {
    const x = a.charCodeAt(i)
    const y = b.charCodeAt(i)
    if (x !== y) {
      // Surrogates (astral characters) sort above every BMP character.
      const sx = x >= 0xd800 && x <= 0xdfff
      const sy = y >= 0xd800 && y <= 0xdfff
      if (sx !== sy) return sx ? 1 : -1
      return x < y ? -1 : 1
    }
  }
  return a.length === b.length ? 0 : a.length < b.length ? -1 : 1
}

/** Value 0-9 of one Unicode decimal digit (category Nd), as `int()` reads it.
 * Every run of Nd characters in Unicode is a whole number of 0-9 blocks, so
 * the value is the offset from the start of the run, modulo ten. */
function digitValue(cp: number): number {
  if (cp >= 0x30 && cp <= 0x39) return cp - 0x30
  let start = cp
  while (start > 0 && ND_RE.test(String.fromCodePoint(start - 1))) start--
  return (cp - start) % 10
}

/** `int(s)` for a string already known to be all decimal digits (any script).
 * Python ints are unbounded; past 2**53 this loses precision. */
export function pyInt(digits: string): number {
  let n = 0
  for (const ch of digits) n = n * 10 + digitValue(ch.codePointAt(0) as number)
  return n
}

/** `int(text)`: optional sign, digits of any script, single underscores between digits. */
export function pyIntOf(text: string): number {
  const m = /^([+-]?)(\p{Nd}+(?:_\p{Nd}+)*)$/u.exec(numericStrip(text))
  if (!m) throw new ValueError(`invalid literal for int() with base 10: ${pyRepr(text)}`)
  return (m[1] === '-' ? -1 : 1) * pyInt(m[2].replaceAll('_', ''))
}

/** Strips the whitespace `int()` and `float()` ignore around a number: Python's whitespace
 * except U+001C-U+001F, which CPython leaves in place (and which then spoil the number). */
export function numericStrip(text: string): string {
  return text.replace(NUMERIC_STRIP_RE, '')
}

/** `repr()` of a str: Python's quoting and escaping rules. */
export function pyRepr(s: string): string {
  const quote = s.includes("'") && !s.includes('"') ? '"' : "'"
  let out = quote
  for (const ch of s) {
    const cp = ch.codePointAt(0) as number
    if (ch === quote || ch === '\\') out += '\\' + ch
    else if (ch === '\t') out += '\\t'
    else if (ch === '\n') out += '\\n'
    else if (ch === '\r') out += '\\r'
    else if (ch === ' ' || !UNPRINTABLE_RE.test(ch)) out += ch
    else if (cp < 0x100) out += '\\x' + cp.toString(16).padStart(2, '0')
    else if (cp < 0x10000) out += '\\u' + cp.toString(16).padStart(4, '0')
    else out += '\\U' + cp.toString(16).padStart(8, '0')
  }
  return out + quote
}

/** Floor division and modulo with Python's sign rules (JS `%` truncates). */
export function floorDiv(a: number, b: number): number {
  return Math.floor(a / b)
}

export function pyMod(a: number, b: number): number {
  return a - Math.floor(a / b) * b
}

// -- dates ---------------------------------------------------------------
// Python `date` values are carried as 'YYYY-MM-DD' strings. All arithmetic on
// them is done on calendar parts in UTC, so no local-time parsing can shift a day.

export type Ymd = [year: number, month: number, day: number]

const pad = (n: number, width: number): string => String(n).padStart(width, '0')

/** `calendar.monthrange(year, month)[1]`. */
export function daysInMonth(year: number, month: number): number {
  if (month === 2) return year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28
  return [4, 6, 9, 11].includes(month) ? 30 : 31
}

/** `date(y, m, d).isoformat()`. */
export function isoDate(year: number, month: number, day: number): string {
  return `${pad(year, 4)}-${pad(month, 2)}-${pad(day, 2)}`
}

/** Milliseconds since the Unix epoch for a UTC calendar date and time (any year 1-9999). */
function utcMs(year: number, month: number, day: number, hour = 0, minute = 0, second = 0): number {
  const t = new Date(0)
  t.setUTCFullYear(year, month - 1, day)
  t.setUTCHours(hour, minute, second, 0)
  return t.getTime()
}

/** Days since the Unix epoch for a calendar date (differences match Python's `(a - b).days`). */
export function dayNumber([year, month, day]: Ymd): number {
  return Math.round(utcMs(year, month, day) / 86_400_000)
}

/** The calendar date `days` after `iso`. */
export function addDays(iso: string, days: number): string {
  const [y, m, d] = parseIsoDate(iso)
  const t = new Date(utcMs(y, m, d + days))
  return isoDate(t.getUTCFullYear(), t.getUTCMonth() + 1, t.getUTCDate())
}

function checkDate(year: number, month: number, day: number): void {
  if (year < 1 || year > 9999) throw new ValueError(`year must be in 1..9999, not ${year}`)
  if (month < 1 || month > 12) throw new ValueError(`month must be in 1..12, not ${month}`)
  const dim = daysInMonth(year, month)
  if (day < 1 || day > dim) {
    throw new ValueError(`day ${day} must be in range 1..${dim} for month ${month} in year ${year}`)
  }
}

/** `date.fromisoformat(s)` as calendar parts, with Python 3.14's error text.
 * Accepts 'YYYY-MM-DD' and 'YYYYMMDD'. Python also accepts ISO week dates
 * ('2026-W05-2'); those are refused here as an invalid string. */
export function parseIsoDate(s: string): Ymd {
  if (typeof s !== 'string') throw new TypeError('fromisoformat: argument must be str')
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s) ?? /^(\d{4})(\d{2})(\d{2})$/.exec(s)
  // JS `$` never matches before a trailing newline, and `\d` is ASCII-only here, as in fromisoformat.
  if (!m) throw new ValueError(`Invalid isoformat string: ${pyRepr(s)}`)
  const ymd: Ymd = [Number(m[1]), Number(m[2]), Number(m[3])]
  checkDate(...ymd)
  return ymd
}

/** A date string in canonical 'YYYY-MM-DD' form, validated like `date.fromisoformat`.
 * Canonical strings compare the way the dates do. */
export function canonicalDate(s: string): string {
  return isoDate(...parseIsoDate(s))
}

const TIME_RE = new RegExp(
  '^(\\d{2})(?::?(\\d{2})(?::?(\\d{2})(?:[.,](\\d+))?)?)?' + // HH[:MM[:SS[.ffffff]]]
    '(?: ?(Z|[+-]\\d{2}(?::?\\d{2}(?::?\\d{2}(?:\\.\\d+)?)?)?))?$' // Z or +HH[:MM[:SS[.ffffff]]]
)

/**
 * `datetime.fromisoformat(timestamp).astimezone().date()` — the desk's own
 * calendar date for a register timestamp.
 *
 * Timezone: Python's `astimezone()` with no argument converts to the system
 * local zone of the machine running the app, and so does this (JS `Date` local
 * getters; both honour the TZ environment variable). A timestamp with no UTC
 * offset is taken by Python to already be local time, so its date part is
 * returned as written. The register writes '2026-11-12T07:00:00+00:00'.
 *
 * Accepts the ISO forms the register and SQLite produce (date, optional time
 * to fractional seconds, optional 'Z' or numeric offset, any separator
 * character). Rarer forms Python 3.14 also takes (ISO week dates) are refused.
 */
export function localDateOf(timestamp: string): string {
  if (typeof timestamp !== 'string') throw new TypeError('fromisoformat: argument must be str')
  const bad = (): never => {
    throw new ValueError(`Invalid isoformat string: ${pyRepr(timestamp)}`)
  }
  const dm = /^(\d{4})-(\d{2})-(\d{2})/.exec(timestamp) ?? /^(\d{4})(\d{2})(\d{2})/.exec(timestamp)
  if (!dm) return bad()
  const [year, month, day] = [Number(dm[1]), Number(dm[2]), Number(dm[3])]
  // After the date: nothing, or any one separator character and a time.
  const rest = [...timestamp.slice(dm[0].length)].slice(1).join('')
  const m = timestamp.length === dm[0].length ? ([] as string[]) : (TIME_RE.exec(rest) ?? bad())
  checkDate(year, month, day)
  const hour = Number(m[1] ?? 0)
  const minute = Number(m[2] ?? 0)
  const second = Number(m[3] ?? 0)
  // 24:00:00 is the one hour past 23 that Python accepts: midnight ending the day.
  const endOfDay = hour === 24 && minute === 0 && second === 0 && !/[1-9]/.test(m[4] ?? '')
  if (hour === 24 && !endOfDay) {
    throw new ValueError('minute, second, and microsecond must be 0 when hour is 24')
  }
  if (hour > 24) throw new ValueError(`hour must be in 0..23, not ${hour}`)
  if (minute > 59) throw new ValueError(`minute must be in 0..59, not ${minute}`)
  if (second > 59) throw new ValueError(`second must be in 0..59, not ${second}`)

  const zone = m[5]
  if (zone === undefined) {
    // Naive: already local. Only 24:00 can move it off the written date.
    return endOfDay ? addDays(isoDate(year, month, day), 1) : isoDate(year, month, day)
  }
  let offsetSeconds = 0
  if (zone !== 'Z') {
    const z = /^([+-])(\d{2})(?::?(\d{2})(?::?(\d{2}))?)?/.exec(zone) as RegExpExecArray
    offsetSeconds = (Number(z[2]) * 3600 + Number(z[3] ?? 0) * 60 + Number(z[4] ?? 0)) * (z[1] === '-' ? -1 : 1)
  }
  const local = new Date(utcMs(year, month, day, hour, minute, second) - offsetSeconds * 1000)
  return isoDate(local.getFullYear(), local.getMonth() + 1, local.getDate())
}
