export function configuredUserDefaultModelId(settings: Record<string, unknown> | null | undefined): string {
  const value = settings?.default_model_id
  return typeof value === 'string' ? value.trim() : ''
}

export function resolveNewConversationFastMode(
  settings: Record<string, unknown> | null | undefined,
  fastAvailable: boolean,
  forceAdvanced = false,
): boolean {
  if (forceAdvanced || !fastAvailable) return false
  return configuredUserDefaultModelId(settings) === ''
}
