import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: 'e2e',
  workers: 1,
  reporter: 'list',
  timeout: 180_000,
  // A freshly built app starts slowly the first time macOS checks it.
  expect: { timeout: 15_000 }
})
