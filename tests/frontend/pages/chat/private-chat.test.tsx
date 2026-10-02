import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import PrivateChat from '@/pages/chat/PrivateChat'
import zh from '@/i18n/locales/zh/chat.json'

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>()
  return {
    ...actual,
    useTranslation: () => ({ t: (key: string) => key.startsWith('private.') ? zh.private[key.slice(8) as keyof typeof zh.private] : key }),
  }
})

describe('private chat presentation', () => {
  it('renders the ordinary home screen with only the composer swapped', () => {
    const html = renderToStaticMarkup(createElement(MemoryRouter, null, createElement(TooltipProvider, null, createElement(PrivateChat))))

    // The private composer, with its tab-only placeholder and no upload input
    // until a vision-capable model is known.
    expect(html).toContain(`placeholder="${zh.private.placeholder}"`)
    expect(html).toContain('chat-composer-shell')
    expect(html).not.toContain('type="file"')
    // Everything around it is the shared home layout: greeting, suggestions
    // area and disclaimer, the drawer button, and the engaged private toggle.
    expect(html).toContain('<h1')
    expect(html).toContain('greeting.')
    expect(html).toContain('empty.disclaimer')
    expect(html).toContain('commandMenu.actions.toggleSidebar')
    expect(html).toContain(`aria-label="${zh.private.exit}"`)
    expect(html).toContain('aria-pressed="true"')
    expect(html).not.toContain(zh.private.footer)
    expect(html).not.toContain('h-svh')
  })
})
