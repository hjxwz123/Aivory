import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiConversation, ApiMessage, ApiSseEvent } from '@/api/types'
import type { Conversation, Message } from '@/types/chat'

const apiMocks = vi.hoisted(() => ({
  get: vi.fn(),
  stop: vi.fn(),
  streamSSE: vi.fn(),
  streamSSEGet: vi.fn(),
  toastInfo: vi.fn(),
  toastError: vi.fn(),
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
      get: apiMocks.get,
      stop: apiMocks.stop,
    },
    streamSSE: apiMocks.streamSSE,
    streamSSEGet: apiMocks.streamSSEGet,
  }
})

vi.mock('@/hooks/use-toast', () => ({
  toast: {
    info: apiMocks.toastInfo,
    success: vi.fn(),
    warning: vi.fn(),
    danger: vi.fn(),
    error: apiMocks.toastError,
    custom: vi.fn(),
  },
}))

import { useConversations } from './conversations'

function localConversation(messages: Message[] = []): Conversation {
  return {
    id: 'conv_stop',
    title: 'Stop reconcile',
    modelId: 'model_1',
    messages,
    createdAt: 1_700_000_000_000,
    updatedAt: 1_700_000_000_000,
  }
}

function apiConversation(activeLeafId: string): ApiConversation {
  return {
    id: 'conv_stop',
    user_id: 'user_1',
    project_id: '',
    title: 'Stop reconcile',
    provider: 'openai',
    model_id: 'model_1',
    kb_ids: [],
    rag_mode: 'auto',
    summary_blocks: [],
    active_leaf_id: activeLeafId,
    provider_state: {},
    pinned: false,
    archived: false,
    starred: false,
    created_at: 1_700_000_000,
    updated_at: 1_700_000_001,
  }
}

function apiMessage(
  id: string,
  role: 'user' | 'assistant',
  parentId: string,
  status: ApiMessage['status'],
  text: string,
): ApiMessage {
  return {
    id,
    conversation_id: 'conv_stop',
    parent_id: parentId,
    role,
    provider: 'openai',
    model_id: 'model_1',
    blocks: [{ kind: 'text', text }],
    attachments: [],
    citations: [],
    stop_reason: status === 'stopped' ? 'stopped' : '',
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    cost: 0,
    currency: 'USD',
    status,
    error: '',
    created_at: role === 'user' ? 1_700_000_001 : 1_700_000_002,
  }
}

function pathResponse(assistantStatus: ApiMessage['status']) {
  const user = apiMessage('msg_server_user', 'user', '', 'complete', 'question')
  const assistant = apiMessage(
    'msg_server_assistant',
    'assistant',
    user.id,
    assistantStatus,
    'partial answer',
  )
  return {
    conversation: apiConversation(assistant.id),
    messages: [user, assistant],
    has_more: false,
    next_before: undefined,
  }
}

function abortBeforeMessageStart(signal: AbortSignal): AsyncGenerator<{ data: ApiSseEvent }> {
  return (async function* () {
    await new Promise<void>((_resolve, reject) => {
      const fail = () => reject(new DOMException('Aborted', 'AbortError'))
      if (signal.aborted) fail()
      else signal.addEventListener('abort', fail, { once: true })
    })
    // The promise above only rejects, but the yield keeps this mock a genuine
    // async generator so it satisfies the same contract as streamSSE.
    yield { data: { type: 'done' } }
  })()
}

function oneEvent(event: ApiSseEvent): AsyncGenerator<{ data: ApiSseEvent }> {
  return (async function* () {
    yield { data: event }
  })()
}

function events(...items: ApiSseEvent[]): AsyncGenerator<{ data: ApiSseEvent }> {
  return (async function* () {
    for (const item of items) yield { data: item }
  })()
}

function followUpPathResponse() {
  const firstUser = apiMessage('msg_server_user', 'user', '', 'complete', 'question')
  const firstAssistant = apiMessage(
    'msg_server_assistant',
    'assistant',
    firstUser.id,
    'stopped',
    'partial answer',
  )
  const followUpUser = apiMessage(
    'msg_follow_user',
    'user',
    firstAssistant.id,
    'complete',
    'follow up',
  )
  const followUpAssistant = apiMessage(
    'msg_follow_assistant',
    'assistant',
    followUpUser.id,
    'complete',
    'follow-up answer',
  )
  return {
    conversation: apiConversation(followUpAssistant.id),
    messages: [firstUser, firstAssistant, followUpUser, followUpAssistant],
    has_more: false,
    next_before: undefined,
  }
}

function resetStore(messages: Message[] = []) {
  useConversations.setState({
    conversations: [localConversation(messages)],
    loaded: true,
    loading: false,
    loadingMore: false,
    hasMore: false,
    error: null,
  })
}

describe('stopped turn optimistic-id reconciliation', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    apiMocks.stop.mockResolvedValue({ ok: true })
    resetStore()
  })

  it('retries a stop before message_start, adopts canonical ids, and never restores the spinner', async () => {
    apiMocks.streamSSE.mockImplementation(
      (_path: string, _body: unknown, signal: AbortSignal) => abortBeforeMessageStart(signal),
    )
    apiMocks.get
      .mockResolvedValueOnce(pathResponse('streaming'))
      .mockResolvedValueOnce(pathResponse('stopped'))

    const serverSpinnerStates: boolean[] = []
    const unsubscribe = useConversations.subscribe((state) => {
      const assistant = state.conversations[0]?.messages.find(
        (message) => message.id === 'msg_server_assistant',
      )
      if (assistant) serverSpinnerStates.push(assistant.streaming === true)
    })

    const sending = useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'question',
      modelId: 'model_1',
      toolMode: 'auto',
    })
    const optimisticAssistant = useConversations
      .getState()
      .conversations[0].messages.find((message) => message.role === 'assistant')
    expect(optimisticAssistant?.localOnly).toBe(true)

    useConversations.getState().abortStream(optimisticAssistant!.id)
    await sending
    unsubscribe()

    const messages = useConversations.getState().conversations[0].messages
    expect(apiMocks.get).toHaveBeenCalledTimes(2)
    expect(messages.map((message) => message.id)).toEqual([
      'msg_server_user',
      'msg_server_assistant',
    ])
    expect(messages.every((message) => !message.localOnly && !message.streaming)).toBe(true)
    expect(serverSpinnerStates).not.toContain(true)
  })

  it('blocks edit-resend when its explicit parent is still client-only', async () => {
    const messages: Message[] = [
      {
        id: 'm_local_parent',
        localOnly: true,
        role: 'assistant',
        content: 'partial',
        createdAt: 1,
      },
      {
        id: 'm_local_question',
        localOnly: true,
        parentId: 'm_local_parent',
        role: 'user',
        content: 'edited question',
        createdAt: 2,
      },
    ]
    resetStore(messages)

    await useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'edited question',
      parentId: 'm_local_parent',
      branch: true,
      toolMode: 'auto',
    })

    expect(apiMocks.streamSSE).not.toHaveBeenCalled()
    expect(useConversations.getState().conversations[0].messages).toEqual(messages)
    expect(apiMocks.toastInfo).toHaveBeenCalledTimes(1)
  })

  it('serializes no local parent when follow-up and edit-resend start immediately after abort', async () => {
    const requestBodies: Array<Record<string, unknown>> = []
    let streamCall = 0
    apiMocks.streamSSE.mockImplementation(
      (_path: string, body: Record<string, unknown>, signal: AbortSignal) => {
        requestBodies.push(body)
        streamCall++
        if (streamCall === 1) return abortBeforeMessageStart(signal)
        if (streamCall === 2) {
          return events(
            { type: 'message_start', message_id: 'msg_follow_assistant' },
            { type: 'done', stop_reason: 'stop' },
          )
        }
        return oneEvent({ type: 'error', message: 'end branch test' })
      },
    )
    apiMocks.get
      .mockResolvedValueOnce(pathResponse('streaming'))
      .mockResolvedValueOnce(pathResponse('stopped'))
      .mockResolvedValueOnce(followUpPathResponse())

    const firstSend = useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'question',
      modelId: 'model_1',
      toolMode: 'auto',
    })
    const localStoppedAssistant = useConversations
      .getState()
      .conversations[0].messages.find((message) => message.role === 'assistant')!
    useConversations.getState().abortStream(localStoppedAssistant.id)
    const immediateLocalBranch = useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'must not become a root branch',
      modelId: 'model_1',
      parentId: localStoppedAssistant.id,
      branch: true,
      toolMode: 'auto',
    })
    const immediateFollowUp = useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'follow up',
      modelId: 'model_1',
      toolMode: 'auto',
    })
    // abortStream installs its barrier synchronously, before the first send's
    // catch runs; neither immediate action may open a second POST yet.
    expect(apiMocks.streamSSE).toHaveBeenCalledTimes(1)
    await Promise.all([firstSend, immediateLocalBranch, immediateFollowUp])
    const canonicalFollowUp = useConversations
      .getState()
      .conversations[0].messages.find((message) => message.id === 'msg_follow_user')!
    expect(canonicalFollowUp.parentId).toBe('msg_server_assistant')

    await useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'edited follow up',
      modelId: 'model_1',
      parentId: canonicalFollowUp.parentId,
      branch: true,
      toolMode: 'auto',
    })

    expect(requestBodies).toHaveLength(3)
    expect(requestBodies[1].parent_id).toBeUndefined()
    expect(requestBodies[2].parent_id).toBe('msg_server_assistant')
    expect(requestBodies.some((body) => body.parent_id === localStoppedAssistant.id)).toBe(false)
    expect(apiMocks.toastInfo).toHaveBeenCalledTimes(1)
  })

  it('omits a local parent from a normal append request and optimistic tree edge', async () => {
    resetStore([
      {
        id: 'm_local_parent',
        localOnly: true,
        role: 'assistant',
        content: 'partial',
        createdAt: 1,
      },
    ])
    let requestBody: Record<string, unknown> | undefined
    apiMocks.streamSSE.mockImplementation((_path: string, body: Record<string, unknown>) => {
      requestBody = body
      return oneEvent({ type: 'error', message: 'test terminal error' })
    })

    await useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'follow up',
      parentId: 'm_local_parent',
      toolMode: 'auto',
    })

    expect(requestBody?.parent_id).toBeUndefined()
    const followUp = useConversations
      .getState()
      .conversations[0].messages.find((message) => message.role === 'user' && message.content === 'follow up')
    expect(followUp?.parentId).toBeUndefined()
  })

  it('removes optimistic ids when every successful retry still returns the previous leaf', async () => {
    vi.useFakeTimers()
    try {
      const previousUser = apiMessage('msg_previous_user', 'user', '', 'complete', 'earlier')
      const previousAssistant = apiMessage(
        'msg_previous_assistant',
        'assistant',
        previousUser.id,
        'complete',
        'earlier answer',
      )
      resetStore([
        {
          id: previousUser.id,
          role: 'user',
          content: 'earlier',
          createdAt: 1,
        },
        {
          id: previousAssistant.id,
          parentId: previousUser.id,
          role: 'assistant',
          content: 'earlier answer',
          createdAt: 2,
        },
      ])
      apiMocks.streamSSE.mockImplementation(
        (_path: string, _body: unknown, signal: AbortSignal) => abortBeforeMessageStart(signal),
      )
      apiMocks.get.mockResolvedValue({
        conversation: apiConversation(previousAssistant.id),
        messages: [previousUser, previousAssistant],
        has_more: false,
        next_before: undefined,
      })

      const sending = useConversations.getState().sendMessage({
        conversationId: 'conv_stop',
        text: 'stopped before persistence',
        modelId: 'model_1',
        toolMode: 'auto',
      })
      const localAssistant = useConversations
        .getState()
        .conversations[0].messages.find((message) => message.localOnly && message.role === 'assistant')!
      useConversations.getState().abortStream(localAssistant.id)
      await vi.runAllTimersAsync()
      await sending

      expect(apiMocks.get).toHaveBeenCalledTimes(6)
      const messages = useConversations.getState().conversations[0].messages
      expect(messages.map((message) => message.id)).toEqual([
        'msg_previous_user',
        'msg_previous_assistant',
      ])
      expect(messages.some((message) => message.localOnly || message.streaming)).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })
})
