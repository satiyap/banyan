// Check that what the renderer produces lands where the calibration says.
//
// A silent geometry regression in this app is not a failed test, it is a ruined
// cheque leaf. So rather than trusting that a change to the renderer, the shared
// layout module or the print CSS was harmless, this renders through the real
// composition code and measures the result.
//
// It asserts properties, not a golden file: a golden PDF would have to be
// regenerated on every calibration change, which trains you to accept differences.
// These checks stay true whatever the measurements are.
//
//     pnpm verify-print
//
// Needs pdftoppm (poppler) to rasterise and an installed Google Chrome to render.
// The desktop app itself renders with its built-in Chromium; `pnpm render-check`
// confirms the two produce the same page. Nothing here goes near a printer.
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { afterAll, describe, expect, it } from 'vitest'
import {
  CHEQUE_H_MM, CHEQUE_W_MM, HARDWARE_INSET_MM, MICR_TOP_MM, PAGE_H_MM, PAGE_W_MM, loadCalibration
} from '../src/server/calibration'
import { renderPdf } from '../src/server/render'

const DPI = 600
const INK = 128 // a pixel darker than this is toner

// Room for the parts of a glyph that sit outside its declared box: ascenders
// above the box top, descenders below its baseline.
const GLYPH_SLACK_MM = 1.5

const BLANK = {
  payee: '', cheque_date: '', amount_words: '', amount_figures: '',
  crossing: false, strike_order: false, star_fill: false, test_pattern: false
}

// One probe per field, each rendering that field and nothing else.
const PROBES: [string, Record<string, unknown>][] = [
  ['date', { cheque_date: '2026-08-12' }],
  ['payee', { payee: 'Wjgqy Wjgqy Wjgqy' }],
  ['words1', { amount_words: 'WJGQY WJGQY' }],
  ['amount', { amount_figures: '1,00,000.73/-' }]
]

const root = resolve(__dirname, '..')
const paths = { templatesDir: join(root, 'templates'), staticDir: join(root, 'static') }
const cal = loadCalibration(join(root, 'cheque-calibration.json'))
const tmp = mkdtempSync(join(tmpdir(), 'verify-print-'))
afterAll(() => rmSync(tmp, { recursive: true, force: true }))

function readPgm(path: string): { w: number; h: number; px: Buffer } {
  const data = readFileSync(path)
  const isSpace = (b: number) => b === 32 || (b >= 9 && b <= 13)
  const parts: string[] = []
  let i = 0
  while (parts.length < 4) {
    while (isSpace(data[i])) i++
    let j = i
    while (!isSpace(data[j])) j++
    parts.push(data.subarray(i, j).toString('latin1'))
    i = j
  }
  i += 1
  const w = Number(parts[1])
  const h = Number(parts[2])
  return { w, h, px: data.subarray(i, i + w * h) }
}

/**
 * Ink extent of a rendered page, returned in cheque millimetres: [x0, x1, y0, y1].
 *
 * The leaf is fed sideways, so the page axes are the leaf's axes rotated:
 *     cheque_x = page_y        cheque_y = PAGE_W - page_x
 */
function inkBoxMm(pdf: string): [number, number, number, number] | null {
  const out = pdf.replace(/\.pdf$/, '')
  execFileSync('pdftoppm', ['-r', String(DPI), '-gray', pdf, out], { stdio: 'pipe' })
  const { w, h, px } = readPgm(`${out}-1.pgm`)
  let minX = w, maxX = -1, minY = h, maxY = -1
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      if (px[y * w + x] < INK) {
        if (x < minX) minX = x
        if (x > maxX) maxX = x
        if (y < minY) minY = y
        if (y > maxY) maxY = y
      }
    }
  }
  if (maxX < 0) return null
  const mm = 25.4 / DPI
  return [minY * mm, maxY * mm, PAGE_W_MM - maxX * mm, PAGE_W_MM - minX * mm]
}

describe(`calibration v${cal.version}, page ${PAGE_W_MM}x${PAGE_H_MM}mm`, () => {
  // A full cheque: page geometry, and the two zones nothing may enter.
  it('a full cheque is one 92 x 204mm page with no ink in the MICR band or past the printable edge', async () => {
    const full = await renderPdf(
      cal,
      { ...BLANK, payee: 'Wjgqy Wjgqy', cheque_date: '2026-08-12', amount_words: 'ONE LAKH ONLY',
        amount_figures: '1,00,000/-', crossing: true },
      join(tmp, 'full.pdf'),
      paths
    )
    expect(Math.abs(full.page_w_mm - PAGE_W_MM), 'page width is 92mm at 1:1').toBeLessThanOrEqual(0.5)
    expect(Math.abs(full.page_h_mm - PAGE_H_MM), 'page height is 204mm at 1:1').toBeLessThanOrEqual(0.5)
    expect(full.pages, 'exactly one page').toBe(1)

    const box = inkBoxMm(full.pdf_path)
    expect(box, 'the cheque rendered any ink at all').not.toBeNull()
    const [, x1, , y1] = box!
    expect(y1, `no ink in the MICR band (starts ${MICR_TOP_MM}mm)`).toBeLessThan(MICR_TOP_MM)
    expect(x1, 'all ink is on the leaf (x)').toBeLessThanOrEqual(CHEQUE_W_MM)
    expect(y1, 'all ink is on the leaf (y)').toBeLessThanOrEqual(CHEQUE_H_MM)
    // The crossing is deliberately anchored inside the printer's dead margin
    // and accepted as clipped, so only the right/bottom edges are asserted.
    expect(x1, 'nothing runs past the printable right edge').toBeLessThanOrEqual(CHEQUE_W_MM - HARDWARE_INSET_MM)
  })

  // Each field lands inside the box the calibration declares for it.
  for (const [name, data] of PROBES) {
    it(`${name} lands inside the box the calibration declares for it`, async () => {
      const f = cal.fields[name] as Record<string, number>
      const res = await renderPdf(cal, { ...BLANK, ...data }, join(tmp, `${name}.pdf`), paths)
      const box = inkBoxMm(res.pdf_path)
      expect(box, `${name}: rendered nothing`).not.toBeNull()
      const [x0, x1, y0, y1] = box!
      const top = f.y + cal.dy - GLYPH_SLACK_MM
      const bottom = f.y + cal.dy + (f.size ?? 0) + GLYPH_SLACK_MM
      const left = (name === 'date' ? f.x - (f.pitch ?? 0) / 2 : f.x) + cal.dx - GLYPH_SLACK_MM
      expect(y0, `${name} sits in its declared band (top)`).toBeGreaterThanOrEqual(top)
      expect(y1, `${name} sits in its declared band (bottom)`).toBeLessThanOrEqual(bottom)
      expect(x0, `${name} starts at or after its x`).toBeGreaterThanOrEqual(left)
      if (f.w) expect(x1, `${name} stays inside its width`).toBeLessThanOrEqual(f.x + cal.dx + f.w + GLYPH_SLACK_MM)
    })
  }
})
