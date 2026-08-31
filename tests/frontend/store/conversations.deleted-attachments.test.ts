import { beforeEach, describe, expect, it } from 'vitest'
import type { Conversation } from '@/types/chat'
import { useConversations } from '@/store/conversations'

function conversation(id = 'conversation-1'): Conversation {
  return {
    id,
    title: 'Files',
    modelId: 'model-1',
    createdAt: 1,
    updatedAt: 1,
    messages: [
      {
        id: `message-${id}`,
        role: 'user',
        content: 'Review the attachment.',
        createdAt: 1,
        attachments: [
          {
            id: 'file-1',
            documentId: 'document-1',
            name: 'contract.pdf',
            kind: 'pdf',
            size: 100,
            previewUrl: '/api/files/file-1',
          },
        ],
      },
    ],
  }
}

describe('deleted conversation attachments', () => {
  beforeEach(() => {
    useConversations.setState({
      conversations: [conversation(), conversation('conversation-2')],
    })
  })

  it('marks matching historical attachments unavailable in one conversation', () => {
    useConversations
      .getState()
      .markAttachmentsDeleted(['file-1'], 'conversation-1')

    expect(
      useConversations.getState().conversations[0].messages[0].attachments?.[0],
    ).toMatchObject({
      id: 'file-1',
      deleted: true,
      previewUrl: undefined,
    })
    expect(
      useConversations.getState().conversations[1].messages[0].attachments?.[0],
    ).not.toHaveProperty('deleted')
  })

  it('matches deleted document ids when no conversation scope is known', () => {
    useConversations
      .getState()
      .markAttachmentsDeleted(['document-1'])

    for (const cached of useConversations.getState().conversations) {
      expect(cached.messages[0].attachments?.[0]).toMatchObject({
        id: 'file-1',
        documentId: 'document-1',
        deleted: true,
        previewUrl: undefined,
      })
    }
  })
})
