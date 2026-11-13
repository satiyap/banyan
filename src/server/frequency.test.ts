// Holds frequency.ts to its recorded answers: tests/golden/frequency.json holds what the
// rules answered over invented registers, and every answer here must
// match.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import {
  CADENCES,
  DUE_SOON_DAYS,
  MIN_FOR_CADENCE,
  PayeeRhythm,
  addMonths,
  analyse,
  analysePayee,
  summarise,
  type ChequeRow,
  type EntityMeta,
  type FrequencySummary,
  type PayeeRhythmDict
} from './frequency'

interface Run {
  today: string
  analyse: PayeeRhythmDict[]
  amounts_paise: number[][]
  summarise: FrequencySummary
  analyse_without_meta?: PayeeRhythmDict[]
  summarise_without_meta?: FrequencySummary
  analyse_payee?: PayeeRhythmDict
}

interface Case {
  name: string
  note: string
  rows: ChequeRow[]
  meta: Record<string, EntityMeta>
  runs: Run[]
}

interface Fixture {
  constants: { CADENCES: [string, number, number][]; MIN_FOR_CADENCE: number; DUE_SOON_DAYS: number }
  add_months: [string, number, string][]
  cases: Case[]
}

const fixture = JSON.parse(readFileSync(join(process.cwd(), 'tests/golden/frequency.json'), 'utf8')) as Fixture

/**
 * Deep equality, and then the same JSON text: that also pins the order of the
 * keys and tells null from a missing key. Floats are compared exactly.
 */
function expectSame(actual: unknown, expected: unknown): void {
  expect(actual).toEqual(expected)
  expect(JSON.stringify(actual)).toBe(JSON.stringify(expected))
}

describe('frequency golden fixtures', () => {
  it('has enough invented registers to mean something', () => {
    expect(fixture.cases.length).toBeGreaterThanOrEqual(40)
  })

  it('keeps the constants', () => {
    expectSame(CADENCES, fixture.constants.CADENCES)
    expect(MIN_FOR_CADENCE).toBe(fixture.constants.MIN_FOR_CADENCE)
    expect(DUE_SOON_DAYS).toBe(fixture.constants.DUE_SOON_DAYS)
  })

  it('addMonths clamps to the end of short months', () => {
    expect(fixture.add_months.length).toBeGreaterThan(100)
    for (const [d, months, expected] of fixture.add_months) {
      expect(addMonths(d, months), `${d} + ${months} months`).toBe(expected)
    }
  })

  for (const c of fixture.cases) {
    describe(`${c.name}: ${c.note}`, () => {
      const asMap = new Map(Object.entries(c.meta).map(([id, info]) => [Number(id), info]))
      const frozenRows = JSON.stringify(c.rows)

      for (const run of c.runs) {
        it(`today ${run.today}`, () => {
          // The entity list as a Map and as a plain object must read the same.
          const rhythms = analyse(c.rows, run.today, asMap)
          expectSame(
            rhythms.map((r) => r.asDict()),
            run.analyse
          )
          expectSame(
            rhythms.map((r) => r.amounts_paise),
            run.amounts_paise
          )
          expectSame(summarise(rhythms, c.rows, run.today), run.summarise)
          expectSame(
            analyse(c.rows, run.today, c.meta).map((r) => r.asDict()),
            run.analyse
          )

          if (run.analyse_without_meta && run.summarise_without_meta) {
            const bare = analyse(c.rows, run.today)
            expectSame(
              bare.map((r) => r.asDict()),
              run.analyse_without_meta
            )
            expectSame(summarise(bare, c.rows, run.today), run.summarise_without_meta)
          }
          if (run.analyse_payee) {
            const whole = analysePayee('Everyone', c.rows, run.today)
            expect(whole).toBeInstanceOf(PayeeRhythm)
            expectSame(whole.asDict(), run.analyse_payee)
          }
          expect(JSON.stringify(c.rows), 'the rows passed in are left untouched').toBe(frozenRows)
        })
      }
    })
  }
})

describe('frequency outside the fixtures', () => {
  const row = (id: number, date: string, paise: number): ChequeRow => ({
    id,
    cheque_no: String(id),
    payee: 'Greenleaf Gardening',
    amount_paise: paise,
    cheque_date: date,
    memo: '',
    status: 'printed',
    entity_id: null
  })

  it('defaults today to the local calendar date', () => {
    const now = new Date()
    const local = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, '0')).join('-')
    const rows = [row(1, '2025-01-05', 100), row(2, '2025-02-05', 100), row(3, '2025-03-05', 100)]
    expectSame(
      analyse(rows).map((r) => r.asDict()),
      analyse(rows, local).map((r) => r.asDict())
    )
    expectSame(summarise(analyse(rows), rows), summarise(analyse(rows, local), rows, local))
  })

  it('rejects a date that is not an ISO date, as date.fromisoformat does', () => {
    expect(() => analyse([row(1, '05/01/2025', 100)], '2025-06-01')).toThrow("Invalid isoformat string: '05/01/2025'")
    expect(() => analyse([row(1, '2025-02-30', 100)], '2025-06-01')).toThrow()
  })
})
