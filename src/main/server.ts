import { randomBytes } from 'node:crypto'
import { copyFileSync, existsSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { app, utilityProcess, type UtilityProcess } from 'electron'
import type { RenderReply, RenderRequest, ServerReady, ServerRestart } from '../shared/types'
import { htmlFileToPdf } from './pdf'

export interface RunningServer {
  url: string
  token: string
  registerPath: string
  calibrationPath: string
  stop(): void
}

/** Files that ship with the app: the frontend, the print template, the default calibration. */
const shipped = (...parts: string[]) => join(app.isPackaged ? process.resourcesPath : app.getAppPath(), ...parts)

/**
 * Starts the cheque desk's server in its own process and resolves once it is listening.
 *
 * The register, the calibration and rendered PDFs live in the app's data
 * folder, so they survive updates. The calibration starts as a copy of the
 * shipped one and is the treasurer's to edit from then on: it holds
 * measurements of their printer.
 */
export function startServer(onCrash: (code: number) => void, onRestart: () => void): Promise<RunningServer> {
  const data = app.getPath('userData')
  mkdirSync(data, { recursive: true })
  const registerPath = join(data, 'banyan.db')
  const calibrationPath = join(data, 'cheque-calibration.json')
  if (!existsSync(calibrationPath)) copyFileSync(shipped('cheque-calibration.json'), calibrationPath)

  // A fresh secret on every launch. Only this app's window is given it.
  const token = randomBytes(24).toString('hex')

  // Development builds never reach the printer: a cheque leaf is usually loaded.
  const printDisabled = app.isPackaged ? process.env.BANYAN_PRINT_DISABLED === '1' : process.env.BANYAN_ALLOW_PRINT !== '1'

  return new Promise((resolve, reject) => {
    let ready = false
    let stopping = false

    const child: UtilityProcess = utilityProcess.fork(join(__dirname, 'server.js'), [], {
      serviceName: `${app.getName()} server`,
      stdio: 'inherit',
      env: {
        ...process.env,
        BANYAN_DB: registerPath,
        BANYAN_CAL: calibrationPath,
        BANYAN_PDF_DIR: join(data, 'cheques'),
        BANYAN_STATIC_DIR: join(data, 'static'),
        BANYAN_CONNECTION: join(data, 'connection.json'),
        BANYAN_APP_STATIC: shipped('static'),
        BANYAN_TEMPLATES: shipped('templates'),
        BANYAN_TOKEN: token,
        BANYAN_PRINT_DISABLED: printDisabled ? '1' : '0'
      }
    })

    child.on('message', (message: ServerReady | RenderRequest | ServerRestart) => {
      if (message?.type === 'restart') {
        onRestart()
      } else if (message?.type === 'ready') {
        ready = true
        resolve({
          url: `http://127.0.0.1:${message.port}`,
          token,
          registerPath,
          calibrationPath,
          stop() {
            stopping = true
            child.kill()
          }
        })
      } else if (message?.type === 'render') {
        // The server composes the page; this process owns the Chromium that prints it.
        const reply = (error?: string) =>
          child.postMessage({ type: 'rendered', id: message.id, error } satisfies RenderReply)
        htmlFileToPdf(message.htmlPath, message.pdfPath).then(
          () => reply(),
          (error: Error) => reply(`no PDF was produced: ${error.message}`)
        )
      }
    })

    child.on('exit', (code) => {
      if (stopping) return
      if (ready) onCrash(code)
      else reject(new Error(`The server exited before it was ready (code ${code}).`))
    })
  })
}
