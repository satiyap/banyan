/**
 * Finds the rhythm in the cheque register.
 *
 * A treasurer pays the same handful of people over and over: the security
 * agency every month, the tanker supplier every week or two, the auditor once a
 * year. This module reads the issued-cheque history and works out, per payee, how
 * often a cheque goes out, how regular that is, roughly how much it is for, and
 * when the next one is due.
 *
 * The statistics are deliberately robust rather than clever. The median and the
 * median absolute deviation are used throughout, because one emergency plumbing
 * cheque should not convince the app that a monthly payment is now fortnightly.
 *
 * Dates are ISO strings (YYYY-MM-DD); all date arithmetic is done in UTC on
 * the calendar parts.
 */

// Named cadences and the tolerance, in days, within which a median interval is
// recognised as that cadence. Values are real calendar averages, not round
// numbers: a monthly payment averages 30.44 days, not 30.
export const CADENCES: ReadonlyArray<readonly [name: string, period: number, tolerance: number]> = [
  ['weekly', 7.0, 2.0],
  ['fortnightly', 14.0, 3.5],
  ['monthly', 30.44, 5.0],
  ['two-monthly', 60.9, 8.0],
  ['quarterly', 91.3, 12.0],
  ['half-yearly', 182.6, 20.0],
  ['yearly', 365.25, 30.0]
]

export const MIN_FOR_CADENCE = 3 // cheques needed before a rhythm is claimed
export const DUE_SOON_DAYS = 10 // how far ahead the "due soon" window reaches

/** An issued cheque as it comes out of the register; only the fields read here. */
export interface ChequeRow {
  id: number
  cheque_no: string
  payee: string
  amount_paise: number
  cheque_date: string
  memo: string
  status: string
  entity_id?: number | null
}

/** What the entity list knows about one entity, keyed by entity id in `analyse`. */
export interface EntityMeta {
  category?: string
  notes?: string
  entity_name?: string
  kind?: string
}

export type EntityMetaMap = ReadonlyMap<number, EntityMeta> | Readonly<Record<number, EntityMeta>>

export interface RecentCheque {
  id: number
  cheque_date: string
  amount_paise: number
  cheque_no: string
  status: string
  memo: string
}

/** The JSON shape of a rhythm, as sent to the frontend. */
export interface PayeeRhythmDict {
  payee: string
  count: number
  first_date: string
  last_date: string
  dates: string[]
  total_paise: number
  typical_paise: number
  amount_pattern: string
  cadence: string
  median_interval_days: number | null
  regularity: number
  confidence: string
  next_due: string | null
  days_until_due: number | null
  status: string
  category: string
  notes: string
  entity_name: string
  entity_id: number | null
  kind: string
  recent: RecentCheque[]
}

export interface FrequencySummary {
  total_cheques: number
  total_paise: number
  month_count: number
  month_paise: number
  year_count: number
  year_paise: number
  payee_count: number
  recurring_count: number
  committed_yearly_paise: number
  overdue: string[]
  due_soon: string[]
}

type RhythmFields = Pick<
  PayeeRhythm,
  | 'payee'
  | 'count'
  | 'first_date'
  | 'last_date'
  | 'dates'
  | 'amounts_paise'
  | 'total_paise'
  | 'typical_paise'
  | 'amount_pattern'
  | 'cadence'
  | 'median_interval_days'
  | 'regularity'
  | 'confidence'
  | 'next_due'
  | 'days_until_due'
  | 'status'
> &
  Partial<Pick<PayeeRhythm, 'category' | 'notes' | 'entity_name' | 'entity_id' | 'kind' | 'recent'>>

// The field names are the JSON keys too.
export class PayeeRhythm {
  payee: string
  count: number
  first_date: string
  last_date: string
  dates: string[]
  amounts_paise: number[]
  total_paise: number
  typical_paise: number
  amount_pattern: string // fixed | near-fixed | varies | one-off
  cadence: string // weekly … yearly | irregular | too-few
  median_interval_days: number | null
  regularity: number // 0..1, 1 = perfectly evenly spaced
  confidence: string // high | medium | low | none
  next_due: string | null
  days_until_due: number | null
  status: string // overdue | due-now | due-soon | on-track | dormant | one-off
  category: string
  notes: string
  entity_name: string
  entity_id: number | null
  kind: string
  recent: RecentCheque[]

  constructor(fields: RhythmFields) {
    this.payee = fields.payee
    this.count = fields.count
    this.first_date = fields.first_date
    this.last_date = fields.last_date
    this.dates = fields.dates
    this.amounts_paise = fields.amounts_paise
    this.total_paise = fields.total_paise
    this.typical_paise = fields.typical_paise
    this.amount_pattern = fields.amount_pattern
    this.cadence = fields.cadence
    this.median_interval_days = fields.median_interval_days
    this.regularity = fields.regularity
    this.confidence = fields.confidence
    this.next_due = fields.next_due
    this.days_until_due = fields.days_until_due
    this.status = fields.status
    this.category = fields.category ?? ''
    this.notes = fields.notes ?? ''
    this.entity_name = fields.entity_name ?? ''
    this.entity_id = fields.entity_id ?? null
    this.kind = fields.kind ?? 'regular'
    this.recent = fields.recent ?? []
  }

  asDict(): PayeeRhythmDict {
    return {
      payee: this.payee,
      count: this.count,
      first_date: this.first_date,
      last_date: this.last_date,
      dates: [...this.dates],
      total_paise: this.total_paise,
      typical_paise: this.typical_paise,
      amount_pattern: this.amount_pattern,
      cadence: this.cadence,
      median_interval_days: this.median_interval_days,
      regularity: roundTo(this.regularity, 3),
      confidence: this.confidence,
      next_due: this.next_due ? this.next_due : null,
      days_until_due: this.days_until_due,
      status: this.status,
      category: this.category,
      notes: this.notes,
      entity_name: this.entity_name,
      entity_id: this.entity_id,
      kind: this.kind,
      recent: this.recent
    }
  }
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

function formatIso(year: number, month: number, day: number): string {
  return `${String(year).padStart(4, '0')}-${String(month).padStart(2, '0')}-${String(day).padStart(2, '0')}`
}

/** Whole days since the Unix epoch, counted in UTC so no timezone or DST can shift a date. */
function toDayNumber({ year, month, day }: Ymd): number {
  const at = new Date(0)
  at.setUTCFullYear(year, month - 1, day)
  return Math.round(at.getTime() / 86_400_000)
}

function fromDayNumber(days: number): string {
  const at = new Date(days * 86_400_000)
  return formatIso(at.getUTCFullYear(), at.getUTCMonth() + 1, at.getUTCDate())
}

/**
 * Today: the calendar date on this machine's clock, in its local timezone.
 */
function localToday(): string {
  const now = new Date()
  return formatIso(now.getFullYear(), now.getMonth() + 1, now.getDate())
}

/** statistics.median: the middle value, or the mean of the middle two. */
function median(values: readonly number[]): number {
  if (values.length === 0) throw new Error('no median for empty data')
  const data = [...values].sort((a, b) => a - b)
  const i = Math.floor(data.length / 2)
  return data.length % 2 === 1 ? data[i] : (data[i - 1] + data[i]) / 2
}

/** round(x): to the nearest integer, an exact half going to the even neighbour. */
function roundHalfEven(x: number): number {
  const floor = Math.floor(x)
  const rest = x - floor
  if (rest < 0.5) return floor
  if (rest > 0.5) return floor + 1
  return floor % 2 === 0 ? floor : floor + 1
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

// ---------------------------------------------------------------------------

/**
 * Advance by whole calendar months, clamping to the end of a short month.
 *
 * A cheque written on the 31st recurs on the 30th in a 30-day month, which is
 * what a person means by 'monthly' — adding 30.44 days would drift.
 */
export function addMonths(d: string, months: number): string {
  const { year: y, month: m, day } = parseIso(d)
  const total = m - 1 + months
  const year = y + Math.floor(total / 12)
  const month = (((total % 12) + 12) % 12) + 1
  return formatIso(year, month, Math.min(day, daysInMonth(year, month)))
}

/** Return the closest named cadence and how well it fits (0..1). */
function classify(medianDays: number): [string, number] {
  let best = 'irregular'
  let bestFit = 0.0
  for (const [name, period, tol] of CADENCES) {
    const distance = Math.abs(medianDays - period)
    if (distance <= tol) {
      const fit = 1.0 - distance / tol
      if (fit > bestFit) {
        best = name
        bestFit = fit
      }
    }
  }
  return [best, bestFit]
}

function amountPattern(amounts: readonly number[]): string {
  if (amounts.length < 2) return 'one-off'
  if (new Set(amounts).size === 1) return 'fixed'
  const mid = median(amounts)
  if (mid <= 0) return 'varies'
  const spread = median(amounts.map((a) => Math.abs(a - mid))) / mid
  if (spread <= 0.02) return 'near-fixed'
  if (spread <= 0.25) return 'varies'
  return 'irregular'
}

const CADENCE_MONTHS: Readonly<Record<string, number>> = {
  monthly: 1,
  'two-monthly': 2,
  quarterly: 3,
  'half-yearly': 6,
  yearly: 12
}

/** Work out one payee's rhythm from their issued cheques (oldest first). */
export function analysePayee(payee: string, rows: readonly ChequeRow[], today: string): PayeeRhythm {
  const dayNumbers = rows.map((r) => toDayNumber(parseIso(r.cheque_date)))
  const amounts = rows.map((r) => r.amount_paise)

  // Two cheques on one day are one payment event as far as rhythm goes.
  const uniqueDays = [...new Set(dayNumbers)].sort((a, b) => a - b)
  const intervals = uniqueDays.slice(1).map((day, i) => day - uniqueDays[i])

  let cadence = 'too-few'
  let regularity = 0.0
  let confidence = 'none'
  let medianInterval: number | null = null

  if (intervals.length >= 1) {
    medianInterval = median(intervals)
    if (medianInterval > 0 && uniqueDays.length >= MIN_FOR_CADENCE) {
      const mid = medianInterval
      const deviation = median(intervals.map((i) => Math.abs(i - mid)))
      regularity = Math.max(0.0, 1.0 - deviation / medianInterval)
      const [name, fit] = classify(medianInterval)
      cadence = name
      if (cadence === 'irregular') confidence = 'low'
      else if (uniqueDays.length >= 4 && regularity >= 0.75 && fit >= 0.4) confidence = 'high'
      else if (regularity >= 0.5) confidence = 'medium'
      else confidence = 'low'
    } else if (medianInterval > 0) {
      // Two payments is a hint, not a rhythm.
      const [name] = classify(medianInterval)
      cadence = name !== 'irregular' ? `maybe ${name}` : 'too-few'
      confidence = 'low'
    }
  }

  // The last date is read here, which fails when there are none.
  if (uniqueDays.length === 0) throw new RangeError('list index out of range')
  const lastDay = uniqueDays[uniqueDays.length - 1]
  const last = fromDayNumber(lastDay)
  let nextDue: string | null = null
  if (medianInterval && (confidence === 'high' || confidence === 'medium' || confidence === 'low') &&
      cadence !== 'too-few') {
    const base = cadence.replace('maybe ', '')
    const months = Object.hasOwn(CADENCE_MONTHS, base) ? CADENCE_MONTHS[base] : undefined
    nextDue = months ? addMonths(last, months) : fromDayNumber(lastDay + roundHalfEven(medianInterval))
  }

  const daysUntil = nextDue ? toDayNumber(parseIso(nextDue)) - toDayNumber(parseIso(today)) : null
  const status = statusOf(nextDue, daysUntil, medianInterval, uniqueDays.length)

  // Newest first; cheques sharing a date keep the order they arrived in, as
  // Python's stable sort with reverse=True leaves them.
  const recent = [...rows]
    .sort((a, b) => (a.cheque_date < b.cheque_date ? 1 : a.cheque_date > b.cheque_date ? -1 : 0))
    .slice(0, 6)
    .map((r) => ({
      id: r.id,
      cheque_date: r.cheque_date,
      amount_paise: r.amount_paise,
      cheque_no: r.cheque_no,
      status: r.status,
      memo: r.memo
    }))

  return new PayeeRhythm({
    payee,
    count: rows.length,
    first_date: fromDayNumber(uniqueDays[0]),
    last_date: last,
    dates: uniqueDays.map(fromDayNumber),
    amounts_paise: amounts,
    total_paise: amounts.reduce((sum, a) => sum + a, 0),
    typical_paise: Math.trunc(median(amounts)),
    amount_pattern: amountPattern(amounts),
    cadence,
    median_interval_days: medianInterval,
    regularity,
    confidence,
    next_due: nextDue,
    days_until_due: daysUntil,
    status,
    recent
  })
}

function statusOf(nextDue: string | null, daysUntil: number | null, medianDays: number | null, count: number): string {
  if (count < 2) return 'one-off'
  if (nextDue === null || daysUntil === null || medianDays === null) return 'on-track'
  // A late cheque is only "overdue" once it is late by more than a sensible
  // fraction of its own cycle — a monthly payment three days late is normal.
  const grace = Math.max(3, roundHalfEven(medianDays * 0.2))
  if (daysUntil < -grace * 4) return 'dormant'
  if (daysUntil < -grace) return 'overdue'
  if (daysUntil < 0) return 'due-now'
  if (daysUntil <= DUE_SOON_DAYS) return 'due-soon'
  return 'on-track'
}

const STATUS_ORDER: Readonly<Record<string, number>> = {
  overdue: 0,
  'due-now': 1,
  'due-soon': 2,
  'on-track': 3,
  dormant: 4,
  'one-off': 5
}

function metaFor(meta: EntityMetaMap | null | undefined, entityId: number): EntityMeta {
  if (!meta) return {}
  if (meta instanceof Map) return meta.get(entityId) ?? {}
  const record = meta as Readonly<Record<number, EntityMeta>>
  return Object.hasOwn(record, entityId) ? record[entityId] : {}
}

/**
 * Group issued cheques by entity and analyse each one's rhythm.
 *
 * By entity, not by printed name: one vendor paid for two different things
 * prints the same name on both, but each has its own cadence. A cheque not
 * yet linked to an entity falls back to its printed name.
 *
 * `today` defaults to the local calendar date, as date.today() did.
 */
export function analyse(
  rows: Iterable<ChequeRow>,
  today?: string | null,
  meta?: EntityMetaMap | null
): PayeeRhythm[] {
  const now = today || localToday()
  const grouped = new Map<string, { entityId: number | null; rows: ChequeRow[] }>()
  for (const row of rows) {
    const id = row.entity_id
    const linked = id !== null && id !== undefined
    const key = linked ? `entity:${id}` : `payee:${row.payee}`
    let group = grouped.get(key)
    if (!group) {
      group = { entityId: linked ? id : null, rows: [] }
      grouped.set(key, group)
    }
    group.rows.push(row)
  }

  const out: PayeeRhythm[] = []
  for (const { entityId, rows: payeeRows } of grouped.values()) {
    const rhythm = analysePayee(payeeRows[payeeRows.length - 1].payee, payeeRows, now)
    rhythm.entity_id = entityId
    const info = entityId !== null ? metaFor(meta, entityId) : {}
    rhythm.category = info.category !== undefined ? info.category : ''
    rhythm.notes = info.notes !== undefined ? info.notes : ''
    rhythm.entity_name = info.entity_name !== undefined ? info.entity_name : ''
    rhythm.kind = info.kind !== undefined ? info.kind : 'regular'
    if (rhythm.kind === 'one_off') {
      // Someone paid once is not expected again, however the dates fall:
      // no cadence, no due date, and never "overdue".
      rhythm.cadence = 'one-off'
      rhythm.confidence = 'none'
      rhythm.status = 'one-off'
      rhythm.next_due = null
      rhythm.days_until_due = null
    }
    out.push(rhythm)
  }

  // Most actionable first: anything owed, then by how regular and how frequent.
  const rank = (r: PayeeRhythm): [number, number, number] => [
    Object.hasOwn(STATUS_ORDER, r.status) ? STATUS_ORDER[r.status] : 9,
    r.days_until_due !== null ? r.days_until_due : 9999,
    -r.count
  ]
  out.sort((a, b) => {
    const x = rank(a)
    const y = rank(b)
    return x[0] - y[0] || x[1] - y[1] || x[2] - y[2]
  })
  return out
}

/**
 * Headline numbers for the dashboard.
 *
 * `today` defaults to the local calendar date, as date.today() did.
 */
export function summarise(
  rhythms: readonly PayeeRhythm[],
  rows: readonly ChequeRow[],
  today?: string | null
): FrequencySummary {
  const now = parseIso(today || localToday())
  const monthStart = toDayNumber({ ...now, day: 1 })
  // The financial year, April to March — the year the association's books are kept in.
  const yearStart = toDayNumber({ year: now.month >= 4 ? now.year : now.year - 1, month: 4, day: 1 })

  const totalSince = (start: number): [number, number] => {
    const picked = rows.filter((r) => toDayNumber(parseIso(r.cheque_date)) >= start)
    return [picked.length, picked.reduce((sum, r) => sum + r.amount_paise, 0)]
  }

  const [monthCount, monthPaise] = totalSince(monthStart)
  const [yearCount, yearPaise] = totalSince(yearStart)

  const recurring = rhythms.filter((r) => r.confidence === 'high' || r.confidence === 'medium')
  let committed = 0
  for (const r of recurring) {
    const days = r.median_interval_days || 0
    if (days > 0) committed += roundHalfEven(r.typical_paise * (365.25 / days))
  }
  // This is a projection, not a ledger figure. Showing it to the paise would
  // imply a precision that annualising a median interval does not have.
  committed = roundHalfEven(committed / 100) * 100

  return {
    total_cheques: rows.length,
    total_paise: rows.reduce((sum, r) => sum + r.amount_paise, 0),
    month_count: monthCount,
    month_paise: monthPaise,
    year_count: yearCount,
    year_paise: yearPaise,
    payee_count: rhythms.length, // entities paid, not distinct names
    recurring_count: recurring.length,
    committed_yearly_paise: committed,
    overdue: rhythms.filter((r) => r.status === 'overdue').map((r) => r.entity_name || r.payee),
    due_soon: rhythms
      .filter((r) => r.status === 'due-now' || r.status === 'due-soon')
      .map((r) => r.entity_name || r.payee)
  }
}
