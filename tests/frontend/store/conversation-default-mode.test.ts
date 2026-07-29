import { beforeEach, describe, expect, it } from 'vitest'
import { useAuth } from '@/store/auth'
import { useConversations } from '@/store/conversations'
import { useModels } from '@/store/models'

function setUserSettings(settings: Record<string, unknown>) {
  useAuth.setState({
    user: {
      id: 'u_test',
      email: 'user@example.test',
      name: 'User',
      role: 'user',
      status: 'active',
      settings,
      created_at: 1,
    },
    status: 'authenticated',
  })
}

describe('optimistic conversation default mode', () => {
  beforeEach(() => {
    useConversations.setState({ conversations: [] })
    useModels.setState({
      defaultId: 'm_global',
      fastAvailable: true,
      loaded: true,
    })
  })

  it('starts fast when the user has no default model', () => {
    setUserSettings({})
    const id = useConversations.getState().beginOptimisticConversation('Hello')
    expect(useConversations.getState().getConversation(id)?.fast).toBe(true)
  })

  it('starts advanced when the user configured a default model', () => {
    setUserSettings({ default_model_id: 'm_user' })
    const id = useConversations.getState().beginOptimisticConversation('Hello')
    expect(useConversations.getState().getConversation(id)?.fast).toBe(false)
  })

  it('honors an explicit picker mode', () => {
    setUserSettings({})
    const id = useConversations.getState().beginOptimisticConversation('Hello', 'm_global', false)
    expect(useConversations.getState().getConversation(id)?.fast).toBe(false)
  })
})
