import { describe, expect, it } from 'vitest'

import { normalizeOpenAIBaseUrl } from '@/lib/channel-base-url'

describe('normalizeOpenAIBaseUrl', () => {
  it.each([
    ['https://api.openai.com/v1', 'https://api.openai.com/v1'],
    ['https://api.openai.com/v1/', 'https://api.openai.com/v1'],
    ['https://api.openai.com/v2', 'https://api.openai.com/v2'],
    ['https://api.openai.com/v3/', 'https://api.openai.com/v3'],
    ['https://api.openai.com', 'https://api.openai.com'],
    ['https://proxy.example.com/openai/custom/', 'https://proxy.example.com/openai/custom'],
    [' https://proxy.example.com/openai/v1/ ', 'https://proxy.example.com/openai/v1'],
    ['http://localhost:8080/v1', 'http://localhost:8080/v1'],
    ['', ''],
    ['   ', ''],
  ])('normalizes %j', (input, expected) => {
    expect(normalizeOpenAIBaseUrl(input)).toBe(expected)
  })

  it.each([
    'api.openai.com/v1',
    'https://api.openai.com/v1?tenant=one',
    'https://api.openai.com/v1#config',
    'https://user:secret@api.openai.com/v1',
    'ftp://api.openai.com/v1',
    'https://api.openai.com/open ai/v1',
  ])('rejects %j', (input) => {
    expect(normalizeOpenAIBaseUrl(input)).toBeNull()
  })
})
