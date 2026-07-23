import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ParamControls } from './param-controls'

function renderControls(): string {
  return renderToStaticMarkup(
    createElement(
      TooltipProvider,
      null,
      createElement(ParamControls, {
        controls: [
          { key: 'thinking', type: 'toggle', label: 'Deep thinking', icon: 'Brain', default: false },
          {
            key: 'quality',
            type: 'select',
            label: 'Quality',
            icon: 'Gauge',
            default: 'high',
            options: [
              { value: 'low', label: 'Low', icon: 'Gauge' },
              { value: 'high', label: 'High', icon: 'Sparkles' },
            ],
          },
        ],
        values: { thinking: false, quality: 'high' },
        onChange: () => {},
      }),
    ),
  )
}

describe('ParamControls', () => {
  it('keeps toolbar triggers icon-only while preserving accessible names and values', () => {
    const html = renderControls()

    expect(html).toContain('aria-label="Deep thinking"')
    expect(html).toContain('aria-label="Quality: High"')
    expect(html).toContain('lucide-brain')
    expect(html).toContain('lucide-sparkles')
    expect(html).not.toContain('>Deep thinking<')
    expect(html).not.toContain('>Quality<')
  })
})
