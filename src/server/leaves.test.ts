// Holds leaves.ts to its recorded answers: tests/golden/leaves.json holds what the
// rules answered over invented books, cheques and blank leaves, and every answer here must
// match.
import { describe, expect, it } from 'vitest'
import { attempt, expected, inZone, loadFixture, type Outcome } from '../../tests/golden/harness'
import {
  ALERT_SHARE,
  LOOSE_GAP,
  MAX_BOOK_LEAVES,
  ValueError,
  bookRange,
  booksView,
  checkNewBook,
  fmtLeaf,
  localDate,
  parseLeaf,
  pdcState,
  registerView,
  type BlankLeafRow,
  type BookRow,
  type BooksView,
  type ChequeRow,
  type NewBookCheck,
  type RegisterView
} from './leaves'

interface Args {
  books: BookRow[]
  cheques: ChequeRow[]
  blanks: BlankLeafRow[]
  today: string
}

interface Fixture {
  constants: { MAX_BOOK_LEAVES: number; ALERT_SHARE: number; LOOSE_GAP: number }
  parse_leaf: ({ input: string | null } & Outcome<[string, number, number] | null>)[]
  fmt_leaf: { args: [string, number, number]; ok: string }[]
  book_range: ({ book: { first_no: string; last_no: string } } & Outcome<[string, number, number, number]>)[]
  check_new_book: ({
    first_no: string
    last_no: string
    books: BookRow[]
    used: string[]
    already_used: boolean
  } & Outcome<NewBookCheck>)[]
  local_date: { zone: string; cases: ({ input: string } & Outcome<string>)[] }[]
  pdc_state: {
    zone: string
    cases: ({ cheque: { created_at: string; cheque_date: string }; today: string } & Outcome<string>)[]
  }[]
  books_view: ({ zone: string; args: Args } & Outcome<BooksView>)[]
  registers: { zone: string; args: Args; register_view: Outcome<RegisterView>; books_view: Outcome<BooksView> }[]
}

const golden = loadFixture<Fixture>('leaves.json')

describe('leaves (golden against app/leaves.py)', () => {
  it('has the same constants', () => {
    expect({ MAX_BOOK_LEAVES, ALERT_SHARE, LOOSE_GAP }).toEqual(golden.constants)
  })

  it('reads and writes leaf numbers the same way', () => {
    for (const c of golden.parse_leaf) {
      expect(attempt(() => parseLeaf(c.input)), JSON.stringify(c.input)).toEqual(expected(c))
    }
    for (const c of golden.fmt_leaf) expect(fmtLeaf(...c.args), JSON.stringify(c.args)).toBe(c.ok)
    for (const c of golden.book_range) {
      expect(attempt(() => bookRange(c.book)), JSON.stringify(c.book)).toEqual(expected(c))
    }
  })

  it(`accepts or refuses ${golden.check_new_book.length} new books with the same answer`, () => {
    for (const c of golden.check_new_book) {
      const actual = attempt(() => checkNewBook(c.first_no, c.last_no, c.books, new Set(c.used), c.already_used))
      const label = `${c.first_no}..${c.last_no} used=${c.used.length} already=${c.already_used}`
      expect(actual, label).toEqual(expected(c))
    }
    expect(() => checkNewBook('x', 'y', [], [], false)).toThrow(ValueError)
  })

  it('gives the desk the same local date in every time zone', () => {
    for (const { zone, cases } of golden.local_date) {
      inZone(zone, () => {
        for (const c of cases) {
          expect(attempt(() => localDate(c.input)), `${zone} ${JSON.stringify(c.input)}`).toEqual(expected(c))
        }
      })
    }
  })

  it('classifies post-dated cheques the same way', () => {
    for (const { zone, cases } of golden.pdc_state) {
      inZone(zone, () => {
        for (const c of cases) {
          const label = `${zone} ${JSON.stringify(c.cheque)} today=${c.today}`
          expect(attempt(() => pdcState(c.cheque, c.today)), label).toEqual(expected(c))
        }
      })
    }
  })

  const views = [...golden.books_view, ...golden.registers.map((r) => ({ zone: r.zone, args: r.args, ...r.books_view }))]
  it(`builds the same books view for ${views.length} shelves`, () => {
    views.forEach((c, n) => {
      const { books, cheques, blanks, today } = c.args
      const actual = inZone(c.zone, () => attempt(() => booksView(books, cheques, blanks, today)))
      expect(actual, `books view ${n}`).toEqual(expected(c as Outcome<BooksView>))
    })
  })

  it(`builds the same register view for ${golden.registers.length} registers`, () => {
    golden.registers.forEach((c, n) => {
      const { books, cheques, blanks, today } = c.args
      const actual = inZone(c.zone, () => attempt(() => registerView(cheques, blanks, books, today)))
      const want = expected(c.register_view)
      // Row by row first, so a mismatch names the row rather than dumping the register.
      actual.ok?.rows.forEach((row, i) => expect(row, `register ${n} (${c.zone}) row ${i}`).toEqual(want.ok?.rows[i]))
      expect(actual, `register ${n} (${c.zone})`).toEqual(want)
    })
  })
})
