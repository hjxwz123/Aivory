import { describe, expect, it } from 'vitest'
import { skillDisplayDescription } from '@/lib/skill-description'

describe('skillDisplayDescription', () => {
  it('prefers administrator display copy', () => {
    expect(skillDisplayDescription({
      display_description: '  Build polished presentation decks.  ',
      description: 'Use when the user asks for slides.',
    })).toBe('Build polished presentation decks.')
  })

  it('falls back to the Agent Skill when-to-use description', () => {
    expect(skillDisplayDescription({
      display_description: '   ',
      description: '  Use when the user asks for slides.  ',
    })).toBe('Use when the user asks for slides.')
  })

  it('returns an empty string when neither description is configured', () => {
    expect(skillDisplayDescription({})).toBe('')
  })
})
