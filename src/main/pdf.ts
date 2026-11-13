import { writeFile } from 'node:fs/promises'
import { pathToFileURL } from 'node:url'
import { BrowserWindow } from 'electron'

// How long a page gets to finish composing itself before it is printed.
const SETTLE_TIMEOUT_MS = 6000

/**
 * Prints one local HTML file to one PDF file with the app's own Chromium.
 *
 * The page declares its own size with @page (92 x 204mm for a cheque, A4 for a
 * report) and that declaration is honoured as is: no margins added, no
 * scaling, no header or footer. The server checks the resulting page size
 * after every render and refuses anything that is not exact.
 */
export async function htmlFileToPdf(htmlPath: string, pdfPath: string): Promise<void> {
  const window = new BrowserWindow({
    show: false,
    width: 1000,
    height: 1200,
    webPreferences: {
      sandbox: true,
      contextIsolation: true,
      nodeIntegration: false,
      // A print page is self-contained. It never needs the network.
      webSecurity: true
    }
  })
  try {
    window.webContents.session.setPermissionRequestHandler((_contents, _permission, callback) => callback(false))
    await window.loadURL(pathToFileURL(htmlPath).href)
    // Let fonts load and the layout script finish, as the headless path did with its time budget.
    await Promise.race([
      window.webContents.executeJavaScript(
        'document.fonts.ready.then(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(() => done(true)))))'
      ),
      new Promise((resolve) => setTimeout(resolve, SETTLE_TIMEOUT_MS))
    ])
    const pdf = await window.webContents.printToPDF({
      preferCSSPageSize: true,
      printBackground: true,
      displayHeaderFooter: false,
      scale: 1,
      // Margins come from each page's own @page rule: zero for a cheque, set in millimetres for a report.
      margins: { top: 0, bottom: 0, left: 0, right: 0 }
    })
    await writeFile(pdfPath, pdf)
  } finally {
    window.destroy()
  }
}
