import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiConversation } from '@/api/types'

const apiMocks = vi.hoisted(() => ({
  list: vi.fn(),
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
      list: apiMocks.list,
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
import { useWorkspaces } from '@/store/workspaces'

function conversation(id: string, updatedAt: number): ApiConversation {
  return {
    id,
    user_id: 'user-1',
    project_id: '',
    title: id,
    provider: 'openai',
    model_id: 'model-1',
    kb_ids: [],
    rag_mode: 'auto',
    summary_blocks: [],
    active_leaf_id: '',
    provider_state: {},
    pinned: false,
    archived: false,
    starred: false,
    created_at: updatedAt,
    updated_at: updatedAt,
  }
}

describe('conversation sidebar pagination', () => {
  beforeEach(() => {
    apiMocks.list.mockReset()
    useWorkspaces.setState({ activeId: null })
    useConversations.setState({
      conversations: [],
      loaded: false,
      loading: false,
      loadingMore: false,
      hasMore: false,
      error: null,
    })
  })

  it('loads 20 rows initially and advances the server offset for the next page', async () => {
    const firstPage = Array.from({ length: 20 }, (_, index) =>
      conversation(`conversation-${index + 1}`, 100 - index),
    )
    const secondPage = Array.from({ length: 3 }, (_, index) =>
      conversation(`conversation-${index + 21}`, 80 - index),
    )
    apiMocks.list
      .mockResolvedValueOnce({ conversations: firstPage, limit: 20, offset: 0, has_more: true })
      .mockResolvedValueOnce({ conversations: secondPage, limit: 20, offset: 20, has_more: false })

    await useConversations.getState().load()

    expect(apiMocks.list).toHaveBeenNthCalledWith(1, undefined, 20, 0, undefined)
    expect(useConversations.getState()).toMatchObject({
      loaded: true,
      loading: false,
      hasMore: true,
    })
    expect(useConversations.getState().conversations).toHaveLength(20)

    await useConversations.getState().loadMore()

    expect(apiMocks.list).toHaveBeenNthCalledWith(2, undefined, 20, 20, undefined)
    expect(useConversations.getState()).toMatchObject({
      loadingMore: false,
      hasMore: false,
    })
    expect(useConversations.getState().conversations).toHaveLength(23)
  })
})
