import { describe, expect, it } from 'vitest'
import {
  chipRailItems,
  fileFolderTree,
  folderGroupSize,
  folderRootOf,
  groupAttachmentsByFolder,
  pathWithinFolder,
  type FolderGroupableAttachment,
} from '@/lib/folder-attachments'

function attachment(id: string, name: string, relPath?: string, size = 10): FolderGroupableAttachment & { size: number } {
  return { id, name, relPath, size }
}

describe('attachment folder grouping', () => {
  it('leaves single-file uploads loose', () => {
    const { folders, loose } = groupAttachmentsByFolder([
      attachment('1', 'report.pdf'),
      attachment('2', 'photo.png'),
    ])
    expect(folders).toEqual([])
    expect(loose.map((a) => a.id)).toEqual(['1', '2'])
  })

  it('groups a folder upload into one node, keeping every member', () => {
    const { folders, loose } = groupAttachmentsByFolder([
      attachment('1', 'main.ts', 'my-project/src/main.ts'),
      attachment('2', 'readme.md', 'my-project/readme.md'),
      attachment('3', 'notes.txt'),
      attachment('4', 'index.ts', 'my-project/src/index.ts'),
    ])
    expect(loose.map((a) => a.id)).toEqual(['3'])
    expect(folders).toHaveLength(1)
    expect(folders[0].folder).toBe('my-project')
    // Members stay in attach order so the expanded list does not reshuffle.
    expect(folders[0].members.map((a) => a.id)).toEqual(['1', '2', '4'])
  })

  it('keeps two folders separate', () => {
    const { folders } = groupAttachmentsByFolder([
      attachment('1', 'a.ts', 'proj-a/a.ts'),
      attachment('2', 'b.ts', 'proj-b/b.ts'),
      attachment('3', 'c.ts', 'proj-a/c.ts'),
    ])
    expect(folders.map((f) => f.folder)).toEqual(['proj-a', 'proj-b'])
    expect(folders[0].members.map((a) => a.id)).toEqual(['1', '3'])
  })

  it('groups a single-member folder too — it was still picked as a folder', () => {
    const { folders, loose } = groupAttachmentsByFolder([
      attachment('1', 'only.ts', 'solo/only.ts'),
    ])
    expect(loose).toEqual([])
    expect(folders[0].members).toHaveLength(1)
  })

  it('normalizes Windows separators from a desktop picker', () => {
    const { folders } = groupAttachmentsByFolder([
      attachment('1', 'main.ts', 'my-project\\src\\main.ts'),
    ])
    expect(folders[0].folder).toBe('my-project')
  })

  it('never invents a folder from a hostile or empty path', () => {
    const { folders, loose } = groupAttachmentsByFolder([
      attachment('1', 'a.txt', '../escape/a.txt'),
      attachment('2', 'b.txt', '.'),
      attachment('3', 'c.txt', '/'),
      attachment('4', 'd.txt', ''),
      attachment('5', 'e.txt', undefined),
    ])
    expect(folders).toEqual([])
    expect(loose).toHaveLength(5)
  })
})

// The rail renders this list, so it must keep each folder where its first file
// was attached — otherwise the chips reshuffle as an upload progresses.
describe('chip rail order', () => {
  it('keeps a folder at the position of its first file', () => {
    const items = chipRailItems([
      attachment('1', 'notes.txt'),
      attachment('2', 'a.ts', 'proj/a.ts'),
      attachment('3', 'report.pdf'),
      attachment('4', 'b.ts', 'proj/b.ts'),
    ])
    expect(items.map((item) => (item.type === 'folder' ? `folder:${item.folder}` : item.attachment.id))).toEqual([
      '1',
      'folder:proj',
      '3',
    ])
    const folder = items[1]
    if (folder.type !== 'folder') throw new Error('expected a folder node')
    expect(folder.members.map((m) => m.id)).toEqual(['2', '4'])
  })

  it('splits two folders and keeps them distinct', () => {
    const items = chipRailItems([
      attachment('1', 'a.ts', 'one/a.ts'),
      attachment('2', 'b.ts', 'two/b.ts'),
      attachment('3', 'c.ts', 'one/c.ts'),
    ])
    expect(items.map((item) => (item.type === 'folder' ? item.folder : item.attachment.id))).toEqual(['one', 'two'])
  })

  it('never groups a plain file', () => {
    const items = chipRailItems([attachment('1', 'a.pdf'), attachment('2', 'b.png')])
    expect(items.every((item) => item.type === 'file')).toBe(true)
  })

  it('drops a member as soon as it is removed, and the node with the last one', () => {
    const all = [attachment('1', 'a.ts', 'proj/a.ts'), attachment('2', 'b.ts', 'proj/b.ts')]
    const afterOne = chipRailItems(all.slice(1))
    expect(afterOne[0].type).toBe('folder')
    expect(chipRailItems([])).toEqual([])
  })
})

describe('folder path helpers', () => {  it('extracts the folder root', () => {
    expect(folderRootOf('p/src/a.ts')).toBe('p')
    expect(folderRootOf('p\\src\\a.ts')).toBe('p')
    expect(folderRootOf('p')).toBe('p')
    expect(folderRootOf('')).toBe('')
    expect(folderRootOf(undefined)).toBe('')
    expect(folderRootOf('../x')).toBe('')
  })

  it('shows a member path relative to its folder', () => {
    expect(pathWithinFolder('my-project/src/main.ts', 'my-project')).toBe('src/main.ts')
    expect(pathWithinFolder('my-project\\src\\main.ts', 'my-project')).toBe('src/main.ts')
    // A path that does not carry the prefix is shown whole rather than mangled.
    expect(pathWithinFolder('other/a.ts', 'my-project')).toBe('other/a.ts')
    expect(pathWithinFolder(undefined, 'my-project')).toBe('')
  })

  it('sums member sizes defensively', () => {
    expect(folderGroupSize([{ size: 10 }, { size: 32 }])).toBe(42)
    expect(folderGroupSize([{ size: Number.NaN }, { size: 5 }])).toBe(5)
    expect(folderGroupSize([])).toBe(0)
  })
})

// The files drawer used to list every uploaded file flat. These pin the tree
// that puts a picked folder — and the subdirectories inside it — back together.
describe('files drawer folder tree', () => {
  const meta = (file: { relPath?: string; size: number }) => ({ relPath: file.relPath, size: file.size })

  it('keeps loose single-file uploads at the top level', () => {
    const tree = fileFolderTree([attachment('1', 'report.pdf'), attachment('2', 'photo.png')], meta)

    expect(tree.folders).toEqual([])
    expect(tree.rootFiles.map((f) => f.id)).toEqual(['1', '2'])
  })

  it('nests subdirectories and counts every descendant', () => {
    const tree = fileFolderTree(
      [
        attachment('1', 'a.ts', 'p/src/deep/a.ts', 10),
        attachment('2', 'b.ts', 'p/src/b.ts', 20),
        attachment('3', 'c.ts', 'p/c.ts', 30),
      ],
      meta,
    )

    expect(tree.rootFiles).toEqual([])
    expect(tree.folders.map((f) => f.path)).toEqual(['p'])
    const root = tree.folders[0]
    expect(root.name).toBe('p')
    expect(root.fileCount).toBe(3)
    expect(root.size).toBe(60)
    // Direct child directories only; "deep" hangs off "src", not off the root.
    expect(root.children.map((c) => c.path)).toEqual(['p/src'])
    const src = root.children[0]
    expect(src.files.map((f) => f.id)).toEqual(['2'])
    expect(src.children.map((c) => c.path)).toEqual(['p/src/deep'])
    expect(src.children[0].files.map((f) => f.id)).toEqual(['1'])
    // A file directly in the picked folder stays in that folder's own list.
    expect(root.files.map((f) => f.id)).toEqual(['3'])
  })

  it('groups two separately picked folders side by side', () => {
    const tree = fileFolderTree(
      [
        attachment('1', 'a.ts', 'alpha/a.ts', 1),
        attachment('2', 'b.ts', 'beta/b.ts', 1),
        attachment('3', 'loose.ts', undefined, 1),
      ],
      meta,
    )

    expect(tree.folders.map((f) => f.path)).toEqual(['alpha', 'beta'])
    expect(tree.rootFiles.map((f) => f.id)).toEqual(['3'])
  })

  it('treats a hostile path as a loose file instead of inventing a folder', () => {
    for (const relPath of ['', '..', './x.ts', '/absolute/x.ts']) {
      const tree = fileFolderTree([attachment('1', 'x.ts', relPath)], meta)
      expect(tree.folders).toEqual([])
      expect(tree.rootFiles.map((f) => f.id)).toEqual(['1'])
    }
  })
})
