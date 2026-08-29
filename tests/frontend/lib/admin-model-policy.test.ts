import { describe, expect, it } from 'vitest'
import type { ApiChannel, ApiModel } from '@/api/types'
import {
  availablePolicyModels,
  unavailablePolicyModelIDs,
} from '@/lib/admin-model-policy'

const channels = [
  { id: 'channel-on', enabled: true },
  { id: 'channel-off', enabled: false },
] as ApiChannel[]

const models = [
  { id: 'chat-on', channel_id: 'channel-on', kind: 'chat', enabled: true },
  { id: 'chat-off', channel_id: 'channel-on', kind: 'chat', enabled: false },
  { id: 'chat-channel-off', channel_id: 'channel-off', kind: 'chat', enabled: true },
  { id: 'image-on', channel_id: 'channel-on', kind: 'image', enabled: true },
] as ApiModel[]

describe('admin model policy availability', () => {
  it('only exposes enabled chat models on enabled channels', () => {
    expect(availablePolicyModels(models, channels).map((model) => model.id)).toEqual(['chat-on'])
  })

  it('reports distinct unavailable saved references', () => {
    const available = availablePolicyModels(models, channels)
    expect(unavailablePolicyModelIDs({
      default_model_id: 'chat-on',
      task_model_id: 'chat-off',
      tool_route_model_id: 'chat-channel-off',
      verify_model_id: 'chat-off',
      fallback_model_id: 'missing',
    }, available)).toEqual(['chat-off', 'chat-channel-off', 'missing'])
  })
})
