import { create } from 'zustand'

import type { ToolMode } from '@/lib/tool-mode'
import { uid } from '@/lib/utils'
import type { Attachment } from '@/types/chat'

export interface QueuedTurnInput {
  conversationId: string
  text: string
  modelId?: string
  attachments: Attachment[]
  mode?: 'default' | 'deep-research' | 'canvas'
  params?: Record<string, unknown>
  imageStyleId?: string
  optimizeImagePrompt?: boolean
  verify?: boolean
  toolMode: ToolMode
  webSearch?: boolean
  selectedUserSkillIds?: string[]
  selectedToolIds?: string[]
  fast?: boolean
}

export interface QueuedTurn extends QueuedTurnInput {
  id: string
  status: 'queued' | 'dispatching'
}

interface QueuedTurnsStore {
  turnsByConversation: Record<string, QueuedTurn>
  enqueue: (input: QueuedTurnInput) => QueuedTurn | undefined
  beginDispatch: (conversationId: string) => QueuedTurn | undefined
  markStarted: (conversationId: string, turnId: string) => void
  release: (conversationId: string, turnId: string) => void
  withdraw: (conversationId: string) => QueuedTurn | undefined
  clear: (conversationId: string) => void
}

function snapshotTurn(input: QueuedTurnInput): QueuedTurn {
  return {
    ...input,
    id: uid('queued'),
    status: 'queued',
    attachments: input.attachments.map((attachment) => ({ ...attachment })),
    params: input.params ? { ...input.params } : undefined,
    selectedUserSkillIds: input.selectedUserSkillIds
      ? [...input.selectedUserSkillIds]
      : undefined,
    selectedToolIds: input.selectedToolIds ? [...input.selectedToolIds] : undefined,
  }
}

/**
 * A single in-memory follow-up slot per conversation. It deliberately is not
 * persisted: queued turns can contain short-lived attachment preview URLs and
 * should never become a second, hidden draft system in browser storage.
 */
export const useQueuedTurns = create<QueuedTurnsStore>((set, get) => ({
  turnsByConversation: {},
  enqueue(input) {
    const conversationId = input.conversationId.trim()
    if (!conversationId || get().turnsByConversation[conversationId]) return undefined
    const turn = snapshotTurn({ ...input, conversationId })
    set((state) => ({
      turnsByConversation: {
        ...state.turnsByConversation,
        [conversationId]: turn,
      },
    }))
    return turn
  },
  beginDispatch(conversationId) {
    const current = get().turnsByConversation[conversationId]
    if (!current || current.status !== 'queued') return undefined
    const dispatching = { ...current, status: 'dispatching' as const }
    set((state) => ({
      turnsByConversation: {
        ...state.turnsByConversation,
        [conversationId]: dispatching,
      },
    }))
    return dispatching
  },
  markStarted(conversationId, turnId) {
    set((state) => {
      const current = state.turnsByConversation[conversationId]
      if (!current || current.id !== turnId) return state
      const turnsByConversation = { ...state.turnsByConversation }
      delete turnsByConversation[conversationId]
      return { turnsByConversation }
    })
  },
  release(conversationId, turnId) {
    set((state) => {
      const current = state.turnsByConversation[conversationId]
      if (!current || current.id !== turnId || current.status !== 'dispatching') return state
      return {
        turnsByConversation: {
          ...state.turnsByConversation,
          [conversationId]: { ...current, status: 'queued' },
        },
      }
    })
  },
  withdraw(conversationId) {
    const current = get().turnsByConversation[conversationId]
    if (!current || current.status !== 'queued') return undefined
    set((state) => {
      const turnsByConversation = { ...state.turnsByConversation }
      delete turnsByConversation[conversationId]
      return { turnsByConversation }
    })
    return current
  },
  clear(conversationId) {
    set((state) => {
      if (!state.turnsByConversation[conversationId]) return state
      const turnsByConversation = { ...state.turnsByConversation }
      delete turnsByConversation[conversationId]
      return { turnsByConversation }
    })
  },
}))
