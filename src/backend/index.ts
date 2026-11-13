// Starts the Banyan server. Everything it needs comes from the environment:
//
//   PORT                 where to listen (8322)
//   DATA_DIR             keys, tokens and registers (/data)
//   BANYAN_ADMIN_TOKEN   lets its holder issue keys; unset switches that off.
//                        At least 32 random characters (openssl rand -hex 32),
//                        or the server refuses to start.
//   BANYAN_ASSETS        the folder holding static/, templates/ and
//                        cheque-calibration.json (the working directory)
//   BANYAN_SERVER_NAME   what the server calls itself to a desk that connects
//   BANYAN_INSECURE_COOKIES=1   only for running over plain http on one machine
import { resolve } from 'node:path'
import { serve } from '@hono/node-server'
import { createBackend } from './app'

const assets = resolve(process.env.BANYAN_ASSETS || process.cwd())
const dataDir = resolve(process.env.DATA_DIR || './data/server')
const port = Number(process.env.PORT || 8322)

const { app, close } = createBackend({
  dataDir,
  staticDir: resolve(assets, 'static'),
  templatesDir: resolve(assets, 'templates'),
  calPath: resolve(assets, 'cheque-calibration.json'),
  adminToken: process.env.BANYAN_ADMIN_TOKEN ?? '',
  secureCookies: process.env.BANYAN_INSECURE_COOKIES !== '1',
  name: process.env.BANYAN_SERVER_NAME
})

const server = serve({ fetch: app.fetch, hostname: process.env.HOST || '0.0.0.0', port }, (address) => {
  console.log(`Banyan server listening on ${address.port}, data in ${dataDir}`)
  if (!process.env.BANYAN_ADMIN_TOKEN) console.log('No BANYAN_ADMIN_TOKEN: keys cannot be issued.')
})

const shutdown = () => {
  server.close()
  close()
  process.exit(0)
}
process.on('SIGTERM', shutdown)
process.on('SIGINT', shutdown)
