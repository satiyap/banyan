// Holds reports.ts to its recorded answers: tests/golden/reports.json holds what the
// rules answered over invented registers, and every answer here must
// match.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import {
  HEADS,
  MISC_HEAD,
  MONTH_NAMES,
  UNCATEGORISED,
  build,
  casefold,
  fyLabel,
  fyMonths,
  fyOf,
  headOf,
  monthly,
  periodBounds,
  periodLabel,
  previousPeriod,
  quarterOf,
  type MonthlyReport,
  type Report,
  type ReportChequeRow,
  type ReportEntityRow
} from './reports'

interface Run {
  today: string
  build: { fy: number; quarter: number | null; out: Report }[]
  monthly: { fy: number | null; last: number | null; out: MonthlyReport }[]
}

interface Case {
  name: string
  note: string
  rows: ReportChequeRow[]
  entities: ReportEntityRow[]
  runs: Run[]
}

interface Fixture {
  constants: { MONTH_NAMES: string[]; HEADS: string[]; MISC_HEAD: string; UNCATEGORISED: string }
  dates: [string, number, number][]
  years: { fy: number; label: string; months: [number, number][] }[]
  periods: { fy: number; quarter: number | null; bounds: [string, string]; label: string; previous: [number, number | null] }[]
  head_of: [Pick<ReportEntityRow, 'head' | 'kind'> | null, string][]
  casefold: { unicode: string; folds: Record<string, string>; unassigned: [number, number][] }
  cases: Case[]
}

const fixture = JSON.parse(readFileSync(join(process.cwd(), 'tests/golden/reports.json'), 'utf8')) as Fixture

/**
 * Deep equality, and then the same JSON text: that also pins the order of the
 * keys and tells null from a missing key. Floats are compared exactly.
 */
function expectSame(actual: unknown, expected: unknown): void {
  expect(actual).toEqual(expected)
  expect(JSON.stringify(actual)).toBe(JSON.stringify(expected))
}

describe('reports golden fixtures', () => {
  it('has enough invented registers to mean something', () => {
    expect(fixture.cases.length).toBeGreaterThanOrEqual(40)
  })

  it('keeps the constants', () => {
    expectSame(MONTH_NAMES, fixture.constants.MONTH_NAMES)
    expectSame(HEADS, fixture.constants.HEADS)
    expect(MISC_HEAD).toBe(fixture.constants.MISC_HEAD)
    expect(UNCATEGORISED).toBe(fixture.constants.UNCATEGORISED)
  })

  it('fyOf and quarterOf place every date', () => {
    expect(fixture.dates.length).toBeGreaterThan(200)
    for (const [d, fy, quarter] of fixture.dates) {
      expect(fyOf(d), d).toBe(fy)
      expect(quarterOf(d), d).toBe(quarter)
    }
  })

  it('fyLabel and fyMonths', () => {
    for (const y of fixture.years) {
      expect(fyLabel(y.fy)).toBe(y.label)
      expectSame(fyMonths(y.fy), y.months)
    }
  })

  it('periodBounds, periodLabel and previousPeriod', () => {
    for (const p of fixture.periods) {
      const at = `fy ${p.fy} quarter ${p.quarter}`
      expect(periodBounds(p.fy, p.quarter), at).toEqual(p.bounds)
      expect(periodLabel(p.fy, p.quarter), at).toBe(p.label)
      expect(previousPeriod(p.fy, p.quarter), at).toEqual(p.previous)
    }
    expect(periodBounds(2025)).toEqual(periodBounds(2025, null))
    expect(() => periodBounds(2025, 5)).toThrow()
  })

  it('headOf', () => {
    for (const [entity, head] of fixture.head_of) expect(headOf(entity), JSON.stringify(entity)).toBe(head)
    expect(headOf()).toBe(UNCATEGORISED)
  })

  it('casefold matches str.casefold() on every code point Python knows', () => {
    const { folds, unassigned } = fixture.casefold
    expect(Object.keys(folds).length).toBeGreaterThan(1000)
    // Code points unassigned in Python's Unicode version are skipped: a newer
    // Unicode in Node may already know letters there.
    const skip = new Uint8Array(0x110000)
    for (const [from, to] of unassigned) skip.fill(1, from, to + 1)
    const wrong: string[] = []
    for (let cp = 0; cp < 0x110000; cp++) {
      if (skip[cp] || (cp >= 0xd800 && cp <= 0xdfff)) continue
      const ch = String.fromCodePoint(cp)
      const expected = folds[cp.toString(16)] ?? ch
      // Padded so the non-ASCII path is taken even for an ASCII code point.
      if (casefold(ch) !== expected || casefold(`é${ch}`) !== `é${expected}`) wrong.push(cp.toString(16))
    }
    expect(wrong).toEqual([])
    expect(casefold('Straße ΣΊΣΥΦΟΣ İstasyon')).toBe('strasse σίσυφοσ i̇stasyon')
  })

  for (const c of fixture.cases) {
    describe(`${c.name}: ${c.note}`, () => {
      const frozen = JSON.stringify([c.rows, c.entities])

      for (const run of c.runs) {
        it(`today ${run.today}`, () => {
          for (const b of run.build) {
            const at = `build fy ${b.fy} quarter ${b.quarter}`
            const out = build(c.rows, c.entities, b.fy, b.quarter, run.today)
            expect(out, at).toEqual(b.out)
            expect(JSON.stringify(out), at).toBe(JSON.stringify(b.out))
          }
          for (const m of run.monthly) {
            const at = `monthly fy ${m.fy} last ${m.last}`
            const out = monthly(c.rows, c.entities, m.fy, m.last, run.today)
            expect(out, at).toEqual(m.out)
            expect(JSON.stringify(out), at).toBe(JSON.stringify(m.out))
          }
          expect(JSON.stringify([c.rows, c.entities]), 'the rows passed in are left untouched').toBe(frozen)
        })
      }
    })
  }
})

describe('reports outside the fixtures', () => {
  const rows: ReportChequeRow[] = [
    { id: 1, cheque_no: '1', payee: 'Meridian Lifts', amount_paise: 250000, cheque_date: '2025-05-02', memo: '' }
  ]

  it('defaults today to the local calendar date', () => {
    const now = new Date()
    const local = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, '0')).join('-')
    expectSame(build(rows, [], 2025), build(rows, [], 2025, null, local))
    expectSame(monthly(rows, []), monthly(rows, [], null, null, local))
    expectSame(monthly(rows, [], undefined, 6), monthly(rows, [], null, 6, local))
  })

  it('rejects a date that is not an ISO date, as date.fromisoformat does', () => {
    const bad = [{ ...rows[0], cheque_date: '2 May 2025' }]
    expect(() => build(bad, [], 2025, null, '2025-06-01')).toThrow("Invalid isoformat string: '2 May 2025'")
    expect(() => monthly(bad, [], 2025, null, '2025-06-01')).toThrow("Invalid isoformat string: '2 May 2025'")
  })
})
