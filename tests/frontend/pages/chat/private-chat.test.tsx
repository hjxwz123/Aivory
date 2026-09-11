import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import PrivateChat from '@/pages/chat/PrivateChat'
import zh from '@/i18n/locales/zh/chat.json'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key.startsWith('private.') ? zh.private[key.slice(8) as keyof typeof zh.private] : key }),
}))

describe('private chat presentation', () => {
  it('uses the ordinary composer styling with a tab-only placeholder, without explanatory paragraphs', () => {
    const html = renderToStaticMarkup(createElement(MemoryRouter, null, createElement(TooltipProvider, null, createElement(PrivateChat))))

    expect(html).toContain(`placeholder="${zh.private.placeholder}"`)
    expect(html).toContain('chat-composer-shell')
    expect(html).not.toMatch(/<p(?:\s|>)/)
    expect(html).not.toContain(zh.private.footer)
    expect(html).not.toContain('h-svh')
    expect(html).toContain('commandMenu.actions.toggleSidebar')
    expect(html).toContain(zh.private.exit)
    expect(html).not.toContain('type="file"')
  })
})
