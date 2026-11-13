/**
 * Cheque books and the register in leaf order.
 *
 * A cheque book is a run of numbered leaves used front to back, so how far a book
 * has got is simply the highest of its numbers the register knows. Nothing here is
 * stored on a cheque: a book is a number range, and which book a cheque came from
 * is worked out from its number. Adding a book therefore rewrites no register
 * row, and a register from before books existed reads exactly as it did.
 *
 * Every leaf counts as used once its number is in the register in any status —
 * printed, handwritten, misprinted, void — or recorded as a blank leaf. That is
 * the same rule the batch path uses to refuse a number, applied to whole books.
 */

import { ValueError, canonicalDate, cmpStr, localDateOf, pyInt, pyStrip } from './pycompat'

export { ValueError }

// Python's `.` stops only at \n and its `\d` is any Unicode decimal digit.
export const LEAF_RE = /^([^\n]*?)(\p{Nd}+)$/u
export const MAX_BOOK_LEAVES = 500
export const ALERT_SHARE = 0.8
// Without books, a jump this small between two numbers in the same series
// looks like leaves skipped rather than a new book.
export const LOOSE_GAP = 10

/** (prefix, value, digit width) of a leaf number. */
export type Leaf = [prefix: string, value: number, width: number]

/** A `cheque_books` row. */
export interface BookRow {
  id: number
  name: string
  first_no: string
  last_no: string
  received_on: string
  position: number
  alert_state: string
  alert_until: string
  [column: string]: unknown
}

/** A `cheques` row as the register lists it (with the entity's name joined in). */
export interface ChequeRow {
  id: number
  cheque_no: string
  payee: string
  entity_id: number | null
  entity_name?: string | null
  amount_paise: number
  cheque_date: string
  memo: string
  status: string
  batch_id: number | null
  created_at: string
  pdf_path: string
  cal_version: number
  [column: string]: unknown
}

/** A `blank_leaves` row. */
export interface BlankLeafRow {
  id: number
  cheque_no: string
  reason: string
  created_at: string
  [column: string]: unknown
}

export interface NewBookCheck {
  prefix: string
  first: number
  last: number
  width: number
  count: number
  in_register: number
  alert_count: number
  alert_leaf: string
}

export type BookState = 'finished' | 'in_use' | 'next' | 'shelf' | 'paused'

export interface BookView {
  id: number
  name: string
  first_no: string
  last_no: string
  received_on: string
  position: number
  count: number
  consumed: number
  left: number
  next_leaf: string
  finished: boolean
  alert_count: number
  alert_leaf: string
  alert_reached: boolean
  alert_state: string
  alert_until: string
  statuses: {
    printed: number
    logged: number
    queued: number
    spoiled: number
    void: number
    failed: number
    blank: number
  }
  missing: string[]
  state: BookState
}

export interface BookAlert {
  book_id: number
  book: string
  consumed: number
  count: number
  left: number
  next_book: string
  needs_answer: boolean
}

export interface BooksView {
  books: BookView[]
  in_use: number | null
  next_leaf: string
  alert: BookAlert | null
}

export type PdcState = '' | 'post-dated' | 'now-valid'

export interface RegisterChequeRow {
  kind: 'cheque'
  id: number
  cheque_no: string
  payee: string
  entity_id: number | null
  entity_name: string | null | undefined
  amount_paise: number
  cheque_date: string
  memo: string
  status: string
  batch_id: number | null
  created_at: string
  pdf_path: string
  cal_version: number
  pdc: PdcState
  duplicate: boolean
  /** Absent on a cheque with no leaf number. */
  book_id?: number | null
}

export interface RegisterBlankRow {
  kind: 'blank'
  id: number
  cheque_no: string
  reason: string
  created_at: string
  status: 'blank'
  duplicate: boolean
  book_id: number | null
}

export interface RegisterGapRow {
  kind: 'gap'
  book_id: number | null
  sure: boolean
  from_no: string
  to_no: string
  numbers: string[]
  count: number
}

export type RegisterRow = RegisterChequeRow | RegisterBlankRow | RegisterGapRow

export interface RegisterView {
  rows: RegisterRow[]
}

/** (prefix, value, digit width) of a leaf number, or null if it does not
 * end in digits. */
export function parseLeaf(number: string | null | undefined): Leaf | null {
  const m = LEAF_RE.exec(pyStrip(number || ''))
  if (!m) return null
  const [, head, digits] = m
  return [head, pyInt(digits), [...digits].length]
}

export function fmtLeaf(prefix: string, value: number, width: number): string {
  // Python's `0{width}d` pads after the sign.
  const digits = String(Math.abs(value)).padStart(value < 0 ? width - 1 : width, '0')
  return `${prefix}${value < 0 ? '-' : ''}${digits}`
}

/** (prefix, first, last, width) of a stored book. */
export function bookRange(book: Pick<BookRow, 'first_no' | 'last_no'>): [string, number, number, number] {
  const a = parseLeaf(book.first_no)
  const b = parseLeaf(book.last_no)
  if (!a || !b) throw new TypeError('cannot unpack non-iterable NoneType object')
  return [a[0], a[1], b[1], a[2]]
}

/** Validate a book about to be shelved. Returns its range and the checks
 * that passed; throws ValueError naming the first that did not. */
export function checkNewBook(
  firstNo: string,
  lastNo: string,
  books: Iterable<BookRow>,
  used: Iterable<string>,
  alreadyUsed: boolean
): NewBookCheck {
  const a = parseLeaf(firstNo)
  const b = parseLeaf(lastNo)
  if (!a || !b) throw new ValueError('both leaf numbers must end in digits')
  if (a[0] !== b[0] || a[2] !== b[2]) {
    throw new ValueError(
      'the first and last numbers are from different series — ' +
        'they should have the same length and any prefix'
    )
  }
  const [prefix, first, width] = a
  const last = b[1]
  if (last < first) throw new ValueError('the last leaf number is before the first')
  const count = last - first + 1
  if (count > MAX_BOOK_LEAVES) {
    throw new ValueError(`${count} leaves is more than any cheque book holds — check the two numbers`)
  }
  for (const other of books) {
    const [op, of, ol] = bookRange(other)
    if (op === prefix && first <= ol && of <= last) {
      throw new ValueError(`these numbers overlap ${other.name} (${other.first_no}–${other.last_no})`)
    }
  }
  // `used` is a set in Python: each number counts once.
  const inRegister = [...new Set(used)]
    .filter((n) => {
      const p = parseLeaf(n)
      return p !== null && p[0] === prefix && first <= p[1] && p[1] <= last
    })
    .sort(cmpStr)
  if (inRegister.length && !alreadyUsed) {
    throw new ValueError(
      `${inRegister.length} of these leaves are already in the register ` +
        `(${inRegister[0]}${inRegister.length > 1 ? '…' + inRegister[inRegister.length - 1] : ''}). ` +
        'If this is the book you are using now, say it is already in use'
    )
  }
  const alertCount = Math.ceil(count * ALERT_SHARE)
  return {
    prefix,
    first,
    last,
    width,
    count,
    in_register: inRegister.length,
    alert_count: alertCount,
    alert_leaf: fmtLeaf(prefix, first + alertCount - 1, width)
  }
}

/** Every book with how far it has got, which one is in use, and whether
 * the new-book alert is due. `today` is 'YYYY-MM-DD'. */
export function booksView(
  books: Iterable<BookRow>,
  cheques: Iterable<Pick<ChequeRow, 'cheque_no' | 'status'>>,
  blanks: Iterable<Pick<BlankLeafRow, 'cheque_no'>>,
  today: string
): BooksView {
  const byNumber = new Map<string, string[]>()
  const add = (number: string, status: string): void => {
    const list = byNumber.get(number)
    if (list) list.push(status)
    else byNumber.set(number, [status])
  }
  for (const c of cheques) {
    if (c.cheque_no) add(c.cheque_no, c.status)
  }
  for (const b of blanks) add(b.cheque_no, 'blank')

  const out: BookView[] = []
  for (const book of books) {
    const [prefix, first, last, width] = bookRange(book)
    const statuses = new Map<string, number>()
    const tally = (status: string): number => statuses.get(status) ?? 0
    const values = new Set<number>()
    for (const [number, sts] of byNumber) {
      const p = parseLeaf(number)
      if (p && p[0] === prefix && first <= p[1] && p[1] <= last) {
        values.add(p[1])
        for (const s of sts) statuses.set(s, tally(s) + 1)
      }
    }
    const count = last - first + 1
    let highest: number | null = null
    for (const v of values) highest = highest === null || v > highest ? v : highest
    const consumed = highest !== null ? highest - first + 1 : 0
    const alertCount = Math.ceil(count * ALERT_SHARE)
    const missing: string[] = []
    if (highest !== null) {
      for (let v = first; v < (highest || first); v++) {
        if (!values.has(v)) missing.push(fmtLeaf(prefix, v, width))
      }
    }
    out.push({
      id: book.id,
      name: book.name,
      first_no: book.first_no,
      last_no: book.last_no,
      received_on: book.received_on,
      position: book.position,
      count,
      consumed,
      left: count - consumed,
      next_leaf:
        highest !== null && highest < last
          ? fmtLeaf(prefix, highest + 1, width)
          : highest !== null
            ? ''
            : book.first_no,
      finished: highest === last,
      alert_count: alertCount,
      alert_leaf: fmtLeaf(prefix, first + alertCount - 1, width),
      alert_reached: consumed >= alertCount,
      alert_state: book.alert_state,
      alert_until: book.alert_until,
      statuses: {
        printed: tally('printed'),
        logged: tally('logged'),
        queued: tally('queued'),
        spoiled: tally('failed') + tally('void') + tally('blank'),
        void: tally('void'),
        failed: tally('failed'),
        blank: tally('blank')
      },
      missing,
      state: 'paused' // set below, once the book in use is known
    })
  }

  const inUse = out.find((b) => !b.finished) ?? null
  const shelf = out.filter((b) => inUse && b !== inUse && !b.finished && b.consumed === 0)
  for (const b of out) {
    b.state = b.finished
      ? 'finished'
      : b === inUse
        ? 'in_use'
        : shelf.length && b === shelf[0]
          ? 'next'
          : b.consumed === 0
            ? 'shelf'
            : 'paused'
  }

  let alert: BookAlert | null = null
  if (inUse && inUse.alert_reached) {
    let snoozed = false
    if (inUse.alert_state === 'snoozed') {
      if (typeof inUse.alert_until !== 'string') {
        throw new TypeError("'>' not supported between instances of 'NoneType' and 'str'")
      }
      snoozed = cmpStr(inUse.alert_until, today) > 0
    }
    const answered = inUse.alert_state === 'ordered' || snoozed
    alert = {
      book_id: inUse.id,
      book: inUse.name,
      consumed: inUse.consumed,
      count: inUse.count,
      left: inUse.left,
      next_book: shelf.length ? shelf[0].name : '',
      // With a book waiting on the shelf there is nothing to do but
      // know it; with none, the desk asks until it is answered.
      needs_answer: !shelf.length && !answered
    }
  }
  return {
    books: out,
    in_use: inUse ? inUse.id : null,
    next_leaf: inUse ? inUse.next_leaf : '',
    alert
  }
}

/**
 * The desk's own date for a UTC timestamp in the register, as 'YYYY-MM-DD'.
 *
 * Timezone: like Python's `datetime.fromisoformat(ts).astimezone().date()`,
 * this is the date in the system local zone of the machine running the app
 * (it follows the TZ environment variable), because the desk and the
 * treasurer are in the same place. A timestamp with no offset is read as
 * already local.
 */
export function localDate(timestamp: string): string {
  return localDateOf(timestamp)
}

/** 'post-dated' while a cheque dated after the day it was written is not
 * yet valid, 'now-valid' once its date has come, '' for any other cheque.
 * `today` is 'YYYY-MM-DD'. */
export function pdcState(cheque: Pick<ChequeRow, 'created_at' | 'cheque_date'>, today: string): PdcState {
  const written = localDate(cheque.created_at)
  const dated = canonicalDate(cheque.cheque_date)
  if (dated <= written) return ''
  return dated > today ? 'post-dated' : 'now-valid'
}

/** The register newest first, with leaves the register has no record of
 * shown where they fall in their book, and duplicate numbers flagged. Gaps
 * are found in leaf order; cheques with no leaf number are listed by date
 * with the rest. `today` is 'YYYY-MM-DD'. */
export function registerView(
  cheques: readonly ChequeRow[],
  blanks: readonly BlankLeafRow[],
  books: readonly BookRow[],
  today: string
): RegisterView {
  const ranges = books.map((b) => [b.id, ...bookRange(b)] as [number, string, number, number, number])

  const bookOf = (p: Leaf): number | null => {
    for (const [bookId, prefix, first, last] of ranges) {
      if (p[0] === prefix && first <= p[1] && p[1] <= last) return bookId
    }
    return null
  }

  const seen = new Map<string, number>()
  for (const c of cheques) {
    if (c.cheque_no) seen.set(c.cheque_no, (seen.get(c.cheque_no) ?? 0) + 1)
  }
  for (const b of blanks) seen.set(b.cheque_no, (seen.get(b.cheque_no) ?? 0) + 1)

  const leaves: [Leaf, RegisterChequeRow | RegisterBlankRow][] = []
  const unnumbered: RegisterChequeRow[] = []
  for (const c of cheques) {
    const row: RegisterChequeRow = {
      kind: 'cheque',
      id: c.id,
      cheque_no: c.cheque_no,
      payee: c.payee,
      entity_id: c.entity_id,
      entity_name: c.entity_name,
      amount_paise: c.amount_paise,
      cheque_date: c.cheque_date,
      memo: c.memo,
      status: c.status,
      batch_id: c.batch_id,
      created_at: c.created_at,
      pdf_path: c.pdf_path,
      cal_version: c.cal_version,
      pdc: pdcState(c, today),
      duplicate: c.cheque_no ? (seen.get(c.cheque_no) ?? 0) > 1 : false
    }
    const p = parseLeaf(c.cheque_no)
    if (!p) {
      unnumbered.push(row)
      continue
    }
    row.book_id = bookOf(p)
    leaves.push([p, row])
  }
  for (const b of blanks) {
    const p = parseLeaf(b.cheque_no)
    const row: RegisterBlankRow = {
      kind: 'blank',
      id: b.id,
      cheque_no: b.cheque_no,
      reason: b.reason,
      created_at: b.created_at,
      status: 'blank',
      duplicate: (seen.get(b.cheque_no) ?? 0) > 1,
      book_id: p ? bookOf(p) : null
    }
    if (p) leaves.push([p, row])
  }

  leaves.sort((a, b) => cmpStr(a[0][0], b[0][0]) || a[0][1] - b[0][1] || (a[1].id ?? 0) - (b[1].id ?? 0))
  const rows: RegisterRow[] = []

  const gap = (prefix: string, lo: number, hi: number, width: number, bookId: number | null, sure: boolean): void => {
    const numbers: string[] = []
    for (let v = lo; v <= hi; v++) numbers.push(fmtLeaf(prefix, v, width))
    rows.push({
      kind: 'gap',
      book_id: bookId,
      sure,
      from_no: fmtLeaf(prefix, lo, width),
      to_no: fmtLeaf(prefix, hi, width),
      numbers,
      count: hi - lo + 1
    })
  }

  // Leaves skipped at the front of a book come before its first known one.
  const starts = new Map<number, number>()
  for (const [bookId, , first] of ranges) starts.set(bookId, first)
  let prev: [Leaf, RegisterChequeRow | RegisterBlankRow] | null = null
  for (const [p, row] of leaves) {
    const bookId = row.book_id ?? null
    if (prev === null || (prev[1].book_id ?? null) !== bookId || prev[0][0] !== p[0]) {
      const start = bookId !== null ? starts.get(bookId) : undefined
      if (start !== undefined && p[1] > start) gap(p[0], start, p[1] - 1, p[2], bookId, true)
    } else {
      const step = p[1] - prev[0][1]
      if (step > 1 && (bookId !== null || step - 1 <= LOOSE_GAP)) {
        gap(p[0], prev[0][1] + 1, p[1] - 1, p[2], bookId, bookId !== null)
      }
    }
    rows.push(row)
    prev = [p, row]
  }
  rows.reverse()

  // Shown newest first by when each was written, not by leaf number: a new
  // book can be numbered below the last one, and leaf order would bury it.
  // A gap or blank leaf stays under the leaf above it, where it was found.
  const groups: RegisterRow[][] = []
  for (const row of rows) {
    if (row.kind === 'cheque' || !groups.length) groups.push([row])
    else groups[groups.length - 1].push(row)
  }
  for (const row of unnumbered) groups.push([row])
  const createdAt = (row: RegisterRow): string => ('created_at' in row && row.created_at) || ''
  const idOf = (row: RegisterRow): number => ('id' in row ? (row.id ?? 0) : 0)
  // Descending and stable: groups that tie keep the order they were found in, as Python's reverse sort does.
  groups.sort((a, b) => cmpStr(createdAt(b[0]), createdAt(a[0])) || idOf(b[0]) - idOf(a[0]))
  return { rows: groups.flat() }
}
