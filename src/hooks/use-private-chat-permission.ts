import { useAuth } from '@/store/auth'
import { useWorkspaces } from '@/store/workspaces'
import { userCan } from '@/lib/user-permissions'
import { workspaceCapabilitiesForScope, workspacePolicyResolvedForScope } from '@/lib/workspace-permissions'

/** Workspace policy can only narrow the system user-group permission. */
export function usePrivateChatPermission() {
  const user = useAuth((s) => s.user)
  const workspaceId = useWorkspaces((s) => s.lockedWorkspaceId || s.activeId)
  const workspace = useWorkspaces((s) => s.workspaces.find((w) => w.id === workspaceId))
  const policy = useWorkspaces((s) => workspaceId ? s.policies[workspaceId] : undefined)
  const loaded = useWorkspaces((s) => s.loaded)
  const loading = useWorkspaces((s) => workspaceId ? s.policyLoading[workspaceId] : false)
  const error = useWorkspaces((s) => workspaceId ? s.policyErrors[workspaceId] : null)
  const switching = useWorkspaces((s) => s.switching)
  const options = { workspacesLoaded: loaded, policyLoading: loading, policyError: error, switching }
  const caps = workspaceCapabilitiesForScope(workspaceId, policy, options)
  const groupAllowed = userCan(user, 'allow_private_chat')
  const memberAllowed = !workspaceId || (!!workspace && workspace.role !== 'guest')
  return {
    workspaceId,
    allowed: groupAllowed && caps.privateChat && memberAllowed,
    // Keep an already authorized transcript mounted during a routine policy
    // refresh, while disabling new sends until the fresh policy arrives.
    canRender: groupAllowed && memberAllowed && workspaceCapabilitiesForScope(workspaceId, policy, { ...options, policyLoading: false }).privateChat,
    resolved: !groupAllowed || !!error || workspacePolicyResolvedForScope(workspaceId, policy, options),
    canUpload: userCan(user, 'allow_file_upload') && caps.fileUpload,
  }
}
