/**
 * The directory walk behind the modern folder picker.
 *
 * The bug this covers: the composer used to rely solely on
 * `<input webkitdirectory>`, whose `webkitRelativePath` is empty on a host that
 * ignores the (non-standard) attribute. Every picked file then failed the "is
 * inside a folder" test and the upload silently did nothing. Walking real
 * directory handles removes that dependency, so the walk itself is pinned here:
 * paths include the folder name, nesting is recursive, and a file reachable
 * twice is only reported once.
 */
import { describe, expect, it } from 'vitest'
import {
  canPickDirectoryWithFileSystemApi,
  isPickerCancellation,
  walkDirectory,
  type DirectoryChildLike,
  type DirectoryEntryLike,
  type FileHandleLike,
} from '@/lib/folder-picker'

/** A hand-built handle tree; the walk only needs `name`/`values`/`kind`. */
function dir(name: string, children: DirectoryEntryLike[]): DirectoryChildLike {
  return {
    kind: 'directory',
    name,
    values: () => ({
      async *[Symbol.asyncIterator]() {
        for (const child of children) yield child
      },
    }),
  }
}

function file(name: string): FileHandleLike {
  return {
    kind: 'file',
    name,
    getFile: async () => new File(['x'], name, { type: 'text/plain' }),
  }
}

describe('walkDirectory', () => {
  it('lists nested files with the folder name as the first path segment', async () => {
    const root = dir('my-project', [
      file('main.ts'),
      dir('src', [file('app.ts'), dir('lib', [file('util.ts')])]),
      file('README.md'),
    ])

    const picked = await walkDirectory(root)

    expect(picked.name).toBe('my-project')
    expect(picked.files.map((entry) => entry.path).sort()).toEqual([
      'my-project/README.md',
      'my-project/main.ts',
      'my-project/src/app.ts',
      'my-project/src/lib/util.ts',
    ])
  })

  it('reports a file reachable twice only once', async () => {
    const shared = new File(['x'], 'shared.ts', { type: 'text/plain' })
    const handle = (): FileHandleLike => ({
      kind: 'file',
      name: 'shared.ts',
      getFile: async () => shared,
    })
    const root = dir('p', [dir('a', [handle()]), dir('b', [handle()])])

    const picked = await walkDirectory(root)

    expect(picked.files).toHaveLength(1)
    expect(picked.files[0].path).toBe('p/a/shared.ts')
  })

  it('stops descending at the depth ceiling', async () => {
    let node = dir('deep', [file('bottom.ts')])
    for (let i = 0; i < 5; i += 1) node = dir('deep', [node])

    const shallow = await walkDirectory(node, { maxDepth: 2 })
    const full = await walkDirectory(node, { maxDepth: 32 })

    expect(shallow.files).toHaveLength(0)
    expect(full.files).toHaveLength(1)
  })

  it('stops collecting at the file ceiling', async () => {
    const root = dir('p', [file('a.ts'), file('b.ts'), file('c.ts')])

    const picked = await walkDirectory(root, { maxFiles: 2 })

    expect(picked.files.map((entry) => entry.file.name)).toHaveLength(2)
  })

  it('returns nothing for an empty folder rather than throwing', async () => {
    const picked = await walkDirectory(dir('empty', []))
    expect(picked.files).toEqual([])
  })
})

describe('picker capability detection', () => {
  it('reports the File System Access API as unavailable without a DOM', () => {
    // The unit suite runs in node: the composer must then fall back to the
    // hidden webkitdirectory input rather than crashing.
    expect(canPickDirectoryWithFileSystemApi()).toBe(false)
  })

  it('classifies a dismissed picker as a cancellation, not a failure', () => {
    const abort = new Error('The user aborted a request.')
    abort.name = 'AbortError'
    expect(isPickerCancellation(abort)).toBe(true)
    expect(isPickerCancellation(new Error('boom'))).toBe(false)
    expect(isPickerCancellation(undefined)).toBe(false)
  })
})
