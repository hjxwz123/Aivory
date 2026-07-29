import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiModelTag } from '@/api/types'

const apiMocks = vi.hoisted(() => ({
  list: vi.fn(),
  listImage: vi.fn(),
  tags: vi.fn(),
}))

vi.mock('@/api', () => {
  class ApiError extends Error {}

  return {
    ApiError,
    modelsApi: apiMocks,
  }
})

import { useModels } from '@/store/models'

function tag(id: string, name: string, sortOrder: number): ApiModelTag {
  return { id, name, sort_order: sortOrder, created_at: 1 }
}

describe('model tag picker cache', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useModels.setState({
      models: [],
      imageModels: [],
      tags: [],
      defaultId: '',
      loaded: false,
      loading: false,
      error: null,
    })
  })

  it('publishes admin reorders to picker consumers in the same session', () => {
    useModels.getState().setTags([
      tag('claude', 'Claude', 0),
      tag('openai', 'OpenAI', 1),
      tag('gemini', 'Gemini', 2),
    ])

    useModels.getState().setTags((current) => [
      { ...current[2], sort_order: 0 },
      { ...current[0], sort_order: 1 },
      { ...current[1], sort_order: 2 },
    ])

    expect(useModels.getState().tags.map((item) => [item.id, item.sort_order])).toEqual([
      ['gemini', 0],
      ['claude', 1],
      ['openai', 2],
    ])
  })

  it('does not let an older hydration response overwrite an admin reorder', async () => {
    let resolveTags!: (tags: ApiModelTag[]) => void
    const pendingTags = new Promise<ApiModelTag[]>((resolve) => {
      resolveTags = resolve
    })
    apiMocks.list.mockResolvedValue({ models: [], default_id: '' })
    apiMocks.listImage.mockResolvedValue({ models: [], default_id: '' })
    apiMocks.tags.mockReturnValue(pendingTags)

    const loading = useModels.getState().load()
    useModels.getState().setTags([
      tag('gemini', 'Gemini', 0),
      tag('claude', 'Claude', 1),
    ])
    resolveTags([
      tag('claude', 'Claude', 0),
      tag('gemini', 'Gemini', 1),
    ])
    await loading

    expect(useModels.getState().tags.map((item) => item.id)).toEqual(['gemini', 'claude'])
  })

  it('preserves the picker cache when optional tag hydration fails', async () => {
    useModels.getState().setTags([tag('gemini', 'Gemini', 0)])
    apiMocks.list.mockResolvedValue({ models: [], default_id: '' })
    apiMocks.listImage.mockResolvedValue({ models: [], default_id: '' })
    apiMocks.tags.mockRejectedValue(new Error('offline'))

    await useModels.getState().load()

    expect(useModels.getState().tags.map((item) => item.id)).toEqual(['gemini'])
  })
})
