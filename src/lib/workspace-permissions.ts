import type {
  ApiWorkspace,
  ApiWorkspaceMember,
  ApiWorkspaceMemberPermissions,
  ApiWorkspacePolicy,
} from '@/api/types'

/** The three resource families controlled independently by a workspace. */
export type WorkspaceResourceKind = 'prompt' | 'skill' | 'mcp'

/**
 * Member capability limits apply only to ordinary members and guests. Owners
 * and admins always receive the full workspace ceiling and the API rejects
 * attempts to persist a member-permission payload for them.
 */
export function canEditWorkspaceMemberPermissions(
  member: Pick<ApiWorkspaceMember, 'role' | 'is_owner'>,
): boolean {
  return member.is_owner !== true && member.role !== 'admin'
}

export interface WorkspaceCapabilityState {
  toolCalling: boolean
  drawing: boolean
  mcp: boolean
  skills: boolean
  prompts: boolean
  knowledgeBases: boolean
  fileUpload: boolean
}

export interface WorkspacePolicyResolutionOptions {
  /** Kept for call-site readability while the workspace list hydrates. */
  workspacesLoaded?: boolean
  /** True while the latest policy request is in flight. */
  policyLoading?: boolean
  /** True while space-scoped data is changing to another workspace. */
  switching?: boolean
  /** The latest policy request failed. */
  policyError?: boolean | string | null
}

/** All workspace capabilities disabled while the active workspace policy is
 * still being hydrated. Personal space callers should continue using
 * `workspaceCapabilities` directly; this state is only for a known workspace
 * whose policy has not arrived yet. */
export function unavailableWorkspaceCapabilities(): WorkspaceCapabilityState {
  return {
    toolCalling: false,
    drawing: false,
    mcp: false,
    skills: false,
    prompts: false,
    knowledgeBases: false,
    fileUpload: false,
  }
}

/**
 * Normalize policy responses from both the current API and older deployments.
 * New fields are authoritative when present. Missing fields remain permissive
 * so a policy endpoint rollout cannot blank the whole workspace UI.
 */
export function workspaceCapabilities(policy?: Partial<ApiWorkspacePolicy> | null): WorkspaceCapabilityState {
  // During a rolling upgrade an older API may only expose the retired
  // AllowSandbox / AllowImageGeneration switches. Preserve their effective
  // meaning instead of treating a missing AllowToolCalling field as enabled:
  // the merged tool capability was historically the intersection of those two
  // switches, while direct drawing followed image generation alone.
  const legacyToolCalling =
    (policy?.AllowSandbox ?? true) &&
    (policy?.AllowImageGeneration ?? true)
  return {
    toolCalling: policy?.AllowToolCalling ?? legacyToolCalling,
    drawing: policy?.AllowDrawing ?? policy?.AllowImageGeneration ?? true,
    mcp: policy?.AllowMCP ?? true,
    skills: policy?.AllowSkills ?? true,
    prompts: policy?.AllowPrompts ?? true,
    knowledgeBases: policy?.AllowKnowledgeBases ?? true,
    fileUpload: policy?.AllowFileUpload ?? true,
  }
}

/**
 * Resolve capabilities for a particular workspace scope without exposing the
 * permissive compatibility fallback while the policy is unknown. A workspace
 * policy is the security ceiling for every capability, so a missing policy (or
 * a failed refresh) must fail closed. Personal-space callers have no workspace
 * ceiling and continue to use the compatibility-normalized defaults above.
 */
export function workspaceCapabilitiesForScope(
  workspaceId: string | null | undefined,
  policy: Partial<ApiWorkspacePolicy> | null | undefined,
  options: WorkspacePolicyResolutionOptions = {},
): WorkspaceCapabilityState {
  const scoped = Boolean(workspaceId)
  // Do not let an omitted option accidentally re-open a capability. The
  // policy itself is authoritative; while it is absent or known-bad the only
  // safe result is a fully disabled capability set.
  const unavailable = scoped && (
    !policy ||
    options.workspacesLoaded === false ||
    Boolean(options.policyLoading) ||
    Boolean(options.switching) ||
    Boolean(options.policyError)
  )
  return unavailable ? unavailableWorkspaceCapabilities() : workspaceCapabilities(policy)
}

/**
 * Whether a capability result represents a settled policy decision. UI may
 * fail closed while this returns false, but must not destructively rewrite a
 * user's saved preference until a successful policy response explicitly
 * denies that capability.
 */
export function workspacePolicyResolvedForScope(
  workspaceId: string | null | undefined,
  policy: Partial<ApiWorkspacePolicy> | null | undefined,
  options: WorkspacePolicyResolutionOptions = {},
): boolean {
  if (!workspaceId) return true
  return Boolean(policy) &&
    options.workspacesLoaded !== false &&
    !options.policyLoading &&
    !options.switching &&
    !options.policyError
}

/**
 * Stable fingerprint for the workspace fields that shape the chat/image model
 * catalog. It lets the model cache distinguish an ordinary same-policy refresh
 * from the moment a newly hydrated or updated policy changes its allowlist.
 */
export function workspaceModelPolicyKey(
  workspaceId: string | null | undefined,
  policy: Partial<ApiWorkspacePolicy> | null | undefined,
): string {
  if (!workspaceId) return 'personal'
  if (!policy) return `${workspaceId}:missing`
  const allowedModelIDs = Array.isArray(policy.AllowedModelIDs)
    ? [...policy.AllowedModelIDs].sort()
    : []
  return JSON.stringify([
    workspaceId,
    workspaceCapabilities(policy).drawing,
    allowedModelIDs,
  ])
}

/** True when a resource family is enabled by the workspace policy. */
export function workspaceAllowsResource(
  policy: Partial<ApiWorkspacePolicy> | null | undefined,
  kind: WorkspaceResourceKind,
): boolean {
  const capabilities = workspaceCapabilities(policy)
  if (kind === 'mcp') return capabilities.mcp
  if (kind === 'skill') return capabilities.skills
  return capabilities.prompts
}

/**
 * Workspace member rows historically exposed one combined creation flag. The
 * granular fields are optional while old servers are in the wild; fall back to
 * that flag for prompt/skill creation and keep MCP creation disabled only when
 * the server explicitly reports the new field as false.
 */
export function memberCanCreate(
  member: Pick<ApiWorkspaceMember | ApiWorkspaceMemberPermissions, 'can_create_skills_prompts'> &
    Partial<Pick<ApiWorkspaceMember | ApiWorkspaceMemberPermissions, 'can_create_prompts' | 'can_create_skills' | 'can_create_mcp'>>,
  kind: WorkspaceResourceKind,
): boolean {
  if (kind === 'prompt') return member.can_create_prompts ?? member.can_create_skills_prompts
  if (kind === 'skill') return member.can_create_skills ?? member.can_create_skills_prompts
  // Older workspaces had no user-owned MCP creation capability. Preserve the
  // old combined behavior only when the new field is absent so existing users
  // are not unexpectedly locked out during a rolling deployment.
  return member.can_create_mcp ?? member.can_create_skills_prompts
}

/** Usage permissions are separate from creation permissions. */
export function memberCanUse(
  member: Partial<Pick<ApiWorkspaceMember | ApiWorkspaceMemberPermissions, 'can_use_prompts' | 'can_use_skills' | 'can_use_mcp'>>,
  kind: WorkspaceResourceKind,
): boolean {
  if (kind === 'prompt') return member.can_use_prompts ?? true
  if (kind === 'skill') return member.can_use_skills ?? true
  return member.can_use_mcp ?? true
}

/**
 * Resolve a usage capability from the active workspace row. Workspace admins
 * (including the canonical owner) are intentionally unrestricted; ordinary
 * members use their explicit per-resource capability. A missing row is
 * treated as unavailable so a workspace cannot briefly expose a resource
 * while membership hydration is still in flight.
 */
export function workspaceMemberCanUse(
  workspace: ApiWorkspace | null | undefined,
  kind: WorkspaceResourceKind,
): boolean {
  if (!workspace) return false
  if (workspace.is_owner || workspace.role === 'admin') return true
  return memberCanUse(workspace, kind)
}

/**
 * Read a capability from an enriched workspace row. This is useful before the
 * separate policy request finishes: workspace rows carry member-level limits,
 * while policy carries workspace-wide limits.
 */
export function workspaceMemberCanCreate(
  workspace: ApiWorkspace | null | undefined,
  kind: WorkspaceResourceKind,
): boolean {
  if (!workspace) return true
  return memberCanCreate(workspace, kind)
}
