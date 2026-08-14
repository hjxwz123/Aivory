import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const realtimeAuth = vi.hoisted(() => ({
  user: null as { id: string; group_id?: string } | null,
  subscriber: null as (() => void) | null,
  yieldCompactionAfterAbort: false,
}))

vi.mock('@/api/client', () => ({
  streamSSEGet: vi.fn(async function* (_path: string, signal?: AbortSignal) {
    if (!signal) {
      yield { event: 'hello', data: { type: 'hello' } }
      return
    }
    await new Promise<void>((resolve) => {
      if (signal.aborted) resolve()
      else signal.addEventListener('abort', () => resolve(), { once: true })
    })
    if (realtimeAuth.yieldCompactionAfterAbort) {
      yield {
        event: 'compaction.started',
        data: { type: 'compaction.started', conversation_id: 'stale-conversation' },
      }
    }
  }),
}))
vi.mock('@/api/endpoints', () => ({
  conversationsApi: {
    get: vi.fn(),
    list: vi.fn(),
  },
}))
vi.mock('@/i18n', () => ({
  default: {
    t: (key: string) => key,
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

import { TOAST_REMOVE_DELAY_MS, useToastStore } from '@/hooks/use-toast'
import {
  dismissCompactionNotifications,
  handleCompactionNotification,
} from '@/lib/compaction-notifications'

function openToasts() {
  return useToastStore.getState().toasts.filter((item) => item.open)
}

describe('realtime compaction notifications', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    dismissCompactionNotifications()
    useToastStore.setState({ toasts: [] })
  })

  afterEach(() => {
    realtimeAuth.user = null
    realtimeAuth.yieldCompactionAfterAbort = false
    realtimeAuth.subscriber?.()
    dismissCompactionNotifications()
    vi.clearAllTimers()
    vi.useRealTimers()
    useToastStore.setState({ toasts: [] })
  })

  it('replaces an existing sticky notification when compaction starts again', () => {
    handleCompactionNotification('compaction.started', 'conversation-1')
    const first = openToasts()[0]

    expect(first).toMatchObject({
      title: 'chat:composer.commands.autoCompacting',
      variant: 'info',
      duration: 0,
    })

    handleCompactionNotification('compaction.started', 'conversation-1')

    expect(useToastStore.getState().toasts.find((item) => item.id === first.id)?.open).toBe(false)
    expect(openToasts()).toHaveLength(1)
    expect(openToasts()[0].id).not.toBe(first.id)
  })

  it.each([
    ['compaction.completed', 'success', 'chat:composer.commands.autoCompacted'],
    ['compaction.failed', 'danger', 'chat:composer.commands.autoFailed'],
  ] as const)('closes the sticky notification when %s arrives', (event, variant, title) => {
    handleCompactionNotification('compaction.started', 'conversation-1')
    const stickyId = openToasts()[0].id

    handleCompactionNotification(event, 'conversation-1')

    expect(useToastStore.getState().toasts.find((item) => item.id === stickyId)?.open).toBe(false)
    expect(openToasts()).toMatchObject([{ title, variant }])

    vi.advanceTimersByTime(TOAST_REMOVE_DELAY_MS)
    expect(useToastStore.getState().toasts.some((item) => item.id === stickyId)).toBe(false)
  })

  it('ignores a stale terminal event after a newer operation starts', () => {
    handleCompactionNotification('compaction.started', 'conversation-1', 'operation-old')
    handleCompactionNotification('compaction.started', 'conversation-1', 'operation-new')
    const currentSticky = openToasts()[0]

    handleCompactionNotification('compaction.completed', 'conversation-1', 'operation-old')

    expect(openToasts()).toHaveLength(1)
    expect(openToasts()[0]).toMatchObject({
      id: currentSticky.id,
      title: 'chat:composer.commands.autoCompacting',
      variant: 'info',
      duration: 0,
    })

    handleCompactionNotification('compaction.completed', 'conversation-1', 'operation-new')
    expect(openToasts()).toMatchObject([
      { title: 'chat:composer.commands.autoCompacted', variant: 'success' },
    ])
  })

  it('ignores a stale terminal event after the newer terminal toast expires', () => {
    handleCompactionNotification('compaction.started', 'conversation-1', 'operation-old')
    handleCompactionNotification('compaction.started', 'conversation-1', 'operation-new')
    handleCompactionNotification('compaction.completed', 'conversation-1', 'operation-new')
    vi.advanceTimersByTime(5000)

    handleCompactionNotification('compaction.failed', 'conversation-1', 'operation-old')

    expect(openToasts()).toHaveLength(0)
  })

  it('closes every sticky notification during reconnect or user-switch cleanup', () => {
    handleCompactionNotification('compaction.started', 'conversation-1')
    handleCompactionNotification('compaction.started', 'conversation-2')

    dismissCompactionNotifications()

    expect(openToasts()).toHaveLength(0)
    expect(useToastStore.getState().toasts.every((item) => !item.open)).toBe(true)
  })

  it.each(['compaction.completed', 'compaction.failed'] as const)(
    'clears a %s terminal notification during account cleanup',
    (event) => {
      handleCompactionNotification(event, 'conversation-1')
      expect(openToasts()).toHaveLength(1)

      dismissCompactionNotifications()

      expect(openToasts()).toHaveLength(0)
      expect(useToastStore.getState().toasts.every((item) => !item.open)).toBe(true)
    },
  )

  it('clears sticky progress when a group change replaces the realtime connection', async () => {
    realtimeAuth.user = { id: 'user-1', group_id: 'group-1' }
    const { initRealtime } = await import('@/lib/realtime')
    initRealtime()
    await Promise.resolve()

    handleCompactionNotification('compaction.started', 'conversation-1')
    expect(openToasts()).toHaveLength(1)

    realtimeAuth.user = { id: 'user-1', group_id: 'group-2' }
    realtimeAuth.subscriber?.()

    expect(openToasts()).toHaveLength(0)
  })

  it.each([
    ['account', { id: 'user-2', group_id: 'group-2' }],
    ['group', { id: 'user-1', group_id: 'group-2' }],
  ] as const)('drops a buffered compaction frame after an aborted %s connection', async (_kind, nextUser) => {
    realtimeAuth.user = { id: 'user-1', group_id: 'group-1' }
    realtimeAuth.yieldCompactionAfterAbort = true
    const { initRealtime } = await import('@/lib/realtime')
    initRealtime()
    realtimeAuth.subscriber?.()
    await Promise.resolve()

    realtimeAuth.user = nextUser
    realtimeAuth.subscriber?.()
    await Promise.resolve()
    await Promise.resolve()

    expect(openToasts()).toHaveLength(0)
  })
})
