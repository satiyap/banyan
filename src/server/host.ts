// Turning an HTML file into a PDF.
//
// Inside the desktop app the server asks the main process, which prints with
// the Chromium that ships in the app: the same engine on every machine, at a
// version that only changes when the app is updated.
//
// On its own under plain Node there is no such process to ask, so it falls back
// to an installed Chrome, headless, with the
// flags that were verified against a ruler.
import { execFile } from 'node:child_process'
import { existsSync } from 'node:fs'
import { pathToFileURL } from 'node:url'
import type { RenderReply, RenderRequest } from '../shared/types'

export class RenderError extends Error {}

export const CHROME_CANDIDATES = [
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge',
  '/usr/bin/chromium'
]

// Extra renderer flags for the standalone path. None of these may touch page
// size or scaling: the page box check after every render would refuse it.
export const EXTRA_FLAGS = (process.env.BANYAN_CHROME_FLAGS ?? '').split(/\s+/).filter(Boolean)

const RENDER_TIMEOUT_MS = 90_000

/** True when the desktop app's own Chromium does the rendering. */
export const hasHostRenderer = (): boolean => Boolean(process.parentPort)

export function findChrome(explicit = ''): string {
  if (explicit) {
    if (existsSync(explicit)) return explicit
    throw new RenderError(`browser not found at ${explicit}`)
  }
  for (const candidate of CHROME_CANDIDATES) {
    if (existsSync(candidate)) return candidate
  }
  throw new RenderError('Google Chrome is required to render cheques and was not found')
}

/** What renders PDFs here, for the health check. */
export function rendererInfo(chrome = ''): { ok: boolean; path: string } {
  if (hasHostRenderer()) return { ok: true, path: 'built-in' }
  try {
    return { ok: true, path: findChrome(chrome) }
  } catch (error) {
    return { ok: false, path: (error as Error).message }
  }
}

let nextId = 1
const waiting = new Map<number, (reply: RenderReply) => void>()
let listening = false

function askHost(htmlPath: string, pdfPath: string): Promise<void> {
  if (!listening) {
    listening = true
    process.parentPort.on('message', (event: { data: RenderReply }) => {
      const reply = event.data
      if (reply?.type !== 'rendered') return
      waiting.get(reply.id)?.(reply)
      waiting.delete(reply.id)
    })
  }
  return new Promise((resolve, reject) => {
    const id = nextId++
    const timer = setTimeout(() => {
      waiting.delete(id)
      reject(new RenderError('the renderer timed out'))
    }, RENDER_TIMEOUT_MS)
    waiting.set(id, (reply) => {
      clearTimeout(timer)
      if (reply.error) reject(new RenderError(reply.error))
      else resolve()
    })
    process.parentPort.postMessage({ type: 'render', id, htmlPath, pdfPath } satisfies RenderRequest)
  })
}

function askChrome(htmlPath: string, pdfPath: string, chrome: string): Promise<void> {
  const browser = findChrome(chrome)
  // These flags match the print path that was physically verified against a
  // ruler. Do not add to them casually: --user-data-dir in particular makes
  // this invocation hang.
  const args = [
    '--headless=new', '--disable-gpu', '--no-pdf-header-footer',
    ...EXTRA_FLAGS,
    `--print-to-pdf=${pdfPath}`, '--virtual-time-budget=6000',
    pathToFileURL(htmlPath).href
  ]
  return new Promise((resolve, reject) => {
    execFile(browser, args, { timeout: RENDER_TIMEOUT_MS }, (error, stdout, stderr) => {
      if (error && (error as NodeJS.ErrnoException & { killed?: boolean }).killed) {
        return reject(new RenderError('the renderer timed out'))
      }
      if (!existsSync(pdfPath)) {
        const lines = (stderr || stdout || '').trim().split('\n')
        const tail = lines[lines.length - 1] || `exit code ${(error as { code?: number } | null)?.code ?? 0}`
        return reject(new RenderError(`no PDF was produced: ${tail}`))
      }
      resolve()
    })
  })
}

/** Render one HTML file to one PDF file. Rejects with RenderError if no PDF results. */
export function htmlToPdf(htmlPath: string, pdfPath: string, chrome = ''): Promise<void> {
  return hasHostRenderer() ? askHost(htmlPath, pdfPath) : askChrome(htmlPath, pdfPath, chrome)
}
