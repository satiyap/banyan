import { defineConfig } from 'vitest/config'

// `pnpm verify-print`: renders cheques and measures where the ink lands.
export default defineConfig({
  test: { include: ['scripts/verify-print.test.ts'], environment: 'node', testTimeout: 120_000 }
})
