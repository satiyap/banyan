// Where the server finds its files. Every path comes from an environment
// variable so the same server runs inside the desktop app (which sets them all)
// and on its own under plain Node (where the defaults point at this checkout).
import { resolve } from 'node:path'

const root = process.cwd()
const env = (name: string, fallback: string) => resolve(process.env[name] || fallback)

export interface Config {
  /** The register. Financial records: never seeded, reset or bulk-edited. */
  dbPath: string
  /** Measured field geometry. Re-read whenever the file changes. */
  calPath: string
  /** Rendered cheque and report PDFs. Disposable. */
  pdfDir: string
  /** The frontend: index.html, scripts, styles, fonts. */
  staticDir: string
  /** The print template. */
  templatesDir: string
  /** Where a prepared leaf photo may live. It shows a real cheque, so it is never shipped with the app. */
  dataStaticDir: string
  /** The API URL and user key entered on first launch, or the fact that it was skipped. */
  connectionPath: string
  /** A specific browser to render with when not running inside the desktop app. */
  chrome: string
  /** When set, every request must carry this value in the session cookie. */
  token: string
  /** When true, nothing is ever handed to the print queue. On during development and tests. */
  printDisabled: boolean
  port: number
}

export function loadConfig(): Config {
  const pdfDir = env('BANYAN_PDF_DIR', `${root}/build/cheques`)
  return {
    dbPath: env('BANYAN_DB', `${root}/data/banyan.db`),
    calPath: env('BANYAN_CAL', `${root}/cheque-calibration.json`),
    pdfDir,
    staticDir: env('BANYAN_APP_STATIC', `${root}/static`),
    templatesDir: env('BANYAN_TEMPLATES', `${root}/templates`),
    dataStaticDir: env('BANYAN_STATIC_DIR', resolve(pdfDir, '..', 'static')),
    connectionPath: env('BANYAN_CONNECTION', `${root}/data/connection.json`),
    chrome: process.env.BANYAN_CHROME ?? '',
    token: process.env.BANYAN_TOKEN ?? '',
    printDisabled: process.env.BANYAN_PRINT_DISABLED === '1',
    port: Number(process.env.BANYAN_PORT ?? 0)
  }
}

export const SESSION_COOKIE = 'banyan_session'
