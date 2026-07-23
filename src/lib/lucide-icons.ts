import * as Icons from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

const ICON_EXPORTS = Icons as unknown as Record<string, unknown>

/** Canonical Lucide component names shown by admin icon pickers. */
export const LUCIDE_ICON_NAMES = Object.freeze(
  Object.keys(Icons)
    .filter((name) =>
      /^[A-Z][A-Za-z0-9]*$/.test(name)
      && !name.endsWith('Icon')
      && !name.startsWith('Lucide'),
    )
    .filter((name) => {
      const value = ICON_EXPORTS[name]
      return typeof value === 'object' || typeof value === 'function'
    })
    .sort(),
)

const LUCIDE_ICON_NAME_SET = new Set<string>(LUCIDE_ICON_NAMES)

function toPascalCase(value: string): string {
  return value
    .split(/[\s_-]+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join('')
}

/** Resolve picker output and legacy kebab/snake/camel names to one Lucide export name. */
export function resolveLucideIconName(value?: string): string | null {
  const trimmed = value?.trim() ?? ''
  if (!trimmed) return null

  const withoutAliasSuffix = trimmed.endsWith('Icon') ? trimmed.slice(0, -4) : trimmed
  const withoutLucidePrefix = withoutAliasSuffix.startsWith('Lucide')
    ? withoutAliasSuffix.slice('Lucide'.length)
    : withoutAliasSuffix
  const candidates = [
    trimmed,
    withoutAliasSuffix,
    withoutLucidePrefix,
    toPascalCase(withoutLucidePrefix),
  ]
  return candidates.find((candidate) => LUCIDE_ICON_NAME_SET.has(candidate)) ?? null
}

/** Resolve a supported name to its Lucide component. */
export function resolveLucideIcon(value?: string): LucideIcon | null {
  const resolved = resolveLucideIconName(value)
  return resolved ? (ICON_EXPORTS[resolved] as LucideIcon) : null
}
