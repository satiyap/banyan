import { mkdtempSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawn, type ChildProcess } from 'node:child_process'
import { createServer } from 'node:net'
import { _electron as electron, expect, test } from '@playwright/test'

const ADMIN = 'e2e-admin-token-0123456789abcdef0123456789abcdef'

/** A real Banyan server on this machine, with an empty data folder. Built by `pnpm e2e` before the tests run. */
async function startBackend(): Promise<{ url: string; key: (role: 'write' | 'read') => Promise<string>; stop: () => void }> {
  // A port nothing else is using: ask the system for one, then hand it over.
  const port = await new Promise<number>((resolve, reject) => {
    const probe = createServer()
    probe.once('error', reject)
    probe.listen(0, '127.0.0.1', () => {
      const { port } = probe.address() as { port: number }
      probe.close(() => resolve(port))
    })
  })
  const child: ChildProcess = spawn(process.execPath, ['out/backend/index.mjs'], {
    env: {
      ...process.env,
      PORT: String(port),
      HOST: '127.0.0.1',
      DATA_DIR: mkdtempSync(join(tmpdir(), 'banyan-e2e-server-')),
      BANYAN_ADMIN_TOKEN: ADMIN,
      BANYAN_INSECURE_COOKIES: '1'
    },
    stdio: 'inherit'
  })
  const url = `http://127.0.0.1:${port}`
  for (let i = 0; ; i++) {
    const up = await fetch(`${url}/healthz`).then(
      (reply) => reply.ok,
      () => false
    )
    if (up) break
    if (i > 100) throw new Error('the Banyan server did not start')
    await new Promise((r) => setTimeout(r, 100))
  }
  return {
    url,
    stop: () => void child.kill(),
    async key(role) {
      const reply = await fetch(`${url}/admin/keys`, {
        method: 'POST',
        headers: { authorization: `Bearer ${ADMIN}`, 'content-type': 'application/json' },
        body: JSON.stringify({ role, label: 'e2e' })
      })
      return ((await reply.json()) as { key: string }).key
    }
  }
}

// This test never prints. The app switches printing off in development builds,
// and BANYAN_PRINT_DISABLED keeps it off when a packaged build is tested.
// It also never clicks a print button: a cheque leaf is usually loaded.
test('connects to the server, renders a test sheet at the exact page size, visits every place, and syncs', async () => {
  const backend = await startBackend()
  const userData = mkdtempSync(join(tmpdir(), 'banyan-e2e-'))
  const packaged = process.env.APP_EXECUTABLE
  const app = await electron.launch({
    ...(packaged ? { executablePath: packaged } : { args: ['.'] }),
    env: { ...process.env, APP_USER_DATA: userData, BANYAN_PRINT_DISABLED: '1' },
    timeout: 120_000
  })
  const window = await app.firstWindow({ timeout: 120_000 })

  // A desk that has never been asked opens on the server connection screen.
  await expect(window).toHaveTitle(/Settings/)
  await expect(window.locator('#setupTitle')).toHaveText('Connect to your Banyan server')

  // No server address ships with the app: both the address and the key are typed.
  await expect(window.locator('#apiUrl')).toHaveValue('')
  await expect(window.locator('#apiUrl')).toBeFocused()

  // Step 1, the address. A bad one is refused and says why.
  await expect(window.locator('#userKey')).toBeHidden()
  await window.locator('#apiUrl').fill('http://api.example.org')
  await window.locator('#btnFind').click()
  await expect(window.locator('#setupError')).toHaveText('The API URL must start with https://')

  // The desk asks the address whether it is a Banyan server, and only then asks for the key.
  await window.locator('#apiUrl').fill(backend.url)
  await window.locator('#btnFind').click()
  await expect(window.locator('#serverFound')).toHaveText(`Found Banyan server at ${backend.url}.`)
  await expect(window.locator('#apiUrl')).toBeHidden()
  await expect(window.locator('#userKey')).toBeFocused()

  // Step 2, the key. A wrong one is refused by the server, in its words.
  await window.locator('#userKey').fill('invented-key')
  await window.locator('#btnSave').click()
  await expect(window.locator('#setupError')).toContainText('not valid')

  // A real key connects, the desk opens, and the register has gone to the server.
  await window.locator('#userKey').fill(await backend.key('write'))
  await window.locator('#btnSave').click()
  await expect(window).toHaveTitle(/Desk · Banyan/)
  const saved = JSON.parse(readFileSync(join(userData, 'connection.json'), 'utf8'))
  expect(saved).toMatchObject({ status: 'connected', api_url: backend.url })
  expect(saved.token).toMatch(/^[0-9a-f]{64}$/)
  expect(saved.user_key).toBeUndefined()
  const syncNow = async (): Promise<any> => ((await window.evaluate(`api('/api/connection')`)) as any).sync
  expect(await syncNow()).toMatchObject({ connected: true, revision: 1, error: '', choice: null })

  // The desk's own page, served by the local server.
  await expect(window).toHaveTitle(/Desk · Banyan/)
  await expect(window.locator('#placeTitle')).toHaveText('Desk')

  // The app's own Chromium does the rendering, and an empty register is in the test folder.
  const health: any = await window.evaluate(`api('/api/health')`)
  expect(health.renderer).toEqual({ ok: true, path: 'built-in' })
  expect(health.db).toContain(userData)
  expect(health.page_mm).toEqual({ w: 92, h: 204 })

  // Render the calibration test sheet without sending it anywhere. The server
  // refuses any PDF that is not 92 x 204mm within half a millimetre, so a
  // result at all means the page box check passed.
  // (`post` is the desk's own helper from core.js; it is a page-level constant, so it is called by name.)
  const sheet: any = await window.evaluate(`post('/api/test-print', { pattern: 'test', confirm: false })`)
  expect(sheet.printed).toBe(false)
  expect(sheet.page.pages).toBe(1)
  expect(Math.abs(sheet.page.page_w_mm - 92)).toBeLessThan(0.5)
  expect(Math.abs(sheet.page.page_h_mm - 204)).toBeLessThan(0.5)

  // Asking it to print is refused, and says why.
  const refusal = await window.evaluate(
    `post('/api/test-print', { pattern: 'test', confirm: true, printer: 'no-such-printer' }).then(() => 'printed', (e) => e.message)`
  )
  expect(refusal).not.toBe('printed')

  for (const [place, title] of [['tray', 'Tray'], ['register', 'Register'], ['year', 'Year'], ['monthly', 'Monthly'], ['desk', 'Desk']]) {
    await window.locator(`.rail-item[data-place="${place}"]`).click()
    await expect(window.locator('#placeTitle')).toHaveText(title)
  }
  await window.screenshot({ path: 'test-results/desk.png' })

  // Settings, for a connected desk: in the desk's own shell.
  await window.locator('.rail-settings').click()
  await expect(window.locator('#roleTag')).toHaveText('Cheque desk')
  await expect(window.locator('#keyState')).toHaveText('Write key')
  await expect(window.locator('#syncState')).toContainText('Synced')
  await window.screenshot({ path: 'test-results/settings-desk.png' })
  // Every way out of Settings works: the rail, the app's View menu, and the Back button.
  await window.locator('#rail .rail-item[data-place="register"]').click()
  await expect(window.locator('#placeTitle')).toHaveText('Register')
  await window.locator('.rail-settings').click()
  await expect(window.locator('#placeTitle')).toHaveText('Settings')
  await app.evaluate(({ Menu }) => {
    const view = Menu.getApplicationMenu()!.items.find((item) => item.label === 'View')!
    view.submenu!.items.find((item) => item.label === 'Tray')!.click()
  })
  await expect(window.locator('#placeTitle')).toHaveText('Tray')
  await window.locator('.rail-settings').click()
  await window.locator('#btnBack').click()
  await expect(window.locator('#placeTitle')).toHaveText('Desk')

  // A change to the register reaches the server without anyone asking.
  await window.evaluate(`post('/api/entities', { payee_name: 'Invented Electricals' })`)
  await expect.poll(async () => (await syncNow()).revision, { timeout: 15_000 }).toBe(2)
  expect(await syncNow()).toMatchObject({ pending: false, error: '' })
  // And the title bar says so.
  await expect
    .poll(() => app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].getTitle()), { timeout: 20_000 })
    .toMatch(/— Synced /)

  // A member signs in on the website with a read key and sees the reports.
  const readKey = await backend.key('read')
  const opened = app.waitForEvent('window')
  await app.evaluate(({ BrowserWindow }, url) => void new BrowserWindow({ width: 1200, height: 800 }).loadURL(url), backend.url)
  const site = await opened
  await expect(site).toHaveTitle(/Sign in/)
  await site.locator('#key').fill('WRONG-KEY')
  await site.locator('button[type=submit]').click()
  await expect(site.locator('#loginError')).toContainText('not valid')
  await site.locator('#key').fill(readKey)
  await site.locator('button[type=submit]').click()
  await expect(site.locator('#placeTitle')).toHaveText('Year')
  await expect(site.locator('#asOf')).toContainText('Register as of')
  await expect(site.locator('.rail-item')).toHaveCount(2)
  await site.locator('.rail-item[data-place="monthly"]').click()
  await expect(site.locator('#placeTitle')).toHaveText('Monthly')
  await expect(site.locator('#content')).not.toContainText('Not signed in')
  await site.screenshot({ path: 'test-results/reader.png' })
  await app.close()

  // A second Mac with its own register connects. Neither register is touched: the treasurer is asked.
  const second = await electron.launch({
    ...(packaged ? { executablePath: packaged } : { args: ['.'] }),
    env: { ...process.env, APP_USER_DATA: mkdtempSync(join(tmpdir(), 'banyan-e2e-')), BANYAN_PRINT_DISABLED: '1' },
    timeout: 120_000
  })
  const other = await second.firstWindow({ timeout: 120_000 })
  await other.locator('#apiUrl').fill(backend.url)
  await other.locator('#btnFind').click()
  await other.locator('#userKey').fill(await backend.key('write'))
  await other.locator('#btnSave').click()
  await expect(other.locator('#choice')).toBeVisible()
  await expect(other.locator('#syncState')).toHaveText('Not syncing')
  await expect(other.locator('#choiceText')).toContainText('copy 2')
  await other.screenshot({ path: 'test-results/choice.png' })
  await second.close()

  // A third Mac is given a read key. The same app becomes the reports, and nothing else.
  const third = await electron.launch({
    ...(packaged ? { executablePath: packaged } : { args: ['.'] }),
    env: { ...process.env, APP_USER_DATA: mkdtempSync(join(tmpdir(), 'banyan-e2e-')), BANYAN_PRINT_DISABLED: '1' },
    timeout: 120_000
  })
  const reader = await third.firstWindow({ timeout: 120_000 })
  await reader.locator('#apiUrl').fill(backend.url)
  await reader.locator('#btnFind').click()
  await reader.locator('#userKey').fill(await backend.key('read'))
  await reader.locator('#btnSave').click()
  await expect(reader.locator('#placeTitle')).toHaveText('Year')
  await expect(reader.locator('.rail-item[data-place]')).toHaveCount(2)
  await expect(reader.locator('#asOf')).toContainText('Register as of')
  await expect(reader.locator('#signOut')).toBeHidden()
  await reader.locator('.rail-item[data-place="monthly"]').click()
  await expect(reader.locator('#content')).toContainText('FY')
  // The desk's own API is closed to it.
  for (const path of ['/api/cheques', '/api/desk', '/api/health']) {
    expect(await reader.evaluate((p) => fetch(p).then((r) => r.status), path), path).toBe(403)
  }
  await reader.screenshot({ path: 'test-results/read-mode.png' })

  // Settings is in the rail here too, inside the same shell. The address is settled and shown as text.
  await reader.locator('.rail-settings').click()
  await expect(reader.locator('#placeTitle')).toHaveText('Settings')
  await expect(reader.locator('#rail .rail-item[data-place]')).toHaveCount(2)
  await expect(reader.locator('#roleTag')).toHaveText('Reports only')
  await expect(reader.locator('#addrUrl')).toHaveText(backend.url)
  await expect(reader.locator('#keyState')).toHaveText('Read key')
  await expect(reader.locator('#setAddress')).toBeHidden()
  await reader.screenshot({ path: 'test-results/settings-reader.png' })

  // The key alone can be switched.
  await reader.locator('#btnSwitch').click()
  await reader.locator('#setKey').fill('WRONG-KEY')
  await reader.locator('#btnConnect').click()
  await expect(reader.locator('#settingsError')).toContainText('not valid')
  await reader.screenshot({ path: 'test-results/settings-error.png' })
  await reader.locator('#setKey').fill(await backend.key('read'))
  await reader.locator('#btnConnect').click()
  await expect(reader.locator('#placeTitle')).toHaveText('Year')

  // Resetting the key forgets the key and keeps the address: only a new key is asked for.
  await reader.locator('.rail-settings').click()
  await reader.locator('#btnReset').click()
  await reader.locator('#btnReset').click()
  await expect(reader.locator('#keyState')).toHaveText('No key')
  await expect(reader.locator('#roleTag')).toHaveText('Not connected')
  await expect(reader.locator('#addrUrl')).toHaveText(backend.url)
  await expect(reader.locator('#btnSwitch')).toHaveText('Enter a key')
  await expect(reader.locator('#btnReset')).toBeHidden()
  // Without a key the app is the desk again, and its rail says so.
  await expect(reader.locator('#rail .rail-item[data-place]')).toHaveCount(5)
  await reader.screenshot({ path: 'test-results/settings-nokey.png' })

  // Changing the address is there, and optional.
  await reader.locator('#btnChange').click()
  await expect(reader.locator('#setAddress')).toHaveValue(backend.url)
  await reader.locator('#btnCancelAddress').click()
  await expect(reader.locator('#setAddress')).toBeHidden()
  await third.close()
  backend.stop()
})

test('a desk can skip the server connection, and is not asked again', async () => {
  const userData = mkdtempSync(join(tmpdir(), 'banyan-e2e-'))
  const packaged = process.env.APP_EXECUTABLE
  const launch = () =>
    electron.launch({
      ...(packaged ? { executablePath: packaged } : { args: ['.'] }),
      env: { ...process.env, APP_USER_DATA: userData, BANYAN_PRINT_DISABLED: '1' },
      timeout: 120_000
    })

  let app = await launch()
  let window = await app.firstWindow({ timeout: 120_000 })
  await expect(window).toHaveTitle(/Settings/)
  await window.screenshot({ path: 'test-results/setup.png' })
  await window.locator('#btnSkip').click()
  await expect(window.locator('#placeTitle')).toHaveText('Desk')
  expect(JSON.parse(readFileSync(join(userData, 'connection.json'), 'utf8')).status).toBe('skipped')
  await app.close()

  // Second launch: straight to the desk.
  app = await launch()
  window = await app.firstWindow({ timeout: 120_000 })
  await expect(window).toHaveTitle(/Desk · Banyan/)

  // The screen is still reachable, and going back changes nothing.
  await window.goto(new URL('/setup', window.url()).href)
  await expect(window.locator('#placeTitle')).toHaveText('Settings')
  await expect(window.locator('#keyState')).toHaveText('No key')
  await expect(window.locator('#addrName')).toHaveText('No server set')
  await expect(window.locator('#btnSwitch')).toBeDisabled()
  await window.locator('#btnBack').click()
  await expect(window.locator('#placeTitle')).toHaveText('Desk')
  await app.close()
})
