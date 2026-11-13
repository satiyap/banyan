// The desk's link to the Banyan server: an API URL and a user key.
//
// Asked for once, on first launch, and skippable: the desk writes and prints
// cheques without it. This module only remembers the answer; sync.ts is what
// talks to the server.
//
// The key is single-use: the desk redeems it for a long-lived token and the
// key is spent, so the token is what is kept. It sits in a plain file beside
// the register on purpose. The server is what protects it: a token can be
// retired there, and is, the moment another desk takes over.
//
// No server address ships with the app. Anyone can download the app; only
// someone given the address and a key can reach the association's server.
import { mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'

export type Connection =
  /** Never asked. The app shows the connection screen before the desk. */
  | { status: 'unset' }
  /**
   * Asked, and no key is on file: skipped, or the key was reset. A server
   * address that worked before is remembered, so only a new key is needed.
   */
  | { status: 'skipped'; api_url?: string; server_name?: string }
  /** `role` is what the key allowed: `write` makes this the cheque desk, `read` a reports-only app. */
  | { status: 'connected'; api_url: string; token: string; server_name: string; role: 'write' | 'read' }

export class ConnectionError extends Error {}

const MAX_KEY = 512

/** The API URL as it will be stored: https, no credentials, no query, no trailing slash. */
export function cleanApiUrl(input: unknown): string {
  const text = typeof input === 'string' ? input.trim() : ''
  if (!text) throw new ConnectionError('Enter the API URL.')
  let url: URL
  try {
    url = new URL(text)
  } catch {
    throw new ConnectionError('That is not a web address. It should look like https://api.example.org')
  }
  const local = ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)
  if (url.protocol !== 'https:' && !(url.protocol === 'http:' && local)) {
    throw new ConnectionError('The API URL must start with https://')
  }
  if (url.username || url.password) throw new ConnectionError('Leave any user name or password out of the API URL.')
  if (url.search || url.hash) throw new ConnectionError('Leave anything after ? or # out of the API URL.')
  return `${url.origin}${url.pathname.replace(/\/+$/, '')}`
}

export function cleanUserKey(input: unknown): string {
  const text = typeof input === 'string' ? input.trim() : ''
  if (!text) throw new ConnectionError('Enter the user key.')
  if (/\s/.test(text)) throw new ConnectionError('The user key has a space or line break in it. Paste it again.')
  if (text.length > MAX_KEY) throw new ConnectionError('That is too long to be a user key.')
  return text
}

/** What is on file. A missing or unreadable file counts as never asked. */
export function readConnection(path: string): Connection {
  let saved: Record<string, unknown>
  try {
    saved = JSON.parse(readFileSync(path, 'utf8'))
  } catch {
    return { status: 'unset' }
  }
  if (saved?.status === 'skipped') {
    try {
      if (typeof saved.api_url === 'string' && saved.api_url) {
        return {
          status: 'skipped',
          api_url: cleanApiUrl(saved.api_url),
          server_name: typeof saved.server_name === 'string' ? saved.server_name : ''
        }
      }
    } catch {
      // An address that no longer passes is simply forgotten.
    }
    return { status: 'skipped' }
  }
  if (saved?.status === 'connected' && typeof saved.token === 'string' && saved.token) {
    try {
      return {
        status: 'connected',
        api_url: cleanApiUrl(saved.api_url),
        token: saved.token,
        server_name: typeof saved.server_name === 'string' ? saved.server_name : '',
        role: saved.role === 'read' ? 'read' : 'write'
      }
    } catch {
      return { status: 'unset' }
    }
  }
  return { status: 'unset' }
}

function write(path: string, connection: Connection): Connection {
  mkdirSync(dirname(path), { recursive: true })
  // Written whole and moved into place, so a crash cannot leave half a file.
  const partial = `${path}.partial`
  writeFileSync(partial, `${JSON.stringify({ ...connection, saved_at: new Date().toISOString() }, null, 2)}\n`)
  renameSync(partial, path)
  return connection
}

export function saveConnection(
  path: string,
  apiUrl: string,
  token: string,
  serverName = '',
  role: 'write' | 'read' = 'write'
): Connection {
  return write(path, { status: 'connected', api_url: cleanApiUrl(apiUrl), token, server_name: serverName, role })
}

/**
 * Carry on without a server, or forget the key. `remember` keeps the address
 * of a server that has already answered, so the key alone can be replaced.
 */
export function skipConnection(path: string, remember?: { api_url: string; server_name: string }): Connection {
  return write(path, remember ? { status: 'skipped', ...remember } : { status: 'skipped' })
}

/** What the page is shown. The token is never sent to it. */
export function describeConnection(connection: Connection): {
  status: string
  api_url: string
  has_key: boolean
  server_name: string
  role: string
} {
  return connection.status === 'connected'
    ? {
        status: 'connected',
        api_url: connection.api_url,
        has_key: true,
        server_name: connection.server_name,
        role: connection.role
      }
    : {
        status: connection.status,
        api_url: (connection.status === 'skipped' && connection.api_url) || '',
        has_key: false,
        server_name: (connection.status === 'skipped' && connection.server_name) || '',
        role: ''
      }
}
