import { afterEach, describe, expect, it, vi } from 'vitest'

const realtimeAuth = vi.hoisted(() => ({
  user: null as { id: string; group_id?: string } | null,
  subscriber: null as (() => void) | null,
}))

vi.mock('@/api/client', () => ({
  streamSSEGet: vi.fn(async function* (_path: string, signal?: AbortSignal) {
    yield {
      event: 'compaction.started',
      data: {
        type: 'compaction.started',
        conversation_id: 'conversation-1',
        operation_id: 'cmp-1',
      },
    }
    if (!signal) return
    await new Promise<void>((resolve) => {
      if (signal.aborted) resolve()
      else signal.addEventListener('abort', () => resolve(), { once: true })
    })
  }),
}))
vi.mock('@/api/endpoints', () => ({
  conversationsApi: {
    get: vi.fn(),
    list: vi.fn(),
  },
}))
vi.mock('@/lib/app-update', () => ({ checkForUpdate: vi.fn() }))
vi.mock('@/lib/access-events', () => ({ invalidateAccessState: vi.fn() }))
vi.mock('@/lib/device-id', () => ({ getDeviceId: () => 'test-device' }))
vi.mock('@/lib/env-config', () => ({ envNum: (_name: string, fallback: number) => fallback }))
vi.mock('@/lib/sync-guards', () => ({
  isConversationTombstoned: () => false,
  markConversationsDeleted: vi.fn(),
}))
vi.mock('@/store/auth', () => ({
  useAuth: {
    getState: () => ({ user: realtimeAuth.user }),
    subscribe: vi.fn((subscriber: () => void) => {
      realtimeAuth.subscriber = subscriber
    }),
  },
}))
vi.mock('@/store/conversations', () => ({
  CONV_PAGE: 20,
  MSG_PAGE: 50,
  captureKnowledgeBaseSelectionGuard: vi.fn(),
  collectDoomedConversationIds: () => new Set<string>(),
  preservePendingKnowledgeBaseSelection: (next: unknown) => next,
  toLocalConversation: (conversation: unknown) => conversation,
  useConversations: {
    getState: () => ({ conversations: [] }),
    setState: vi.fn(),
  },
}))
vi.mock('@/store/workspaces', () => ({
  activeWorkspaceId: () => undefined,
  useWorkspaces: { getState: () => ({ load: vi.fn() }) },
}))
vi.mock('@/store/models', () => ({
  useModels: { getState: () => ({ load: vi.fn() }) },
}))

import { useToastStore } from '@/hooks/use-toast'

describe('automatic compaction notifications', () => {
  afterEach(() => {
    realtimeAuth.user = null
    realtimeAuth.subscriber?.()
    realtimeAuth.subscriber = null
    useToastStore.setState({ toasts: [] })
  })

  it('keeps automatic compaction silent', async () => {
    realtimeAuth.user = { id: 'user-1', group_id: 'group-1' }
    const { initRealtime, setRealtimeEnabled } = await import('@/lib/realtime')
    setRealtimeEnabled(true)
    initRealtime()
    await Promise.resolve()
    await Promise.resolve()

    expect(useToastStore.getState().toasts).toHaveLength(0)
  })
})
