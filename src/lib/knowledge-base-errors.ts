import type { TFunction } from 'i18next'
import { ApiError } from '@/api'

export function knowledgeBaseErrorText(
  t: TFunction,
  error: unknown,
  fallback: string,
): string {
  const code = error instanceof ApiError ? error.message : typeof error === 'string' ? error : ''
  switch (code) {
    case 'knowledge_base_group_permission_required':
      return t('kb:groupPermissionRequired')
    case 'workspace_kb_creation_permission_required':
      return t('kb:workspaceCreatePermissionRequired')
    case 'file_upload_group_permission_required':
      return t('kb:fileUploadPermissionRequired')
    case 'knowledge_base_sharing_group_permission_required':
    case 'sharing_group_permission_required':
      return t('kb:sharingPermissionRequired')
    case 'kb_limit_reached':
      return t('kb:limitReached')
    case 'name_exists':
      return t('kb:dialog.nameExists')
    case 'knowledge_base_unavailable':
      return t('kb:dialog.unavailable')
    case 'not found':
      return t('kb:accessRevokedBody')
    default:
      if (error instanceof ApiError && error.status === 403) return t('kb:permissionChanged')
      if (error instanceof ApiError && error.status === 404) return t('kb:accessRevokedBody')
      return code || fallback
  }
}

export function knowledgeBaseOperationErrorText(
  t: TFunction,
  error: unknown,
  fallback: string,
): string {
  if (error instanceof ApiError && (error.status === 403 || error.status === 404)) {
    switch (error.message) {
      case 'knowledge_base_group_permission_required':
      case 'file_upload_group_permission_required':
      case 'knowledge_base_sharing_group_permission_required':
      case 'sharing_group_permission_required':
        return knowledgeBaseErrorText(t, error.message, fallback)
      default:
        return t('kb:permissionChanged')
    }
  }
  return knowledgeBaseErrorText(t, error, fallback)
}
