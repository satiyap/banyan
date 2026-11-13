// The monthly report as an A4 PDF, rendered by the same engine that renders
// cheques. This is paperwork for a committee, not a leaf: nothing here touches
// the cheque geometry, and a report never goes near the printer.
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { htmlToPdf, RenderError } from './host'

// Same slots as the desk (year.js HEAD_COLOURS), in head order.
const COLOURS = ['#34548a', '#2f7d74', '#a07a1c', '#7a4e8c', '#a8503a', '#4f7a4a', '#5f7896']
const OTHER = '#9aa3a8'

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

interface Head {
  head: string
  paise: number
}

interface Month {
  label: string
  future: boolean
  current: boolean
  count: number
  paise: number
  heads: Record<string, number>
  entities: { name: string; head: string; paise: number }[]
}

/** The shape reports.monthly() returns. Only the fields the report reads are listed. */
export interface MonthlyReport {
  label: string
  mode: string
  heads: Head[]
  months: Month[]
  totals: { paise: number; count: number; per_month_paise: number; months_paid: number }
}

/** Python's html.escape, quotes included. */
function e(text: string): string {
  return String(text)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#x27;')
}

/**
 * Python's `{x:.Nf}`: like toFixed, but an exact tie rounds to the even digit.
 *
 * A tie exists only when the stored binary value sits exactly halfway, which
 * is when x * 2^(digits+1) is an odd whole number. Testing x * 10^digits for a
 * trailing .5 instead would misread values such as 62.05, which is stored
 * slightly above or below the halfway point.
 */
function fixed(x: number, digits: number): string {
  const n = x * 2 ** (digits + 1)
  if (Number.isInteger(n) && n % 2 !== 0) {
    const lower = Math.floor(x * 10 ** digits)
    return ((lower % 2 === 0 ? lower : lower + 1) / 10 ** digits).toFixed(digits)
  }
  return x.toFixed(digits)
}

function inr(paise: number): string {
  let s = String(Math.floor(Math.abs(paise) / 100))
  if (s.length > 3) {
    let head = s.slice(0, -3)
    const tail = s.slice(-3)
    const parts: string[] = []
    while (head.length > 2) {
      parts.unshift(head.slice(-2))
      head = head.slice(0, -2)
    }
    s = [...(head ? [head] : []), ...parts].join(',') + ',' + tail
  }
  return (paise < 0 ? '-' : '') + '₹' + s
}

const money = (paise: number) => (paise ? inr(paise) : '<span class="nil">–</span>')

function lakhs(paise: number): string {
  const r = paise / 100
  if (r >= 1e7) return `₹${fixed(r / 1e7, 1)} crore`
  if (r >= 1e5) return `₹${fixed(r / 1e5, 1)} lakh`
  return inr(paise)
}

export function buildHtml(d: MonthlyReport, fontsDir: string, today: Date = new Date()): string {
  const colour: Record<string, string> = {}
  d.heads.forEach((x, i) => (colour[x.head] = i < COLOURS.length ? COLOURS[i] : OTHER))
  const begun = d.months.filter((m) => !m.future)
  const first = begun.findIndex((m) => m.count)
  const months = first >= 0 ? begun.slice(first) : []
  const paying = d.heads.filter((x) => x.paise)
  const heads = paying.length ? paying : d.heads
  const t = d.totals
  const sw = (h: string) => `<i class="sw" style="background:${colour[h]}"></i>`

  const short = (m: Month) => {
    const [mon, yr] = m.label.split(/\s+/)
    return `${mon} ${yr.slice(2)}`
  }

  const span = months.length ? `${months[0].label} to ${months[months.length - 1].label}` : d.label
  const title = d.mode === 'fy' ? d.label : 'The last six months'
  const th = months
    .map((m) => `<th class="num">${short(m)}${m.current ? '<small>to date</small>' : ''}</th>`)
    .join('')
  const body = heads
    .map(
      (x) =>
        `<tr><th>${sw(x.head)}${e(x.head)}</th>` +
        months.map((m) => `<td class="num">${money(m.heads[x.head] ?? 0)}</td>`).join('') +
        `<td class="num strong">${money(x.paise)}</td></tr>`
    )
    .join('')
  const foot = months.map((m) => `<td class="num">${money(m.paise)}</td>`).join('')

  const top = Math.max(...(months.length ? months.map((m) => m.paise) : [1])) || 1
  const bars = months
    .map(
      (m) =>
        `<div class="bar-row"><span class="bl">${short(m)}</span><div class="bar">` +
        heads
          .filter((x) => m.heads[x.head])
          .map(
            (x) =>
              `<span style="background:${colour[x.head]};width:${fixed((m.heads[x.head] / top) * 100, 2)}%"></span>`
          )
          .join('') +
        `</div><span class="num">${money(m.paise)}</span></div>`
    )
    .join('')
  const legend = heads.map((x) => `<span>${sw(x.head)}${e(x.head)}</span>`).join('')

  let detail = ''
  for (const m of [...months].reverse()) {
    if (!m.count) continue
    let groups = ''
    for (const x of heads) {
      const amt = m.heads[x.head] ?? 0
      if (!amt) continue
      const lis = m.entities
        .filter((n) => n.head === x.head)
        .map((n) => `<li><span>${e(n.name)}</span><span class="num">${inr(n.paise)}</span></li>`)
        .join('')
      groups +=
        `<div class="grp"><h4>${sw(x.head)}${e(x.head)}` +
        `<span class="num">${inr(amt)}</span></h4><ul>${lis}</ul></div>`
    }
    const note = m.current ? '<small>month to date</small>' : ''
    detail +=
      `<section class="month"><header><h3>${e(m.label)}${note}</h3>` +
      `<p><b>${inr(m.paise)}</b> across ${m.count} cheque${m.count !== 1 ? 's' : ''}</p>` +
      `</header><div class="grps">${groups}</div></section>`
  }

  const lede = t.count
    ? `${lakhs(t.per_month_paise)} a month on average, over the ${t.months_paid} ` +
      `month${t.months_paid !== 1 ? 's' : ''} with cheques.`
    : 'No cheques in this period.'
  const files: [string, number, string][] = [
    ['Hedvig Letters Serif', 400, 'hedvig-letters-serif-latin-400-normal.woff2'],
    ['Geist', 400, 'geist-sans-latin-400-normal.woff2'],
    ['Geist', 500, 'geist-sans-latin-500-normal.woff2'],
    ['Geist', 600, 'geist-sans-latin-600-normal.woff2']
  ]
  const fonts = files
    .map(([n, w, f]) => `@font-face{font-family:"${n}";font-weight:${w};src:url("${pathToFileURL(join(fontsDir, f)).href}")}`)
    .join('')
  const generated = `${String(today.getDate()).padStart(2, '0')} ${MONTHS[today.getMonth()]} ${today.getFullYear()}`

  return `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Monthly spend by head</title><style>
${fonts}
@page{size:A4;margin:16mm 15mm 18mm}
:root{--ink:#16202e;--ink-2:#3d4754;--muted:#6a737d;--rule:#d3d9dc;--zebra:#f3f6f7}
*{box-sizing:border-box;-webkit-print-color-adjust:exact;print-color-adjust:exact}
body{margin:0;color:var(--ink);font:9.5pt/1.45 "Geist",sans-serif;font-variant-numeric:tabular-nums}
h1,h2,h3{font-family:"Hedvig Letters Serif",serif;font-weight:400;letter-spacing:-.015em;margin:0}
.mast{display:flex;justify-content:space-between;align-items:baseline;border-bottom:2px solid var(--ink);padding-bottom:6pt;color:var(--ink-2);font-size:8.5pt}
.mast b{font:400 11pt "Hedvig Letters Serif",serif;color:var(--ink)}
.eyebrow{margin:16pt 0 2pt;color:var(--ink-2);font-weight:500}
h1{font-size:24pt;line-height:1.1}.lede{color:var(--ink-2);margin:4pt 0 0}
.kpis{display:flex;gap:28pt;margin:14pt 0 4pt}.kpis div{display:flex;flex-direction:column}
.kpis b{font:400 16pt "Hedvig Letters Serif",serif}.kpis span{color:var(--muted);font-size:8pt}
h2{font-size:13pt;margin:20pt 0 7pt}
.sw{display:inline-block;width:7pt;height:7pt;border-radius:1.5pt;margin-right:5pt}
table{width:100%;border-collapse:collapse}th,td{padding:5pt 6pt;border-bottom:.5pt solid var(--rule);text-align:left}
thead th{font-weight:500;color:var(--ink-2);font-size:8pt;vertical-align:bottom}
tbody th{font-weight:500}tbody tr:nth-child(even){background:var(--zebra)}
tfoot td,tfoot th{font-weight:600;border-top:1.2pt solid var(--ink);border-bottom:0}
.num{text-align:right;white-space:nowrap}.strong{font-weight:600}.nil{color:var(--muted)}
small{display:block;font-weight:400;font-size:7pt;color:var(--muted)}
.legend{display:flex;gap:12pt;color:var(--ink-2);font-size:8pt;margin-bottom:6pt}
.bar-row{display:grid;grid-template-columns:40pt 1fr 62pt;gap:8pt;align-items:center;margin:4pt 0}
.bl{color:var(--ink-2);font-size:8.5pt}.bar{display:flex;height:11pt}.bar span+span{border-left:1pt solid #fff}
.month{border-top:.5pt solid var(--rule);padding:10pt 0 4pt;break-inside:avoid}
.month header{display:flex;justify-content:space-between;align-items:baseline}
.month h3{font-size:12pt}.month h3 small{display:inline;margin-left:6pt;font-family:"Geist",sans-serif}
.month p{margin:0;color:var(--muted);font-size:8.5pt}.month p b{font:400 11pt "Hedvig Letters Serif",serif;color:var(--ink)}
.grps{columns:2;column-gap:22pt;margin-top:6pt}.grp{break-inside:avoid;margin-bottom:8pt}
h4{display:flex;align-items:center;margin:0 0 2pt;font-size:9pt;font-weight:600}h4 .num{margin-left:auto}
ul{list-style:none;margin:0;padding:0}li{display:flex;justify-content:space-between;gap:8pt;padding:1.5pt 0 1.5pt 12pt;color:var(--ink-2);border-bottom:.5pt dotted var(--rule)}
.keep{break-inside:avoid}
.foot{margin-top:16pt;color:var(--muted);font-size:7.5pt;border-top:.5pt solid var(--rule);padding-top:5pt}
</style></head><body>
<div class="mast"><b>Banyan</b><span>Monthly spend by head · generated ${generated}</span></div>
<p class="eyebrow">${e(span)}${d.mode === 'fy' ? ', April to March' : ''}</p>
<h1>${e(title)}: ${lakhs(t.paise)} paid</h1><p class="lede">${lede}</p>
<div class="kpis"><div><b>${inr(t.paise)}</b><span>total paid</span></div>
<div><b>${inr(t.per_month_paise)}</b><span>a month on average</span></div>
<div><b>${t.count}</b><span>cheques</span></div></div>
<div class="keep"><h2>Month by head</h2><table><thead><tr><th>Head</th>${th}<th class="num">Total</th></tr></thead>
<tbody>${body}</tbody><tfoot><tr><th>Total</th>${foot}<td class="num">${money(t.paise)}</td></tr></tfoot></table></div>
<div class="keep"><h2>How each month splits</h2><div class="legend">${legend}</div>${bars}</div>
<h2>Month by month</h2>${detail}
<p class="foot">From the cheque register: issued cheques only (printed, logged and queued); void and failed cheques are left out. Amounts by cheque date, in whole rupees.</p>
</body></html>`
}

export async function render(d: MonthlyReport, outPdf: string, fontsDir: string, chrome = ''): Promise<string> {
  mkdirSync(dirname(outPdf), { recursive: true })
  rmSync(outPdf, { force: true })
  const tmpDir = mkdtempSync(join(tmpdir(), 'monthly-report-'))
  const htmlPath = join(tmpDir, 'report.html')
  writeFileSync(htmlPath, buildHtml(d, fontsDir))
  try {
    await htmlToPdf(htmlPath, outPdf, chrome)
  } catch (error) {
    if ((error as Error).message === 'the renderer timed out') throw new RenderError('the report renderer timed out')
    throw new RenderError('no report PDF was produced')
  } finally {
    rmSync(tmpDir, { recursive: true, force: true })
  }
  return outPdf
}
