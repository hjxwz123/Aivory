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
      title_model_id: 'chat-on',
      file_route_model_id: 'missing-file-route',
      tool_route_model_id: 'chat-channel-off',
      verify_model_id: 'chat-off',
      fallback_model_id: 'missing',
    }, available)).toEqual(['chat-off', 'missing-file-route', 'chat-channel-off', 'missing'])
  })
})

it('offers configured decision models only to supported decision policies', () => {
  const decisionChannels = [...channels,
    { id: 'ts', type: 'typesafe', enabled: true, has_api_key: true },
    { id: 'ts-no-key', type: 'typesafe', enabled: true, has_api_key: false },
    { id: 'ts-disabled', type: 'typesafe', enabled: false, has_api_key: true },
  ] as ApiChannel[]
  const decisionModels = [...models,
    { id: 'jev', channel_id: 'ts', kind: 'decision', enabled: true },
    { id: 'jev-no-key', channel_id: 'ts-no-key', kind: 'decision', enabled: true },
    { id: 'jev-channel-off', channel_id: 'ts-disabled', kind: 'decision', enabled: true },
    { id: 'jev-off', channel_id: 'ts', kind: 'decision', enabled: false },
    { id: 'jev-wrong-channel', channel_id: 'channel-on', kind: 'decision', enabled: true },
  ] as ApiModel[]
  for (const key of ['file_route_model_id', 'tool_route_model_id', 'memory_dedup_model_id', 'memory_adjudicate_model_id', 'moderation_model_id']) {
    expect(availablePolicyModels(decisionModels, decisionChannels, key).map(m => m.id)).toEqual(['chat-on', 'jev'])
  }
  for (const key of ['default_model_id', 'task_model_id', 'title_model_id', 'verify_model_id', 'fallback_model_id']) {
    expect(availablePolicyModels(decisionModels, decisionChannels, key).map(m => m.id)).toEqual(['chat-on'])
  }
  const chat = availablePolicyModels(decisionModels, decisionChannels)
  const decisions = availablePolicyModels(decisionModels, decisionChannels, 'tool_route_model_id')
  expect(unavailablePolicyModelIDs({ tool_route_model_id: 'jev', moderation_model_id: 'jev' }, chat, decisions)).toEqual([])
  expect(unavailablePolicyModelIDs({ task_model_id: 'jev', memory_dedup_model_id: 'jev' }, chat, decisions)).toEqual(['jev'])
})
