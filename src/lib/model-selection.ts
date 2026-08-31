import type { ApiModel } from '@/api/types'

export function isModelCatalogReadyForScope({
  loaded,
  loadedScope,
  loadedPolicyKey,
  expectedScope,
  expectedPolicyKey,
}: {
  loaded: boolean
  loadedScope: string | null | undefined
  loadedPolicyKey: string | undefined
  expectedScope: string | null
  expectedPolicyKey: string
}): boolean {
  return loaded && loadedScope === expectedScope && loadedPolicyKey === expectedPolicyKey
}

export function findSelectedModel(
  modelId: string,
  models: ApiModel[],
  imageModels: ApiModel[],
): ApiModel | undefined {
  return models.find((model) => model.id === modelId) ?? imageModels.find((model) => model.id === modelId)
}

export function isSelectedModelUnavailable({
  modelId,
  currentModel,
  modelsLoaded,
  fast,
  fastAvailable,
}: {
  modelId: string
  currentModel: ApiModel | undefined
  modelsLoaded: boolean
  fast?: boolean
  fastAvailable: boolean
}): boolean {
  if (!modelsLoaded || !modelId.trim()) return false
  if (fast && fastAvailable) return false
  return !currentModel
}
