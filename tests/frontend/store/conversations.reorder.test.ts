import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Conversation } from '@/types/chat'

const mocks = vi.hoisted(() => ({ reorder: vi.fn(), error: vi.fn() }))
vi.mock('@/api', () => ({
  ApiError: class extends Error {},
  conversationsApi: { reorder: mocks.reorder },
  streamSSE: vi.fn(), streamSSEGet: vi.fn(),
}))
vi.mock('@/hooks/use-toast', () => ({ toast: { error: mocks.error } }))

import { useConversations } from '@/store/conversations'
import { useWorkspaces } from '@/store/workspaces'

function conversation(id: string, updatedAt: number): Conversation {
  return { id, title: id, modelId: '', createdAt: 1000, updatedAt, messages: [{ id: `message-${id}`, role: 'user', content: id, createdAt: 1000 }] }
}

describe('persisting sidebar conversation order', () => {
  beforeEach(() => {
    mocks.reorder.mockReset()
    mocks.error.mockReset()
    useWorkspaces.setState({ activeId: null })
    useConversations.setState({ conversations: [conversation('a', 300000), conversation('b', 200000), conversation('c', 100000)] })
  })

  it('uses server timestamps without replacing messages or concurrent edits', async () => {
    let resolve!: (value: { conversations: { id: string; updated_at: number }[] }) => void
    mocks.reorder.mockReturnValue(new Promise((done) => { resolve = done }))
    const request = useConversations.getState().reorderConversation('c', 'a', 'before')
    const before = useConversations.getState().conversations
    useConversations.setState({ conversations: before.map((row) => row.id === 'c' ? { ...row, title: 'edited while saving' } : row) })
    resolve({ conversations: [{ id: 'c', updated_at: 400 }, { id: 'a', updated_at: 399 }] })
    expect(await request).toBe(true)
    expect(mocks.reorder).toHaveBeenCalledWith('c', 'a', 'before')
    const after = useConversations.getState().conversations
    expect(after.find((row) => row.id === 'c')).toMatchObject({ updatedAt: 400000, title: 'edited while saving' })
    expect(after.find((row) => row.id === 'c')?.messages).toBe(before[2].messages)
    expect(after.find((row) => row.id === 'a')?.updatedAt).toBe(399000)
    expect(after[1]).toBe(before[1])
  })

  it('keeps the original order on a failed save', async () => {
    mocks.reorder.mockRejectedValue(new Error('offline'))
    const original = useConversations.getState().conversations
    expect(await useConversations.getState().reorderConversation('c', 'a', 'before')).toBe(false)
    expect(useConversations.getState().conversations).toBe(original)
    expect(mocks.error).toHaveBeenCalledOnce()
  })

  it.each(['workspace', 'project', 'starred', 'archived', 'inline', 'streaming'])('rejects an incompatible %s move before sending', async (scope) => {
    const original = useConversations.getState().conversations
    useConversations.setState({ conversations: original.map((row) => row.id !== 'b' ? row : {
      ...row,
      ...(scope === 'workspace' ? { workspaceId: 'other-space' } : {}),
      ...(scope === 'project' ? { projectId: 'project' } : {}),
      ...(scope === 'starred' ? { starred: true } : {}),
      ...(scope === 'archived' ? { archived: true } : {}),
      ...(scope === 'inline' ? { inline: { sourceConvId: 'a', messageId: 'message-a', quote: 'text' } } : {}),
      ...(scope === 'streaming' ? { messages: [{ ...row.messages[0], streaming: true }] } : {}),
    }) })
    expect(await useConversations.getState().reorderConversation('a', 'b', 'after')).toBe(false)
    expect(mocks.reorder).not.toHaveBeenCalled()
  })

  it('does not apply a response to a newly selected workspace', async () => {
    let resolve!: (value: { conversations: { id: string; updated_at: number }[] }) => void
    mocks.reorder.mockReturnValue(new Promise((done) => { resolve = done }))
    const request = useConversations.getState().reorderConversation('c', 'a', 'before')
    useWorkspaces.setState({ activeId: 'another-workspace' })
    const next = [conversation('next', 120000)]
    useConversations.setState({ conversations: next })
    resolve({ conversations: [{ id: 'c', updated_at: 400 }] })
    expect(await request).toBe(true)
    expect(useConversations.getState().conversations).toBe(next)
  })

  it('orders starred and unstarred conversations together inside a project', async () => {
    useConversations.setState({ conversations: [
      { ...conversation('a', 300000), projectId: 'project', starred: true },
      { ...conversation('b', 200000), projectId: 'project' },
    ] })
    mocks.reorder.mockResolvedValue({ conversations: [{ id: 'b', updated_at: 400 }] })
    expect(await useConversations.getState().reorderConversation('b', 'a', 'before')).toBe(true)
    expect(mocks.reorder).toHaveBeenCalledWith('b', 'a', 'before')
  })
})
