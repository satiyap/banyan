/** Sent from the server process to the main process once it is listening. */
export interface ServerReady {
  type: 'ready'
  port: number
}

/** The server asks the main process to turn an HTML file into a PDF. */
export interface RenderRequest {
  type: 'render'
  id: number
  htmlPath: string
  pdfPath: string
}

/** The main process's answer. `error` is set when no PDF was written. */
export interface RenderReply {
  type: 'rendered'
  id: number
  error?: string
}

/** The server has fetched a register to restore and needs the app started again to put it in place. */
export interface ServerRestart {
  type: 'restart'
}
