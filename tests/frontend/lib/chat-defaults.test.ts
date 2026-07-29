import { describe, expect, it } from 'vitest'
import {
  configuredUserDefaultModelId,
  resolveNewConversationFastMode,
} from '@/lib/chat-defaults'

describe('new conversation model defaults', () => {
  it('defaults to fast only when the user has not configured a model', () => {
    expect(resolveNewConversationFastMode({}, true)).toBe(true)
    expect(resolveNewConversationFastMode({ default_model_id: '' }, true)).toBe(true)
    expect(resolveNewConversationFastMode({ default_model_id: '   ' }, true)).toBe(true)
    expect(resolveNewConversationFastMode({ default_model_id: 'm_user' }, true)).toBe(false)
  })

  it('uses advanced mode when fast is unavailable or the caller requires it', () => {
    expect(resolveNewConversationFastMode({}, false)).toBe(false)
    expect(resolveNewConversationFastMode({}, true, true)).toBe(false)
  })

  it('normalizes the configured model id', () => {
    expect(configuredUserDefaultModelId({ default_model_id: '  m_user  ' })).toBe('m_user')
    expect(configuredUserDefaultModelId({ default_model_id: 42 })).toBe('')
    expect(configuredUserDefaultModelId(undefined)).toBe('')
  })
})
