import { resolve } from 'node:path'
import { defineConfig } from 'vite'

// The Banyan server, built as one file for plain Node with its dependencies
// inlined, so the container needs Node and nothing else. It shares the
// register, report and page code with the desktop app but none of Electron.
export default defineConfig({
  build: {
    ssr: resolve(__dirname, 'src/backend/index.ts'),
    outDir: 'out/backend',
    emptyOutDir: true,
    target: 'node24',
    minify: false,
    rollupOptions: { output: { entryFileNames: 'index.mjs', format: 'es' } }
  },
  ssr: { noExternal: true }
})
