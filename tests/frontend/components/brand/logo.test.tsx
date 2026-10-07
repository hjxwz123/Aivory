import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { AIVORY_MARK_PATH, LogoMark, TracedLogo } from '@/components/brand/logo'
import * as brandLogo from '@/components/brand/logo'

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

  it('combines the accent mark with a theme-tinted wordmark', () => {
    const html = renderToStaticMarkup(createElement(TracedLogo, { size: 'sm' }))

    expect(html).toContain(`d="${AIVORY_MARK_PATH}"`)
    expect(html).toContain('stop-color="var(--color-accent)"')
    expect(html).toContain('aivory-wordmark.svg')
    expect(html).toContain('background-color:var(--color-fg)')
    expect(html).not.toContain('<img')
    expect(html).not.toContain('>Aivory<')
  })

  it('does not expose the retired plain-text lockup', () => {
    expect(brandLogo).not.toHaveProperty('Logo')
  })
})
