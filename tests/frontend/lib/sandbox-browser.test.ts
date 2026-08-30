import { describe, expect, it } from 'vitest'
import { sandboxEntriesAtPath, sandboxParentPath } from '@/lib/sandbox-browser'

const files = [
  { path: 'outputs/report.pdf', size: 1200 },
  { path: 'outputs/charts/chart-2.png', size: 20 },
  { path: 'outputs/charts/chart-10.png', size: 40 },
  { path: 'uploads/data.csv', size: 100 },
  { path: 'README.md', size: 30 },
]

describe('sandbox browser directory projection', () => {
  it('shows folders before direct files at the root', () => {
    expect(sandboxEntriesAtPath(files, '')).toEqual([
      { name: 'outputs', path: 'outputs', type: 'directory', size: 0 },
      { name: 'uploads', path: 'uploads', type: 'directory', size: 0 },
      { name: 'README.md', path: 'README.md', type: 'file', size: 30 },
    ])
  })

  it('lists only immediate children and sorts names naturally', () => {
    expect(sandboxEntriesAtPath(files, 'outputs')).toEqual([
      { name: 'charts', path: 'outputs/charts', type: 'directory', size: 0 },
      { name: 'report.pdf', path: 'outputs/report.pdf', type: 'file', size: 1200 },
    ])
    expect(sandboxEntriesAtPath(files, 'outputs/charts').map((entry) => entry.name)).toEqual([
      'chart-2.png',
      'chart-10.png',
    ])
  })

  it('rejects unsafe paths and computes parent folders', () => {
    expect(sandboxEntriesAtPath([...files, { path: '../secret.txt', size: 1 }], '')).toHaveLength(3)
    expect(sandboxParentPath('outputs/charts')).toBe('outputs')
    expect(sandboxParentPath('outputs')).toBe('')
  })
})
