import { describe, expect, it } from 'vitest'
import type { Attachment } from '@/types/chat'
import {
  hasImageAttachment,
  hasSendableMessageContent,
  initialConversationTitle,
} from '@/lib/chat-message-input'

const image: Attachment = {
  id: 'f_image',
  name: 'Quarterly dashboard.png',
  kind: 'image',
  size: 100,
}

const document: Attachment = {
  id: 'f_document',
  name: 'report.pdf',
  kind: 'pdf',
  size: 100,
}

describe('chat message input', () => {
  it('allows image-only vision chat but keeps image generation prompt-driven', () => {
    expect(hasImageAttachment([image])).toBe(true)
    expect(hasSendableMessageContent('', [image], false)).toBe(true)
    expect(hasSendableMessageContent('', [image], true)).toBe(false)
  })

  it('does not treat a document-only draft as a sendable empty request', () => {
    expect(hasImageAttachment([document])).toBe(false)
    expect(hasSendableMessageContent('', [document], false)).toBe(false)
    expect(hasSendableMessageContent('Summarize this', [document], false)).toBe(true)
  })

  it('derives an immediate image title from the filename with a localized fallback', () => {
    expect(initialConversationTitle('', [image], 'Image conversation')).toBe('Quarterly dashboard')
    expect(initialConversationTitle('', [{ ...image, name: '.png' }], '图片对话')).toBe('图片对话')
    expect(initialConversationTitle('Inspect $x^2$', [image], 'Image conversation')).toBe('Inspect x^2')
  })
})
