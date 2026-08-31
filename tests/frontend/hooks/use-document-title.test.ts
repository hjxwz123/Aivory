import { describe, expect, it } from 'vitest'
import { applyDocumentTitle, formatDocumentTitle } from '@/hooks/use-document-title'

describe('document title', () => {
  it('puts the current page title before the application name', () => {
    expect(formatDocumentTitle('  Quarterly planning  ', ' Aivory ')).toBe(
      'Quarterly planning | Aivory',
    )
  })

  it('does not render a separator when either title is empty', () => {
    expect(formatDocumentTitle('', 'Aivory')).toBe('Aivory')
    expect(formatDocumentTitle('Quarterly planning', '')).toBe('Quarterly planning')
  })

  it('restores the previous browser title when the page exits', () => {
    const target = { title: 'Aivory — A thoughtful AI companion' }
    const restore = applyDocumentTitle('Quarterly planning | Aivory', target)

    expect(target.title).toBe('Quarterly planning | Aivory')
    restore()
    expect(target.title).toBe('Aivory — A thoughtful AI companion')
  })

  it('supports a title update followed by a final cleanup', () => {
    const target = { title: 'Aivory — A thoughtful AI companion' }
    const restoreFirst = applyDocumentTitle('Draft title | Aivory', target)
    restoreFirst()
    const restoreSecond = applyDocumentTitle('Generated title | Aivory', target)

    expect(target.title).toBe('Generated title | Aivory')
    restoreSecond()
    expect(target.title).toBe('Aivory — A thoughtful AI companion')
  })
})
