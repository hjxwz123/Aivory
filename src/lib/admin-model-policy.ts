import type { TFunction } from 'i18next'
import { ApiError } from '@/api'
import type { ApiChannel, ApiModel } from '@/api/types'

export const MODEL_POLICY_MODEL_KEYS = [
  'default_model_id',
  'task_model_id',
  'tool_route_model_id',
  'verify_model_id',
  'fallback_model_id',
] as const

export function availablePolicyModels(models: ApiModel[], channels: ApiChannel[]): ApiModel[] {
  const enabledChannelIDs = new Set(channels.filter((channel) => channel.enabled).map((channel) => channel.id))
  return models.filter(
    (model) => model.kind === 'chat' && model.enabled && enabledChannelIDs.has(model.channel_id),
  )
}

export function unavailablePolicyModelIDs(
  settings: Record<string, unknown>,
  availableModels: ApiModel[],
): string[] {
  const availableIDs = new Set(availableModels.map((model) => model.id))
  const unavailable = new Set<string>()
  for (const key of MODEL_POLICY_MODEL_KEYS) {
    const modelID = typeof settings[key] === 'string' ? settings[key].trim() : ''
    if (modelID && !availableIDs.has(modelID)) unavailable.add(modelID)
  }
  return [...unavailable]
}

export function modelPolicyErrorText(t: TFunction, error: unknown): string {
  if (!(error instanceof ApiError) || error.message !== 'model_policy_model_unavailable') return ''
  return t('admin:settings.modelPolicy.unavailable')
}
