import { describe, expect, it } from 'vitest'
import {
  MAX_SELECTED_USER_SKILLS,
  addSelectedUserSkill,
  createScopedCommandGate,
  findComposerCommandQuery,
  normalizeSelectedUserSkillIds,
  selectedUserSkillIdsForRequest,
} from '@/lib/composer-commands'

describe('composer commands', () => {
  it('identifies the exact slash range at the start of a text block', () => {
    expect(findComposerCommandQuery('/prompt', 1, 8)).toEqual({
      trigger: '/',
      query: 'prompt',
      from: 1,
      to: 8,
    })
  })

  it('identifies a slash range after whitespace without consuming the whitespace', () => {
    expect(findComposerCommandQuery('Ask this /deck', 12, 26)).toEqual({
      trigger: '/',
      query: 'deck',
      from: 21,
      to: 26,
    })
  })

  it('supports the empty command that opens the menu immediately after slash', () => {
    expect(findComposerCommandQuery('hello /', 1, 8)).toEqual({
      trigger: '/',
      query: '',
      from: 7,
      to: 8,
    })
  })

  it('rejects slash characters inside words or commands containing another slash', () => {
    expect(findComposerCommandQuery('hello/prompt', 1, 13)).toBeNull()
    expect(findComposerCommandQuery('hello /prompt/more', 1, 19)).toBeNull()
  })

  it('opens the knowledge-base menu for an empty at-sign mention', () => {
    expect(findComposerCommandQuery('@', 1, 2)).toEqual({
      trigger: '@',
      query: '',
      from: 1,
      to: 2,
    })
  })

  it('identifies a Chinese knowledge-base query after whitespace', () => {
    expect(findComposerCommandQuery('请参考 @产品资料', 4, 13)).toEqual({
      trigger: '@',
      query: '产品资料',
      from: 8,
      to: 13,
    })
  })

  it('does not treat email addresses or inline at-signs as knowledge-base mentions', () => {
    expect(findComposerCommandQuery('me@example.com', 1, 15)).toBeNull()
    expect(findComposerCommandQuery('ask@product', 1, 12)).toBeNull()
    expect(findComposerCommandQuery('hello @product@other', 1, 21)).toBeNull()
  })

  it('allows the other trigger character inside a query without changing slash behavior', () => {
    expect(findComposerCommandQuery('/owner@example', 1, 15)).toEqual({
      trigger: '/',
      query: 'owner@example',
      from: 1,
      to: 15,
    })
    expect(findComposerCommandQuery('@HR/Policy', 1, 11)).toEqual({
      trigger: '@',
      query: 'HR/Policy',
      from: 1,
      to: 11,
    })
  })
})

describe('scoped command gate', () => {
  it('rejects same-tick duplicate starts for the same conversation', () => {
    const gate = createScopedCommandGate()
    const first = gate.begin('conversation-1')

    expect(first).not.toBeNull()
    expect(gate.begin('conversation-1')).toBeNull()
    expect(gate.owns(first!.run)).toBe(true)
  })

  it('invalidates an old conversation without letting it release the replacement', () => {
    const gate = createScopedCommandGate()
    const first = gate.begin('conversation-1')!
    const second = gate.begin('conversation-2')!

    expect(second.replaced).toEqual(first.run)
    expect(gate.owns(first.run)).toBe(false)
    expect(gate.release(first.run)).toBe(false)
    expect(gate.owns(second.run)).toBe(true)
    expect(gate.release(second.run)).toBe(true)
  })

  it('clears ownership when the composer leaves the command scope', () => {
    const gate = createScopedCommandGate()
    const run = gate.begin('conversation-1')!.run

    expect(gate.invalidateExcept('conversation-2')).toEqual(run)
    expect(gate.owns(run)).toBe(false)
  })

  it('invalidates an in-flight command when the composer unmounts', () => {
    const gate = createScopedCommandGate()
    const run = gate.begin('conversation-1')!.run

    expect(gate.clear()).toEqual(run)
    expect(gate.owns(run)).toBe(false)
    expect(gate.release(run)).toBe(false)
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
