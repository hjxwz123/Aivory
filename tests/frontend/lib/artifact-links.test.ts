import { describe, expect, it } from 'vitest'
import { rewriteSandboxArtifactLinks } from '@/lib/artifact-links'
import { isSafeMarkdownUrl } from '@/lib/markdown'
import type { ArtifactRef } from '@/types/chat'

function artifact(id: string, filename: string, mimeType = 'application/octet-stream'): ArtifactRef {
  return { id, filename, mimeType, url: `/api/artifacts/${id}` }
}

describe('sandbox artifact links', () => {
  it('maps a URL-encoded Chinese output filename to its authenticated artifact', () => {
    const markdown = '[下载报告](sandbox:/workspace/outputs/%E6%8A%A5%E5%91%8A.docx)'
    expect(rewriteSandboxArtifactLinks(markdown, [artifact('art-report', '报告.docx')]))
      .toBe('[下载报告](/api/artifacts/art-report)')
  })

  it('recovers a truncated percent-encoded filename only when the extension is unique', () => {
    const markdown = '[下载](sandbox:/workspace/outputs/%E8.docx)'
    expect(rewriteSandboxArtifactLinks(markdown, [
      artifact('art-report', '季度报告.docx'),
      artifact('art-sheet', '数据.xlsx'),
    ])).toBe('[下载](/api/artifacts/art-report)')

    expect(rewriteSandboxArtifactLinks(markdown, [
      artifact('art-report-a', '季度报告.docx'),
      artifact('art-report-b', '年度报告.docx'),
    ])).toBe(markdown)
  })

  it('does not rewrite unknown paths, ordinary links, or code examples', () => {
    const unknown = '[下载](sandbox:/workspace/outputs/other.docx)'
    expect(rewriteSandboxArtifactLinks(unknown, [artifact('art-report', '报告.docx')])).toBe(unknown)
    expect(rewriteSandboxArtifactLinks('[网站](https://example.com/a)', [artifact('art-report', '报告.docx')]))
      .toBe('[网站](https://example.com/a)')
    expect(rewriteSandboxArtifactLinks('`sandbox:/workspace/outputs/报告.docx`', [artifact('art-report', '报告.docx')]))
      .toBe('`sandbox:/workspace/outputs/报告.docx`')
  })

  it('rejects unresolved sandbox schemes at the markdown sanitizer boundary', () => {
    expect(isSafeMarkdownUrl('sandbox:/workspace/outputs/report.docx')).toBe(false)
    expect(isSafeMarkdownUrl('/api/artifacts/art-report')).toBe(true)
    expect(isSafeMarkdownUrl('https://example.com/report.docx')).toBe(true)
  })
})
