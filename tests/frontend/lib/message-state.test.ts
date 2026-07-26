import { describe, expect, it } from 'vitest'
import {
  isEmptyStoppedMessage,
  messageHasActions,
  protectedFirstRoundMessageIds,
} from '@/lib/message-state'
import type { Conversation, Message } from '@/types/chat'

function message(patch: Partial<Message>): Message {
  return {
    id: patch.id ?? 'message',
    role: patch.role ?? 'assistant',
    content: patch.content ?? '',
    createdAt: patch.createdAt ?? 1,
    ...patch,
  }
}

function conversation(messages: Message[]): Conversation {
  return {
    id: 'conversation',
    title: 'Branches',
    modelId: 'model',
    createdAt: 1,
    updatedAt: 1,
    messages,
  }
}

describe('stopped message state', () => {
  it('keeps a pre-token stop visible and actionable without treating partial output as empty', () => {
    const emptyStopped = message({ stopped: true })
    const partialStopped = message({ stopped: true, content: 'Partial answer' })

    expect(isEmptyStoppedMessage(emptyStopped)).toBe(true)
    expect(messageHasActions(emptyStopped)).toBe(true)
    expect(isEmptyStoppedMessage(partialStopped)).toBe(false)
    expect(messageHasActions(partialStopped)).toBe(true)
  })

  it('keeps citations visible and withholds API actions until an optimistic stop is persisted', () => {
    const citedStop = message({
      stopped: true,
      citations: [{ id: 'citation', index: 1, title: 'Source', url: 'https://example.com', domain: 'example.com' }],
    })
    const optimisticStop = message({ stopped: true, localOnly: true })

    expect(isEmptyStoppedMessage(citedStop)).toBe(false)
    expect(messageHasActions(citedStop)).toBe(true)
    expect(messageHasActions(optimisticStop)).toBe(false)
  })
})

describe('opening-round deletion protection', () => {
  it('protects the original root exchange', () => {
    const root = message({ id: 'root', role: 'user', branchIndex: 0, branchCount: 2 })
    const answer = message({ id: 'answer', parentId: root.id })

    expect([...protectedFirstRoundMessageIds(conversation([root, answer]))]).toEqual(['root', 'answer'])
  })

  it('allows an edited root sibling and its empty stopped answer to be deleted', () => {
    const editedRoot = message({
      id: 'edited-root',
      role: 'user',
      branchIndex: 1,
      branchCount: 2,
      siblings: ['root', 'edited-root'],
    })
    const stopped = message({ id: 'stopped-answer', parentId: editedRoot.id, stopped: true })

    expect(protectedFirstRoundMessageIds(conversation([editedRoot, stopped])).size).toBe(0)
  })

  it('allows one regenerated opening answer to be deleted but protects the final answer', () => {
    const root = message({ id: 'root', role: 'user', branchIndex: 0, branchCount: 1 })
    const regenerated = message({ id: 'answer-2', parentId: root.id, branchIndex: 1, branchCount: 2 })
    const onlyAnswer = message({ id: 'answer-only', parentId: root.id, branchIndex: 0, branchCount: 1 })

    expect([...protectedFirstRoundMessageIds(conversation([root, regenerated]))]).toEqual(['root'])
    expect([...protectedFirstRoundMessageIds(conversation([root, onlyAnswer]))]).toEqual([
      'root',
      'answer-only',
    ])
  })
})
