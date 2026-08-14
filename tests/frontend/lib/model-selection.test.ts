import { describe, expect, it } from 'vitest'
import type { ApiModel } from '@/api/types'
import { findSelectedModel, isSelectedModelUnavailable } from '@/lib/model-selection'

function model(id: string, kind: ApiModel['kind'] = 'chat'): ApiModel {
  return {
    id,
    kind,
    channel_id: 'channel',
    request_id: id,
    label: id,
    description: '',
    icon: '',
    enabled: true,
    sort_order: 0,
    tool_mode: 'none',
    vision: false,
    stream: true,
    system_prompt: '',
    param_controls: null,
    price_input: 0,
    price_output: 0,
    price_cache_read: 0,
    price_cache_write: 0,
    price_per_image: 0,
    currency: 'USD',
    dim: 0,
    updated_at: 0,
  }
}

describe('model selection availability', () => {
  it('does not visually substitute the first chat model for a missing selection', () => {
    expect(findSelectedModel('revoked-image', [model('chat')], [])).toBeUndefined()
  })

  it('marks a removed or revoked concrete model unavailable after hydration', () => {
    expect(
      isSelectedModelUnavailable({
        modelId: 'revoked-image',
        currentModel: undefined,
        modelsLoaded: true,
        fast: false,
        fastAvailable: true,
      }),
    ).toBe(true)
  })

  it('does not report a transient loading state or an active fast selection as unavailable', () => {
    expect(
      isSelectedModelUnavailable({
        modelId: 'pending',
        currentModel: undefined,
        modelsLoaded: false,
        fast: false,
        fastAvailable: false,
      }),
    ).toBe(false)
    expect(
      isSelectedModelUnavailable({
        modelId: 'hidden-advanced-choice',
        currentModel: undefined,
        modelsLoaded: true,
        fast: true,
        fastAvailable: true,
      }),
    ).toBe(false)
  })

  it('keeps an available image model selected while drawing is allowed', () => {
    const image = model('image', 'image')
    const currentModel = findSelectedModel(image.id, [model('chat')], [image])
    expect(currentModel).toBe(image)
    expect(
      isSelectedModelUnavailable({
        modelId: image.id,
        currentModel,
        modelsLoaded: true,
        fast: false,
        fastAvailable: false,
      }),
    ).toBe(false)
  })
})
