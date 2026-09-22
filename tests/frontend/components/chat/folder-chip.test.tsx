import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { FolderChip } from '@/components/chat/folder-chip'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string } & Record<string, unknown>) => {
      // Substitute the same placeholders i18next would, so the assertions can
      // check what the user actually reads.
      let out = options?.defaultValue ?? key
      for (const [name, value] of Object.entries(options ?? {})) {
        if (name === 'defaultValue') continue
        out = out.replaceAll(`{{${name}}}`, String(value))
      }
      return out
    },
    i18n: { language: 'en' },
  }),
}))

function member(id: string, name: string, relPath: string, size = 1024, extra: Record<string, unknown> = {}) {
  return { id, name, size, kind: 'code', relPath, ...extra }
}

function render(members: ReturnType<typeof member>[], compact = false): string {
  return renderToStaticMarkup(
    createElement(FolderChip, {
      folder: 'my-project',
      members,
      compact,
      onRemoveMember: () => undefined,
      onRemoveFolder: () => undefined,
    }),
  )
}

/**
 * The point of this node is that a shared project reads as ONE attachment. These
 * tests pin the collapsed summary and the fact that every member is still
 * reachable (the server needs all of them on send).
 */
describe('FolderChip', () => {
  it('collapses a folder into one node showing the folder name and file count', () => {
    const html = render([
      member('1', 'main.ts', 'my-project/src/main.ts', 512),
      member('2', 'readme.md', 'my-project/readme.md', 512),
    ])
    expect(html).toContain('my-project')
    expect(html).toContain('2 files')
    // 1024 bytes total, formatted for humans.
    expect(html).toContain('1 KB')
    // Collapsed by default: the individual files are not rendered.
    expect(html).not.toContain('src/main.ts')
  })

  it('offers removal at folder level', () => {
    const html = render([member('1', 'a.ts', 'my-project/a.ts')])
    expect(html).toContain('Remove my-project')
    expect(html).toContain('Show the files in my-project')
  })

  it('summarizes readiness for a single-file folder', () => {
    const html = render([member('1', 'only.ts', 'solo/only.ts', 42)])
    expect(html).toContain('1 files')
    expect(html).toContain('42 B')
  })

  it('reports upload progress instead of readiness while files are in flight', () => {
    const html = render([
      member('1', 'a.ts', 'my-project/a.ts', 10, { uploading: true, uploadProgress: 0 }),
      member('2', 'b.ts', 'my-project/b.ts', 10),
    ])
    expect(html).toContain('Uploading 1/2')
    expect(html).not.toContain('files ·')
  })

  it('flags failed members without hiding the rest', () => {
    const html = render([
      member('1', 'a.ts', 'my-project/a.ts', 10, { ingest: 'failed' }),
      member('2', 'b.ts', 'my-project/b.ts', 10),
    ])
    expect(html).toContain('1 failed')
  })
})
