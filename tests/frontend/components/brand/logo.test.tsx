import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { AIVORY_MARK_PATH, LogoMark, TracedLogo } from '@/components/brand/logo'

describe('LogoMark', () => {
  it('renders the centered compound mark as a non-selectable SVG', () => {
    const html = renderToStaticMarkup(createElement(LogoMark, { size: 24 }))

    expect(html).toContain('viewBox="0 0 32 32"')
    expect(html).toContain(`d="${AIVORY_MARK_PATH}"`)
    expect(html).toContain('fill-rule="evenodd"')
    expect(html).toContain('shape-rendering="geometricPrecision"')
    expect(html).toContain('select-none')
    expect(html).toContain('focusable="false"')
  })

  it('combines the system mark with the traced artistic wordmark', () => {
    const html = renderToStaticMarkup(createElement(TracedLogo, { size: 'sm' }))

    expect(html).toContain(`d="${AIVORY_MARK_PATH}"`)
    expect(html).toContain('aivory-mark-lockup')
    expect(html).toContain('#466b78')
    expect(html).toContain('trace-wordmark.svg')
    expect(html).toContain('<img')
    expect(html).not.toContain('>Aivory<')
  })
})
