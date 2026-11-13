/**
 * Rupee amounts: parsing, Indian-system words, and cheque-safe figures.
 *
 * Money is carried as an integer number of paise everywhere in this app. A cheque
 * is a payment instruction, so a half-paise rounding artefact is not a cosmetic
 * problem — floats are never used to hold a value.
 *
 * The words produced here are what gets printed on the leaf and what is stored in
 * the register, so this module is the single authority on both.
 */

import { PY_WS, floorDiv, pyInt, pyMod, pyRepr } from './pycompat'

export const ONES = [
  '', 'One', 'Two', 'Three', 'Four', 'Five', 'Six', 'Seven', 'Eight',
  'Nine', 'Ten', 'Eleven', 'Twelve', 'Thirteen', 'Fourteen', 'Fifteen',
  'Sixteen', 'Seventeen', 'Eighteen', 'Nineteen'
]
export const TENS = ['', '', 'Twenty', 'Thirty', 'Forty', 'Fifty', 'Sixty', 'Seventy', 'Eighty', 'Ninety']

// A cheque above this is refused rather than printed with a number the layout was
// never measured for. 100 crore in paise.
export const MAX_PAISE = 100_00_00_000 * 100

// Python's `\d` and `\s` are Unicode-aware: digits of any script are read as
// numbers and Python's own whitespace set is stripped. Both are mirrored here.
const STRIP_RE = new RegExp(`[₹,${PY_WS}]`, 'g')
const AMOUNT_RE = /^\p{Nd}+(\.\p{Nd}{0,2})?$/u

/** Raised when a string cannot be read as a printable rupee amount. */
export class AmountError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'AmountError'
  }
}

/** A rupee amount held as integer paise. */
export class Amount {
  constructor(readonly paiseTotal: number) {}

  get rupees(): number {
    return floorDiv(this.paiseTotal, 100)
  }

  get paise(): number {
    return pyMod(this.paiseTotal, 100)
  }

  // -- presentation ----------------------------------------------------
  /** Amount in words, uppercase, as printed after the RUPEES label. */
  get words(): string {
    return amountWords(this.paiseTotal)
  }

  /** Amount in digits for the figures box, always terminated by '/-'. */
  get figures(): string {
    return amountFigures(this.paiseTotal)
  }

  /** Indian-grouped digits with no terminator, for screens and CSV. */
  get plain(): string {
    return `${groupIndian(this.rupees)}.${String(this.paise).padStart(2, '0')}`
  }

  toString(): string {
    return this.plain
  }
}

/**
 * Read user input into an Amount, or throw AmountError.
 *
 * Accepts '12,500', '₹12500.50', ' 12500 '. Rejects anything with more than
 * two decimals rather than rounding it: silently turning 100.005 into 100.01
 * on a payment instruction is not this function's decision to make.
 *
 * A number is read as Python reads an int (whole rupees) when it is an integer
 * and refused as a float otherwise. JS cannot tell 5.0 from 5, so 5.0 is rupees.
 */
export function parseAmount(raw: string | number | Amount): Amount {
  if (raw instanceof Amount) return raw
  if (typeof raw === 'number') {
    if (Number.isInteger(raw)) return checked(raw * 100)
    throw new AmountError('amounts must be given as text or paise, not a float')
  }

  const text = String(raw)
  const clean = text.replace(STRIP_RE, '')
  if (!clean) throw new AmountError('enter an amount')
  if (!AMOUNT_RE.test(clean)) throw new AmountError(`${pyRepr(text)} is not a valid amount`)

  const dot = clean.indexOf('.')
  const rupeePart = dot < 0 ? clean : clean.slice(0, dot)
  const paisePart = dot < 0 ? '' : clean.slice(dot + 1)
  // Sliced by code point: a digit outside the BMP is two UTF-16 units.
  const paise = paisePart ? pyInt([...(paisePart + '00')].slice(0, 2).join('')) : 0
  return checked(pyInt(rupeePart) * 100 + paise)
}

function checked(paiseTotal: number): Amount {
  if (paiseTotal <= 0) throw new AmountError('amount must be more than zero')
  if (paiseTotal >= MAX_PAISE) {
    throw new AmountError('amount is above the ₹100 crore limit this layout was measured for')
  }
  return new Amount(paiseTotal)
}

// -- words ---------------------------------------------------------------
function two(n: number): string {
  return n < 20 ? ONES[n] : TENS[floorDiv(n, 10)] + (n % 10 ? ' ' + ONES[n % 10] : '')
}

function three(n: number): string {
  const hundreds = floorDiv(n, 100)
  const rest = pyMod(n, 100)
  let head = hundreds ? `${ONES[hundreds]} Hundred` : ''
  if (head && rest) head += ' '
  return head + (rest ? two(rest) : '')
}

/** Non-negative integer in the Indian crore/lakh/thousand system. */
export function indianWords(n: number): string {
  if (n === 0) return 'Zero'
  const parts: string[] = []
  const crore = floorDiv(n, 10_000_000)
  n = pyMod(n, 10_000_000)
  const lakh = floorDiv(n, 100_000)
  n = pyMod(n, 100_000)
  const thousand = floorDiv(n, 1_000)
  n = pyMod(n, 1_000)
  if (crore) parts.push(`${indianWords(crore)} Crore`)
  if (lakh) parts.push(`${two(lakh)} Lakh`)
  if (thousand) parts.push(`${two(thousand)} Thousand`)
  if (n) parts.push(three(n))
  return parts.join(' ')
}

/**
 * Cheque wording for an amount in paise.
 *
 * Sub-rupee amounts read 'FIFTY PAISE ONLY', not 'ZERO AND FIFTY PAISE ONLY' —
 * a leading Zero on a payment instruction invites a query at the counter.
 */
export function amountWords(paiseTotal: number): string {
  const rupees = floorDiv(paiseTotal, 100)
  const paise = pyMod(paiseTotal, 100)
  let words: string
  if (rupees && paise) words = `${indianWords(rupees)} And ${indianWords(paise)} Paise`
  else if (rupees) words = indianWords(rupees)
  else words = `${indianWords(paise)} Paise`
  return `${words} Only`.toUpperCase()
}

// -- figures -------------------------------------------------------------
/** Group digits 12,34,567 rather than 1,234,567. */
export function groupIndian(n: number): string {
  const s = String(n)
  if (s.length <= 3) return s
  let head = s.slice(0, -3)
  const tail = s.slice(-3)
  const parts: string[] = []
  while (head.length > 2) {
    parts.unshift(head.slice(-2))
    head = head.slice(0, -2)
  }
  if (head) parts.unshift(head)
  return [...parts, tail].join(',')
}

/**
 * Figures for the amount box.
 *
 * Always ends in '/-' so no digit can be appended in the blank space that
 * follows it inside the printed box.
 */
export function amountFigures(paiseTotal: number): string {
  const rupees = floorDiv(paiseTotal, 100)
  const paise = pyMod(paiseTotal, 100)
  let body = groupIndian(rupees)
  if (paise) body += `.${String(paise).padStart(2, '0')}`
  return body + '/-'
}
