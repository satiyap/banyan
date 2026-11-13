// Print queue submission and job tracking.
//
// The cheque goes to the printer through `lp` with scaling and fit-to-page pinned
// off, because any resizing at this stage would invalidate every calibrated
// millimetre upstream of it.
//
// A queued job is never recorded as a printed cheque on its own. The print system
// can tell us that it accepted and finished a job; it cannot tell us that the
// toner landed correctly on a bank leaf that was fed by hand. That last step is
// the treasurer's confirmation, and the register waits for it.
import { execFile } from 'node:child_process'
import { accessSync, constants, existsSync, statSync } from 'node:fs'
import { connect } from 'node:net'
import { delimiter, join } from 'node:path'
import { pyRepr } from './pycompat'

export const DEFAULT_PRINTER_ENV = 'CHEQUE_PRINTER'
const JOB_RE = /request id is (\S+)/

// Default ports for the device URI schemes that name a host we can knock on.
const NETWORK_PORTS: Record<string, number> = { ipp: 631, ipps: 631, http: 80, https: 443, socket: 9100, lpd: 515 }
const PROBE_TIMEOUT_MS = 1500
const DNSSD_TIMEOUT_S = 2

// dnssd:// device URI -> the ipp:// URI it last resolved to, so only the first
// probe of a Bonjour queue pays for a browse (ippfind runs its full timeout).
const dnssdResolved = new Map<string, string>()

/** The job could not be handed to the print system. */
export class PrintError extends Error {}

/** A command could not be run at all, or ran out of time. */
class SubprocessError extends Error {}

export interface Printer {
  name: string
  state: string
  is_default: boolean
  // true/false when the device answered or did not; null when the queue's
  // device URI gives us nothing to probe (USB, an unknown backend).
  online: boolean | null
}

// When set, nothing is ever handed to the print queue. The desktop app turns
// this on for development builds and tests: a cheque leaf is usually loaded.
let disabled = false
export function setPrintingDisabled(value: boolean): void {
  disabled = value
}

interface Completed {
  returncode: number
  stdout: string
  stderr: string
}

function run(cmd: string, args: string[], timeoutS = 15): Promise<Completed> {
  return new Promise((resolve, reject) => {
    execFile(cmd, args, { timeout: timeoutS * 1000, encoding: 'utf8' }, (error, stdout, stderr) => {
      if (!error) return resolve({ returncode: 0, stdout, stderr })
      const failure = error as NodeJS.ErrnoException & { killed?: boolean; code?: number | string }
      if (failure.killed) return reject(new SubprocessError(`${cmd} timed out`))
      if (typeof failure.code === 'number') return resolve({ returncode: failure.code, stdout, stderr })
      reject(new SubprocessError(failure.message))
    })
  })
}

function which(name: string): string | null {
  for (const dir of (process.env.PATH ?? '').split(delimiter)) {
    if (!dir) continue
    const candidate = join(dir, name)
    try {
      if (existsSync(candidate) && statSync(candidate).isFile()) {
        accessSync(candidate, constants.X_OK)
        return candidate
      }
    } catch {
      // not executable; keep looking
    }
  }
  return null
}

export function cupsAvailable(): boolean {
  return which('lp') !== null && which('lpstat') !== null
}

async function deviceUris(): Promise<Record<string, string>> {
  let out: string
  try {
    out = (await run('lpstat', ['-v'])).stdout
  } catch {
    return {}
  }
  return Object.fromEntries([...out.matchAll(/^device for (\S+): (\S+)/gm)].map((m) => [m[1], m[2]]))
}

function tcpReachable(uri: string): Promise<boolean | null> {
  let parts: URL
  try {
    parts = new URL(uri)
  } catch {
    return Promise.resolve(null)
  }
  const defaultPort = NETWORK_PORTS[parts.protocol.replace(/:$/, '')]
  if (defaultPort === undefined || !parts.hostname) return Promise.resolve(null)
  const host = parts.hostname.replace(/^\[|\]$/g, '')
  return new Promise((resolve) => {
    const socket = connect({ host, port: Number(parts.port) || defaultPort })
    const finish = (answer: boolean) => {
      socket.destroy()
      resolve(answer)
    }
    socket.setTimeout(PROBE_TIMEOUT_MS, () => finish(false))
    socket.once('connect', () => finish(true))
    socket.once('error', () => finish(false))
  })
}

const escapeRegExp = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

/**
 * Turn a Bonjour device URI into the ipp:// URI the printer advertises.
 *
 * Returns '' when nothing by that name is on the network, null when there is
 * no `ippfind` to ask.
 */
async function resolveDnssd(uri: string): Promise<string | null> {
  if (which('ippfind') === null) return null
  // The authority of a dnssd:// URI is a percent-encoded service name, which
  // the URL parser will not accept as a host, so it is cut out by hand.
  const netloc = uri.replace(/^[a-z]+:\/\//i, '').split(/[/?#]/)[0]
  let decoded: string
  try {
    decoded = decodeURIComponent(netloc)
  } catch {
    return null
  }
  const m = decoded.match(/^(.+)\.(_[a-z-]+\._tcp)\./s)
  if (!m) return null
  const [, name, regtype] = m
  let out: string
  try {
    out = (await run('ippfind', ['-T', String(DNSSD_TIMEOUT_S), regtype, '--name', `^${escapeRegExp(name)}$`, '--print']))
      .stdout
  } catch {
    return null
  }
  return out.trim() ? out.trim().split('\n')[0] : ''
}

/**
 * Whether the device behind a queue answers right now.
 *
 * A print queue reports "idle" for a printer that is switched off: the queue
 * is local and only notices when a job fails to go out. So the queue state
 * alone cannot say the printer is there; this opens a connection to find out.
 */
export async function deviceOnline(uri: string): Promise<boolean | null> {
  if (!uri.startsWith('dnssd://')) return tcpReachable(uri)
  const cached = dnssdResolved.get(uri)
  if (cached && (await tcpReachable(cached))) return true
  // Never resolved, or the cached address stopped answering: look again.
  // A stale Bonjour record can outlive the printer, so a name that resolves
  // still has to accept a connection.
  dnssdResolved.delete(uri)
  const resolved = await resolveDnssd(uri)
  if (resolved === null) return null
  if (!resolved) return false
  const online = await tcpReachable(resolved)
  if (online) dnssdResolved.set(uri, resolved)
  return online
}

/** The print queues. With `probe`, also check each device is reachable. */
export async function listPrinters(probe = false): Promise<Printer[]> {
  if (!cupsAvailable()) return []
  let listing: string, defaultOut: string
  try {
    listing = (await run('lpstat', ['-p'])).stdout
    defaultOut = (await run('lpstat', ['-d'])).stdout
  } catch {
    return []
  }
  const uris = probe ? await deviceUris() : {}

  const defaultName = defaultOut.match(/system default destination:\s*(\S+)/)?.[1] ?? ''

  const printers: Printer[] = []
  for (const line of listing.split('\n')) {
    const m = line.trim().match(/^printer (\S+) (.+)$/)
    if (!m) continue
    const name = m[1]
    const rest = m[2].toLowerCase()
    // lpstat has several shapes: "is idle.", "is busy.", "now printing job-1",
    // and, for a paused queue, "disabled since <date>". Reading only the
    // first of those makes a paused printer look like no printer at all,
    // which is exactly the state a queue is in while a leaf is being loaded.
    let state: string
    if (rest.startsWith('is ')) state = (rest.split(/\s+/)[1] ?? '').replace(/\.+$/, '')
    else if (rest.includes('disabled')) state = 'paused'
    else if (rest.includes('now printing')) state = 'printing'
    else state = 'unknown'
    const online = name in uris ? await deviceOnline(uris[name]) : null
    if (online === false) state = 'offline'
    printers.push({ name, state, is_default: name === defaultName, online })
  }
  return printers
}

/** Pick the printer to use: explicit choice, then the system default, then first. */
export async function resolvePrinter(preferred = ''): Promise<string> {
  const printers = await listPrinters()
  const names = printers.map((p) => p.name)
  if (preferred) {
    if (!names.includes(preferred)) throw new PrintError(`printer ${pyRepr(preferred)} is not available`)
    return preferred
  }
  for (const p of printers) if (p.is_default) return p.name
  if (names.length) return names[0]
  throw new PrintError('no printer is configured')
}

/**
 * Hand the PDF to the print system and return the job id.
 *
 * Scaling and fit-to-page are pinned off. If the driver ever refuses these
 * options the job fails here rather than printing a subtly resized cheque.
 */
export async function submit(pdf: string, printer: string, pageWMm: number, pageHMm: number, title = 'cheque'): Promise<string> {
  if (!existsSync(pdf)) throw new PrintError('the rendered PDF is missing')
  if (disabled) {
    throw new PrintError('printing is switched off in this build, so nothing was sent to the printer')
  }
  if (!cupsAvailable()) throw new PrintError('CUPS is not available on this machine')

  const g = (n: number) => String(Number(n.toPrecision(6)))
  const args = [
    '-d', printer,
    '-t', title,
    '-o', `media=Custom.${g(pageWMm)}x${g(pageHMm)}mm`,
    '-o', 'fit-to-page=false',
    '-o', 'scaling=100',
    '-o', 'page-border=none',
    '--', pdf
  ]
  let proc: Completed
  try {
    proc = await run('lp', args, 30)
  } catch (error) {
    throw new PrintError(`could not reach the print system: ${(error as Error).message}`)
  }

  if (proc.returncode !== 0) throw new PrintError((proc.stderr || proc.stdout || 'lp failed').trim())

  const m = proc.stdout.match(JOB_RE)
  if (!m) throw new PrintError(`the print system gave no job id: ${proc.stdout.trim()}`)
  return m[1]
}

/**
 * Where a job has got to: 'active', 'finished', 'cancelled' or 'unknown'.
 *
 * 'finished' means the print system is done with the job, not that the cheque is correct.
 */
export async function jobState(jobId: string): Promise<string> {
  if (!jobId || !cupsAvailable()) return 'unknown'
  const mentions = (out: string) => new RegExp(`^${escapeRegExp(jobId)}\\s`, 'm').test(out)
  try {
    if (mentions((await run('lpstat', ['-o'])).stdout)) return 'active'
    if (mentions((await run('lpstat', ['-W', 'completed', '-o'])).stdout)) return 'finished'
  } catch {
    return 'unknown'
  }
  return 'cancelled'
}

export async function cancel(jobId: string): Promise<boolean> {
  if (!jobId || !cupsAvailable() || disabled) return false
  try {
    return (await run('cancel', [jobId])).returncode === 0
  } catch {
    return false
  }
}
