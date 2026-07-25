import { readdirSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const FRONTEND_TEST_FILE = /\.(?:test|spec)\.[cm]?[jt]sx?$/

function findTestFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const absolute = path.join(directory, entry.name)
    if (entry.isDirectory()) return findTestFiles(absolute)
    return FRONTEND_TEST_FILE.test(entry.name) ? [path.relative(process.cwd(), absolute)] : []
  })
}

describe('frontend test layout', () => {
  it('keeps executable test files out of the production source tree', () => {
    expect(findTestFiles(path.join(process.cwd(), 'src'))).toEqual([])
  })
})
