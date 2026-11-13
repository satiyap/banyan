// Composes a cheque into a print-ready PDF.
//
// The page is rendered at 92 x 204mm and the resulting PDF is checked against
// that size before anyone is allowed to spend a cheque leaf on it. The print
// dialog is not involved, so it cannot silently rescale or refit the page: the
// one failure mode that is indistinguishable from a geometry bug.
//
// The page box is verified on every single render. A PDF that is not 92 x 204mm
// at 1:1 is an error, never a warning.
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { PAGE_H_MM, PAGE_W_MM, type Calibration } from './calibration'
import { htmlToPdf, RenderError } from './host'

export { RenderError } from './host'

// Anything further than this from the declared page size means the render did
// not come out at 1:1 and must not be printed. Chromium's own rounding is ~0.15mm.
export const PAGE_TOLERANCE_MM = 0.5

export interface RenderPaths {
  /** Folder holding cheque_print.html.tpl. */
  templatesDir: string
  /** Folder holding cheque-layout.js and cheque.css, the same files the on-screen preview loads. */
  staticDir: string
  chrome?: string
}

export interface RenderResult {
  pdf_path: string
  page_w_mm: number
  page_h_mm: number
  pages: number
  html_path: string | null
}

const round2 = (n: number) => Math.round(n * 100) / 100

export function resultAsDict(r: RenderResult) {
  return { pdf: r.pdf_path, page_w_mm: round2(r.page_w_mm), page_h_mm: round2(r.page_h_mm), pages: r.pages }
}

/** Python's `{x:g}`: six significant digits, no trailing zeros. */
const g = (n: number) => String(Number(n.toPrecision(6)))

/** Python's string.Template.substitute for `$name` and `${name}`. */
function substitute(template: string, values: Record<string, string>): string {
  return template.replace(/\$\$|\$\{(\w+)\}|\$(\w+)/g, (match, braced: string, bare: string) => {
    if (match === '$$') return '$'
    const key = braced ?? bare
    if (!(key in values)) throw new RenderError(`the print template asks for an unknown value: ${key}`)
    return values[key]
  })
}

/**
 * Inline everything into one self-contained page.
 *
 * Self-contained matters: the page is written to a temp file and opened over
 * file://, where any external fetch would be blocked. If data were fetched
 * rather than inlined, a blocked request would print an empty or default
 * cheque instead of failing.
 */
export function buildHtml(cal: Calibration, cheque: Record<string, unknown>, paths: RenderPaths): string {
  const pageW = PAGE_W_MM
  const pageH = PAGE_H_MM
  let pageCss: string, rootW: string, rootH: string, sheetX: string, sheetY: string
  if (cal.paper === 'a4') {
    pageCss = '@page { size: A4; margin: 0; }'
    rootW = '210mm'
    rootH = '297mm'
    sheetX = `${g(cal.a4x)}mm`
    sheetY = `${g(cal.a4y)}mm`
  } else {
    pageCss = `@page { size: ${g(pageW)}mm ${g(pageH)}mm; margin: 0; }`
    rootW = `${g(pageW)}mm`
    rootH = `${g(pageH)}mm`
    sheetX = '0mm'
    sheetY = '0mm'
  }

  // A payee named "</script>" would otherwise close the tag it is sitting in.
  // These escapes are valid inside JSON strings and parse back to the originals.
  const payload = JSON.stringify({ cal: cal.asDict(), cheque })
    .replace(/</g, '\\u003c')
    .replace(/>/g, '\\u003e')
    .replace(/&/g, '\\u0026')

  return substitute(readFileSync(join(paths.templatesDir, 'cheque_print.html.tpl'), 'utf8'), {
    page_css: pageCss,
    root_w: rootW,
    root_h: rootH,
    sheet_x: sheetX,
    sheet_y: sheetY,
    page_w: g(pageW),
    page_h: g(pageH),
    cheque_css: readFileSync(join(paths.staticDir, 'cheque.css'), 'utf8'),
    layout_js: readFileSync(join(paths.staticDir, 'cheque-layout.js'), 'utf8'),
    data: payload
  })
}

/** Render one cheque to PDF and verify the page box before returning. */
export async function renderPdf(
  cal: Calibration,
  cheque: Record<string, unknown>,
  outPdf: string,
  paths: RenderPaths,
  keepHtml = false
): Promise<RenderResult> {
  mkdirSync(dirname(outPdf), { recursive: true })
  const html = buildHtml(cal, cheque, paths)

  const tmpDir = mkdtempSync(join(tmpdir(), 'cheque-render-'))
  const htmlPath = join(tmpDir, 'cheque.html')
  writeFileSync(htmlPath, html)
  // A PDF left over from an earlier render must never pass for this one.
  rmSync(outPdf, { force: true })

  try {
    await htmlToPdf(htmlPath, outPdf, paths.chrome)

    const [width, height, pages] = readPageBox(outPdf)
    const expected = cal.paper === 'a4' ? [210.0, 297.0] : [PAGE_W_MM, PAGE_H_MM]
    const off = Math.max(Math.abs(width - expected[0]), Math.abs(height - expected[1]))
    if (off > PAGE_TOLERANCE_MM) {
      throw new RenderError(
        `rendered page is ${width.toFixed(1)} x ${height.toFixed(1)}mm but must be ` +
          `${g(expected[0])} x ${g(expected[1])}mm — not safe to print`
      )
    }
    if (pages !== 1) throw new RenderError(`rendered ${pages} pages; a cheque must be exactly one`)

    return { pdf_path: outPdf, page_w_mm: width, page_h_mm: height, pages, html_path: keepHtml ? htmlPath : null }
  } finally {
    if (!keepHtml) rmSync(tmpDir, { recursive: true, force: true })
  }
}

/** Page width, height in mm and the page count, straight from the PDF. */
export function readPageBox(pdf: string): [number, number, number] {
  const data = readFileSync(pdf).toString('latin1')
  const boxes = [...data.matchAll(/\/MediaBox\s*\[\s*([\d.\-]+)\s+([\d.\-]+)\s+([\d.\-]+)\s+([\d.\-]+)/g)]
  if (boxes.length === 0) throw new RenderError('could not read the page size back from the PDF')
  const [x0, y0, x1, y1] = boxes[0].slice(1, 5).map(Number)
  return [((x1 - x0) / 72) * 25.4, ((y1 - y0) / 72) * 25.4, boxes.length]
}
