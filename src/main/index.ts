import { app, BrowserWindow, dialog, session, shell } from 'electron'
import { setupMenu } from './menu'
import { startServer, type RunningServer } from './server'

// Tests point this at a temp folder so they never touch the real register.
if (process.env.APP_USER_DATA) app.setPath('userData', process.env.APP_USER_DATA)

// Must match SESSION_COOKIE in src/server/config.ts.
const SESSION_COOKIE = 'banyan_session'

let server: RunningServer | undefined
/** True once the register has been sent on the way out, so quitting is not held up twice. */
let flushed = false
/** What the title bar says about the Banyan server, after the page's own title. */
let syncNote = ''
let deskVersion = ''

interface ConnectionNow {
  status?: string
  desk_version?: string
  sync?: {
    connected: boolean
    read_only: boolean
    synced_at: string
    pending: boolean
    error: string
    choice: unknown
    retired: boolean
  }
}

/** Call the desk's own server as the app, not as a page. */
async function deskApi(path: string, method = 'GET', timeoutMs = 5_000): Promise<ConnectionNow> {
  const reply = await fetch(`${server!.url}${path}`, {
    method,
    headers: {
      cookie: `${SESSION_COOKIE}=${server!.token}`,
      'content-type': 'application/json',
      // Reads are answered without it; writes need the fingerprint a read has told us.
      ...(deskVersion ? { 'x-desk-version': deskVersion } : {})
    },
    signal: AbortSignal.timeout(timeoutMs)
  })
  return (await reply.json()) as ConnectionNow
}

/** In the title bar's words: is the server's copy of the register up to date? */
function describeSync(sync: ConnectionNow['sync']): string {
  if (!sync?.connected) return ''
  if (sync.read_only) return 'Reports only'
  if (sync.retired) return 'Another desk has taken over. This desk is not syncing'
  if (sync.choice) return 'The server holds a different register. Open Banyan ▸ Settings'
  const at = sync.synced_at ? new Date(sync.synced_at) : null
  const when = at
    ? `${at.toLocaleDateString('en-IN', { day: '2-digit', month: 'short' })} ${at.toLocaleTimeString('en-IN', { hour: '2-digit', minute: '2-digit', hour12: false })}`
    : 'never'
  if (sync.error) return `Not synced since ${when}: ${sync.error}`
  return sync.pending ? 'Syncing…' : `Synced ${when}`
}

function retitle(window: BrowserWindow, pageTitle = window.webContents.getTitle()): void {
  window.setTitle(syncNote ? `${pageTitle} — ${syncNote}` : pageTitle)
}

async function watchSync(): Promise<void> {
  try {
    const now = await deskApi('/api/connection')
    deskVersion = now.desk_version ?? deskVersion
    syncNote = describeSync(now.sync)
  } catch {
    return
  }
  for (const window of BrowserWindow.getAllWindows()) retitle(window)
}

function createWindow(): BrowserWindow {
  // The desk draws its own rail and top bar and was designed inside a browser
  // window, so it gets the standard macOS title bar rather than a custom one.
  const window = new BrowserWindow({
    width: 1360,
    height: 880,
    minWidth: 1024,
    minHeight: 640,
    show: false,
    title: 'Cheque Desk',
    webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false }
  })
  window.once('ready-to-show', () => window.show())
  // The page names itself; the app adds how the Banyan server's copy stands.
  window.on('page-title-updated', (event, title) => {
    event.preventDefault()
    retitle(window, title)
  })

  // Links inside the desk (a cheque PDF, a report, a CSV) open in the app.
  // Anything else goes to the browser.
  const own = (url: string) => url.startsWith(server!.url)
  window.webContents.setWindowOpenHandler(({ url }) => {
    if (own(url)) return { action: 'allow', overrideBrowserWindowOptions: { width: 900, height: 1000, title: 'Cheque Desk' } }
    if (url.startsWith('https://')) void shell.openExternal(url)
    return { action: 'deny' }
  })
  window.webContents.on('will-navigate', (event, url) => {
    if (!own(url)) event.preventDefault()
  })

  void firstPage().then((page) => window.loadURL(page))
  return window
}

/**
 * The desk, or the server connection screen if this desk has never been asked.
 * Skipping counts as an answer, so the question is put once.
 */
async function firstPage(): Promise<string> {
  try {
    const now = await deskApi('/api/connection')
    deskVersion = now.desk_version ?? ''
    // Also when the desk and the server disagree: that needs an answer before any cheque is written.
    if (now.status === 'unset' || now.sync?.choice) return `${server!.url}/setup`
  } catch {
    // The desk matters more than the question: open it.
  }
  return server!.url
}

/** Open the server connection screen in the desk's window. */
function showConnection(): void {
  showWindow()
  void BrowserWindow.getAllWindows()[0]?.loadURL(`${server!.url}/setup`)
}

function showWindow(): void {
  const window = BrowserWindow.getAllWindows()[0] ?? createWindow()
  if (window.isMinimized()) window.restore()
  window.show()
  window.focus()
}

if (!app.requestSingleInstanceLock()) {
  app.quit()
} else {
  app.on('second-instance', showWindow)

  app
    .whenReady()
    .then(async () => {
      server = await startServer(
        (code) => {
          dialog.showErrorBox('The cheque desk stopped', `Its server exited with code ${code}. Reopen the app to restart it.`)
          app.quit()
        },
        // A register fetched from the Banyan server is put in place on the way back up.
        () => {
          flushed = true
          app.relaunch()
          app.exit(0)
        }
      )

      // The server only answers requests carrying this launch's secret.
      await session.defaultSession.cookies.set({
        url: server.url,
        name: SESSION_COOKIE,
        value: server.token,
        httpOnly: true,
        sameSite: 'strict'
      })
      // The desk needs no camera, microphone, location or notifications.
      session.defaultSession.setPermissionRequestHandler((_contents, _permission, callback) => callback(false))

      setupMenu(server, showConnection)
      createWindow()
      void watchSync()
      setInterval(() => void watchSync(), 10_000).unref()
      app.on('activate', showWindow)
    })
    .catch((error: Error) => {
      dialog.showErrorBox('The cheque desk did not start', error.message)
      app.quit()
    })

  app.on('window-all-closed', () => {
    if (process.platform !== 'darwin') app.quit()
  })
  // Before quitting, give the register one last chance to reach the Banyan server.
  app.on('before-quit', (event) => {
    if (flushed || !server) return
    event.preventDefault()
    flushed = true
    void deskApi('/api/sync/flush', 'POST', 15_000)
      .catch(() => undefined)
      .finally(() => app.quit())
  })
  app.on('will-quit', () => server?.stop())
}
