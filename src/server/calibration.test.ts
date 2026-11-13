// Holds calibration.ts to its recorded answers: tests/golden/calibration.json
// holds what the rules answered over invented calibration files: valid ones,
// every validation failure, and several hundred hand-edit accidents.
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterAll, describe, expect, it } from 'vitest'
import { attempt, expected, loadFixture, type Outcome } from '../../tests/golden/harness'
import {
  CHEQUE_H_MM,
  CHEQUE_W_MM,
  Calibration,
  CalibrationError,
  HARDWARE_INSET_MM,
  MICR_TOP_MM,
  PAGE_H_MM,
  PAGE_W_MM,
  REQUIRED_FIELDS,
  loadCalibration,
  type CalibrationDict
} from './calibration'

interface Loaded {
  as_dict: CalibrationDict
  leaf: number[]
  version?: number
  dx?: number
  dy?: number
  flip?: boolean
  paper?: string
  a4x?: number
  a4y?: number
  fields?: unknown
  source_name?: string
  warnings: string[]
}

type Case = {
  label: string
  filename: string
  kind: 'file' | 'dir' | 'missing'
  text?: string
  base64?: string
} & Outcome<Loaded>

interface Fixture {
  constants: Record<string, number | string[]>
  cases: Case[]
  real: ({ filename: string; text: string } & Outcome<Loaded>) | null
}

const golden = loadFixture<Fixture>('calibration.json')
const dir = mkdtempSync(join(tmpdir(), 'banyan-calibration-'))
afterAll(() => rmSync(dir, { recursive: true, force: true }))

function describeCal(cal: Calibration): Loaded {
  return {
    as_dict: cal.asDict(),
    leaf: cal.leaf,
    version: cal.version,
    dx: cal.dx,
    dy: cal.dy,
    flip: cal.flip,
    paper: cal.paper,
    a4x: cal.a4x,
    a4y: cal.a4y,
    fields: cal.fields,
    source_name: cal.asDict().source,
    warnings: cal.warnings
  }
}

describe('calibration (golden against app/calibration.py)', () => {
  it('has the same constants', () => {
    expect({
      CHEQUE_W_MM,
      CHEQUE_H_MM,
      PAGE_W_MM,
      PAGE_H_MM,
      HARDWARE_INSET_MM,
      MICR_TOP_MM,
      REQUIRED_FIELDS: [...REQUIRED_FIELDS]
    }).toEqual(golden.constants)
  })

  it(`loads or refuses ${golden.cases.length} files exactly as recorded`, () => {
    golden.cases.forEach((c, n) => {
      const folder = join(dir, String(n))
      mkdirSync(folder)
      const path = join(folder, c.filename)
      if (c.kind === 'dir') mkdirSync(path)
      else if (c.kind === 'file') {
        writeFileSync(path, c.base64 !== undefined ? Buffer.from(c.base64, 'base64') : (c.text as string))
      }
      const actual = attempt(() => describeCal(loadCalibration(path)))
      const want = expected(c)
      if (want.error?.type === 'UnicodeDecodeError') {
        // Bytes that are not UTF-8: refused, though not in Python's words.
        expect(actual.error?.type, c.label).toBe('ValueError')
        return
      }
      expect(actual, c.label).toEqual(want)
    })
  })

  it('throws CalibrationError for a file it refuses', () => {
    expect(() => loadCalibration(join(dir, 'nowhere.json'))).toThrow(CalibrationError)
  })

  it('reads the real cheque-calibration.json', () => {
    const real = golden.real
    expect(real, 'the recording holds no cheque-calibration.json').not.toBeNull()
    if (!real) return
    const path = join(import.meta.dirname, '../..', real.filename)
    const actual = attempt(() => {
      const cal = loadCalibration(path)
      return { as_dict: cal.asDict(), leaf: cal.leaf, warnings: cal.warnings }
    })
    // The file is recalibrated from time to time. Whatever it says, it must load.
    expect(actual.error).toBeUndefined()
    // While it is still the file that was recorded, it must also read the same.
    if (readFileSync(path, 'utf8') === real.text) expect(actual).toEqual(expected(real))
    expect(actual.ok?.as_dict.version).toBeTypeOf('number')
  })
})
