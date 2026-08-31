import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiWorkspacePolicy } from '@/api/types'

const apiMocks = vi.hoisted(() => ({
  list: vi.fn(),
  getPolicy: vi.fn(),
  remove: vi.fn(),
  leave: vi.fn(),
}))

const loadMocks = vi.hoisted(() => ({
  conversations: vi.fn(),
  projects: vi.fn(),
  models: vi.fn(),
}))

vi.mock('@/api', () => ({ workspacesApi: apiMocks }))
vi.mock('@/store/conversations', () => ({
  useConversations: { getState: () => ({ load: loadMocks.conversations }) },
}))
vi.mock('@/store/projects', () => ({
  useProjects: { getState: () => ({ load: loadMocks.projects }) },
}))
vi.mock('@/store/models', () => ({
  useModels: { getState: () => ({ load: loadMocks.models }) },
}))

import { useComposerPrefs } from '@/store/composer-prefs'
import { useWorkspaces } from '@/store/workspaces'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

const policy: ApiWorkspacePolicy = {
  WorkspaceID: 'workspace-next',
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

describe('workspace switch tool selection isolation', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useComposerPrefs.setState({
      selectedToolIdsByModel: { model_1: ['usermcp:old-server'] },
    })
    useWorkspaces.setState({
      workspaces: [],
      activeId: 'workspace-current',
      switching: false,
      policies: {},
      policyLoading: {},
      policyErrors: {},
    })
    apiMocks.getPolicy.mockResolvedValue(policy)
    apiMocks.remove.mockResolvedValue(undefined)
    apiMocks.leave.mockResolvedValue(undefined)
    loadMocks.conversations.mockImplementation(async () => {
      // The clear happens before activeId flips and before any space-scoped
      // cache starts loading.
      expect(useComposerPrefs.getState().selectedToolIdsByModel).toEqual({})
    })
    loadMocks.projects.mockResolvedValue(undefined)
    loadMocks.models.mockResolvedValue(undefined)
  })

  it('drops the previous space selections before reloading the next space', async () => {
    await useWorkspaces.getState().switchTo('workspace-next')

    expect(useComposerPrefs.getState().selectedToolIdsByModel).toEqual({})
    expect(useWorkspaces.getState().activeId).toBe('workspace-next')
    expect(loadMocks.conversations).toHaveBeenCalledTimes(1)
    expect(loadMocks.projects).toHaveBeenCalledTimes(1)
    // The policy refresh also invalidates the model picker once it resolves.
    expect(loadMocks.models).toHaveBeenCalled()
  })

  it('drops a cached policy and records the error when a refresh fails', async () => {
    const stalePolicy = { ...policy, WorkspaceID: 'workspace-current' }
    useWorkspaces.setState({
      activeId: 'workspace-current',
      policies: { 'workspace-current': stalePolicy },
      policyLoading: {},
      policyErrors: {},
    })
    apiMocks.getPolicy.mockRejectedValueOnce(new Error('offline'))

    await useWorkspaces.getState().loadPolicy('workspace-current')

    expect(useWorkspaces.getState().policies['workspace-current']).toBeUndefined()
    expect(useWorkspaces.getState().policyLoading['workspace-current']).toBe(false)
    expect(useWorkspaces.getState().policyErrors['workspace-current']).toBe('offline')
    expect(loadMocks.models).not.toHaveBeenCalled()
  })

  it('does not let an older GET overwrite a policy saved while it was in flight', async () => {
    const pending = deferred<ApiWorkspacePolicy>()
    const stalePolicy = { ...policy, WorkspaceID: 'workspace-race', UpdatedAt: 1 }
    const savedPolicy = {
      ...stalePolicy,
      AllowToolCalling: false,
      UpdatedAt: 2,
    }
    apiMocks.getPolicy.mockReturnValueOnce(pending.promise)

    const loading = useWorkspaces.getState().loadPolicy('workspace-race')
    expect(useWorkspaces.getState().policyLoading['workspace-race']).toBe(true)

    useWorkspaces.getState().setPolicy('workspace-race', savedPolicy)
    pending.resolve(stalePolicy)

    await expect(loading).resolves.toEqual(savedPolicy)
    expect(useWorkspaces.getState().policies['workspace-race']).toEqual(savedPolicy)
    expect(useWorkspaces.getState().policyLoading['workspace-race']).toBe(false)
    expect(useWorkspaces.getState().policyErrors['workspace-race']).toBeNull()
  })

  it('does not let an older GET failure erase a policy saved while it was in flight', async () => {
    const pending = deferred<ApiWorkspacePolicy>()
    const savedPolicy = {
      ...policy,
      WorkspaceID: 'workspace-race-error',
      AllowToolCalling: false,
      UpdatedAt: 2,
    }
    apiMocks.getPolicy.mockReturnValueOnce(pending.promise)

    const loading = useWorkspaces.getState().loadPolicy('workspace-race-error')
    useWorkspaces.getState().setPolicy('workspace-race-error', savedPolicy)
    pending.reject(new Error('late offline error'))

    await expect(loading).resolves.toEqual(savedPolicy)
    expect(useWorkspaces.getState().policies['workspace-race-error']).toEqual(savedPolicy)
    expect(useWorkspaces.getState().policyLoading['workspace-race-error']).toBe(false)
    expect(useWorkspaces.getState().policyErrors['workspace-race-error']).toBeNull()
  })

  it.each(['remove', 'leave'] as const)(
    'does not restore policy state after a workspace %s completes',
    async (operation) => {
      const workspaceID = `workspace-${operation}`
      const pending = deferred<ApiWorkspacePolicy>()
      const stalePolicy = { ...policy, WorkspaceID: workspaceID }
      apiMocks.getPolicy.mockReturnValueOnce(pending.promise)
      useWorkspaces.setState({
        workspaces: [{ id: workspaceID } as never],
        activeId: 'workspace-current',
      })

      const loading = useWorkspaces.getState().loadPolicy(workspaceID)
      await useWorkspaces.getState()[operation](workspaceID)
      pending.resolve(stalePolicy)
      await loading

      expect(useWorkspaces.getState().policies[workspaceID]).toBeUndefined()
      expect(useWorkspaces.getState().policyLoading[workspaceID]).toBeUndefined()
      expect(useWorkspaces.getState().policyErrors[workspaceID]).toBeUndefined()
    },
  )
})
