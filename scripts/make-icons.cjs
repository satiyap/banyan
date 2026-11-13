// Turns build/icon.svg into the PNG the app ships with.
// Run with `pnpm icons` after editing it.
// It uses Electron as the renderer, so no extra image tools are needed.
const { app, BrowserWindow } = require('electron')
const { readFileSync, writeFileSync } = require('node:fs')
const { join } = require('node:path')

const root = join(__dirname, '..')
const jobs = [
  { from: 'build/icon.svg', to: 'build/icon.png', size: 1024 } // app icon; electron-builder makes the .icns
]

app.dock?.hide()
app.on('window-all-closed', () => {}) // stay alive between images
app.whenReady().then(async () => {
  for (const { from, to, size } of jobs) {
    const window = new BrowserWindow({
      width: size,
      height: size,
      useContentSize: true,
      show: false,
      frame: false,
      transparent: true,
      webPreferences: { offscreen: true }
    })
    const svg = readFileSync(join(root, from)).toString('base64')
    const page = `<body style="margin:0;background:transparent"><img width="${size}" height="${size}" src="data:image/svg+xml;base64,${svg}">`
    await window.loadURL(`data:text/html;base64,${Buffer.from(page).toString('base64')}`)
    await new Promise((resolve) => setTimeout(resolve, 200))
    const image = (await window.webContents.capturePage()).resize({ width: size, height: size, quality: 'best' })
    writeFileSync(join(root, to), image.toPNG())
    console.log(`${to}  ${size}x${size}`)
    window.destroy()
  }
  app.quit()
}).catch((error) => {
  console.error(error)
  app.exit(1)
})
