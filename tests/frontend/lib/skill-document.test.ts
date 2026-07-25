import { describe, expect, it } from 'vitest'
import { isValidSkillName, parseSkillDocument } from '@/lib/skill-document'

describe('parseSkillDocument', () => {
  it('parses the official instruction-only SKILL.md shape', () => {
    expect(parseSkillDocument(`---
name: meeting-follow-up
description: Extract decisions, owners, and next steps from meeting notes.
---

Review the notes and return a concise follow-up.`)).toEqual({
      name: 'meeting-follow-up',
      description: 'Extract decisions, owners, and next steps from meeting notes.',
      instructions: 'Review the notes and return a concise follow-up.',
    })
  })

  it('accepts CRLF documents and quoted scalar metadata', () => {
    expect(parseSkillDocument(
      '---\r\nname: "release-notes"\r\ndescription: \'Prepare a release summary\'\r\n---\r\n\r\nSummarize the changes.\r\n',
    )).toEqual({
      name: 'release-notes',
      description: 'Prepare a release summary',
      instructions: 'Summarize the changes.',
    })
  })

  it('keeps plain Markdown as instructions when frontmatter is absent', () => {
    expect(parseSkillDocument('  Follow the project conventions.\n')).toEqual({
      instructions: 'Follow the project conventions.',
    })
  })
})

describe('isValidSkillName', () => {
  it('accepts lowercase kebab-case names up to 64 characters', () => {
    expect(isValidSkillName('meeting-follow-up')).toBe(true)
    expect(isValidSkillName('a'.repeat(64))).toBe(true)
  })

  it('rejects spaces, uppercase, underscores, duplicate dashes, and long names', () => {
    expect(isValidSkillName('Meeting Follow Up')).toBe(false)
    expect(isValidSkillName('meeting_follow_up')).toBe(false)
    expect(isValidSkillName('meeting--follow-up')).toBe(false)
    expect(isValidSkillName('a'.repeat(65))).toBe(false)
  })
})
