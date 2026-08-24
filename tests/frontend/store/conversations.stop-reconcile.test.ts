import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiConversation, ApiMessage, ApiModel, ApiSseEvent } from '@/api/types'
import type { Conversation, Message } from '@/types/chat'

const apiMocks = vi.hoisted(() => ({
  create: vi.fn(),
  get: vi.fn(),
  update: vi.fn(),
  stop: vi.fn(),
  inlineThreads: vi.fn(),
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
      create: apiMocks.create,
      get: apiMocks.get,
      update: apiMocks.update,
      stop: apiMocks.stop,
      inlineThreads: apiMocks.inlineThreads,
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

import { toLocalMessage, useConversations } from '@/store/conversations'
import { useComposerPrefs } from '@/store/composer-prefs'
import { useModels } from '@/store/models'
import { messageHasActions } from '@/lib/message-state'

function localConversation(messages: Message[] = [], title = 'Stop reconcile'): Conversation {
  return {
    id: 'conv_stop',
    title,
    modelId: 'model_1',
    messages,
    createdAt: 1_700_000_000_000,
    updatedAt: 1_700_000_000_000,
  }
}

function apiConversation(activeLeafId: string, title = 'Stop reconcile'): ApiConversation {
  return {
    id: 'conv_stop',
    user_id: 'user_1',
    project_id: '',
    title,
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

function pathResponse(assistantStatus: ApiMessage['status'], title = 'Stop reconcile') {
  const user = apiMessage('msg_server_user', 'user', '', 'complete', 'question')
  const assistant = apiMessage(
    'msg_server_assistant',
    'assistant',
    user.id,
    assistantStatus,
    'partial answer',
  )
  return {
    conversation: apiConversation(assistant.id, title),
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

function resetStore(messages: Message[] = [], title = 'Stop reconcile') {
  useConversations.setState({
    conversations: [localConversation(messages, title)],
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

  it('reconciles a generated first-turn title when the realtime event is missed', async () => {
    vi.useFakeTimers()
    try {
      const question = 'Explain how event-driven systems coordinate several delayed asynchronous tasks safely'
      const optimisticTitle = question.slice(0, 60)
      resetStore([], optimisticTitle)
      apiMocks.streamSSE.mockReturnValue(
        events(
          { type: 'message_start', message_id: 'msg_server_assistant' },
          { type: 'text_delta', text: 'answer' },
          { type: 'done' },
        ),
      )
      apiMocks.get
        // The immediate post-stream reconcile can fail independently of title
        // generation; the optimistic 60-character title must remain retryable.
        .mockRejectedValueOnce(new Error('temporary connection failure'))
        // The delayed metadata reconcile sees the task model's committed title.
        .mockResolvedValueOnce(pathResponse('complete', 'Generated title'))

      await useConversations.getState().sendMessage({
        conversationId: 'conv_stop',
        text: question,
        modelId: 'model_1',
        toolMode: 'auto',
      })

      expect(useConversations.getState().conversations[0].title).toBe(optimisticTitle)
      // First attempt (1s) fails, then the second backoff (2s) succeeds.
      await vi.advanceTimersByTimeAsync(3000)
      expect(useConversations.getState().conversations[0].title).toBe('Generated title')
      expect(apiMocks.get).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('retries a stop before message_start, adopts canonical ids, and never restores the spinner', async () => {
    apiMocks.streamSSE.mockImplementation(
      (_path: string, _body: unknown, signal: AbortSignal) => abortBeforeMessageStart(signal),
    )
    apiMocks.get
      .mockResolvedValueOnce(pathResponse('streaming'))
      .mockResolvedValueOnce(pathResponse('stopped'))

    const serverSpinnerStates: boolean[] = []
    const settledStoppedStates: boolean[] = []
    const unsubscribe = useConversations.subscribe((state) => {
      const assistant = state.conversations[0]?.messages.find(
        (message) => message.id === 'msg_server_assistant',
      )
      if (assistant) serverSpinnerStates.push(assistant.streaming === true)
      const visibleAssistant = state.conversations[0]?.messages.find((message) => message.role === 'assistant')
      if (visibleAssistant && !visibleAssistant.streaming) {
        settledStoppedStates.push(visibleAssistant.stopped === true)
      }
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
    expect(settledStoppedStates.every(Boolean)).toBe(true)
    expect(messageHasActions(messages[1])).toBe(true)
  })

  it('routes a stop from the pre-message_start optimistic id after the row is re-keyed', async () => {
    let release: (() => void) | undefined
    apiMocks.streamSSE.mockImplementation(
      (_path: string, _body: Record<string, unknown>, signal: AbortSignal) =>
        (async function* () {
          yield { data: { type: 'message_start', message_id: 'msg_rekeyed_assistant' } as ApiSseEvent }
          await new Promise<void>((resolve, reject) => {
            release = resolve
            const onAbort = () => reject(new DOMException('Aborted', 'AbortError'))
            if (signal.aborted) onAbort()
            else signal.addEventListener('abort', onAbort, { once: true })
          })
        })(),
    )
    const stoppedUser = apiMessage('msg_rekeyed_user', 'user', '', 'complete', 'question')
    const stoppedAssistant = apiMessage(
      'msg_rekeyed_assistant',
      'assistant',
      stoppedUser.id,
      'stopped',
      '',
    )
    stoppedAssistant.blocks = []
    apiMocks.get.mockResolvedValue({
      conversation: apiConversation(stoppedAssistant.id),
      messages: [stoppedUser, stoppedAssistant],
      has_more: false,
      next_before: undefined,
    })

    const sending = useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'question',
      modelId: 'model_1',
      toolMode: 'auto',
    })
    const optimisticId = useConversations
      .getState()
      .conversations[0].messages.find((message) => message.localOnly && message.role === 'assistant')!.id

    await vi.waitFor(() => {
      expect(
        useConversations.getState().conversations[0].messages.some((message) => message.id === 'msg_rekeyed_assistant'),
      ).toBe(true)
    })
    useConversations.getState().abortStream(optimisticId)

    expect(apiMocks.stop).toHaveBeenCalledWith('conv_stop', {
      generation_id: expect.stringMatching(/^gen_[0-9a-f]{32}$/),
    })
    await sending
    release?.()
  })

  it('persists and stops the first turn when Stop is clicked while its real conversation is being created', async () => {
    let resolveCreate: ((conversation: ApiConversation) => void) | undefined
    apiMocks.create.mockReturnValue(
      new Promise<ApiConversation>((resolve) => {
        resolveCreate = resolve
      }),
    )
    let transportSignal: AbortSignal | undefined
    let transportWasAbortedAtStart: boolean | undefined
    let requestGenerationId: unknown
    apiMocks.streamSSE.mockImplementation(
      (_path: string, body: Record<string, unknown>, signal: AbortSignal) => {
        transportSignal = signal
        transportWasAbortedAtStart = signal.aborted
        requestGenerationId = body.generation_id
        return events(
          { type: 'message_start', message_id: 'msg_first_server_assistant' },
          { type: 'done', stop_reason: 'stopped' },
        )
      },
    )
    const firstUser = apiMessage('msg_first_server_user', 'user', '', 'complete', 'first question')
    const firstAssistant = apiMessage(
      'msg_first_server_assistant',
      'assistant',
      firstUser.id,
      'stopped',
      '',
    )
    firstAssistant.blocks = []
    apiMocks.get.mockResolvedValue({
      conversation: { ...apiConversation(firstAssistant.id), id: 'conv_real' },
      messages: [firstUser, firstAssistant],
      has_more: false,
      next_before: undefined,
    })
    useConversations.setState({ conversations: [] })
    const tempId = useConversations.getState().beginOptimisticConversation('first question', 'model_1')

    const sending = useConversations.getState().sendMessage({
      conversationId: tempId,
      createFirst: true,
      text: 'first question',
      modelId: 'model_1',
      toolMode: 'auto',
    })
    const optimisticAssistant = useConversations
      .getState()
      .conversations.find((conversation) => conversation.id === tempId)!
      .messages.find((message) => message.role === 'assistant')!
    useConversations.getState().abortStream(optimisticAssistant.id)

    resolveCreate?.({ ...apiConversation(''), id: 'conv_real', active_leaf_id: '' })
    await sending

    expect(transportWasAbortedAtStart).toBe(false)
    expect(transportSignal?.aborted).toBe(true)
    expect(requestGenerationId).toMatch(/^gen_[0-9a-f]{32}$/)
    expect(apiMocks.stop).toHaveBeenCalledWith(tempId, {
      generation_id: requestGenerationId,
    })
    expect(apiMocks.stop).toHaveBeenCalledWith('conv_real', {
      message_id: 'msg_first_server_assistant',
    })
    const realConversation = useConversations
      .getState()
      .conversations.find((conversation) => conversation.id === 'conv_real')
    expect(realConversation?.messages.map((message) => message.id)).toEqual([
      firstUser.id,
      firstAssistant.id,
    ])
    expect(realConversation?.messages[1]).toMatchObject({ stopped: true, streaming: false })
  })

  it('can stop a regeneration through the source assistant alias after message_start', async () => {
    const sourceUser: Message = {
      id: 'msg_source_user',
      role: 'user',
      content: 'question',
      createdAt: 1,
    }
    const sourceAssistant: Message = {
      id: 'msg_source_assistant',
      parentId: sourceUser.id,
      role: 'assistant',
      content: 'old answer',
      createdAt: 2,
    }
    resetStore([sourceUser, sourceAssistant])
    apiMocks.streamSSE.mockImplementation(
      (_path: string, _body: Record<string, unknown>, signal: AbortSignal) =>
        (async function* () {
          yield { data: { type: 'message_start', message_id: 'msg_regenerated_server' } as ApiSseEvent }
          await new Promise<void>((_resolve, reject) => {
            const onAbort = () => reject(new DOMException('Aborted', 'AbortError'))
            if (signal.aborted) onAbort()
            else signal.addEventListener('abort', onAbort, { once: true })
          })
        })(),
    )
    const stoppedAssistant = apiMessage(
      'msg_regenerated_server',
      'assistant',
      sourceUser.id,
      'stopped',
      '',
    )
    stoppedAssistant.blocks = []
    apiMocks.get.mockResolvedValue({
      conversation: apiConversation(stoppedAssistant.id),
      messages: [apiMessage(sourceUser.id, 'user', '', 'complete', sourceUser.content), stoppedAssistant],
      has_more: false,
      next_before: undefined,
    })

    const regenerating = useConversations
      .getState()
      .regenerate('conv_stop', sourceAssistant.id, 'model_1')
    await vi.waitFor(() => {
      expect(
        useConversations.getState().conversations[0].messages.some((message) => message.id === 'msg_regenerated_server'),
      ).toBe(true)
    })
    useConversations.getState().abortStream(sourceAssistant.id)

    expect(apiMocks.stop).toHaveBeenCalledWith('conv_stop', {
      generation_id: expect.stringMatching(/^gen_[0-9a-f]{32}$/),
    })
    await regenerating
    expect(useConversations.getState().conversations[0].messages.at(-1)).toMatchObject({
      id: 'msg_regenerated_server',
      stopped: true,
      streaming: false,
    })
  })

  it('does not let a replay handoff marker swallow a later regeneration stop', async () => {
    let postCall = 0
    apiMocks.streamSSE.mockImplementation(
      (_path: string, _body: Record<string, unknown>, signal: AbortSignal) =>
        (async function* () {
          const messageId = postCall++ === 0 ? 'msg_handoff_assistant' : 'msg_after_handoff_regen'
          yield { data: { type: 'message_start', message_id: messageId } as ApiSseEvent }
          await new Promise<void>((_resolve, reject) => {
            const onAbort = () => reject(new DOMException('Aborted', 'AbortError'))
            if (signal.aborted) onAbort()
            else signal.addEventListener('abort', onAbort, { once: true })
          })
        })(),
    )
    apiMocks.streamSSEGet.mockReturnValue(events({ type: 'done', stop_reason: 'stop' }))

    const user = apiMessage('msg_handoff_user', 'user', '', 'complete', 'question')
    const completed = apiMessage(
      'msg_handoff_assistant',
      'assistant',
      user.id,
      'complete',
      'completed through replay',
    )
    const stopped = apiMessage(
      'msg_after_handoff_regen',
      'assistant',
      user.id,
      'stopped',
      '',
    )
    stopped.blocks = []
    apiMocks.get
      .mockResolvedValueOnce({
        conversation: apiConversation(completed.id),
        messages: [user, completed],
        has_more: false,
        next_before: undefined,
      })
      .mockResolvedValueOnce({
        conversation: apiConversation(stopped.id),
        messages: [user, stopped],
        has_more: false,
        next_before: undefined,
      })

    const firstSending = useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'question',
      modelId: 'model_1',
      toolMode: 'auto',
    })
    await vi.waitFor(() => {
      expect(
        useConversations.getState().conversations[0].messages.some((message) => message.id === completed.id),
      ).toBe(true)
    })
    useConversations.getState().resumeStreamingMessages('conv_stop', { replaceExisting: true })
    await firstSending
    await vi.waitFor(() => {
      expect(apiMocks.get).toHaveBeenCalledTimes(1)
      expect(useConversations.getState().conversations[0].messages.at(-1)).toMatchObject({
        id: completed.id,
        streaming: false,
      })
    })

    const regenerating = useConversations
      .getState()
      .regenerate('conv_stop', completed.id, 'model_1')
    await vi.waitFor(() => {
      expect(
        useConversations.getState().conversations[0].messages.some((message) => message.id === stopped.id),
      ).toBe(true)
    })
    useConversations.getState().abortStream(stopped.id)
    await regenerating

    expect(apiMocks.get).toHaveBeenCalledTimes(2)
    expect(useConversations.getState().conversations[0].messages.at(-1)).toMatchObject({
      id: stopped.id,
      stopped: true,
      streaming: false,
    })
  })

  it('targets the current generation without aborting a concurrent sibling branch', async () => {
    type ControlledStream = {
      body: Record<string, unknown>
      signal: AbortSignal
      release?: () => void
    }

    const streams: ControlledStream[] = []
    apiMocks.streamSSE.mockImplementation(
      (_path: string, body: Record<string, unknown>, signal: AbortSignal) => {
        const index = streams.length
        const stream: ControlledStream = { body, signal }
        streams.push(stream)

        return (async function* () {
          yield {
            data: {
              type: 'message_start',
              message_id: index === 0 ? 'msg_server_first' : 'msg_server_second',
            } as ApiSseEvent,
          }
          await new Promise<void>((resolve, reject) => {
            let settled = false
            const finish = (next: () => void) => {
              if (settled) return
              settled = true
              signal.removeEventListener('abort', onAbort)
              next()
            }
            const onAbort = () => finish(() => reject(new DOMException('Aborted', 'AbortError')))
            stream.release = () => finish(resolve)
            if (signal.aborted) onAbort()
            else signal.addEventListener('abort', onAbort, { once: true })
          })
          yield { data: { type: 'done' } as ApiSseEvent }
        })()
      },
    )

    const secondUser = apiMessage('msg_server_second_user', 'user', '', 'complete', 'edited question')
    const secondAssistant = apiMessage(
      'msg_server_second',
      'assistant',
      secondUser.id,
      'stopped',
      '',
    )
    secondAssistant.blocks = []
    apiMocks.get.mockResolvedValue({
      conversation: apiConversation(secondAssistant.id),
      messages: [secondUser, secondAssistant],
      has_more: false,
      next_before: undefined,
    })

    const firstSend = useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'original question',
      modelId: 'model_1',
      toolMode: 'auto',
    })
    await vi.waitFor(() => {
      expect(streams).toHaveLength(1)
      expect(
        useConversations
          .getState()
          .conversations[0].messages.some((message) => message.id === 'msg_server_first'),
      ).toBe(true)
      expect(streams[0].release).toBeTypeOf('function')
    })

    const secondSend = useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'edited question',
      modelId: 'model_1',
      branch: true,
      toolMode: 'auto',
    })
    await vi.waitFor(() => {
      expect(streams).toHaveLength(2)
      expect(
        useConversations
          .getState()
          .conversations[0].messages.some((message) => message.id === 'msg_server_second'),
      ).toBe(true)
      expect(streams[1].release).toBeTypeOf('function')
    })

    try {
      const firstGenerationId = streams[0].body.generation_id
      const secondGenerationId = streams[1].body.generation_id
      expect(firstGenerationId).toEqual(expect.any(String))
      expect(secondGenerationId).toEqual(expect.any(String))
      expect(secondGenerationId).not.toBe(firstGenerationId)

      useConversations.getState().abortStream('msg_server_second')

      expect(apiMocks.stop).toHaveBeenCalledTimes(1)
      expect(apiMocks.stop).toHaveBeenCalledWith('conv_stop', {
        generation_id: secondGenerationId,
      })
      expect(streams[1].signal.aborted).toBe(true)
      expect(streams[0].signal.aborted).toBe(false)

      await secondSend
    } finally {
      for (const stream of streams) stream.release?.()
      await Promise.allSettled([firstSend, secondSend])
    }
  })

  it('maps an empty stopped API assistant to a deliberate stopped state without an error', () => {
    const stopped = apiMessage(
      'msg_empty_stopped',
      'assistant',
      'msg_stopped_user',
      'stopped',
      '',
    )
    stopped.blocks = []

    const local = toLocalMessage(stopped)

    expect(local.content).toBe('')
    expect(local.streaming).toBe(false)
    expect((local as Message & { stopped?: boolean }).stopped).toBe(true)
    expect(local.error).toBeUndefined()
  })

  it('keeps the interruption marker and partial answer from a normal send', async () => {
    apiMocks.streamSSE.mockReturnValue(
      events(
        { type: 'message_start', message_id: 'msg_interrupted_send' },
        { type: 'tool_start', id: 'tool_interrupted', name: 'web_search' },
        { type: 'text_delta', text: 'partial answer' },
        {
          type: 'error',
          message: 'The model provider returned an error.',
        },
      ),
    )

    await useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'question',
      modelId: 'model_1',
      toolMode: 'auto',
    })

    expect(useConversations.getState().conversations[0].messages.at(-1)).toMatchObject({
      id: 'msg_interrupted_send',
      content: 'partial answer',
      streaming: false,
      error: 'The model provider returned an error.',
      errorCode: 'generation_interrupted',
      reasoning: [
        expect.objectContaining({
          kind: 'tool',
          tool: expect.objectContaining({ id: 'tool_interrupted', status: 'error' }),
        }),
      ],
    })
    expect(apiMocks.get).not.toHaveBeenCalled()
  })

  it('replays a detached generation when the POST stream ends without a terminal frame', async () => {
    apiMocks.streamSSE.mockReturnValue(
      events(
        { type: 'message_start', message_id: 'msg_server_assistant' },
        { type: 'text_delta', text: 'partial answer' },
      ),
    )
    apiMocks.streamSSEGet.mockReturnValue(
      events(
        { type: 'message_start', message_id: 'msg_server_assistant' },
        { type: 'text_delta', text: 'recovered answer' },
        { type: 'done' },
      ),
    )
    // The replay itself is sufficient to settle the UI. A temporarily failed
    // canonical-path refresh must not turn the recovered answer into an error.
    apiMocks.get.mockRejectedValue(new Error('temporary refresh failure'))

    await useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'question with a phone image',
      modelId: 'model_1',
      toolMode: 'auto',
    })

    await vi.waitFor(() => {
      expect(useConversations.getState().conversations[0].messages.at(-1)).toMatchObject({
        id: 'msg_server_assistant',
        content: 'recovered answer',
        streaming: false,
      })
    })
    expect(useConversations.getState().conversations[0].messages.at(-1)?.errorCode).toBeUndefined()
    expect(apiMocks.streamSSEGet).toHaveBeenCalledOnce()
  })

  it('uses structured interruption state for regeneration without appending English markdown', async () => {
    const sourceUser: Message = {
      id: 'msg_interrupted_regen_user',
      role: 'user',
      content: 'question',
      createdAt: 1,
    }
    const sourceAssistant: Message = {
      id: 'msg_interrupted_regen_source',
      parentId: sourceUser.id,
      role: 'assistant',
      content: 'old answer',
      createdAt: 2,
    }
    resetStore([sourceUser, sourceAssistant])
    apiMocks.streamSSE.mockReturnValue(
      events(
        { type: 'message_start', message_id: 'msg_interrupted_regen' },
        { type: 'text_delta', text: 'partial new answer' },
        {
          type: 'error',
          message: 'The model provider returned an error.',
          code: 'generation_interrupted',
        },
      ),
    )

    await useConversations
      .getState()
      .regenerate('conv_stop', sourceAssistant.id, 'model_1')

    const regenerated = useConversations.getState().conversations[0].messages.at(-1)
    expect(regenerated).toMatchObject({
      id: 'msg_interrupted_regen',
      content: 'partial new answer',
      streaming: false,
      error: 'The model provider returned an error.',
      errorCode: 'generation_interrupted',
    })
    expect(regenerated?.content).not.toContain('Regeneration failed')
    expect(regenerated?.content).not.toContain('Regeneration interrupted')
    expect(apiMocks.get).not.toHaveBeenCalled()
  })

  it('applies RAG lifecycle events while regenerating and preserves source counts', async () => {
    const sourceUser: Message = {
      id: 'msg_rag_regen_user',
      role: 'user',
      content: 'question',
      createdAt: 1,
    }
    const sourceAssistant: Message = {
      id: 'msg_rag_regen_source',
      parentId: sourceUser.id,
      role: 'assistant',
      content: 'old answer',
      createdAt: 2,
    }
    resetStore([sourceUser, sourceAssistant])
    apiMocks.streamSSE.mockReturnValue(
      events(
        { type: 'message_start', message_id: 'msg_rag_regenerated' },
        {
          type: 'rag',
          status: 'found',
          summary: 'Found 3 sources',
          source_count: 3,
        },
        { type: 'error', message: 'test stream end' },
      ),
    )

    await useConversations
      .getState()
      .regenerate('conv_stop', sourceAssistant.id, 'model_1')

    expect(useConversations.getState().conversations[0].messages.at(-1)).toMatchObject({
      id: 'msg_rag_regenerated',
      ragInjection: {
        strategy: 'found',
        summary: 'Found 3 sources',
        sourceCount: 3,
      },
    })
    expect(apiMocks.get).not.toHaveBeenCalled()
  })

  it('keeps the interruption marker when a resumed stream replays an error', async () => {
    resetStore([
      {
        id: 'msg_replay_user',
        role: 'user',
        content: 'question',
        createdAt: 1,
      },
      {
        id: 'msg_replay_interrupted',
        parentId: 'msg_replay_user',
        role: 'assistant',
        content: 'partial replayed answer',
        createdAt: 2,
        streaming: true,
      },
    ])
    apiMocks.streamSSEGet.mockReturnValue(
      oneEvent({
        type: 'error',
        message: 'The model provider returned an error.',
        code: 'generation_interrupted',
      }),
    )

    useConversations.getState().resumeStreamingMessages('conv_stop')

    await vi.waitFor(() => {
      expect(useConversations.getState().conversations[0].messages.at(-1)).toMatchObject({
        id: 'msg_replay_interrupted',
        content: 'partial replayed answer',
        streaming: false,
        errorCode: 'generation_interrupted',
      })
    })
  })

  it('rehydrates a persisted generation interruption after refresh', () => {
    const interrupted = apiMessage(
      'msg_reloaded_interrupted',
      'assistant',
      'msg_reloaded_user',
      'error',
      'persisted partial answer',
    )
    interrupted.stop_reason = 'generation_interrupted'
    interrupted.error = 'The model provider returned an error.'
    interrupted.blocks = [
      { kind: 'tool_call', tool_id: 'tool_reloaded_interrupted', tool_name: 'web_search' },
      { kind: 'text', text: 'persisted partial answer' },
    ]

    expect(toLocalMessage(interrupted)).toMatchObject({
      content: 'persisted partial answer',
      error: 'The model provider returned an error.',
      errorCode: 'generation_interrupted',
      reasoning: [
        expect.objectContaining({
          kind: 'tool',
          tool: expect.objectContaining({ id: 'tool_reloaded_interrupted', status: 'error' }),
        }),
      ],
    })
  })

  it('redacts transport details from a persisted failed tool result', () => {
    const assistant = apiMessage(
      'msg_reloaded_tool_error',
      'assistant',
      'msg_reloaded_user',
      'complete',
      '',
    )
    const raw = 'Error: Post "https://images.internal.example.test/v1/images/edits": context canceled'
    assistant.blocks = [
      {
        kind: 'tool_call',
        tool_id: 'tool_reloaded_error',
        tool_name: 'image_generate',
        summary: raw,
      },
      {
        kind: 'tool_output',
        tool_id: 'tool_reloaded_error',
        tool_name: 'image_generate',
        text: raw,
        summary: 'error',
      },
    ]

    const local = toLocalMessage(assistant)
    expect(local.reasoning?.[0]).toMatchObject({
      kind: 'tool',
      tool: {
        id: 'tool_reloaded_error',
        status: 'error',
        timingKnown: false,
        output: 'The operation was canceled.',
      },
    })
    expect(JSON.stringify(local)).not.toContain('images.internal.example.test')
    expect(JSON.stringify(local)).not.toContain('/v1/images/edits')
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

  it('serializes only the unified three-state tool policy', async () => {
    useModels.setState({
      models: [
        { id: 'model_1', tools_available: true } as ApiModel,
      ],
      defaultId: 'model_1',
    })
    useComposerPrefs.setState({ toolMode: 'enabled' })
    const requestBodies: Array<Record<string, unknown>> = []
    apiMocks.streamSSE.mockImplementation((_path: string, body: Record<string, unknown>) => {
      requestBodies.push(body)
      return oneEvent({ type: 'error', message: 'test terminal error' })
    })

    await useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'use configured tools',
      modelId: 'model_1',
      toolMode: 'enabled',
    })

    resetStore()
    await useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'disable tools',
      modelId: 'model_1',
      toolMode: 'disabled',
    })

    resetStore()
    useComposerPrefs.setState({ toolMode: 'auto' })
    await useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'automatically choose tools',
      modelId: 'model_1',
      toolMode: 'auto',
    })

    expect(requestBodies).toHaveLength(3)
    expect(requestBodies[0]).toMatchObject({
      model_id: 'model_1',
      tool_mode: 'enabled',
    })
    expect(requestBodies[1]).toMatchObject({
      model_id: 'model_1',
      tool_mode: 'disabled',
    })
    expect(requestBodies[2]).toMatchObject({
      model_id: 'model_1',
      tool_mode: 'auto',
    })
  })

  it('preserves omitted, empty, and non-empty tool subsets on normal sends', async () => {
    const requestBodies: Array<Record<string, unknown>> = []
    apiMocks.streamSSE.mockImplementation((_path: string, body: Record<string, unknown>) => {
      requestBodies.push(body)
      return oneEvent({ type: 'error', message: 'test terminal error' })
    })

    await useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'use the default tool catalog',
      modelId: 'model_1',
      toolMode: 'auto',
    })

    resetStore()
    await useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'use no candidate tools',
      modelId: 'model_1',
      toolMode: 'auto',
      selectedToolIds: [],
    })

    resetStore()
    await useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'use this tool subset',
      modelId: 'model_1',
      toolMode: 'enabled',
      selectedToolIds: ['builtin:web_fetch', 'mcp:rail'],
    })

    const wireBodies = requestBodies.map((body) => JSON.parse(JSON.stringify(body)) as Record<string, unknown>)
    expect(wireBodies[0]).not.toHaveProperty('selected_tool_ids')
    expect(wireBodies[1].selected_tool_ids).toEqual([])
    expect(wireBodies[2].selected_tool_ids).toEqual(['builtin:web_fetch', 'mcp:rail'])
  })

  it('preserves an explicit empty tool selection when regenerating', async () => {
    const sourceUser: Message = {
      id: 'msg_tool_subset_user',
      role: 'user',
      content: 'question',
      createdAt: 1,
    }
    const sourceAssistant: Message = {
      id: 'msg_tool_subset_assistant',
      parentId: sourceUser.id,
      role: 'assistant',
      content: 'old answer',
      createdAt: 2,
    }
    resetStore([sourceUser, sourceAssistant])
    useComposerPrefs.setState({
      toolMode: 'auto',
      selectedToolIdsByModel: { model_1: [] },
    })
    let requestBody: Record<string, unknown> | undefined
    apiMocks.streamSSE.mockImplementation((_path: string, body: Record<string, unknown>) => {
      requestBody = body
      return events(
        { type: 'message_start', message_id: 'msg_tool_subset_regenerated' },
        { type: 'error', message: 'test terminal error' },
      )
    })

    await useConversations
      .getState()
      .regenerate('conv_stop', sourceAssistant.id, 'model_1')

    expect(requestBody?.selected_tool_ids).toEqual([])
  })
})
describe('knowledge-base selection ordering', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetStore()
    apiMocks.stop.mockResolvedValue({ ok: true })
    apiMocks.get.mockResolvedValue({
      conversation: apiConversation(''),
      messages: [],
      has_more: false,
      next_before: undefined,
    })
    apiMocks.streamSSE.mockReturnValue(events({ type: 'done' }))
  })

  it('keeps and persists an optimistic first-chat selection after the real id is created', async () => {
    useConversations.setState({ conversations: [] })
    const created = { ...apiConversation(''), id: 'conv_real', active_leaf_id: '' }
    apiMocks.create.mockResolvedValue(created)
    apiMocks.update.mockResolvedValue({ ...created, kb_ids: ['kb_a', 'kb_b'] })
    apiMocks.get.mockResolvedValue({
      conversation: { ...created, kb_ids: ['kb_a', 'kb_b'] },
      messages: [],
      has_more: false,
      next_before: undefined,
    })

    const tempId = useConversations
      .getState()
      .beginOptimisticConversation('use the sources', 'model_1', false, [
        ' kb_a ',
        'kb_a',
        'kb_b',
      ])

    expect(
      useConversations.getState().conversations.find((conversation) => conversation.id === tempId)
        ?.kbIds,
    ).toEqual(['kb_a', 'kb_b'])

    await useConversations.getState().sendMessage({
      conversationId: tempId,
      createFirst: true,
      text: 'use the sources',
      modelId: 'model_1',
      toolMode: 'auto',
    })

    expect(apiMocks.update).toHaveBeenCalledWith('conv_real', {
      kb_ids: ['kb_a', 'kb_b'],
    })
    expect(apiMocks.update).not.toHaveBeenCalledWith(tempId, expect.anything())
    expect(apiMocks.streamSSE).toHaveBeenCalledWith(
      '/conversations/conv_real/messages',
      expect.objectContaining({ kb_ids: ['kb_a', 'kb_b'] }),
      expect.any(AbortSignal),
    )
    expect(
      useConversations.getState().conversations.find((conversation) => conversation.id === 'conv_real')
        ?.kbIds,
    ).toEqual(['kb_a', 'kb_b'])
  })

  it('keeps a post-send KB change local until createFirst re-keys the conversation', async () => {
    useConversations.setState({ conversations: [] })
    let resolveCreate: ((conversation: ApiConversation) => void) | undefined
    apiMocks.create.mockReturnValue(
      new Promise<ApiConversation>((resolve) => {
        resolveCreate = resolve
      }),
    )
    const created = { ...apiConversation(''), id: 'conv_real', active_leaf_id: '' }
    apiMocks.update.mockResolvedValue({ ...created, kb_ids: ['kb_next'] })
    apiMocks.get.mockResolvedValue({
      conversation: { ...created, kb_ids: ['kb_next'] },
      messages: [],
      has_more: false,
      next_before: undefined,
    })

    const tempId = useConversations
      .getState()
      .beginOptimisticConversation('use the original source', 'model_1', false, ['kb_turn'])
    const sending = useConversations.getState().sendMessage({
      conversationId: tempId,
      createFirst: true,
      text: 'use the original source',
      modelId: 'model_1',
      toolMode: 'auto',
    })

    await vi.waitFor(() => expect(apiMocks.create).toHaveBeenCalledTimes(1))
    await useConversations.getState().setKBs(tempId, ['kb_next'])
    expect(apiMocks.update).not.toHaveBeenCalled()

    resolveCreate?.(created)
    await sending

    expect(apiMocks.update).toHaveBeenCalledWith('conv_real', { kb_ids: ['kb_next'] })
    expect(apiMocks.update).not.toHaveBeenCalledWith(tempId, expect.anything())
    expect(apiMocks.streamSSE).toHaveBeenCalledWith(
      '/conversations/conv_real/messages',
      expect.objectContaining({ kb_ids: ['kb_turn'] }),
      expect.any(AbortSignal),
    )
    expect(
      useConversations.getState().conversations.find((conversation) => conversation.id === 'conv_real')
        ?.kbIds,
    ).toEqual(['kb_next'])
  })

  it('serializes rapid selection PATCHes while sending the latest click as the turn snapshot', async () => {
    let resolveFirst: ((value: ApiConversation) => void) | undefined
    apiMocks.update
      .mockReturnValueOnce(
        new Promise<ApiConversation>((resolve) => {
          resolveFirst = resolve
        }),
      )
      .mockResolvedValueOnce({
        ...apiConversation(''),
        kb_ids: ['kb_a', 'kb_b'],
      })

    const first = useConversations.getState().setKBs('conv_stop', ['kb_a'])
    const second = useConversations.getState().setKBs('conv_stop', ['kb_a', 'kb_b'])

    await vi.waitFor(() => expect(apiMocks.update).toHaveBeenCalledTimes(1))
    expect(apiMocks.update).toHaveBeenNthCalledWith(1, 'conv_stop', {
      kb_ids: ['kb_a'],
    })

    await useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'use the selected sources',
      modelId: 'model_1',
      toolMode: 'auto',
    })

    expect(apiMocks.streamSSE).toHaveBeenCalledWith(
      '/conversations/conv_stop/messages',
      expect.objectContaining({ kb_ids: ['kb_a', 'kb_b'] }),
      expect.any(AbortSignal),
    )

    resolveFirst?.({ ...apiConversation(''), kb_ids: ['kb_a'] })
    await first
    await second

    expect(apiMocks.update).toHaveBeenCalledTimes(2)
    expect(apiMocks.update).toHaveBeenNthCalledWith(2, 'conv_stop', {
      kb_ids: ['kb_a', 'kb_b'],
    })
    expect(useConversations.getState().conversations[0].kbIds).toEqual(['kb_a', 'kb_b'])
  })

  it('sends an explicit empty snapshot when a selected KB is removed before its PATCH settles', async () => {
    resetStore()
    useConversations.setState((state) => ({
      conversations: state.conversations.map((conversation) => ({
        ...conversation,
        kbIds: ['kb_old'],
      })),
    }))

    let resolveUpdate: ((value: ApiConversation) => void) | undefined
    apiMocks.update.mockReturnValue(
      new Promise<ApiConversation>((resolve) => {
        resolveUpdate = resolve
      }),
    )
    const saving = useConversations.getState().setKBs('conv_stop', [])
    await vi.waitFor(() => expect(apiMocks.update).toHaveBeenCalledTimes(1))

    await useConversations.getState().sendMessage({
      conversationId: 'conv_stop',
      text: 'continue without the library',
      modelId: 'model_1',
      toolMode: 'auto',
    })

    expect(apiMocks.streamSSE).toHaveBeenCalledWith(
      '/conversations/conv_stop/messages',
      expect.objectContaining({ kb_ids: [] }),
      expect.any(AbortSignal),
    )

    resolveUpdate?.({ ...apiConversation(''), kb_ids: [] })
    await saving
    expect(useConversations.getState().conversations[0].kbIds).toEqual([])
  })

  it('rolls back only KB state when saving fails, preserving newer conversation changes', async () => {
    resetStore()
    useConversations.setState((state) => ({
      conversations: state.conversations.map((conversation) => ({
        ...conversation,
        kbIds: ['kb_committed'],
      })),
    }))

    let rejectUpdate: ((reason: Error) => void) | undefined
    apiMocks.update.mockReturnValue(
      new Promise<ApiConversation>((_resolve, reject) => {
        rejectUpdate = reject
      }),
    )
    const saving = useConversations.getState().setKBs('conv_stop', ['kb_new'])
    await vi.waitFor(() => expect(apiMocks.update).toHaveBeenCalledTimes(1))
    useConversations.setState((state) => ({
      conversations: state.conversations.map((conversation) =>
        conversation.id === 'conv_stop' ? { ...conversation, title: 'Changed while saving' } : conversation,
      ),
    }))

    rejectUpdate?.(new Error('network unavailable'))
    await saving

    expect(useConversations.getState().conversations[0]).toMatchObject({
      title: 'Changed while saving',
      kbIds: ['kb_committed'],
    })
    expect(apiMocks.toastError).toHaveBeenCalledTimes(1)
  })

  it('does not let a stale conversation reload overwrite a pending selection', async () => {
    let resolveUpdate: ((value: ApiConversation) => void) | undefined
    apiMocks.update.mockReturnValue(
      new Promise<ApiConversation>((resolve) => {
        resolveUpdate = resolve
      }),
    )
    apiMocks.get.mockResolvedValue({
      conversation: { ...apiConversation(''), kb_ids: [] },
      messages: [],
      has_more: false,
      next_before: undefined,
    })

    const saving = useConversations.getState().setKBs('conv_stop', ['kb_pending'])
    await vi.waitFor(() => expect(apiMocks.update).toHaveBeenCalledTimes(1))
    await useConversations.getState().loadOne('conv_stop')

    expect(useConversations.getState().conversations[0].kbIds).toEqual(['kb_pending'])

    resolveUpdate?.({ ...apiConversation(''), kb_ids: ['kb_pending'] })
    await saving
  })

  it('does not let a reload issued before the selection overwrite it after PATCH settles', async () => {
    let resolveGet: ((value: ReturnType<typeof pathResponse>) => void) | undefined
    apiMocks.get.mockReturnValue(
      new Promise<ReturnType<typeof pathResponse>>((resolve) => {
        resolveGet = resolve
      }),
    )
    apiMocks.update.mockReset().mockResolvedValue({
      ...apiConversation(''),
      kb_ids: ['kb_confirmed'],
    })

    const loading = useConversations.getState().loadOne('conv_stop')
    await vi.waitFor(() => expect(apiMocks.get).toHaveBeenCalledTimes(1))

    await useConversations.getState().setKBs('conv_stop', ['kb_confirmed'])
    expect(useConversations.getState().conversations[0].kbIds).toEqual(['kb_confirmed'])

    resolveGet?.({
      conversation: { ...apiConversation(''), kb_ids: [] },
      messages: [],
      has_more: false,
      next_before: undefined,
    })
    await loading

    expect(useConversations.getState().conversations[0].kbIds).toEqual(['kb_confirmed'])
  })
})

describe('optimistic first-conversation preparation', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useConversations.setState({
      conversations: [],
      loaded: true,
      loading: false,
      loadingMore: false,
      hasMore: false,
      error: null,
    })
    apiMocks.stop.mockResolvedValue({ ok: true })
    apiMocks.inlineThreads.mockResolvedValue([])
    apiMocks.streamSSE.mockReturnValue(events({ type: 'error', message: 'test complete' }))
  })

  it('shows the optimistic turn while an attachment-owned conversation is unresolved', async () => {
    let resolvePrepared: ((conversation: ApiConversation) => void) | undefined
    const preparedConversation = new Promise<ApiConversation>((resolve) => {
      resolvePrepared = resolve
    })
    const prepared = {
      ...apiConversation(''),
      id: 'conv_attachment_draft',
      active_leaf_id: '',
    }
    apiMocks.update.mockResolvedValue(prepared)

    const tempId = useConversations
      .getState()
      .beginOptimisticConversation('read the attachment', 'model_1', false)
    const sending = useConversations.getState().sendMessage({
      conversationId: tempId,
      createFirst: true,
      preparedConversation,
      text: 'read the attachment',
      modelId: 'model_1',
      toolMode: 'auto',
      attachments: [
        {
          id: 'file_1',
          name: 'paper.pdf',
          kind: 'pdf',
          size: 1024,
          documentId: 'doc_1',
        },
      ],
    })

    expect(
      useConversations
        .getState()
        .conversations.find((conversation) => conversation.id === tempId)
        ?.messages.map((message) => message.role),
    ).toEqual(['user', 'assistant'])
    expect(apiMocks.create).not.toHaveBeenCalled()
    expect(apiMocks.streamSSE).not.toHaveBeenCalled()

    resolvePrepared?.(prepared)
    await sending

    expect(apiMocks.create).not.toHaveBeenCalled()
    expect(apiMocks.streamSSE).toHaveBeenCalledWith(
      '/conversations/conv_attachment_draft/messages',
      expect.objectContaining({
        attachments: [expect.objectContaining({ id: 'file_1', document_id: 'doc_1' })],
      }),
      expect.any(AbortSignal),
    )
    expect(
      useConversations
        .getState()
        .conversations.some((conversation) => conversation.id === 'conv_attachment_draft'),
    ).toBe(true)
    expect(useConversations.getState().conversations.some((conversation) => conversation.id === tempId)).toBe(false)
  })

  it('does not fetch a client-only temporary route from the server', async () => {
    const tempId = useConversations
      .getState()
      .beginOptimisticConversation('open immediately', 'model_1', false)

    await useConversations.getState().loadOne(tempId)
    await useConversations.getState().loadInlineThreads(tempId)

    expect(apiMocks.get).not.toHaveBeenCalled()
    expect(apiMocks.inlineThreads).not.toHaveBeenCalled()
  })
})
