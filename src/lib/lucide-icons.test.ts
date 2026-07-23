import { describe, expect, it } from 'vitest'
import { LUCIDE_ICON_NAMES, resolveLucideIconName } from './lucide-icons'

describe('Lucide icon catalog', () => {
  it('lists canonical picker names without duplicate Lucide aliases', () => {
    expect(LUCIDE_ICON_NAMES).toContain('Brain')
    expect(LUCIDE_ICON_NAMES).not.toContain('LucideBrain')
    expect(LUCIDE_ICON_NAMES.some((name) => name.startsWith('Lucide'))).toBe(false)
  })

  it('normalizes current and legacy stored names', () => {
    expect(resolveLucideIconName('Brain')).toBe('Brain')
    expect(resolveLucideIconName('brain')).toBe('Brain')
    expect(resolveLucideIconName('square-terminal')).toBe('SquareTerminal')
    expect(resolveLucideIconName('square_terminal')).toBe('SquareTerminal')
    expect(resolveLucideIconName('LucideBrain')).toBe('Brain')
    expect(resolveLucideIconName('BrainIcon')).toBe('Brain')
    expect(resolveLucideIconName('https://example.com/icon.png')).toBeNull()
  })
})
