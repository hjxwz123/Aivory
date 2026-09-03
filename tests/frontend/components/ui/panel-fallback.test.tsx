import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { PanelFallback } from '@/components/ui/panel-fallback'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (_key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? _key,
  }),
}))

describe('PanelFallback', () => {
  it('uses one accessible spinner implementation for every loading scope', () => {
    const panel = renderToStaticMarkup(createElement(PanelFallback))
    const fill = renderToStaticMarkup(createElement(PanelFallback, { scope: 'fill' }))
    const screen = renderToStaticMarkup(createElement(PanelFallback, { scope: 'screen' }))

    for (const html of [panel, fill, screen]) {
      expect(html).toContain('role="status"')
      expect(html).toContain('aria-live="polite"')
      expect(html).toContain('spin_900ms_linear_infinite')
      expect(html).toContain('Loading')
    }
    expect(panel).toContain('min-h-48')
    expect(fill).toContain('h-full')
    expect(screen).toContain('fixed inset-0')
    expect(screen).toContain('z-[var(--z-overlay)]')
  })
})
