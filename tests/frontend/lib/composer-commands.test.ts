import { describe, expect, it } from 'vitest'
import {
  MAX_SELECTED_USER_SKILLS,
  addSelectedUserSkill,
  findComposerCommandQuery,
  normalizeSelectedUserSkillIds,
  selectedUserSkillIdsForRequest,
} from '@/lib/composer-commands'

describe('composer slash commands', () => {
  it('identifies the exact slash range at the start of a text block', () => {
    expect(findComposerCommandQuery('/prompt', 1, 8)).toEqual({
      query: 'prompt',
      from: 1,
      to: 8,
    })
  })

  it('identifies a slash range after whitespace without consuming the whitespace', () => {
    expect(findComposerCommandQuery('Ask this /deck', 12, 26)).toEqual({
      query: 'deck',
      from: 21,
      to: 26,
    })
  })

  it('supports the empty command that opens the menu immediately after slash', () => {
    expect(findComposerCommandQuery('hello /', 1, 8)).toEqual({ query: '', from: 7, to: 8 })
  })

  it('rejects slash characters inside words or commands containing another slash', () => {
    expect(findComposerCommandQuery('hello/prompt', 1, 13)).toBeNull()
    expect(findComposerCommandQuery('hello /prompt/more', 1, 19)).toBeNull()
  })
})

describe('selected user skills', () => {
  it('deduplicates IDs and caps the wire payload at five', () => {
    const skills = [
      { id: ' skill-1 ' },
      { id: 'skill-1' },
      { id: '' },
      { id: 'skill-2' },
      { id: 'skill-3' },
      { id: 'skill-4' },
      { id: 'skill-5' },
      { id: 'skill-6' },
    ]
    expect(selectedUserSkillIdsForRequest(skills)).toEqual([
      'skill-1',
      'skill-2',
      'skill-3',
      'skill-4',
      'skill-5',
    ])
    expect(normalizeSelectedUserSkillIds(skills.map((skill) => skill.id))).toEqual([
      'skill-1',
      'skill-2',
      'skill-3',
      'skill-4',
      'skill-5',
    ])
  })

  it('omits the field when no valid skill is selected', () => {
    expect(selectedUserSkillIdsForRequest([])).toBeUndefined()
    expect(selectedUserSkillIdsForRequest([{ id: '  ' }])).toBeUndefined()
    expect(normalizeSelectedUserSkillIds(undefined)).toBeUndefined()
  })

  it('does not add duplicates or exceed the five-skill selection cap', () => {
    const initial = Array.from({ length: MAX_SELECTED_USER_SKILLS }, (_, index) => ({
      id: `skill-${index + 1}`,
    }))
    expect(addSelectedUserSkill(initial, { id: 'skill-1' })).toBe(initial)
    expect(addSelectedUserSkill(initial, { id: 'skill-6' })).toBe(initial)
    expect(addSelectedUserSkill(initial.slice(0, 2), { id: 'skill-3' })).toEqual([
      { id: 'skill-1' },
      { id: 'skill-2' },
      { id: 'skill-3' },
    ])
  })
})
