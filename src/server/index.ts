// The local server: the same HTTP API and pages the cheque desk has always had.
//
// The desktop app starts this file in its own process (see src/main/server.ts).
// It has no Electron imports, so `pnpm server` runs the same thing under plain
// Node, which is what a read-only mirror will use later.
import { mkdirSync } from 'node:fs'
import { dirname } from 'node:path'
import { serve } from '@hono/node-server'
import type { ServerReady, ServerRestart } from '../shared/types'
import { createApp } from './app'
import { loadConfig } from './config'
import { setPrintingDisabled } from './printing'
import { applyPendingRestore } from './sync'

const config = loadConfig()
mkdirSync(dirname(config.dbPath), { recursive: true })
mkdirSync(config.pdfDir, { recursive: true })
setPrintingDisabled(config.printDisabled)

// A register fetched from the Banyan server goes in before anything opens the old one.
if (applyPendingRestore(config)) console.log('Restored the register from the Banyan server.')

const { app, close } = createApp(config, {
  // The new register can only be put in place by starting again.
  onRestoreReady: () => setTimeout(() => process.parentPort?.postMessage({ type: 'restart' } satisfies ServerRestart), 300)
})

// Loopback only: the register holds payment records and is never exposed.
const server = serve({ fetch: app.fetch, hostname: '127.0.0.1', port: config.port }, (address) => {
  if (process.parentPort) {
    process.parentPort.postMessage({ type: 'ready', port: address.port } satisfies ServerReady)
  } else {
    console.log(`Cheque desk on http://127.0.0.1:${address.port}`)
    console.log(`Register ${config.dbPath}`)
    if (config.printDisabled) console.log('Printing is switched off for this run.')
  }
})

const shutdown = () => {
  server.close()
  close()
  process.exit(0)
}
process.on('SIGTERM', shutdown)
process.on('SIGINT', shutdown)
