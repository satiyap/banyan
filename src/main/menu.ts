import { app, BrowserWindow, Menu, shell, type MenuItemConstructorOptions } from 'electron'

/** The desk's places, in the order of its own rail. Each is a hash route in the page. */
const PLACES: [string, string][] = [
  ['Desk', 'desk'],
  ['Tray', 'tray'],
  ['Register', 'register'],
  ['Year', 'year'],
  ['Monthly', 'monthly']
]

function go(place: string): void {
  const [window] = BrowserWindow.getAllWindows()
  if (!window) return
  window.show()
  const here = new URL(window.webContents.getURL())
  if (here.pathname === '/') {
    void window.webContents.executeJavaScript(`location.hash = ${JSON.stringify(`#/${place}`)}`)
  } else {
    // On Settings (or any other page) the places are not on screen: go back to the app at that place.
    void window.loadURL(`${here.origin}/#/${place}`)
  }
}

export function setupMenu(paths: { registerPath: string; calibrationPath: string }, showConnection: () => void): void {
  const isDev = !app.isPackaged
  const template: MenuItemConstructorOptions[] = [
    {
      label: app.name,
      submenu: [
        { role: 'about' },
        { type: 'separator' },
        { label: 'Settings…', accelerator: 'Cmd+,', click: showConnection },
        { type: 'separator' },
        { role: 'services' },
        { type: 'separator' },
        { role: 'hide' },
        { role: 'hideOthers' },
        { role: 'unhide' },
        { type: 'separator' },
        { role: 'quit' }
      ]
    },
    { role: 'editMenu' },
    {
      label: 'View',
      submenu: [
        ...PLACES.map(
          ([label, place], index): MenuItemConstructorOptions => ({
            label,
            accelerator: `Cmd+${index + 1}`,
            click: () => go(place)
          })
        ),
        { type: 'separator' },
        { role: 'reload' },
        { role: 'resetZoom' },
        { role: 'zoomIn' },
        { role: 'zoomOut' },
        { role: 'togglefullscreen' },
        ...(isDev ? ([{ type: 'separator' }, { role: 'toggleDevTools' }] as MenuItemConstructorOptions[]) : [])
      ]
    },
    { role: 'windowMenu' },
    {
      label: 'Help',
      submenu: [
        { label: 'Show Register in Finder', click: () => shell.showItemInFolder(paths.registerPath) },
        { label: 'Show Calibration File in Finder', click: () => shell.showItemInFolder(paths.calibrationPath) }
      ]
    }
  ]
  Menu.setApplicationMenu(Menu.buildFromTemplate(template))
}
