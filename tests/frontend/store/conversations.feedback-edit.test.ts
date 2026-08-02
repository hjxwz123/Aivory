import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiMessage } from '@/api/types'
import type { Conversation, Message } from '@/types/chat'

const apiMocks = vi.hoisted(() => ({
  editMessage: vi.fn(),
}))

vi.mock('@/api', () => {
  class ApiError extends Error {
    status: number

    constructor(message: string, status = 500) {
      super(message)
      this.status = status
    }
  }

  return {
    ApiError,
    conversationsApi: {
      editMessage: apiMocks.editMessage,
    },
    streamSSE: vi.fn(),
    streamSSEGet: vi.fn(),
  }
})

vi.mock('@/hooks/use-toast', () => ({
  toast: {
    info: vi.fn(),
    success: vi.fn(),
    warning: vi.fn(),
    danger: vi.fn(),
    error: vi.fn(),
    custom: vi.fn(),
  },
}))

import { useConversations } from '@/store/conversations'

function feedbackMessage(
  id: string,
  role: Message['role'],
  parentId: string,
  content: string,
): Message {
  return {
    id,
    role,
    parentId,
    content,
    createdAt: 1_700_000_000_000,
    liked: false,
    disliked: true,
    feedbackReasons: ['incorrect_fact'],
    feedbackComment: 'Old feedback',
  }
}

function resetStore(messages: Message[]) {
  const conversation: Conversation = {
    id: 'conv_feedback_edit',
    title: 'Feedback edit',
    modelId: 'model_1',
    messages,
    createdAt: 1_700_000_000_000,
    updatedAt: 1_700_000_000_000,
  }
  useConversations.setState({ conversations: [conversation] })
}

function editResponse(id: string, role: ApiMessage['role'], feedback: ApiMessage['feedback'] = ''): ApiMessage {
  return {
    id,
    conversation_id: 'conv_feedback_edit',
    parent_id: '',
    role,
    provider: 'openai',
    model_id: 'model_1',
    blocks: [],
    attachments: [],
    citations: [],
    stop_reason: '',
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    cost: 0,
    currency: 'USD',
    status: 'complete',
    error: '',
    feedback,
    feedback_reasons: [],
    feedback_comment: '',
    created_at: 1_700_000_000,
  }
}

describe('feedback invalidation after editing a message in place', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('reconciles an edited assistant with the feedback returned by the API', async () => {
    resetStore([feedbackMessage('assistant_1', 'assistant', 'user_1', 'Original answer')])
    apiMocks.editMessage.mockResolvedValue(editResponse('assistant_1', 'assistant'))

    await useConversations
      .getState()
      .editMessageInPlace('conv_feedback_edit', 'assistant_1', 'Edited answer')

    expect(useConversations.getState().conversations[0].messages[0]).toMatchObject({
      content: 'Edited answer',
      liked: false,
      disliked: false,
      feedbackReasons: [],
      feedbackComment: '',
    })
  })

  it('clears feedback only from direct assistant children of an edited question', async () => {
    const user = feedbackMessage('user_1', 'user', '', 'Original question')
    const direct = feedbackMessage('assistant_direct', 'assistant', 'user_1', 'Direct answer')
    const sibling = feedbackMessage('assistant_sibling', 'assistant', 'user_1', 'Sibling answer')
    const later = feedbackMessage('assistant_later', 'assistant', 'user_2', 'Later answer')
    resetStore([user, direct, sibling, later])
    apiMocks.editMessage.mockResolvedValue(editResponse('user_1', 'user'))

    await useConversations
      .getState()
      .editMessageInPlace('conv_feedback_edit', 'user_1', 'Edited question')

    const messages = useConversations.getState().conversations[0].messages
    expect(messages.find((message) => message.id === 'assistant_direct')?.disliked).toBe(false)
    expect(messages.find((message) => message.id === 'assistant_sibling')?.disliked).toBe(false)
    expect(messages.find((message) => message.id === 'assistant_later')).toMatchObject({
      disliked: true,
      feedbackReasons: ['incorrect_fact'],
      feedbackComment: 'Old feedback',
    })
  })

  it('rolls back only content when the edit request fails', async () => {
    resetStore([feedbackMessage('assistant_1', 'assistant', 'user_1', 'Original answer')])
    apiMocks.editMessage.mockRejectedValue(new Error('network failure'))

    await expect(
      useConversations
        .getState()
        .editMessageInPlace('conv_feedback_edit', 'assistant_1', 'Rejected edit'),
    ).rejects.toThrow('network failure')

    expect(useConversations.getState().conversations[0].messages[0]).toMatchObject({
      content: 'Original answer',
      disliked: true,
      feedbackReasons: ['incorrect_fact'],
      feedbackComment: 'Old feedback',
    })
  })
})
