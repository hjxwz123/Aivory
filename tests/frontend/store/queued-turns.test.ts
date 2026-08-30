import { beforeEach, describe, expect, it } from 'vitest'

import { useQueuedTurns, type QueuedTurnInput } from '@/store/queued-turns'

function turnInput(conversationId = 'conv_1'): QueuedTurnInput {
  return {
    conversationId,
    text: 'Follow up after this answer',
    modelId: 'model_1',
    attachments: [
      {
        id: 'file_1',
        name: 'notes.pdf',
        kind: 'pdf',
        size: 128,
        documentId: 'doc_1',
      },
    ],
    params: { temperature: 0.3 },
    toolMode: 'auto',
    selectedUserSkillIds: ['skill_1'],
    selectedToolIds: ['builtin:web'],
  }
}

describe('queued turns store', () => {
  beforeEach(() => {
    useQueuedTurns.setState({ turnsByConversation: {} })
  })

  it('keeps one isolated queued turn per conversation', () => {
    const first = useQueuedTurns.getState().enqueue(turnInput('conv_1'))
    const duplicate = useQueuedTurns.getState().enqueue({
      ...turnInput('conv_1'),
      text: 'This must stay in the composer',
    })
    const other = useQueuedTurns.getState().enqueue(turnInput('conv_2'))

    expect(first?.text).toBe('Follow up after this answer')
    expect(duplicate).toBeUndefined()
    expect(other?.conversationId).toBe('conv_2')
    expect(Object.keys(useQueuedTurns.getState().turnsByConversation)).toEqual([
      'conv_1',
      'conv_2',
    ])
  })

  it('freezes attachment, parameter, skill, and tool selections at enqueue time', () => {
    const input = turnInput()
    const queued = useQueuedTurns.getState().enqueue(input)

    input.attachments[0].name = 'changed.pdf'
    if (input.params) input.params.temperature = 1
    input.selectedUserSkillIds?.push('skill_2')
    input.selectedToolIds?.push('builtin:python')

    expect(queued).toMatchObject({
      attachments: [{ name: 'notes.pdf' }],
      params: { temperature: 0.3 },
      selectedUserSkillIds: ['skill_1'],
      selectedToolIds: ['builtin:web'],
    })
  })

  it('prevents withdrawal while dispatching and restores it if sending never starts', () => {
    const queued = useQueuedTurns.getState().enqueue(turnInput())
    const dispatching = useQueuedTurns.getState().beginDispatch('conv_1')

    expect(dispatching).toMatchObject({ id: queued?.id, status: 'dispatching' })
    expect(useQueuedTurns.getState().withdraw('conv_1')).toBeUndefined()

    useQueuedTurns.getState().release('conv_1', dispatching!.id)
    const withdrawn = useQueuedTurns.getState().withdraw('conv_1')

    expect(withdrawn).toMatchObject({ id: queued?.id, status: 'queued' })
    expect(useQueuedTurns.getState().turnsByConversation).toEqual({})
  })

  it('removes only the matching turn once optimistic sending starts', () => {
    const queued = useQueuedTurns.getState().enqueue(turnInput())!
    useQueuedTurns.getState().beginDispatch('conv_1')

    useQueuedTurns.getState().markStarted('conv_1', 'another_turn')
    expect(useQueuedTurns.getState().turnsByConversation.conv_1).toBeDefined()

    useQueuedTurns.getState().markStarted('conv_1', queued.id)
    expect(useQueuedTurns.getState().turnsByConversation.conv_1).toBeUndefined()
  })
})
