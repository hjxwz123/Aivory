import type { ApiUser, ApiUserGroupPermissions } from '@/api/types'

export const DEFAULT_USER_PERMISSIONS: ApiUserGroupPermissions = {
  prompts: { mode: 'all', ids: [] },
  skills: { mode: 'all', ids: [] },
  tools: { mode: 'all', ids: [] },
  allow_sharing: true,
  allow_knowledge_bases: true,
  allow_knowledge_base_sharing: true,
  allow_file_upload: true,
  allow_conversation_export: true,
  allow_conversation_deletion: true,
  allow_voice_transcription: true,
  allow_memory: true,
  allow_drawing: true,
}

export type UserCapability = keyof Pick<
  ApiUserGroupPermissions,
  | 'allow_sharing'
  | 'allow_knowledge_bases'
  | 'allow_knowledge_base_sharing'
  | 'allow_file_upload'
  | 'allow_conversation_export'
  | 'allow_conversation_deletion'
  | 'allow_voice_transcription'
  | 'allow_memory'
  | 'allow_drawing'
>

/**
 * Missing permissions mean an older server response, so retain the historical
 * permissive behavior. Current servers always send a complete normalized
 * policy on /me; administrators bypass group restrictions on both tiers.
 */
export function userPermissions(user?: Pick<ApiUser, 'role' | 'permissions'> | null): ApiUserGroupPermissions {
  if (!user || user.role === 'admin' || !user.permissions) return DEFAULT_USER_PERMISSIONS
  return {
    ...DEFAULT_USER_PERMISSIONS,
    ...user.permissions,
    prompts: user.permissions.prompts ?? DEFAULT_USER_PERMISSIONS.prompts,
    skills: user.permissions.skills ?? DEFAULT_USER_PERMISSIONS.skills,
    tools: user.permissions.tools ?? DEFAULT_USER_PERMISSIONS.tools,
  }
}

export function userCan(
  user: Pick<ApiUser, 'role' | 'permissions'> | null | undefined,
  capability: UserCapability,
): boolean {
  return userPermissions(user)[capability]
}
