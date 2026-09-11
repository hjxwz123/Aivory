import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import AdminBackup from '@/pages/admin/AdminBackup'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

function renderBackup() {
  return renderToStaticMarkup(createElement(MemoryRouter, null, createElement(AdminBackup)))
}

describe('AdminBackup', () => {
  it('uses ordered settings sections and leaves destructive restore last', () => {
    const html = renderBackup()
    const sectionTitles = [...html.matchAll(/<section aria-labelledby="([^"]+)"/g)]
      .map((match) => match[1])

    expect(sectionTitles).toEqual([
      'backup-export-title',
      'backup-vectors-title',
      'backup-config-export-title',
      'backup-config-import-title',
      'backup-import-title',
    ])
    expect(html).not.toContain('<aside')
    expect(html).toContain('max-w-[76rem]')
  })

  it('keeps backup options labeled and file pickers restricted to archives', () => {
    const html = renderBackup()

    expect(html).toContain('for="backup-include-files"')
    expect(html).toContain('id="backup-include-files"')
    expect(html).toContain('aria-describedby="backup-include-files-hint"')
    expect(html.match(/accept="\.zip,application\/zip"/g)).toHaveLength(2)
    expect(html).toContain('aria-label="admin:backup.import.choose"')
  })

  it('disables restore without a file and rebuild without missing vectors', () => {
    const html = renderBackup()
    const buttons = [...html.matchAll(/<button\b[^>]*>[\s\S]*?<\/button>/g)]
      .map((match) => match[0])

    expect(buttons.find((button) => button.includes('admin:backup.import.action'))).toContain('disabled=""')
    expect(buttons.find((button) => button.includes('admin:backup.vectors.rebuildAction'))).toContain('disabled=""')
    expect(buttons.find((button) => button.includes('admin:backup.export.action'))).not.toContain('disabled=""')
    expect(html).toContain('aria-label="admin:backup.export.loading"')
    expect(html).toContain('aria-label="admin:backup.vectors.loading"')
  })
})
