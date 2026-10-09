import type { TFunction } from 'i18next'

/** Stored identifiers stay stable across upgrades; labels follow the viewer's language. */
export function auditActionLabel(action: string, t: TFunction<'admin'>): string {
  const key = action.startsWith('auth.') ? action : action.split('.').at(-1) || 'other'
  return t(`logs.actions.${key}`, { defaultValue: t('logs.actions.other') })
}

export function auditTargetLabel(target: string, t: TFunction<'admin'>): string {
  return target ? t(`logs.targetTypes.${target}`, { defaultValue: t('logs.targetTypes.other') }) : '—'
}
