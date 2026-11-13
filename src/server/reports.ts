/**
 * Financial-year reports on what the desk has paid.
 *
 * An Indian association keeps its books April to March, so every period here is a
 * financial year or one of its quarters — Q1 is April to June, Q4 is January to
 * March of the following calendar year. "FY 2026–27" is the year that starts on
 * 1 April 2026, and is keyed by that start year throughout.
 *
 * What counts as paid is what counts as a leaf issued everywhere else: printed,
 * handwritten and still-awaiting-confirmation cheques. Void and misprinted ones
 * represent no payment and are left out.
 *
 * Money stays integer paise end to end; a share is the only float, and it is
 * a display fraction, never an amount.
 *
 * Dates are ISO strings (YYYY-MM-DD); all date arithmetic is done in UTC on
 * the calendar parts.
 */

export const MONTH_NAMES = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'] as const
// The fixed heads the treasurer reports under. An entity's free-text category
// is only a note about what it is; these are what the books add up to.
export const HEADS = ['Salary', 'Utilities', 'AMC', 'Misc', 'GST / Tax'] as const
export const MISC_HEAD = 'Misc'
export const UNCATEGORISED = 'Uncategorised'

/** An issued cheque as it comes out of the register; only the fields read here. */
export interface ReportChequeRow {
  id: number
  cheque_no: string
  payee: string
  amount_paise: number
  cheque_date: string
  memo: string
  entity_id?: number | null
}

/** A row of the entity list; only the fields read here. */
export interface ReportEntityRow {
  id: number
  payee_name: string
  entity_name: string
  head?: string | null
  kind?: string
}

export interface FyOption {
  fy: number
  label: string
}

export interface ReportColumn {
  key: string
  label: string
}

export interface ReportEntity {
  entity_id: number | null
  entity_name: string
  payee_name: string
  category: string
  kind: string
  count: number
  paise: number
  largest_paise: number
  last_date: string
  columns: number[]
  share: number
  average_paise: number
  previous_paise: number
}

export interface ReportCategory {
  category: string
  count: number
  paise: number
  share: number
  entity_count: number
  previous_paise: number
  columns: number[]
}

export interface ReportPayee {
  payee: string
  count: number
  paise: number
  share: number
  entities: string[]
}

export interface ReportQuarter {
  quarter: number
  label: string
  name: string
  start: string
  end: string
  count: number
  paise: number
  state: 'future' | 'current' | 'closed'
}

export interface ReportMonth {
  month: string
  label: string
  quarter: number
  count: number
  paise: number
  future: boolean
  heads: Record<string, number>
}

export interface ReportLargest {
  id: number
  cheque_no: string
  cheque_date: string
  payee: string
  entity_name: string
  category: string
  amount_paise: number
  memo: string
}

export interface Report {
  fy: number
  quarter: number | null
  label: string
  start: string
  end: string
  years: FyOption[]
  columns: ReportColumn[]
  column_totals: number[]
  totals: {
    paise: number
    count: number
    entity_count: number
    payee_count: number
    category_count: number
    average_paise: number
    per_month_paise: number
    months_elapsed: number
    since: string
    previous: { label: string; paise: number; count: number; per_month_paise: number; months_elapsed: number }
    in_progress: boolean
  }
  quarters: ReportQuarter[]
  months: ReportMonth[]
  entities: ReportEntity[]
  categories: ReportCategory[]
  payees: ReportPayee[]
  largest: ReportLargest[]
}

export interface MonthlyEntity {
  head: string
  name: string
  count: number
  paise: number
}

export interface MonthlyMonth {
  month: string
  label: string
  count: number
  paise: number
  future: boolean
  current: boolean
  heads: Record<string, number>
  entities: MonthlyEntity[]
}

export interface MonthlyReport {
  mode: 'last' | 'fy'
  fy: number | null
  last: number | null
  label: string
  months: MonthlyMonth[]
  heads: { head: string; paise: number }[]
  totals: { paise: number; count: number; months_paid: number; per_month_paise: number }
  years: FyOption[]
}

// ---- Python built-ins, mirrored ------------------------------------------

interface Ymd {
  year: number
  month: number
  day: number
}

function daysInMonth(year: number, month: number): number {
  if (month === 2) return year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28
  return month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31
}

/** date.fromisoformat, for the YYYY-MM-DD form the register stores. */
function parseIso(value: string): Ymd {
  const match = typeof value === 'string' ? /^(\d{4})-(\d{2})-(\d{2})$/.exec(value) : null
  if (match) {
    const year = Number(match[1])
    const month = Number(match[2])
    const day = Number(match[3])
    if (year >= 1 && month >= 1 && month <= 12 && day >= 1 && day <= daysInMonth(year, month)) {
      return { year, month, day }
    }
  }
  throw new Error(`Invalid isoformat string: '${value}'`)
}

function pad(value: number, width: number): string {
  return String(value).padStart(width, '0')
}

function formatIso(year: number, month: number, day: number): string {
  return `${pad(year, 4)}-${pad(month, 2)}-${pad(day, 2)}`
}

/** The day before an ISO date, stepped in UTC so no timezone or DST can shift it. */
function dayBefore(iso: string): string {
  const { year, month, day } = parseIso(iso)
  const at = new Date(0)
  at.setUTCFullYear(year, month - 1, day - 1)
  return formatIso(at.getUTCFullYear(), at.getUTCMonth() + 1, at.getUTCDate())
}

/** d.replace(day=1), on an ISO date that has already been parsed once. */
function firstOfMonth(iso: string): string {
  return `${iso.slice(0, 8)}01`
}

/**
 * Today: the calendar date on this machine's clock, in its local timezone.
 */
function localToday(): string {
  const now = new Date()
  return formatIso(now.getFullYear(), now.getMonth() + 1, now.getDate())
}

/** Python's `//` on integers: rounds towards minus infinity, not towards zero. */
function floorDiv(a: number, b: number): number {
  return Math.floor(a / b)
}

/**
 * round(x, digits): correctly rounded on the exact value of the double, an
 * exact half going to the even digit. toFixed is exact too but sends halves
 * up, so those are found from the full decimal expansion and put right.
 */
function roundTo(x: number, digits: number): number {
  if (!Number.isFinite(x) || Math.abs(x) >= 1e21) return x
  const magnitude = Math.abs(x)
  const exact = magnitude.toFixed(100)
  const point = exact.indexOf('.')
  const tail = exact.slice(point + 1 + digits)
  let text = magnitude.toFixed(digits)
  if (tail[0] === '5' && /^0*$/.test(tail.slice(1))) {
    const down = exact.slice(0, point + 1 + digits)
    if (Number(down[down.length - 1]) % 2 === 0) text = down
  }
  const rounded = Number(text)
  return x < 0 || Object.is(x, -0) ? -rounded : rounded
}

/** Python orders strings by code point; JavaScript's `<` goes by UTF-16 unit, which differs above the BMP. */
function compareStrings(a: string, b: string): number {
  const x = Array.from(a)
  const y = Array.from(b)
  const shared = Math.min(x.length, y.length)
  for (let i = 0; i < shared; i++) {
    const diff = (x[i].codePointAt(0) as number) - (y[i].codePointAt(0) as number)
    if (diff !== 0) return diff
  }
  return x.length - y.length
}

// The code points where Python's str.casefold() differs from lowercasing
// (Unicode 16, as in Python 3.14). `a-b:d` shifts a run by d; `c=x.y` maps one
// code point to a sequence. Everything else folds as it lowercases.
const FOLD_EXCEPTIONS =
  'b5=3bc df=73.73 149=2bc.6e 17f=73 1f0=6a.30c 345=3b9 390=3b9.308.301 3b0=3c5.308.301 3c2=3c3 3d0=3b2 ' +
  '3d1=3b8 3d5=3c6 3d6=3c0 3f0=3ba 3f1=3c1 3f5=3b5 587=565.582 13a0-13f5:0 13f8-13fd:-8 1c80=432 ' +
  '1c81=434 1c82=43e 1c83-1c84:-6210 1c85=442 1c86=44a 1c87=463 1c88=a64b 1e96=68.331 1e97=74.308 ' +
  '1e98=77.30a 1e99=79.30a 1e9a=61.2be 1e9b=1e61 1e9e=73.73 1f50=3c5.313 1f52=3c5.313.300 ' +
  '1f54=3c5.313.301 1f56=3c5.313.342 1f80=1f00.3b9 1f81=1f01.3b9 1f82=1f02.3b9 1f83=1f03.3b9 ' +
  '1f84=1f04.3b9 1f85=1f05.3b9 1f86=1f06.3b9 1f87=1f07.3b9 1f88=1f00.3b9 1f89=1f01.3b9 1f8a=1f02.3b9 ' +
  '1f8b=1f03.3b9 1f8c=1f04.3b9 1f8d=1f05.3b9 1f8e=1f06.3b9 1f8f=1f07.3b9 1f90=1f20.3b9 1f91=1f21.3b9 ' +
  '1f92=1f22.3b9 1f93=1f23.3b9 1f94=1f24.3b9 1f95=1f25.3b9 1f96=1f26.3b9 1f97=1f27.3b9 1f98=1f20.3b9 ' +
  '1f99=1f21.3b9 1f9a=1f22.3b9 1f9b=1f23.3b9 1f9c=1f24.3b9 1f9d=1f25.3b9 1f9e=1f26.3b9 1f9f=1f27.3b9 ' +
  '1fa0=1f60.3b9 1fa1=1f61.3b9 1fa2=1f62.3b9 1fa3=1f63.3b9 1fa4=1f64.3b9 1fa5=1f65.3b9 1fa6=1f66.3b9 ' +
  '1fa7=1f67.3b9 1fa8=1f60.3b9 1fa9=1f61.3b9 1faa=1f62.3b9 1fab=1f63.3b9 1fac=1f64.3b9 1fad=1f65.3b9 ' +
  '1fae=1f66.3b9 1faf=1f67.3b9 1fb2=1f70.3b9 1fb3=3b1.3b9 1fb4=3ac.3b9 1fb6=3b1.342 1fb7=3b1.342.3b9 ' +
  '1fbc=3b1.3b9 1fbe=3b9 1fc2=1f74.3b9 1fc3=3b7.3b9 1fc4=3ae.3b9 1fc6=3b7.342 1fc7=3b7.342.3b9 ' +
  '1fcc=3b7.3b9 1fd2=3b9.308.300 1fd3=3b9.308.301 1fd6=3b9.342 1fd7=3b9.308.342 1fe2=3c5.308.300 ' +
  '1fe3=3c5.308.301 1fe4=3c1.313 1fe6=3c5.342 1fe7=3c5.308.342 1ff2=1f7c.3b9 1ff3=3c9.3b9 1ff4=3ce.3b9 ' +
  '1ff6=3c9.342 1ff7=3c9.342.3b9 1ffc=3c9.3b9 ab70-abbf:-38864 fb00=66.66 fb01=66.69 fb02=66.6c ' +
  'fb03=66.66.69 fb04=66.66.6c fb05=73.74 fb06=73.74 fb13=574.576 fb14=574.565 fb15=574.56b ' +
  'fb16=57e.576 fb17=574.56d'

let foldExceptions: Map<number, string> | undefined

function foldTable(): Map<number, string> {
  if (foldExceptions) return foldExceptions
  const table = new Map<number, string>()
  for (const item of FOLD_EXCEPTIONS.split(' ')) {
    if (item.includes('=')) {
      const [from, to] = item.split('=')
      table.set(parseInt(from, 16), String.fromCodePoint(...to.split('.').map((cp) => parseInt(cp, 16))))
    } else {
      const [run, shift] = item.split(':')
      const [first, last] = run.split('-').map((cp) => parseInt(cp, 16))
      for (let cp = first; cp <= last; cp++) table.set(cp, String.fromCodePoint(cp + Number(shift)))
    }
  }
  foldExceptions = table
  return table
}

/**
 * Python's str.casefold(): caseless-comparison form, stronger than lowercasing
 * ('ß' becomes 'ss'). Each code point is folded on its own, as Python does.
 */
export function casefold(text: string): string {
  // Plain ASCII, which is nearly every name, folds exactly as it lowercases.
  if (/^[\x00-\x7f]*$/.test(text)) return text.toLowerCase()
  const table = foldTable()
  let out = ''
  for (const ch of text) out += table.get(ch.codePointAt(0) as number) ?? ch.toLowerCase()
  return out
}

function sum(values: Iterable<number>): number {
  let total = 0
  for (const value of values) total += value
  return total
}

// ---------------------------------------------------------------------------

/** The start year of the financial year a date falls in. */
export function fyOf(d: string): number {
  const { year, month } = parseIso(d)
  return month >= 4 ? year : year - 1
}

export function fyLabel(fy: number): string {
  return `FY ${fy}–${String(fy + 1).slice(-2)}`
}

function quarterOfMonth(month: number): number {
  return Math.floor(((((month - 4) % 12) + 12) % 12) / 3) + 1
}

/** 1..4 within the financial year: Apr–Jun is 1, Jan–Mar is 4. */
export function quarterOf(d: string): number {
  return quarterOfMonth(parseIso(d).month)
}

/** [year, month] for April of fy through March of fy + 1. */
export function fyMonths(fy: number): [number, number][] {
  return [4, 5, 6, 7, 8, 9, 10, 11, 12, 1, 2, 3].map((m) => [fy + (m < 4 ? 1 : 0), m])
}

function quarterMonths(fy: number, quarter: number): [number, number][] {
  return fyMonths(fy).slice((quarter - 1) * 3, quarter * 3)
}

/** First and last day of a financial year, or of one of its quarters. */
export function periodBounds(fy: number, quarter?: number | null): [string, string] {
  const months = quarter ? quarterMonths(fy, quarter) : fyMonths(fy)
  // A quarter outside 1..4 leaves no months and this fails.
  if (months.length === 0) throw new RangeError('list index out of range')
  const [y0, m0] = months[0]
  const [y1, m1] = months[months.length - 1]
  const end = formatIso(y1 + (m1 === 12 ? 1 : 0), (m1 % 12) + 1, 1)
  return [formatIso(y0, m0, 1), dayBefore(end)]
}

export function periodLabel(fy: number, quarter?: number | null): string {
  if (!quarter) return fyLabel(fy)
  const months = quarterMonths(fy, quarter)
  if (months.length === 0) throw new RangeError('list index out of range')
  const first = MONTH_NAMES.at(months[0][1] - 1)
  const last = MONTH_NAMES.at(months[months.length - 1][1] - 1)
  return `Q${quarter} ${fyLabel(fy)} (${first}–${last})`
}

export function previousPeriod(fy: number, quarter?: number | null): [number, number | null] {
  if (!quarter) return [fy - 1, null]
  return quarter > 1 ? [fy, quarter - 1] : [fy - 1, 4]
}

/**
 * The head an entity's cheques report under. A one-off with none chosen is
 * Misc; anything else unassigned stays visibly Uncategorised, never guessed.
 */
export function headOf(entity?: Pick<ReportEntityRow, 'head' | 'kind'> | null): string {
  if (entity && typeof entity.head === 'string' && (HEADS as readonly string[]).includes(entity.head)) {
    return entity.head
  }
  return entity && entity.kind === 'one_off' ? MISC_HEAD : UNCATEGORISED
}

function entityIndex(entities: Iterable<ReportEntityRow>): Map<number, ReportEntityRow> {
  const index = new Map<number, ReportEntityRow>()
  for (const e of entities) index.set(e.id, e)
  return index
}

function entityFor(index: Map<number, ReportEntityRow>, row: ReportChequeRow): ReportEntityRow | undefined {
  return row.entity_id === null || row.entity_id === undefined ? undefined : index.get(row.entity_id)
}

function yearsWithData(rows: readonly ReportChequeRow[], today: string): FyOption[] {
  const years = new Set(rows.map((r) => fyOf(r.cheque_date)))
  years.add(fyOf(today))
  return [...years].sort((a, b) => b - a).map((y) => ({ fy: y, label: fyLabel(y) }))
}

/**
 * Every report for one period, from issued cheques and the entity list.
 *
 * rows are issued cheques (any period — the full list is used to offer the
 * years that have data and to compare against the period before).
 *
 * `today` defaults to the local calendar date, as date.today() did.
 */
export function build(
  rowsIn: Iterable<ReportChequeRow>,
  entities: Iterable<ReportEntityRow>,
  fy: number,
  quarter?: number | null,
  todayIn?: string | null
): Report {
  const today = todayIn || localToday()
  parseIso(today)
  const rows = [...rowsIn]
  // Every date is parsed up front, so a bad one fails before anything else.
  for (const r of rows) parseIso(r.cheque_date)
  const ent = entityIndex(entities)

  const categoryOf = (row: ReportChequeRow): string => headOf(entityFor(ent, row))

  // The key stands in for Python's dict key: the entity id, or ('payee', name).
  const entityOf = (row: ReportChequeRow): { key: string; id: number | null; name: string } => {
    const e = entityFor(ent, row)
    if (e === undefined) return { key: `payee:${row.payee}`, id: null, name: row.payee }
    return { key: `entity:${e.id}`, id: e.id, name: e.entity_name || e.payee_name }
  }

  const amountOf = (r: ReportChequeRow): number => r.amount_paise

  const [start, end] = periodBounds(fy, quarter)
  const inPeriod = rows.filter((r) => start <= r.cheque_date && r.cheque_date <= end)
  const fyRows = rows.filter((r) => fyOf(r.cheque_date) === fy)
  const [pFy, pQ] = previousPeriod(fy, quarter)
  const [pStart, pEnd] = periodBounds(pFy, pQ)
  const prev = rows.filter((r) => pStart <= r.cheque_date && r.cheque_date <= pEnd)

  const total = sum(inPeriod.map(amountOf))

  const share = (paise: number): number => (total ? roundTo(paise / total, 4) : 0.0)

  // Columns that break a row down across the period: the four quarters of
  // a year, or the three months of a quarter.
  let columns: ReportColumn[]
  let columnOf: (r: ReportChequeRow) => string
  if (quarter) {
    columns = quarterMonths(fy, quarter).map(([y, m]) => ({
      key: `${y}-${pad(m, 2)}`,
      label: `${MONTH_NAMES[m - 1]} ${String(y).slice(-2)}`
    }))
    columnOf = (r) => r.cheque_date.slice(0, 7)
  } else {
    columns = [1, 2, 3, 4].map((q) => ({ key: `Q${q}`, label: `Q${q}` }))
    columnOf = (r) => `Q${quarterOf(r.cheque_date)}`
  }
  const emptyColumns = (): Map<string, number> => new Map(columns.map((c) => [c.key, 0]))
  const addToColumn = (into: Map<string, number>, r: ReportChequeRow): void => {
    const key = columnOf(r)
    into.set(key, (into.get(key) as number) + amountOf(r))
  }
  const columnList = (from: Map<string, number>): number[] => columns.map((c) => from.get(c.key) as number)

  // -- by entity
  interface EntityTally {
    entity_id: number | null
    entity_name: string
    payee_name: string
    category: string
    kind: string
    count: number
    paise: number
    largest_paise: number
    last_date: string
    columns: Map<string, number>
  }
  const byEntity = new Map<string, EntityTally>()
  for (const r of inPeriod) {
    const { key, id, name } = entityOf(r)
    const e = entityFor(ent, r)
    let s = byEntity.get(key)
    if (!s) {
      s = {
        entity_id: id,
        entity_name: name,
        payee_name: e ? e.payee_name : r.payee,
        category: categoryOf(r),
        kind: e && e.kind !== undefined ? e.kind : 'regular',
        count: 0,
        paise: 0,
        largest_paise: 0,
        last_date: '',
        columns: emptyColumns()
      }
      byEntity.set(key, s)
    }
    const amt = amountOf(r)
    s.count += 1
    s.paise += amt
    s.largest_paise = Math.max(s.largest_paise, amt)
    s.last_date = s.last_date >= r.cheque_date ? s.last_date : r.cheque_date
    addToColumn(s.columns, r)
  }

  const prevByEntity = new Map<string, number>()
  for (const r of prev) {
    const { key } = entityOf(r)
    prevByEntity.set(key, (prevByEntity.get(key) ?? 0) + amountOf(r))
  }

  const entitiesOut: ReportEntity[] = []
  for (const [key, s] of byEntity) {
    entitiesOut.push({
      entity_id: s.entity_id,
      entity_name: s.entity_name,
      payee_name: s.payee_name,
      category: s.category,
      kind: s.kind,
      count: s.count,
      paise: s.paise,
      largest_paise: s.largest_paise,
      last_date: s.last_date,
      columns: columnList(s.columns),
      share: share(s.paise),
      average_paise: floorDiv(s.paise, s.count),
      previous_paise: prevByEntity.get(key) ?? 0
    })
  }
  const foldedName = new Map(entitiesOut.map((s) => [s, casefold(s.entity_name)]))
  entitiesOut.sort(
    (a, b) => b.paise - a.paise || compareStrings(foldedName.get(a) as string, foldedName.get(b) as string)
  )

  // -- by category (expense head)
  // Every head is always listed, so a nil quarter reads as nil, not missing.
  interface HeadTally {
    count: number
    paise: number
    entities: Set<string>
    columns: Map<string, number>
  }
  const newHead = (): HeadTally => ({ count: 0, paise: 0, entities: new Set(), columns: emptyColumns() })

  const byCat = new Map<string, HeadTally>(HEADS.map((name) => [name, newHead()]))
  for (const r of inPeriod) {
    const name = categoryOf(r)
    let s = byCat.get(name)
    if (!s) {
      s = newHead()
      byCat.set(name, s)
    }
    s.count += 1
    s.paise += amountOf(r)
    s.entities.add(entityOf(r).key)
    addToColumn(s.columns, r)
  }
  const prevByCat = new Map<string, number>()
  for (const r of prev) {
    const name = categoryOf(r)
    prevByCat.set(name, (prevByCat.get(name) ?? 0) + amountOf(r))
  }
  const categoriesOut: ReportCategory[] = []
  for (const [name, s] of byCat) {
    categoriesOut.push({
      category: name,
      count: s.count,
      paise: s.paise,
      share: share(s.paise),
      entity_count: s.entities.size,
      previous_paise: prevByCat.get(name) ?? 0,
      columns: columnList(s.columns)
    })
  }
  categoriesOut.sort(
    (a, b) => Number(a.category === UNCATEGORISED) - Number(b.category === UNCATEGORISED) || b.paise - a.paise
  )

  // -- by printed payee: what the bank sees going to one account, however
  // many entities the desk splits it into.
  const byPayee = new Map<string, { payee: string; count: number; paise: number; entities: Set<string> }>()
  for (const r of inPeriod) {
    let s = byPayee.get(r.payee)
    if (!s) {
      s = { payee: r.payee, count: 0, paise: 0, entities: new Set() }
      byPayee.set(r.payee, s)
    }
    s.count += 1
    s.paise += amountOf(r)
    s.entities.add(entityOf(r).name)
  }
  const payeesOut: ReportPayee[] = [...byPayee.values()].map((s) => ({
    payee: s.payee,
    count: s.count,
    paise: s.paise,
    share: share(s.paise),
    entities: [...s.entities].sort(compareStrings)
  }))
  payeesOut.sort((a, b) => b.paise - a.paise)

  // -- the year at a glance: every quarter and month of the financial year,
  // whichever period is selected, so the selection has context.
  const quartersOut: ReportQuarter[] = []
  for (const q of [1, 2, 3, 4]) {
    const [qStart, qEnd] = periodBounds(fy, q)
    const picked = fyRows.filter((r) => quarterOf(r.cheque_date) === q)
    quartersOut.push({
      quarter: q,
      label: `Q${q}`,
      name: periodLabel(fy, q),
      start: qStart,
      end: qEnd,
      count: picked.length,
      paise: sum(picked.map(amountOf)),
      state: qStart > today ? 'future' : qStart <= today && today <= qEnd ? 'current' : 'closed'
    })
  }
  const monthsOut: ReportMonth[] = []
  for (const [y, m] of fyMonths(fy)) {
    const key = `${y}-${pad(m, 2)}`
    const picked = fyRows.filter((r) => r.cheque_date.slice(0, 7) === key)
    const heads = new Map<string, number>()
    for (const r of picked) {
      const name = categoryOf(r)
      heads.set(name, (heads.get(name) ?? 0) + amountOf(r))
    }
    monthsOut.push({
      month: key,
      label: MONTH_NAMES[m - 1],
      quarter: quarterOfMonth(m),
      count: picked.length,
      paise: sum(picked.map(amountOf)),
      future: formatIso(y, m, 1) > today,
      heads: Object.fromEntries(heads)
    })
  }

  const largest = [...inPeriod]
    .sort((a, b) => amountOf(b) - amountOf(a) || compareStrings(a.cheque_date, b.cheque_date))
    .slice(0, 10)
  const largestOut: ReportLargest[] = largest.map((r) => ({
    id: r.id,
    cheque_no: r.cheque_no,
    cheque_date: r.cheque_date,
    payee: r.payee,
    entity_name: entityOf(r).name,
    category: categoryOf(r),
    amount_paise: amountOf(r),
    memo: r.memo
  }))

  const prevTotal = sum(prev.map(amountOf))
  const first = rows.reduce((least, r) => (r.cheque_date < least ? r.cheque_date : least), rows[0]?.cheque_date ?? today)

  /**
   * Average per month over the months of a period that have begun,
   * counted from when the register did — a desk started in August has no
   * April to average over. The current month counts as whole, so a bill paid
   * on the 2nd is not divided by a sliver of a month. A cheque post-dated
   * into a month not yet begun stays out, or its amount would be shared
   * among months it was not paid in.
   */
  const monthly = (picked: readonly ReportChequeRow[], s: string, e: string): [number, number, string] => {
    const firstMonth = firstOfMonth(first)
    const since = s >= firstMonth ? s : firstMonth
    const last = e <= today ? e : today
    const a = parseIso(since)
    const b = parseIso(last)
    const months = since <= last ? (b.year - a.year) * 12 + b.month - a.month + 1 : 0
    const paise = sum(picked.filter((r) => firstOfMonth(r.cheque_date) <= today).map(amountOf))
    return [months ? floorDiv(paise, months) : 0, months, since]
  }

  const [perMonth, monthsElapsed, since] = monthly(inPeriod, start, end)
  const [prevPerMonth, prevMonths] = monthly(prev, pStart, pEnd)

  return {
    fy,
    quarter: quarter ?? null,
    label: periodLabel(fy, quarter),
    start,
    end,
    years: yearsWithData(rows, today),
    columns,
    column_totals: columns.map((_, i) => sum(categoriesOut.map((s) => s.columns[i]))),
    totals: {
      paise: total,
      count: inPeriod.length,
      entity_count: byEntity.size,
      payee_count: byPayee.size,
      category_count: [...byCat.values()].filter((s) => s.count).length,
      average_paise: inPeriod.length ? floorDiv(total, inPeriod.length) : 0,
      per_month_paise: perMonth,
      months_elapsed: monthsElapsed,
      since,
      previous: {
        label: periodLabel(pFy, pQ),
        paise: prevTotal,
        count: prev.length,
        per_month_paise: prevPerMonth,
        months_elapsed: prevMonths
      },
      in_progress: start <= today && today <= end
    },
    quarters: quartersOut,
    months: monthsOut,
    entities: entitiesOut,
    categories: categoriesOut,
    payees: payeesOut,
    largest: largestOut
  }
}

/**
 * Spend per month under each head, with the entities behind it.
 *
 * Either one financial year (April–March, every month listed so a nil
 * month reads as nil) or the last `last` months ending with the current one,
 * which may straddle two years.
 *
 * `today` defaults to the local calendar date, as date.today() did.
 */
export function monthly(
  rowsIn: Iterable<ReportChequeRow>,
  entities: Iterable<ReportEntityRow>,
  fy?: number | null,
  last?: number | null,
  todayIn?: string | null
): MonthlyReport {
  const today = todayIn || localToday()
  const now = parseIso(today)
  const rows = [...rowsIn]
  const ent = entityIndex(entities)
  let fyOut = fy ?? null
  let months: [number, number][]
  let label: string
  if (last) {
    let y = now.year
    let m = now.month
    months = []
    for (let i = 0; i < last; i++) {
      months.unshift([y, m])
      if (m > 1) m -= 1
      else {
        y -= 1
        m = 12
      }
    }
    label = `Last ${last} months`
  } else {
    fyOut = fyOut !== null ? fyOut : fyOf(today)
    months = fyMonths(fyOut)
    label = fyLabel(fyOut)
  }
  const keys = months.map(([y, m]) => `${y}-${pad(m, 2)}`)

  interface MonthTally {
    month: string
    label: string
    count: number
    paise: number
    future: boolean
    current: boolean
    heads: Map<string, number>
    entities: Map<string, MonthlyEntity>
  }
  const out = new Map<string, MonthTally>()
  months.forEach(([y, m], i) => {
    out.set(keys[i], {
      month: keys[i],
      label: `${MONTH_NAMES[m - 1]} ${y}`,
      count: 0,
      paise: 0,
      future: formatIso(y, m, 1) > today,
      current: y === now.year && m === now.month,
      heads: new Map(),
      entities: new Map()
    })
  })
  for (const r of rows) {
    const s = out.get(r.cheque_date.slice(0, 7))
    if (s === undefined) continue
    const e = entityFor(ent, r)
    const head = headOf(e)
    const name = e ? e.entity_name || e.payee_name : r.payee
    const amt = r.amount_paise
    s.count += 1
    s.paise += amt
    s.heads.set(head, (s.heads.get(head) ?? 0) + amt)
    const key = JSON.stringify([head, name])
    let x = s.entities.get(key)
    if (!x) {
      x = { head, name, count: 0, paise: 0 }
      s.entities.set(key, x)
    }
    x.count += 1
    x.paise += amt
  }
  const seen = new Set<string>()
  for (const s of out.values()) for (const h of s.heads.keys()) seen.add(h)
  const fixed: readonly string[] = HEADS
  const heads = [...fixed, ...[...seen].filter((h) => !fixed.includes(h)).sort(compareStrings)]
  const monthList: MonthlyMonth[] = keys.map((k) => {
    const s = out.get(k) as MonthTally
    const folded = new Map([...s.entities.values()].map((x) => [x, casefold(x.name)]))
    return {
      month: s.month,
      label: s.label,
      count: s.count,
      paise: s.paise,
      future: s.future,
      current: s.current,
      heads: Object.fromEntries(heads.map((h) => [h, s.heads.get(h) ?? 0])),
      entities: [...s.entities.values()].sort(
        (a, b) => b.paise - a.paise || compareStrings(folded.get(a) as string, folded.get(b) as string)
      )
    }
  })
  const headTotals = heads.map((h) => ({ head: h, paise: sum(monthList.map((s) => s.heads[h])) }))
  const total = sum(monthList.map((s) => s.paise))
  const paid = monthList.filter((s) => s.count)
  return {
    mode: last ? 'last' : 'fy',
    fy: fyOut,
    last: last ?? null,
    label,
    months: monthList,
    heads: headTotals,
    totals: {
      paise: total,
      count: sum(monthList.map((s) => s.count)),
      months_paid: paid.length,
      per_month_paise: paid.length ? floorDiv(total, paid.length) : 0
    },
    years: yearsWithData(rows, today)
  }
}
