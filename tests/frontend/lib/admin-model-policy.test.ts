import { describe, expect, it } from 'vitest'
import type { ApiChannel, ApiModel } from '@/api/types'
import {
  availablePolicyModels,
  availableVisionModels,
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

describe('vision recognition model availability', () => {
  const visionModels = [...models,
    { id: 'sees-images', channel_id: 'channel-on', kind: 'chat', enabled: true, vision: true },
    { id: 'text-only', channel_id: 'channel-on', kind: 'chat', enabled: true, vision: false },
    { id: 'vision-off', channel_id: 'channel-on', kind: 'chat', enabled: false, vision: true },
    { id: 'vision-channel-off', channel_id: 'channel-off', kind: 'chat', enabled: true, vision: true },
    { id: 'vision-image-kind', channel_id: 'channel-on', kind: 'image', enabled: true, vision: true },
  ] as ApiModel[]

  it('offers only enabled vision-capable chat models on enabled channels', () => {
    expect(availableVisionModels(visionModels, channels).map((model) => model.id)).toEqual(['sees-images'])
  })

  it('never offers a Jev channel model even though it is a chat-shaped kind', () => {
    const withTypesafe = [...channels, { id: 'ts', type: 'typesafe', enabled: true, has_api_key: true }] as ApiChannel[]
    const withJev = [...visionModels, { id: 'jev-vision', channel_id: 'ts', kind: 'chat', enabled: true, vision: true }] as ApiModel[]
    expect(availableVisionModels(withJev, withTypesafe).map((model) => model.id)).toEqual(['sees-images'])
  })

  it('flags a saved vision model that lost its Vision flag as stale', () => {
    const chat = availablePolicyModels(visionModels, channels)
    const vision = availableVisionModels(visionModels, channels)
    // A plain chat model would look available to every other policy key, so the
    // vision key needs its own narrower check.
    expect(unavailablePolicyModelIDs({ vision_model_id: 'text-only' }, chat, [], vision)).toEqual(['text-only'])
    expect(unavailablePolicyModelIDs({ vision_model_id: 'sees-images' }, chat, [], vision)).toEqual([])
    expect(unavailablePolicyModelIDs({ vision_model_id: 'vision-off' }, chat, [], vision)).toEqual(['vision-off'])
    // Other keys keep their own rules; the vision list must not narrow them.
    expect(unavailablePolicyModelIDs({ default_model_id: 'text-only' }, chat, [], vision)).toEqual([])
  })
})
