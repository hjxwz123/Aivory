import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { OfficialToolIcon } from './official-tool-icon'

function renderIcon(icon?: string, name?: string): string {
  return renderToStaticMarkup(createElement(OfficialToolIcon, { icon, name }))
}

describe('OfficialToolIcon', () => {
  it('renders PascalCase names selected by the built-in icon picker', () => {
    const html = renderIcon('Brain')

    expect(html).toContain('<svg')
    expect(html).toContain('lucide-brain')
    expect(html).not.toContain('>Br<')
  })

  it('normalizes kebab-case and snake_case Lucide names', () => {
    expect(renderIcon('square-terminal')).toContain('lucide-square-terminal')
    expect(renderIcon('circle_help')).toContain('lucide-circle-help')
  })

  it('keeps legacy semantic names and custom text compatible', () => {
    expect(renderIcon('terminal')).toContain('lucide-square-terminal')
    expect(renderIcon('', 'web_search')).toContain('lucide-search')
    expect(renderIcon('\u2728')).toContain('\u2728')
  })

  it('renders an exact picker choice before similarly named legacy aliases', () => {
    expect(renderIcon('Code')).toContain('lucide-code')
    expect(renderIcon('Code')).not.toContain('lucide-square-terminal')
    expect(renderIcon('Terminal')).toContain('lucide-terminal')
    expect(renderIcon('terminal')).toContain('lucide-square-terminal')
  })
})
