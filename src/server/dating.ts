/**
 * How an entity's cheques are dated, and reminders for post-dated ones.
 *
 * Most bills are dated the day they are written. Salaries are not: the cheque is
 * written and handed over early, dated the pay day, and the bank will not honour
 * it before then. An entity's date rule says which, and only proposes a date —
 * every leaf can still be dated by hand before it prints.
 *
 * A post-dated cheque is one whose date is after the day it was written. Nothing
 * about that is stored: it is read off the cheque's own date and the time its
 * row was created, so no register row needs a new status. Reminders before its
 * date are answered with events on the cheque, which change nothing else.
 *
 * Dates are 'YYYY-MM-DD' strings throughout; arithmetic is on calendar parts in UTC.
 */

import { localDate } from './leaves'
import { addDays, cmpStr, dayNumber, daysInMonth, isoDate, parseIsoDate, pyInt, pyIntOf, pyStrip } from './pycompat'

// Python's `\d` is any Unicode decimal digit, its `\b` uses Unicode word
// characters, and its `$` also matches before a final newline. The patterns
// below spell those out, since the JS shorthands are ASCII-only and stricter.
const WORD = '[\\p{L}\\p{N}_]'
export const RULE_RE = /^(today|(next|this):([1-9]|[12]\p{Nd}|3[01]))\n?$/u
export const DAYS_RE = /^(\p{Nd}{1,2}(,\p{Nd}{1,2}){0,3})?\n?$/u
export const STAGE_RE = new RegExp(`(?<!${WORD})stage (\\p{Nd}+)d(?!${WORD})`, 'u')
export const UNTIL_RE = new RegExp(`(?<!${WORD})until (\\p{Nd}{4}-\\p{Nd}{2}-\\p{Nd}{2})(?!${WORD})`, 'u')
export const ISSUED = ['queued', 'printed', 'logged'] as const

// Characters `str.isdigit()` accepts that `int()` does not: superscripts,
// circled digits and the like (Unicode Numeric_Type=Digit, 16.0).
const DIGIT_ONLY =
  '\\u{B2}\\u{B3}\\u{B9}\\u{1369}-\\u{1371}\\u{19DA}\\u{2070}\\u{2074}-\\u{2079}\\u{2080}-\\u{2089}' +
  '\\u{2460}-\\u{2468}\\u{2474}-\\u{247C}\\u{2488}-\\u{2490}\\u{24EA}\\u{24F5}-\\u{24FD}\\u{24FF}' +
  '\\u{2776}-\\u{277E}\\u{2780}-\\u{2788}\\u{278A}-\\u{2792}\\u{10A40}-\\u{10A43}\\u{10E60}-\\u{10E68}' +
  '\\u{11052}-\\u{1105A}\\u{1F100}-\\u{1F10A}'
const ISDIGIT_RE = new RegExp(`^[\\p{Nd}${DIGIT_ONLY}]+$`, 'u')

/** A `cheques` row as the reminders read it. */
export interface ReminderChequeRow {
  id: number
  cheque_no: string
  payee: string
  entity_id: number | null
  amount_paise: number | string
  cheque_date: string
  memo: string
  status: string
  created_at: string
  [column: string]: unknown
}

/** An `entities` row; only these two columns are read. */
export interface ReminderEntityRow {
  entity_name?: string | null
  remind_days?: string | null
  [column: string]: unknown
}

/** An `events` row of kind 'remind-ack' or 'remind-snooze'. */
export interface ReminderEventRow {
  cheque_id: number
  kind: string
  detail: string
  [column: string]: unknown
}

export interface Reminder {
  cheque_id: number
  cheque_no: string
  payee: string
  entity_id: number | null
  entity_name: string
  amount_paise: number
  cheque_date: string
  memo: string
  status: string
  written_on: string
  days_until: number
  stage: number | null
  answered: boolean
  snoozed_until: string
  due: boolean
}

/** The given day of a month, or its last day when the month is shorter —
 * 'the 31st' in February is the 28th or 29th, not March. */
function onDay(year: number, month: number, day: number): string {
  return isoDate(year, month, Math.min(day, daysInMonth(year, month)))
}

/** The date an entity's next cheque would carry, written today. */
export function ruleDate(rule: string | null | undefined, today: string): string {
  const m = RULE_RE.exec(rule || 'today')
  if (!m || m[1] === 'today') return today
  const which = m[2]
  const day = pyInt(m[3])
  const [year, month] = parseIsoDate(today)
  const nxt: [number, number] = [year + Math.floor(month / 12), (month % 12) + 1]
  if (which === 'next') return onDay(...nxt, day)
  const thisMonth = onDay(year, month, day)
  return thisMonth > today ? thisMonth : onDay(...nxt, day)
}

export function remindDays(text: string | null | undefined): number[] {
  const days = new Set<number>()
  for (const d of (text || '').split(',')) {
    const digits = pyStrip(d)
    if (!ISDIGIT_RE.test(digits)) continue
    // `isdigit()` lets through a few characters `int()` then refuses.
    days.add(pyIntOf(d))
  }
  return [...days].sort((a, b) => b - a)
}

/** `int(x)` for a register amount: an integer stays, text is read as digits. */
function toInt(value: number | string): number {
  if (typeof value === 'number') return Math.trunc(value)
  return pyIntOf(String(value))
}

/**
 * Every post-dated cheque whose date has not passed, soonest first.
 *
 * Each says which reminder it is in (the nearest of its entity's remind days
 * that has been reached), and whether that one has been answered or put off.
 * `due` is true when the desk should ask about it now.
 *
 * `entities` is keyed by entity id; `today` is 'YYYY-MM-DD'.
 */
export function reminders(
  cheques: Iterable<ReminderChequeRow>,
  entities: ReadonlyMap<number, ReminderEntityRow>,
  events: Iterable<ReminderEventRow>,
  today: string
): Reminder[] {
  const answered = new Map<number, Set<number>>()
  const snoozed = new Map<number, string>()
  for (const e of events) {
    if (e.kind === 'remind-ack') {
      const m = STAGE_RE.exec(e.detail)
      if (m) {
        const stages = answered.get(e.cheque_id) ?? new Set<number>()
        answered.set(e.cheque_id, stages.add(pyInt(m[1])))
      }
    } else if (e.kind === 'remind-snooze') {
      const m = UNTIL_RE.exec(e.detail)
      if (m) {
        const before = snoozed.get(e.cheque_id) ?? ''
        snoozed.set(e.cheque_id, cmpStr(m[1], before) > 0 ? m[1] : before)
      }
    }
  }

  const todayNumber = dayNumber(parseIsoDate(today))
  const out: Reminder[] = []
  for (const c of cheques) {
    if (!(ISSUED as readonly string[]).includes(c.status)) continue
    const datedParts = parseIsoDate(c.cheque_date)
    const dated = isoDate(...datedParts)
    if (dated < today || dated <= localDate(c.created_at)) continue
    const ent: ReminderEntityRow = (c.entity_id === null ? undefined : entities.get(c.entity_id)) || {}
    const days = dayNumber(datedParts) - todayNumber
    const stages = remindDays(ent.remind_days === undefined ? '3,1,0' : ent.remind_days)
    const reached = stages.filter((s) => days <= s)
    const stage = reached.length ? Math.min(...reached) : null
    const done = stage !== null && [...(answered.get(c.id) ?? [])].some((a) => a <= stage)
    const snoozedUntil = snoozed.get(c.id) ?? ''
    const putOff = cmpStr(snoozedUntil, today) > 0
    out.push({
      cheque_id: c.id,
      cheque_no: c.cheque_no,
      payee: c.payee,
      entity_id: c.entity_id,
      entity_name: ent.entity_name || c.payee,
      amount_paise: toInt(c.amount_paise),
      cheque_date: c.cheque_date,
      memo: c.memo,
      status: c.status,
      written_on: localDate(c.created_at),
      days_until: days,
      stage,
      answered: done,
      snoozed_until: snoozedUntil,
      due: stage !== null && !done && !putOff
    })
  }
  out.sort((a, b) => cmpStr(a.cheque_date, b.cheque_date) || a.cheque_id - b.cheque_id)
  return out
}

export function tomorrow(today: string): string {
  return addDays(today, 1)
}
