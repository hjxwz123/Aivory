import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

function source(relativePath: string): string {
  return readFileSync(path.join(process.cwd(), relativePath), 'utf8')
}

describe('page loading fallbacks', () => {
  it('routes full-page and chat loading through the canonical component', () => {
    const app = source('src/App.tsx')
    const authGate = source('src/components/auth/auth-gate.tsx')
    const chatLayout = source('src/pages/chat/ChatLayout.tsx')
    const chatThread = source('src/pages/chat/ChatThread.tsx')

    expect(app).not.toContain('RouteFallback')
    expect(app).toContain('<PanelFallback scope="screen" />')
    expect(authGate).toContain('<PanelFallback scope="screen" />')
    expect(chatLayout).toContain("scope={isDesktop ? 'panel' : 'screen'}")
    expect(chatThread.match(/<PanelFallback scope="fill" \/>/g)).toHaveLength(2)

    for (const content of [app, authGate, chatLayout]) {
      expect(content).not.toContain('border-r-transparent animate-[spin_')
    }
  })
})
