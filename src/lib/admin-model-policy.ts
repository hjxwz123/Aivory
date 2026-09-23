import type { TFunction } from 'i18next'
import { ApiError } from '@/api'
import type { ApiChannel, ApiModel } from '@/api/types'

export const MODEL_POLICY_MODEL_KEYS = [
  'default_model_id',
  'task_model_id',
  'title_model_id',
  'file_route_model_id',
  'tool_route_model_id',
  'verify_model_id',
  'fallback_model_id',
  'memory_dedup_model_id',
  'memory_adjudicate_model_id',
  'moderation_model_id',
  'vision_model_id',
] as const

export const DECISION_POLICY_KEYS = new Set<string>([
  'file_route_model_id', 'tool_route_model_id', 'memory_dedup_model_id', 'memory_adjudicate_model_id', 'moderation_model_id',
])

export function availablePolicyModels(models: ApiModel[], channels: ApiChannel[], policyKey = ''): ApiModel[] {
  const enabledChannelIDs = new Set(channels.filter((channel) => channel.enabled).map((channel) => channel.id))
  const decisionChannelIDs = new Set(channels.filter((channel) => channel.enabled && channel.type === 'typesafe' && channel.has_api_key).map((channel) => channel.id))
  return models.filter(
    (model) => model.enabled && (
      (model.kind === 'chat' && enabledChannelIDs.has(model.channel_id) && !channels.some((c) => c.id === model.channel_id && c.type === 'typesafe'))
      || (DECISION_POLICY_KEYS.has(policyKey) && model.kind === 'decision' && decisionChannelIDs.has(model.channel_id))
    ),
  )
}

export function unavailablePolicyModelIDs(
  settings: Record<string, unknown>,
  availableModels: ApiModel[],
  availableDecisions: ApiModel[] = [],
  // The image-outsourcing key is narrower than the rest: a saved chat model
  // without the Vision flag is unusable and must be flagged as stale.
  availableVision: ApiModel[] = [],
): string[] {
  const availableIDs = new Set(availableModels.map((model) => model.id))
  const visionIDs = new Set(availableVision.map((model) => model.id))
  const unavailable = new Set<string>()
  for (const key of MODEL_POLICY_MODEL_KEYS) {
    const modelID = typeof settings[key] === 'string' ? settings[key].trim() : ''
    if (!modelID) continue
    if (key === 'vision_model_id') {
      if (!visionIDs.has(modelID)) unavailable.add(modelID)
      continue
    }
    if (!availableIDs.has(modelID) && !(DECISION_POLICY_KEYS.has(key) && availableDecisions.some((m) => m.id === modelID))) unavailable.add(modelID)
  }
  return [...unavailable]
}

/**
 * Models the §4.6 image-outsourcing field may point at. It has to be an enabled
 * chat model on an enabled channel whose Vision flag an administrator turned on:
 * the server rejects anything else, because a model that cannot read images
 * would silently turn the setting into a no-op.
 */
export function availableVisionModels(models: ApiModel[], channels: ApiChannel[]): ApiModel[] {
  const enabledChannelIDs = new Set(
    channels.filter((channel) => channel.enabled && channel.type !== 'typesafe').map((channel) => channel.id),
  )
  return models.filter(
    (model) => model.enabled && model.kind === 'chat' && model.vision === true && enabledChannelIDs.has(model.channel_id),
  )
}

export function modelPolicyErrorText(t: TFunction, error: unknown): string {
  if (!(error instanceof ApiError) || error.message !== 'model_policy_model_unavailable') return ''
  return t('admin:settings.modelPolicy.unavailable')
}
