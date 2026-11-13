// Renders one invented cheque with the app's own Chromium and with an installed
// Google Chrome, and reports the page size of each PDF. It never prints.
// Run with: pnpm build && electron scripts/render-check.cjs <output folder>
const { app, BrowserWindow } = require('electron')
const { execFileSync } = require('node:child_process')
const { mkdirSync, readFileSync, writeFileSync, existsSync } = require('node:fs')
const { join } = require('node:path')
const { pathToFileURL } = require('node:url')

const root = join(__dirname, '..')
const out = process.argv[2] || join(root, 'build', 'render-check')
mkdirSync(out, { recursive: true })

const cal = JSON.parse(readFileSync(join(root, 'cheque-calibration.json'), 'utf8'))
const cheque = {
  payee: 'Greenleaf Gardening Services',
  cheque_date: '2026-11-12',
  amount_words: 'Twelve Thousand Four Hundred Fifty Only',
  amount_figures: '12,450/-',
  crossing: true,
  strike_order: false,
  star_fill: true,
  test_pattern: false
}
const values = {
  page_css: '@page { size: 92mm 204mm; margin: 0; }',
  root_w: '92mm', root_h: '204mm', sheet_x: '0mm', sheet_y: '0mm', page_w: '92', page_h: '204',
  cheque_css: readFileSync(join(root, 'static', 'cheque.css'), 'utf8'),
  layout_js: readFileSync(join(root, 'static', 'cheque-layout.js'), 'utf8'),
  data: JSON.stringify({ cal, cheque })
}
const html = readFileSync(join(root, 'templates', 'cheque_print.html.tpl'), 'utf8').replace(
  /\$\$|\$\{(\w+)\}|\$(\w+)/g,
  (m, a, b) => (m === '$$' ? '$' : values[a ?? b])
)
const htmlPath = join(out, 'cheque.html')
writeFileSync(htmlPath, html)

const box = (file) => {
  const m = readFileSync(file).toString('latin1').match(/\/MediaBox\s*\[\s*([\d.\-]+)\s+([\d.\-]+)\s+([\d.\-]+)\s+([\d.\-]+)/)
  return m ? `${(((m[3] - m[1]) / 72) * 25.4).toFixed(3)} x ${(((m[4] - m[2]) / 72) * 25.4).toFixed(3)} mm` : 'no page box'
}

app.dock?.hide()
app.on('window-all-closed', () => {})
app.whenReady().then(async () => {
  const window = new BrowserWindow({ show: false, webPreferences: { sandbox: true } })
  await window.loadURL(pathToFileURL(htmlPath).href)
  await window.webContents.executeJavaScript('document.fonts.ready.then(() => true)')
  const pdf = await window.webContents.printToPDF({
    preferCSSPageSize: true, printBackground: true, displayHeaderFooter: false, scale: 1,
    margins: { top: 0, bottom: 0, left: 0, right: 0 }
  })
  const own = join(out, 'electron.pdf')
  writeFileSync(own, pdf)
  console.log(`electron ${process.versions.chrome}: ${box(own)}`)

  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
  if (existsSync(chrome)) {
    const ref = join(out, 'chrome.pdf')
    execFileSync(chrome, ['--headless=new', '--disable-gpu', '--no-pdf-header-footer', `--print-to-pdf=${ref}`,
      '--virtual-time-budget=6000', pathToFileURL(htmlPath).href], { stdio: 'ignore', timeout: 90000 })
    const version = execFileSync(chrome, ['--version']).toString().trim()
    console.log(`${version}: ${box(ref)}`)
  } else {
    console.log('Google Chrome is not installed; no reference render.')
  }
  app.quit()
}).catch((error) => { console.error(error); app.exit(1) })
