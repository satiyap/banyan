// Holds dating.ts to its recorded answers: tests/golden/dating.json holds what the
// rules answered over invented cheques, entities and reminder events, and every answer here must
// match.
import { describe, expect, it } from 'vitest'
import { attempt, expected, inZone, loadFixture, type Outcome } from '../../tests/golden/harness'
import {
  DAYS_RE,
  ISSUED,
  RULE_RE,
  remindDays,
  reminders,
  ruleDate,
  tomorrow,
  type Reminder,
  type ReminderChequeRow,
  type ReminderEntityRow,
  type ReminderEventRow
} from './dating'

interface Fixture {
  constants: { ISSUED: string[] }
  rules: (string | null)[]
  rule_date: { today: string; ok: string[]; tomorrow: string }[]
  remind_days: ({ input: string | null } & Outcome<number[]>)[]
  patterns: { name: 'RULE_RE' | 'DAYS_RE'; input: string; ok: boolean }[]
  entities: [number, ReminderEntityRow][]
  reminders: ({
    zone: string
    today: string
    cheques: ReminderChequeRow[]
    events: ReminderEventRow[]
  } & Outcome<Reminder[]>)[]
}

const golden = loadFixture<Fixture>('dating.json')

describe('dating (golden against app/dating.py)', () => {
  it('has the same constants and patterns', () => {
    expect([...ISSUED]).toEqual(golden.constants.ISSUED)
    const patterns = { RULE_RE, DAYS_RE }
    for (const c of golden.patterns) {
      expect(patterns[c.name].test(c.input), `${c.name} ${JSON.stringify(c.input)}`).toBe(c.ok)
    }
  })

  it(`proposes the same date for ${golden.rules.length} rules on ${golden.rule_date.length} days`, () => {
    for (const day of golden.rule_date) {
      const actual = golden.rules.map((rule) => ruleDate(rule, day.today))
      golden.rules.forEach((rule, i) => {
        expect(actual[i], `ruleDate(${JSON.stringify(rule)}, ${day.today})`).toBe(day.ok[i])
      })
      expect(tomorrow(day.today), `tomorrow(${day.today})`).toBe(day.tomorrow)
    }
  })

  it('reads remind days the same way', () => {
    for (const c of golden.remind_days) {
      expect(attempt(() => remindDays(c.input)), JSON.stringify(c.input)).toEqual(expected(c))
    }
  })

  it(`lists the same reminders for ${golden.reminders.length} desks`, () => {
    const entities = new Map(golden.entities)
    golden.reminders.forEach((c, n) => {
      const actual = inZone(c.zone, () => attempt(() => reminders(c.cheques, entities, c.events, c.today)))
      expect(actual, `reminders ${n} (${c.zone}, today ${c.today})`).toEqual(expected(c))
    })
  })
})
