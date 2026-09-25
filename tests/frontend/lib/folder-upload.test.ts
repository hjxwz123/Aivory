import { describe, expect, it } from 'vitest'
import {
  FOLDER_MAX_FILE_BYTES,
  FOLDER_MAX_FILES_DEFAULT,
  FOLDER_MAX_TOTAL_BYTES_DEFAULT,
  containsDroppedDirectory,
  folderUploadFields,
  prepareSandboxFolderUpload,
} from '@/lib/folder-upload'

function candidate(path: string, size = 10) {
  const name = path.replace(/\\/g, '/').split('/').at(-1) ?? path
  return { path, file: { name, size } as File }
}

describe('direct sandbox folder upload', () => {
  it('identifies dropped directories before they reach ordinary file upload', () => {
    const directory = { kind: 'file', webkitGetAsEntry: () => ({ isDirectory: true }) }
    const regular = { kind: 'file', webkitGetAsEntry: () => ({ isDirectory: false }) }
    expect(containsDroppedDirectory([directory] as unknown as DataTransferItemList, null)).toBe(true)
    expect(containsDroppedDirectory([regular] as unknown as DataTransferItemList, null)).toBe(false)
    expect(containsDroppedDirectory(null, [{ webkitRelativePath: 'project/a.txt' }] as unknown as FileList)).toBe(true)
  })
  it('keeps original paths including dependencies, hidden files and extensionless files', () => {
    const entries = [
      candidate('project/node_modules/pkg/index.js'),
      candidate('project/.env'),
      candidate('project/Makefile'),
    ]
    expect(prepareSandboxFolderUpload(entries)).toEqual({
      ok: true,
      folder: 'project',
      paths: entries.map((entry) => entry.path),
      files: entries.map((entry) => entry.file),
    })
  })

  it('preserves files at the root of a folder picked by the modern API', () => {
    expect(prepareSandboxFolderUpload([candidate('README.md'), candidate('src/main.ts')], 'project')).toMatchObject({
      ok: true,
      paths: ['project/README.md', 'project/src/main.ts'],
    })
  })

  it('normalizes Windows separators without flattening the tree', () => {
    expect(prepareSandboxFolderUpload([candidate('project\\src\\main.ts')])).toMatchObject({
      ok: true,
      paths: ['project/src/main.ts'],
    })
  })

  it('rejects unsafe, duplicate and mismatched paths instead of dropping files', () => {
    for (const path of ['/project/a.txt', 'project/../a.txt', 'project//a.txt', 'project/./a.txt']) {
      expect(prepareSandboxFolderUpload([candidate(path)], 'project')).toEqual({ ok: false, reason: 'paths' })
    }
    expect(prepareSandboxFolderUpload([candidate('project/a.txt'), candidate('project/a.txt')])).toEqual({ ok: false, reason: 'paths' })
    expect(folderUploadFields('project/a.txt', 'b.txt')).toBeNull()
  })

  it('rejects the entire selection when any hard size or count limit is exceeded', () => {
    expect(prepareSandboxFolderUpload([candidate('project/large.bin', FOLDER_MAX_FILE_BYTES + 1)])).toEqual({ ok: false, reason: 'limit' })
    const many = Array.from({ length: FOLDER_MAX_FILES_DEFAULT + 1 }, (_, index) => candidate(`project/${index}.txt`))
    expect(prepareSandboxFolderUpload(many)).toEqual({ ok: false, reason: 'limit' })
    const largeTotal = Array.from({ length: 6 }, (_, index) => candidate(`project/${index}.bin`, Math.floor(FOLDER_MAX_TOTAL_BYTES_DEFAULT / 6) + 1))
    expect(prepareSandboxFolderUpload(largeTotal)).toEqual({ ok: false, reason: 'limit' })
  })

  it('reports an empty folder instead of silently returning', () => {
    expect(prepareSandboxFolderUpload([], 'project')).toEqual({ ok: false, reason: 'empty' })
  })
})
