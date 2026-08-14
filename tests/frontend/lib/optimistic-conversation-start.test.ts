import { describe, expect, it, vi } from 'vitest'
import { enterOptimisticConversation } from '@/lib/optimistic-conversation-start'

describe('enterOptimisticConversation', () => {
  it('navigates before unresolved background work can settle', () => {
    const order: string[] = []
    const neverSettles = new Promise<void>(() => {})
    const navigate = vi.fn((id: string) => order.push(`navigate:${id}`))

    const id = enterOptimisticConversation({
      createConversation: () => {
        order.push('create-local')
        return 'temp-chat'
      },
      beforeNavigate: () => order.push('clear-draft'),
      navigate,
      startBackgroundWork: () => {
        order.push('start-network')
        return neverSettles
      },
    })

    expect(id).toBe('temp-chat')
    expect(order).toEqual([
      'create-local',
      'clear-draft',
      'navigate:temp-chat',
      'start-network',
    ])
    expect(navigate).toHaveBeenCalledWith('temp-chat')
  })
})
