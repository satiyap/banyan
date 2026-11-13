import { resolve } from 'node:path'
import { defineConfig } from 'electron-vite'

// There is no bundled frontend: the desk's pages are plain files in static/,
// served by the local server exactly as they were before. Only the main
// process and the server are built, each as a single file with its
// dependencies inlined, so the packaged app ships no node_modules.
export default defineConfig({
  main: {
    build: {
      rollupOptions: {
        input: {
          index: resolve(__dirname, 'src/main/index.ts'),
          server: resolve(__dirname, 'src/server/index.ts')
        }
      }
    }
  }
})
