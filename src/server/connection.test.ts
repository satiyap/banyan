import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { expect, test } from 'vitest'
import {
  cleanApiUrl,
  cleanUserKey,
  ConnectionError,
  describeConnection,
  readConnection,
  saveConnection,
  skipConnection
} from './connection'

const file = () => join(mkdtempSync(join(tmpdir(), 'banyan-connection-')), 'nested', 'connection.json')

test('a desk that was never asked is unset', () => {
  expect(readConnection(file())).toEqual({ status: 'unset' })
})

test('a saved connection is read back, tidied', () => {
  const path = file()
  saveConnection(path, '  https://api.example.org/banyan/  ', 'token-123')
  expect(readConnection(path)).toEqual({
    status: 'connected',
    api_url: 'https://api.example.org/banyan',
    token: 'token-123',
    server_name: '',
    role: 'write'
  })
  expect(JSON.parse(readFileSync(path, 'utf8')).saved_at).toMatch(/^\d{4}-\d\d-\d\dT/)
})

test('skipping is remembered, and removes a saved connection', () => {
  const path = file()
  saveConnection(path, 'https://api.example.org', 'key-123')
  skipConnection(path)
  expect(readConnection(path)).toEqual({ status: 'skipped' })
})

test('resetting the key can keep the address of a server that worked', () => {
  const path = file()
  skipConnection(path, { api_url: 'https://api.example.org', server_name: 'Invented Association' })
  expect(readConnection(path)).toEqual({
    status: 'skipped',
    api_url: 'https://api.example.org',
    server_name: 'Invented Association'
  })
  expect(describeConnection(readConnection(path))).toEqual({
    status: 'skipped',
    api_url: 'https://api.example.org',
    has_key: false,
    server_name: 'Invented Association',
    role: ''
  })
  expect(readFileSync(path, 'utf8')).not.toContain('token')
})

test('a damaged file counts as never asked', () => {
  const path = file()
  skipConnection(path)
  writeFileSync(path, '{ not json')
  expect(readConnection(path)).toEqual({ status: 'unset' })
  writeFileSync(path, JSON.stringify({ status: 'connected', api_url: 'ftp://x', token: 't' }))
  expect(readConnection(path)).toEqual({ status: 'unset' })
})

test('the API URL must be https, except on this machine', () => {
  expect(cleanApiUrl('https://api.example.org')).toBe('https://api.example.org')
  expect(cleanApiUrl('http://localhost:8787/v1/')).toBe('http://localhost:8787/v1')
  for (const bad of ['', '   ', 'api.example.org', 'http://api.example.org', 'https://me:pw@api.example.org', 'https://api.example.org/?a=1', 42, null]) {
    expect(() => cleanApiUrl(bad), String(bad)).toThrow(ConnectionError)
  }
})

test('the user key must be one unbroken value', () => {
  expect(cleanUserKey(' abc ')).toBe('abc')
  for (const bad of ['', '  ', 'two words', 'line\nbreak', 'x'.repeat(513), undefined]) {
    expect(() => cleanUserKey(bad), String(bad)).toThrow(ConnectionError)
  }
})

test('a refused save leaves what was on file alone', () => {
  const path = file()
  saveConnection(path, 'https://api.example.org', 'token-123')
  expect(() => saveConnection(path, 'http://other.example.org', 'token-456')).toThrow(ConnectionError)
  expect(readConnection(path)).toMatchObject({ api_url: 'https://api.example.org', token: 'token-123' })
})

test('the page is told a desk is connected, never its token', () => {
  expect(describeConnection({ status: 'connected', api_url: 'https://api.example.org', token: 'token-123', server_name: 'Invented Association', role: 'read' })).toEqual({
    status: 'connected',
    api_url: 'https://api.example.org',
    has_key: true,
    server_name: 'Invented Association',
    role: 'read'
  })
  expect(describeConnection({ status: 'skipped' })).toEqual({
    status: 'skipped',
    api_url: '',
    has_key: false,
    server_name: '',
    role: ''
  })
})
