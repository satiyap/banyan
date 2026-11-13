// Holds amount.ts to its recorded answers: tests/golden/amount.json holds what the
// rules answered over invented amounts, and every answer here must
// match.
import { describe, expect, it } from 'vitest'
import { attempt, expected, loadFixture, type Outcome } from '../../tests/golden/harness'
import {
  Amount,
  AmountError,
  MAX_PAISE,
  ONES,
  TENS,
  amountFigures,
  amountWords,
  groupIndian,
  indianWords,
  parseAmount
} from './amount'

interface Parsed {
  paise_total: number
  rupees: number
  paise: number
  words: string
  figures: string
  plain: string
  str: string
}

interface Fixture {
  constants: { ONES: string[]; TENS: string[]; MAX_PAISE: number }
  parse: ({ input: string | number } & Outcome<Parsed>)[]
  functions: { n: number; indian_words: string; group_indian: string; amount_words: string; amount_figures: string }[]
  negatives: { n: number; group_indian: string }[]
}

const golden = loadFixture<Fixture>('amount.json')

describe('amount (golden against app/amount.py)', () => {
  it('has the same constants', () => {
    expect({ ONES, TENS, MAX_PAISE }).toEqual(golden.constants)
  })

  it(`parses ${golden.parse.length} inputs to the same amount or the same refusal`, () => {
    expect(golden.parse.length).toBeGreaterThanOrEqual(3000)
    for (const c of golden.parse) {
      const actual = attempt<Parsed>(() => {
        const a = parseAmount(c.input)
        return {
          paise_total: a.paiseTotal,
          rupees: a.rupees,
          paise: a.paise,
          words: a.words,
          figures: a.figures,
          plain: a.plain,
          str: String(a)
        }
      })
      expect(actual, `parseAmount(${JSON.stringify(c.input)})`).toEqual(expected(c))
    }
  })

  it('refuses with AmountError and passes an Amount through', () => {
    expect(() => parseAmount('abc')).toThrow(AmountError)
    const a = parseAmount('1,250.50')
    expect(a).toBeInstanceOf(Amount)
    expect(parseAmount(a)).toBe(a)
  })

  it(`words and figures agree for ${golden.functions.length} integers`, () => {
    for (const c of golden.functions) {
      const actual = {
        n: c.n,
        indian_words: indianWords(c.n),
        group_indian: groupIndian(c.n),
        amount_words: amountWords(c.n),
        amount_figures: amountFigures(c.n)
      }
      expect(actual).toEqual(c)
    }
    for (const c of golden.negatives) expect(groupIndian(c.n), String(c.n)).toBe(c.group_indian)
  })
})
