import { createElement, type ComponentProps } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ParamControls } from '@/components/chat/param-controls'
import { filterVisibleParams, type ParamControlDef } from '@/components/chat/param-controls.utils'

function renderControls(props?: ComponentProps<typeof ParamControls>): string {
  return renderToStaticMarkup(
    createElement(
      TooltipProvider,
      null,
      createElement(ParamControls, props ?? {
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

  const optionalQuality: ParamControlDef = {
    key: 'quality',
    type: 'select',
    label: 'Quality',
    icon: 'Gauge',
    options: [
      { value: 'low', label: 'Low', icon: 'Leaf' },
      { value: 'high', label: 'High', icon: 'Sparkles' },
    ],
  }

  it('leaves a select without a default unselected and omits it from request params', () => {
    const html = renderControls({ controls: [optionalQuality], values: {}, onChange: () => {} })

    expect(html).toContain('aria-label="Quality"')
    expect(html).toContain('data-placeholder=""')
    expect(html).toContain('lucide-gauge')
    expect(html).not.toContain('aria-label="Quality: Low"')
    expect(filterVisibleParams([optionalQuality], {})).toEqual({})
  })

  it('includes a user selection even when the control has no default', () => {
    const values = { quality: 'low' }
    const html = renderControls({ controls: [optionalQuality], values, onChange: () => {} })

    expect(html).toContain('aria-label="Quality: Low"')
    expect(html).toContain('lucide-leaf')
    expect(filterVisibleParams([optionalQuality], values)).toEqual(values)
  })
})
