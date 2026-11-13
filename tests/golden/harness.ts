// Shared by the tests that hold amount, calibration, leaves and dating to
// their recorded answers. The recordings name errors as Python does, because
// the rules were first written in Python.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

/** The recorded outcome of one case: the value returned, or the error raised. */
export type Outcome<T> = { ok: T; error?: undefined } | { ok?: undefined; error: { type: string; message: string } }

export function loadFixture<T>(name: string): T {
  return JSON.parse(readFileSync(join(import.meta.dirname, name), 'utf8')) as T
}

/** Runs the TypeScript side of a case into the same shape as the fixture. */
export function attempt<T>(fn: () => T): Outcome<T> {
  try {
    return { ok: fn() }
  } catch (exc) {
    if (!(exc instanceof Error)) throw exc
    return { error: { type: exc.name, message: exc.message } }
  }
}

/** The recorded outcome, with its error name mapped to the class thrown here. */
export function expected<T>(outcome: Outcome<T>): Outcome<T> {
  if (!outcome.error) return { ok: outcome.ok as T }
  // JS has no AttributeError; TypeError is thrown for both.
  const type = outcome.error.type === 'AttributeError' ? 'TypeError' : outcome.error.type
  return { error: { type, message: outcome.error.message } }
}

/** Runs `fn` with the process in the given IANA time zone, as the recording was made. */
export function inZone<T>(zone: string, fn: () => T): T {
  const before = process.env.TZ
  process.env.TZ = zone
  try {
    return fn()
  } finally {
    if (before === undefined) delete process.env.TZ
    else process.env.TZ = before
  }
}
