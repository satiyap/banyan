/**
 * Loads cheque-calibration.json — the measured geometry of the physical leaf.
 *
 * That file stays the single source of truth for millimetres. Nothing here invents
 * a coordinate: if the JSON is missing or disagrees with the leaf this app was
 * measured for, loading fails loudly rather than printing a plausible-looking
 * cheque at the wrong offsets onto real bank stationery.
 */

import { readFileSync, statSync } from 'node:fs'
import { basename } from 'node:path'
import { ValueError, numericStrip, pyRepr } from './pycompat'

// Physical facts. The leaf is landscape; the printer page is that leaf rotated
// 90 degrees, because 92mm is below the M126nw's minimum feed length.
export const CHEQUE_W_MM = 204.0
export const CHEQUE_H_MM = 92.0
export const PAGE_W_MM = 92.0
export const PAGE_H_MM = 204.0

// The M126nw PPD declares an ImageableArea inset of 11.9906pt on all four edges.
// Nothing closer than this to a paper edge receives toner, whatever the CSS says.
export const HARDWARE_INSET_MM = 4.23

// The MICR band and everything below it must stay clear of dynamic content.
export const MICR_TOP_MM = 76.0

export const REQUIRED_FIELDS = ['date', 'payee', 'words1', 'words2', 'amount', 'cross', 'bearer'] as const

/** The calibration file is missing, malformed, or for a different leaf. */
export class CalibrationError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'CalibrationError'
  }
}

export type JsonValue = null | boolean | number | string | JsonValue[] | { [key: string]: JsonValue }

/** One field's measurements, exactly as written in the file (x, y, w, h, size, pitch, ...). */
export type CalibrationField = { [key: string]: JsonValue }

/** What `Calibration.asDict()` returns — the shape served to the browser. */
export interface CalibrationDict {
  version: number
  dx: number
  dy: number
  flip: boolean
  paper: 'a4' | 'exact'
  a4x: number
  a4y: number
  fields: Record<string, CalibrationField>
  leaf: { w: number; h: number }
  page: { w: number; h: number }
  hardware_inset_mm: number
  micr_top_mm: number
  source: string
  warnings: string[]
}

export class Calibration {
  /** As in the file. Python accepts JSON true/false here (bool is an int there), and so does this. */
  readonly version: number
  readonly dx: number
  readonly dy: number
  readonly flip: boolean
  readonly paper: 'a4' | 'exact'
  readonly a4x: number
  readonly a4y: number
  readonly fields: Record<string, CalibrationField>
  /** Path the calibration was loaded from. */
  readonly source: string
  readonly warnings: string[]

  constructor(init: {
    version: number
    dx: number
    dy: number
    flip: boolean
    paper: 'a4' | 'exact'
    a4x: number
    a4y: number
    fields: Record<string, CalibrationField>
    source: string
    warnings?: string[]
  }) {
    this.version = init.version
    this.dx = init.dx
    this.dy = init.dy
    this.flip = init.flip
    this.paper = init.paper
    this.a4x = init.a4x
    this.a4y = init.a4y
    this.fields = init.fields
    this.source = init.source
    this.warnings = init.warnings ?? []
  }

  get leaf(): [number, number] {
    return [CHEQUE_W_MM, CHEQUE_H_MM]
  }

  asDict(): CalibrationDict {
    return {
      version: this.version,
      dx: this.dx,
      dy: this.dy,
      flip: this.flip,
      paper: this.paper,
      a4x: this.a4x,
      a4y: this.a4y,
      fields: this.fields,
      leaf: { w: CHEQUE_W_MM, h: CHEQUE_H_MM },
      page: { w: PAGE_W_MM, h: PAGE_H_MM },
      hardware_inset_mm: HARDWARE_INSET_MM,
      micr_top_mm: MICR_TOP_MM,
      source: basename(this.source),
      warnings: this.warnings
    }
  }
}

// -- the file as Python sees it ---------------------------------------------
// The validation below depends on things `JSON.parse` throws away: whether a
// number was written as an int or a float (`"version": 15.0` is refused, and
// `210.0` is quoted back as written), the order of keys that look like
// integers, and the exact wording and position of a syntax error, which is
// shown to the person who just edited the file. So the file is read by a small
// decoder that follows CPython's `json` step for step, into this shape.

/** A JSON number written with a fraction or exponent (a Python float). Plain `number` is a Python int. */
class PyFloat {
  constructor(readonly value: number) {}
}

type Raw = null | boolean | number | PyFloat | string | Raw[] | RawDict
type RawDict = Map<string, Raw>

class JsonDecodeError extends Error {}

function decodeError(msg: string, doc: string[], pos: number): JsonDecodeError {
  let line = 1
  let lastNewline = -1
  for (let i = 0; i < pos && i < doc.length; i++) {
    if (doc[i] === '\n') {
      line++
      lastNewline = i
    }
  }
  return new JsonDecodeError(`${msg}: line ${line} column ${pos - lastNewline} (char ${pos})`)
}

const ESCAPES: Record<string, string> = {
  '"': '"', '\\': '\\', '/': '/', b: '\b', f: '\f', n: '\n', r: '\r', t: '\t'
}
const isDigit = (ch: string | undefined): boolean => ch !== undefined && ch >= '0' && ch <= '9'
const isJsonSpace = (ch: string | undefined): boolean => ch === ' ' || ch === '\t' || ch === '\n' || ch === '\r'

/** `json.loads(text)`: same values, same errors at the same positions (counted in code points). */
function pyJsonLoads(text: string): Raw {
  const doc = [...text]
  const len = doc.length
  if (doc[0] === '﻿') throw decodeError('Unexpected UTF-8 BOM (decode using utf-8-sig)', doc, 0)

  const skip = (i: number): number => {
    while (isJsonSpace(doc[i])) i++
    return i
  }
  const literal = (i: number, word: string): boolean => doc.slice(i, i + word.length).join('') === word

  /** Scans a string whose opening quote is at `begin`; returns it and the index after the closing quote. */
  function scanString(begin: number): [string, number] {
    let out = ''
    let end = begin + 1
    for (;;) {
      let next = end
      let c: string | undefined
      for (; next < len; next++) {
        c = doc[next]
        if (c === '"' || c === '\\') break
        if ((c.codePointAt(0) as number) <= 0x1f) throw decodeError('Invalid control character at', doc, next)
      }
      if (next >= len) throw decodeError('Unterminated string starting at', doc, begin)
      out += doc.slice(end, next).join('')
      next++
      if (c === '"') return [out, next]
      if (next === len) throw decodeError('Unterminated string starting at', doc, begin)
      c = doc[next]
      if (c !== 'u') {
        const plain = ESCAPES[c]
        if (plain === undefined) throw decodeError('Invalid \\escape', doc, next - 1)
        out += plain
        end = next + 1
        continue
      }
      const hex4 = (at: number): number => {
        if (at + 4 >= len) throw decodeError('Invalid \\uXXXX escape', doc, at - 1)
        const digits = doc.slice(at, at + 4).join('')
        if (!/^[0-9a-fA-F]{4}$/.test(digits)) throw decodeError('Invalid \\uXXXX escape', doc, at - 1)
        return parseInt(digits, 16)
      }
      next++
      let unit = hex4(next)
      end = next + 4
      // A high surrogate followed by an escaped low surrogate is one character; any other
      // surrogate is kept as it is, as Python keeps it.
      if (unit >= 0xd800 && unit <= 0xdbff && end + 6 < len && doc[end] === '\\' && doc[end + 1] === 'u') {
        const digits = doc.slice(end + 2, end + 6).join('')
        const low = /^[0-9a-fA-F]{4}$/.test(digits) ? parseInt(digits, 16) : 0
        if (low >= 0xdc00 && low <= 0xdfff) {
          unit = 0x10000 + (((unit - 0xd800) << 10) | (low - 0xdc00))
          end += 6
        }
      }
      out += unit > 0xffff ? String.fromCodePoint(unit) : String.fromCharCode(unit)
    }
  }

  function scanNumber(start: number): [Raw, number] {
    let i = start
    if (doc[i] === '-') i++
    if (doc[i] !== undefined && doc[i] >= '1' && doc[i] <= '9') {
      while (isDigit(doc[i])) i++
    } else if (doc[i] === '0') {
      i++
    } else {
      throw decodeError('Expecting value', doc, start)
    }
    let isFloat = false
    if (doc[i] === '.' && isDigit(doc[i + 1])) {
      isFloat = true
      i += 2
      while (isDigit(doc[i])) i++
    }
    if (doc[i] === 'e' || doc[i] === 'E') {
      const eStart = i
      i++
      if (doc[i] === '+' || doc[i] === '-') i++
      while (isDigit(doc[i])) i++
      if (isDigit(doc[i - 1])) isFloat = true
      else i = eStart
    }
    const value = Number(doc.slice(start, i).join(''))
    // An int written '-0' is plain 0 in Python.
    return [isFloat ? new PyFloat(value) : value === 0 ? 0 : value, i]
  }

  function scanOnce(i: number): [Raw, number] {
    if (i >= len) throw decodeError('Expecting value', doc, i)
    switch (doc[i]) {
      case '"':
        return scanString(i)
      case '{':
        return scanObject(i + 1)
      case '[':
        return scanArray(i + 1)
      case 'n':
        if (literal(i, 'null')) return [null, i + 4]
        break
      case 't':
        if (literal(i, 'true')) return [true, i + 4]
        break
      case 'f':
        if (literal(i, 'false')) return [false, i + 5]
        break
      case 'N':
        if (literal(i, 'NaN')) return [new PyFloat(NaN), i + 3]
        break
      case 'I':
        if (literal(i, 'Infinity')) return [new PyFloat(Infinity), i + 8]
        break
      case '-':
        if (literal(i, '-Infinity')) return [new PyFloat(-Infinity), i + 9]
        break
    }
    return scanNumber(i)
  }

  function scanObject(i: number): [Raw, number] {
    const out: RawDict = new Map()
    i = skip(i)
    if (doc[i] !== '}') {
      for (;;) {
        if (doc[i] !== '"') throw decodeError('Expecting property name enclosed in double quotes', doc, i)
        const [key, afterKey] = scanString(i)
        i = skip(afterKey)
        if (doc[i] !== ':') throw decodeError("Expecting ':' delimiter", doc, i)
        const [value, afterValue] = scanOnce(skip(i + 1))
        out.set(key, value)
        i = skip(afterValue)
        if (doc[i] === '}') break
        if (doc[i] !== ',') throw decodeError("Expecting ',' delimiter", doc, i)
        const comma = i
        i = skip(i + 1)
        if (doc[i] === '}') throw decodeError('Illegal trailing comma before end of object', doc, comma)
      }
    }
    return [out, i + 1]
  }

  function scanArray(i: number): [Raw, number] {
    const out: Raw[] = []
    i = skip(i)
    if (doc[i] !== ']') {
      for (;;) {
        const [value, afterValue] = scanOnce(i)
        out.push(value)
        i = skip(afterValue)
        if (doc[i] === ']') break
        if (doc[i] !== ',') throw decodeError("Expecting ',' delimiter", doc, i)
        const comma = i
        i = skip(i + 1)
        if (doc[i] === ']') throw decodeError('Illegal trailing comma before end of array', doc, comma)
      }
    }
    return [out, i + 1]
  }

  const [value, afterValue] = scanOnce(skip(0))
  const end = skip(afterValue)
  if (end !== len) throw decodeError('Extra data', doc, end)
  return value
}

// -- Python's view of those values ---------------------------------------
const isDict = (v: Raw | undefined): v is RawDict => v instanceof Map

function typeName(v: Raw | undefined): string {
  if (v === null || v === undefined) return 'NoneType'
  if (typeof v === 'boolean') return 'bool'
  if (typeof v === 'number') return 'int'
  if (v instanceof PyFloat) return 'float'
  if (typeof v === 'string') return 'str'
  return Array.isArray(v) ? 'list' : 'dict'
}

/** The number a value is in arithmetic (bool counts as 0/1), or undefined if it is not one. */
function numberOf(v: Raw | undefined): number | undefined {
  if (typeof v === 'number') return v
  if (typeof v === 'boolean') return v ? 1 : 0
  if (v instanceof PyFloat) return v.value
  return undefined
}

/** `bool(v)`. */
function truthy(v: Raw | undefined): boolean {
  if (v === null || v === undefined) return false
  if (typeof v === 'boolean') return v
  if (typeof v === 'number') return v !== 0
  if (v instanceof PyFloat) return v.value !== 0
  if (typeof v === 'string' || Array.isArray(v)) return v.length > 0
  return v.size > 0
}

/** `repr(float)`. */
function floatRepr(x: number): string {
  if (Number.isNaN(x)) return 'nan'
  if (!Number.isFinite(x)) return x > 0 ? 'inf' : '-inf'
  if (Object.is(x, -0)) return '-0.0'
  const [mantissa, exponent] = x.toExponential().split('e')
  const exp = Number(exponent)
  if (exp >= -4 && exp < 16) {
    const s = String(x)
    return s.includes('.') ? s : s + '.0'
  }
  return `${mantissa}e${exp < 0 ? '-' : '+'}${String(Math.abs(exp)).padStart(2, '0')}`
}

/** `repr(v)` when `nested`, else `str(v)` — how a value reads inside an f-string. */
function pyStr(v: Raw | undefined, nested = false): string {
  if (v === null || v === undefined) return 'None'
  if (typeof v === 'boolean') return v ? 'True' : 'False'
  if (typeof v === 'number') return String(v)
  if (v instanceof PyFloat) return floatRepr(v.value)
  if (typeof v === 'string') return nested ? pyRepr(v) : v
  if (Array.isArray(v)) return `[${v.map((x) => pyStr(x, true)).join(', ')}]`
  return `{${[...v].map(([k, x]) => `${pyRepr(k)}: ${pyStr(x, true)}`).join(', ')}}`
}

/** `v.get(key)`; an AttributeError-like TypeError when `v` is not a dict, as in Python. */
function getOf(v: Raw | undefined, key: string): Raw | undefined {
  if (!isDict(v)) throw new TypeError(`'${typeName(v)}' object has no attribute 'get'`)
  return v.get(key)
}

const FLOAT_TEXT_RE = /^[+-]?(\d+(_\d+)*\.?(\d+(_\d+)*)?|\.\d+(_\d+)*)([eE][+-]?\d+(_\d+)*)?$/

/** `float(v)`: numbers, bools and numeric text convert; anything else throws as Python does. */
function pyFloat(v: Raw | undefined): number {
  const n = numberOf(v)
  if (n !== undefined) return n
  if (typeof v === 'string') {
    const s = numericStrip(v)
    if (FLOAT_TEXT_RE.test(s)) return Number(s.replaceAll('_', ''))
    const word = /^([+-]?)(inf|infinity|nan)$/i.exec(s)
    if (word) return word[2].toLowerCase() === 'nan' ? NaN : word[1] === '-' ? -Infinity : Infinity
    throw new ValueError(`could not convert string to float: ${pyRepr(v)}`)
  }
  throw new TypeError(`float() argument must be a string or a real number, not '${typeName(v)}'`)
}

/** `a + b` for the few types JSON can hold. */
function pyAdd(a: Raw | undefined, b: Raw | undefined): Raw {
  const x = numberOf(a)
  const y = numberOf(b)
  if (x !== undefined && y !== undefined) {
    return a instanceof PyFloat || b instanceof PyFloat ? new PyFloat(x + y) : x + y
  }
  if (typeof a === 'string' && typeof b === 'string') return a + b
  if (Array.isArray(a) && Array.isArray(b)) return [...a, ...b]
  if (typeof a === 'string' || Array.isArray(a)) {
    throw new TypeError(`can only concatenate ${typeName(a)} (not "${typeName(b)}") to ${typeName(a)}`)
  }
  throw new TypeError(`unsupported operand type(s) for +: '${typeName(a)}' and '${typeName(b)}'`)
}

/** The number in `a`, for an ordering comparison against a float constant. */
function comparable(a: Raw | undefined, op: string): number {
  const n = numberOf(a)
  if (n === undefined) {
    throw new TypeError(`'${op}' not supported between instances of '${typeName(a)}' and 'float'`)
  }
  return n
}

/** `f'{x:.1f}'`. JS `toFixed` rounds an exact tie (x.25, x.75) up; Python rounds it to even. */
function fixed1(x: number): string {
  if (Number.isNaN(x)) return 'nan'
  if (!Number.isFinite(x)) return x > 0 ? 'inf' : '-inf'
  if (Number.isInteger(x * 4) && Math.abs(x * 4) % 2 === 1 && Math.abs(x) < 2 ** 50) {
    const lower = Math.floor(Math.abs(x) * 10)
    const tenths = lower % 2 === 0 ? lower : lower + 1
    return `${x < 0 ? '-' : ''}${Math.floor(tenths / 10)}.${tenths % 10}`
  }
  return Object.is(x, -0) ? '-0.0' : x.toFixed(1)
}

function toPlain(v: Raw): JsonValue {
  if (v instanceof PyFloat) return v.value
  if (Array.isArray(v)) return v.map(toPlain)
  if (isDict(v)) {
    const out: { [key: string]: JsonValue } = {}
    // defineProperty, so a key named '__proto__' is stored like any other.
    for (const [k, x] of v) {
      Object.defineProperty(out, k, { value: toPlain(x), enumerable: true, writable: true, configurable: true })
    }
    return out
  }
  return v
}

/** `Path.read_text()`: UTF-8, strict, with \r\n and \r read as \n. */
function readText(path: string): string {
  let text: string
  try {
    text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(readFileSync(path))
  } catch (exc) {
    if (exc instanceof TypeError) throw new ValueError(`'utf-8' codec can't decode ${basename(path)}`)
    throw exc
  }
  return text.replace(/\r\n?/g, '\n')
}

/** Read and validate the calibration file. */
export function loadCalibration(path: string): Calibration {
  const name = basename(path)
  let isFile = false
  try {
    isFile = statSync(path).isFile()
  } catch {
    isFile = false
  }
  if (!isFile) throw new CalibrationError(`${name} not found — cannot print without measured geometry`)
  const text = readText(path)
  let raw: Raw
  try {
    raw = pyJsonLoads(text)
  } catch (exc) {
    if (exc instanceof JsonDecodeError) throw new CalibrationError(`${name} is not valid JSON: ${exc.message}`)
    throw exc
  }

  if (!isDict(raw) || !raw.has('fields')) throw new CalibrationError(`${name} has no "fields" section`)
  const version = raw.get('version')
  if (typeof version !== 'number' && typeof version !== 'boolean') {
    throw new CalibrationError(`${name} needs an integer "version"`)
  }

  const leafRaw = raw.get('leaf')
  if (truthy(leafRaw)) {
    const w = getOf(leafRaw, 'w')
    const h = getOf(leafRaw, 'h')
    if (numberOf(w) !== CHEQUE_W_MM || numberOf(h) !== CHEQUE_H_MM) {
      throw new CalibrationError(
        `${name} is for a ${pyStr(w)}×${pyStr(h)}mm leaf, ` +
          `but this app is built for ${CHEQUE_W_MM}×${CHEQUE_H_MM}mm`
      )
    }
  }

  const fieldsRaw = raw.get('fields')
  if (!isDict(fieldsRaw)) throw new TypeError(`'${typeName(fieldsRaw)}' object has no attribute 'items'`)
  const fields = new Map<string, RawDict>()
  for (const [k, v] of fieldsRaw) {
    if (!k.startsWith('_') && isDict(v)) fields.set(k, v)
  }
  const missing = REQUIRED_FIELDS.filter((k) => !fields.has(k))
  if (missing.length) throw new CalibrationError(`${name} is missing field(s): ${missing.join(', ')}`)

  const number = (key: string): number => pyFloat(raw.has(key) ? raw.get(key) : 0)
  // Evaluated in the order Python evaluates them, so the first bad value is the one reported.
  const dx = number('dx')
  const dy = number('dy')
  const flip = truthy(raw.get('flip'))
  const paper = raw.get('paper') === 'a4' ? 'a4' : 'exact'
  const a4x = number('a4x')
  const a4y = number('a4y')
  return new Calibration({
    version: version as number,
    dx,
    dy,
    flip,
    paper,
    a4x,
    a4y,
    fields: toPlain(fields) as Record<string, CalibrationField>,
    source: path,
    warnings: geometryWarnings(fields, number('dy'))
  })
}

/**
 * Flag geometry that would print somewhere it must not.
 *
 * These are warnings rather than refusals: the crossing is deliberately
 * anchored inside the printer's non-printable margin, so a blanket refusal
 * would block a settled design decision.
 */
function geometryWarnings(fields: Map<string, RawDict>, dy: number): string[] {
  const warnings: string[] = []
  for (const [name, f] of fields) {
    const y = f.get('y')
    if (y === null || y === undefined) continue
    const size = f.get('size')
    const h = f.get('h')
    const height = truthy(size) ? size : truthy(h) ? h : 0
    const bottom = comparable(pyAdd(pyAdd(pyAdd(y, height), 2), new PyFloat(dy)), '>=')
    if (bottom >= MICR_TOP_MM) {
      warnings.push(`field "${name}" reaches y=${fixed1(bottom)}mm, inside the MICR band at ${MICR_TOP_MM}mm`)
    }
    if (name !== 'cross') {
      const x = f.has('x') ? f.get('x') : 0
      if (comparable(x, '<') < HARDWARE_INSET_MM || comparable(y, '<') < HARDWARE_INSET_MM) {
        warnings.push(
          `field "${name}" starts within the ${HARDWARE_INSET_MM}mm non-printable margin ` +
            'and may not receive toner'
        )
      }
    }
  }
  return warnings
}
