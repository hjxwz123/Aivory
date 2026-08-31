import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiModel, ApiModelTag, ApiWorkspacePolicy } from '@/api/types'

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
import { useWorkspaces } from '@/store/workspaces'

const workspacePolicy: ApiWorkspacePolicy = {
  WorkspaceID: 'workspace-1',
  AllowedModelIDs: [],
  AllowedToolIDs: [],
  AllowedMCPServerIDs: [],
  AllowToolCalling: true,
  AllowDrawing: true,
  AllowMCP: true,
  AllowSkills: true,
  AllowPrompts: true,
  AllowKnowledgeBases: true,
  AllowFileUpload: true,
  MemberMonthlyCreditLimit: 0,
  UpdatedBy: 'admin',
  UpdatedAt: 1,
}

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
      verifyAvailable: false,
      fastAvailable: false,
      fastVision: false,
      loaded: false,
      loadedScope: undefined,
      loadedPolicyKey: undefined,
      loading: false,
      error: null,
    })
    useWorkspaces.setState({
      activeId: null,
      loaded: true,
      switching: false,
      policies: {},
      policyLoading: {},
      policyErrors: {},
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

  it('clears stale image models before a workspace-scoped reload resolves', async () => {
    useModels.setState({
      imageModels: [{ id: 'old-image' } as ApiModel],
    })
    let resolveModels!: (value: { models: ApiModel[]; default_id: string }) => void
    const pendingModels = new Promise<{ models: ApiModel[]; default_id: string }>((resolve) => {
      resolveModels = resolve
    })
    apiMocks.list.mockReturnValue(pendingModels)
    apiMocks.listImage.mockResolvedValue({ models: [], default_id: '' })
    apiMocks.tags.mockResolvedValue([])

    const loading = useModels.getState().load()
    expect(useModels.getState().imageModels).toEqual([])
    expect(useModels.getState().loadedScope).toBeNull()

    resolveModels({ models: [], default_id: '' })
    await loading
  })

  it('marks the workspace scope represented by the completed model catalog', async () => {
    useWorkspaces.setState({
      activeId: 'workspace-1',
      policies: { 'workspace-1': workspacePolicy },
    })
    apiMocks.list.mockResolvedValue({ models: [], default_id: '' })
    apiMocks.listImage.mockResolvedValue({ models: [], default_id: '' })
    apiMocks.tags.mockResolvedValue([])

    await useModels.getState().load()

    expect(useModels.getState().loadedScope).toBe('workspace-1')
    expect(apiMocks.list).toHaveBeenCalledWith('workspace-1')
    expect(apiMocks.listImage).toHaveBeenCalledWith('workspace-1')
  })

  it('clears every scope-sensitive value before loading a different workspace', async () => {
    useModels.setState({
      models: [{ id: 'old-chat' } as ApiModel],
      imageModels: [{ id: 'old-image' } as ApiModel],
      defaultId: 'old-chat',
      verifyAvailable: true,
      fastAvailable: true,
      fastVision: true,
      loaded: true,
      loadedScope: 'workspace-old',
      loadedPolicyKey: JSON.stringify(['workspace-old', true, []]),
    })
    useWorkspaces.setState({
      activeId: 'workspace-1',
      policies: { 'workspace-1': workspacePolicy },
    })
    let resolveModels!: (value: { models: ApiModel[]; default_id: string }) => void
    apiMocks.list.mockReturnValue(new Promise((resolve) => {
      resolveModels = resolve
    }))
    apiMocks.listImage.mockResolvedValue({ models: [], default_id: '' })
    apiMocks.tags.mockResolvedValue([])

    const loading = useModels.getState().load()

    expect(useModels.getState()).toMatchObject({
      models: [],
      imageModels: [],
      defaultId: '',
      verifyAvailable: false,
      fastAvailable: false,
      fastVision: false,
      loaded: false,
      loadedScope: 'workspace-1',
      loading: true,
    })

    resolveModels({ models: [], default_id: '' })
    await loading
  })

  it('keeps a different workspace catalog empty when the new request fails', async () => {
    useModels.setState({
      models: [{ id: 'old-chat' } as ApiModel],
      imageModels: [{ id: 'old-image' } as ApiModel],
      defaultId: 'old-chat',
      verifyAvailable: true,
      fastAvailable: true,
      fastVision: true,
      loaded: true,
      loadedScope: 'workspace-old',
      loadedPolicyKey: JSON.stringify(['workspace-old', true, []]),
    })
    useWorkspaces.setState({
      activeId: 'workspace-1',
      policies: { 'workspace-1': workspacePolicy },
    })
    apiMocks.list.mockRejectedValue(new Error('offline'))
    apiMocks.listImage.mockResolvedValue({ models: [], default_id: '' })
    apiMocks.tags.mockResolvedValue([])

    await useModels.getState().load()

    expect(useModels.getState()).toMatchObject({
      models: [],
      imageModels: [],
      defaultId: '',
      verifyAvailable: false,
      fastAvailable: false,
      fastVision: false,
      loaded: true,
      loadedScope: 'workspace-1',
      loadedPolicyKey: JSON.stringify(['workspace-1', true, []]),
      loading: false,
      error: 'Failed to load models',
    })
  })

  it('preserves a same-scope catalog while its background refresh is pending', async () => {
    useModels.setState({
      models: [{ id: 'current-chat' } as ApiModel],
      imageModels: [{ id: 'current-image' } as ApiModel],
      defaultId: 'current-chat',
      verifyAvailable: true,
      fastAvailable: true,
      fastVision: true,
      loaded: true,
      loadedScope: 'workspace-1',
      loadedPolicyKey: JSON.stringify(['workspace-1', true, []]),
    })
    useWorkspaces.setState({
      activeId: 'workspace-1',
      policies: { 'workspace-1': workspacePolicy },
    })
    let resolveModels!: (value: { models: ApiModel[]; default_id: string }) => void
    apiMocks.list.mockReturnValue(new Promise((resolve) => {
      resolveModels = resolve
    }))
    apiMocks.listImage.mockResolvedValue({ models: [], default_id: '' })
    apiMocks.tags.mockResolvedValue([])

    const loading = useModels.getState().load()

    expect(useModels.getState()).toMatchObject({
      models: [{ id: 'current-chat' }],
      imageModels: [{ id: 'current-image' }],
      defaultId: 'current-chat',
      verifyAvailable: true,
      fastAvailable: true,
      fastVision: true,
      loaded: true,
      loadedScope: 'workspace-1',
      loadedPolicyKey: JSON.stringify(['workspace-1', true, []]),
      loading: true,
    })

    resolveModels({ models: [], default_id: '' })
    await loading
  })

  it('clears immediately when the scope changes during a request and keeps the failed trailing scope empty', async () => {
    useModels.setState({
      models: [{ id: 'old-chat' } as ApiModel],
      imageModels: [{ id: 'old-image' } as ApiModel],
      defaultId: 'old-chat',
      verifyAvailable: true,
      fastAvailable: true,
      fastVision: true,
      loaded: true,
      loadedScope: 'workspace-old',
      loadedPolicyKey: JSON.stringify(['workspace-old', true, []]),
    })
    useWorkspaces.setState({
      activeId: 'workspace-old',
      policies: {
        'workspace-old': { ...workspacePolicy, WorkspaceID: 'workspace-old' },
      },
    })
    let rejectOldRequest!: (error: Error) => void
    apiMocks.list
      .mockImplementationOnce(
        () =>
          new Promise((_resolve, reject) => {
            rejectOldRequest = reject
          }),
      )
      .mockRejectedValueOnce(new Error('new scope offline'))
    apiMocks.listImage.mockResolvedValue({ models: [], default_id: '' })
    apiMocks.tags.mockResolvedValue([])

    const oldLoading = useModels.getState().load()
    useWorkspaces.setState({
      activeId: 'workspace-1',
      policies: { 'workspace-1': workspacePolicy },
    })
    await useModels.getState().load()

    expect(useModels.getState()).toMatchObject({
      models: [],
      imageModels: [],
      defaultId: '',
      verifyAvailable: false,
      fastAvailable: false,
      fastVision: false,
      loaded: false,
      loadedScope: 'workspace-1',
      loading: true,
    })

    rejectOldRequest(new Error('old scope offline'))
    await oldLoading
    await vi.waitFor(() => expect(apiMocks.list).toHaveBeenCalledTimes(2))
    await vi.waitFor(() => expect(useModels.getState().loading).toBe(false))

    expect(useModels.getState()).toMatchObject({
      models: [],
      imageModels: [],
      defaultId: '',
      verifyAvailable: false,
      fastAvailable: false,
      fastVision: false,
      loaded: true,
      loadedScope: 'workspace-1',
      error: 'Failed to load models',
    })
  })

  it('clears same-workspace models when its policy fingerprint changes', async () => {
    useModels.setState({
      models: [{ id: 'old-chat' } as ApiModel],
      imageModels: [],
      defaultId: 'old-chat',
      verifyAvailable: true,
      fastAvailable: true,
      fastVision: true,
      loaded: true,
      loadedScope: 'workspace-1',
      loadedPolicyKey: JSON.stringify(['workspace-1', true, ['old-chat']]),
    })
    useWorkspaces.setState({
      activeId: 'workspace-1',
      policies: { 'workspace-1': workspacePolicy },
    })
    let resolveModels!: (value: { models: ApiModel[]; default_id: string }) => void
    apiMocks.list.mockReturnValue(new Promise((resolve) => {
      resolveModels = resolve
    }))
    apiMocks.listImage.mockResolvedValue({ models: [], default_id: '' })
    apiMocks.tags.mockResolvedValue([])

    const loading = useModels.getState().load()

    expect(useModels.getState()).toMatchObject({
      models: [],
      defaultId: '',
      verifyAvailable: false,
      fastAvailable: false,
      fastVision: false,
      loaded: false,
      loadedScope: 'workspace-1',
      loadedPolicyKey: JSON.stringify(['workspace-1', true, []]),
      loading: true,
    })

    resolveModels({ models: [], default_id: '' })
    await loading
  })
})
