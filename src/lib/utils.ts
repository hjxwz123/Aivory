import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

/**
 * Compose Tailwind class names. Resolves conflicts (e.g. p-2 + p-4 → p-4).
 */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs))
}

/**
 * Allowlist guard for model/tool-controlled URLs (citations, research sources,
 * artifacts). SSE events feed these into `<a href>` / `<img src>` verbatim, and
 * React 19 does NOT block `javascript:` in href — so we must vet the scheme
 * ourselves. Mirrors the defence applied to rendered markdown in
 * `lib/markdown.ts`.
 *
 * Returns the URL only when it is a root-relative path (`/…`, but not the
 * protocol-relative `//…`) or parses to an http:, https:, or mailto: scheme.
 * Anything else (javascript:, vbscript:, data:, blob:, unparsable) → undefined,
 * so the caller can drop the attribute / render a placeholder.
 */
export function safeHref(url?: string): string | undefined {
  if (!url) return undefined
  const trimmed = url.trim()
  if (!trimmed) return undefined
  // Root-relative internal path — allow, but reject protocol-relative `//host`.
  if (trimmed.startsWith('/') && !trimmed.startsWith('//')) return trimmed
  let parsed: URL
  try {
    parsed = new URL(trimmed)
  } catch {
    return undefined
  }
  const allowed = new Set(['http:', 'https:', 'mailto:'])
  return allowed.has(parsed.protocol) ? trimmed : undefined
}

/**
 * Stable pseudo-id without crypto deps. For mock data only.
 */
export function uid(prefix = 'id'): string {
  return `${prefix}_${Math.random().toString(36).slice(2, 9)}${Date.now().toString(36).slice(-4)}`
}

/**
 * Format a date relative to now in the UI language ("今天" / "Today",
 * "昨天" / "Yesterday", a weekday within the week, then a short date). The
 * language comes from <html lang>, which the language store keeps in sync, so
 * call sites that sit inside translated sentences never mix in English.
 */
export function formatRelativeDate(date: Date | string | number): string {
  const d = typeof date === 'number' || typeof date === 'string' ? new Date(date) : date
  const locale = uiLocale()
  const now = new Date()
  const startOf = (value: Date) => new Date(value.getFullYear(), value.getMonth(), value.getDate()).getTime()
  const diffDays = Math.round((startOf(now) - startOf(d)) / (24 * 60 * 60 * 1000))
  if (diffDays === 0 || diffDays === 1) {
    return new Intl.RelativeTimeFormat(locale, { numeric: 'auto' }).format(-diffDays, 'day')
  }
  if (diffDays > 1 && diffDays < 7) return d.toLocaleDateString(locale, { weekday: 'short' })
  if (d.getFullYear() === now.getFullYear()) return d.toLocaleDateString(locale, { month: 'short', day: 'numeric' })
  return d.toLocaleDateString(locale, { month: 'short', day: 'numeric', year: 'numeric' })
}

/** Human-readable byte size (B / KB / MB / GB) with one decimal above 1 KB. */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 1024) return `${Math.max(0, Math.round(bytes || 0))} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value >= 100 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`
}

function uiLocale(): string | undefined {
  if (typeof document === 'undefined') return undefined
  return document.documentElement.lang || undefined
}

/**
 * Absolute date + time (localized, e.g. "2026/06/15 10:42"). For precise
 * timestamps like a user's last-seen, where a relative "Today" hides the detail.
 */
export function formatDateTime(date: Date | string | number): string {
  const d = typeof date === 'number' || typeof date === 'string' ? new Date(date) : date
  return d.toLocaleString(undefined, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}

/**
 * Absolute calendar date (localized, e.g. "June 21, 2026" / "2026年6月21日").
 * Use this for FUTURE dates like a subscription expiry — formatRelativeDate is
 * built for past timestamps and collapses any future date to a weekday ("Tue").
 */
export function formatAbsoluteDate(date: Date | string | number): string {
  const d = typeof date === 'number' || typeof date === 'string' ? new Date(date) : date
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'long', day: 'numeric' })
}

/**
 * Group conversations by relative date bucket.
 */
export type DateBucket = 'today' | 'yesterday' | 'last_7' | 'last_30' | 'older'
export function bucketFor(date: Date | string | number): DateBucket {
  const d = typeof date === 'number' || typeof date === 'string' ? new Date(date) : date
  const now = new Date()
  const diff = Math.floor((now.getTime() - d.getTime()) / (24 * 60 * 60 * 1000))
  if (diff === 0) return 'today'
  if (diff === 1) return 'yesterday'
  if (diff < 7) return 'last_7'
  if (diff < 30) return 'last_30'
  return 'older'
}

export const bucketLabel: Record<DateBucket, string> = {
  today: 'Today',
  yesterday: 'Yesterday',
  last_7: 'Previous 7 days',
  last_30: 'Previous 30 days',
  older: 'Older',
}

/**
 * Truncate string to length with ellipsis.
 */
export function truncate(s: string, max = 60): string {
  if (s.length <= max) return s
  return s.slice(0, max - 1).trimEnd() + '…'
}

/**
 * Detect macOS for showing Cmd vs Ctrl.
 */
export function isMac(): boolean {
  if (typeof navigator === 'undefined') return false
  return /Mac|iPhone|iPad|iPod/.test(navigator.platform)
}

export function modKey(): string {
  return isMac() ? '⌘' : 'Ctrl'
}

/**
 * One compact shortcut label for `mod + [shift +] key`: "⇧⌘O" on macOS,
 * "Ctrl+Shift+O" elsewhere. Every surface that names a shortcut uses it, so
 * the sidebar, tooltips and the command menu spell the same keys alike.
 */
export function formatShortcut(key: string, { shift = false, mac = isMac() }: { shift?: boolean; mac?: boolean } = {}): string {
  if (mac) return `${shift ? '⇧' : ''}⌘${key}`
  return ['Ctrl', shift ? 'Shift' : '', key].filter(Boolean).join('+')
}

/**
 * Localized "time ago" for a past timestamp in ms ("2 小时前", "yesterday"),
 * falling back to a short calendar date after a week.
 */
export function formatTimeAgo(timestamp: number, locale: string, now = Date.now()): string {
  const seconds = Math.round((timestamp - now) / 1000)
  const elapsed = Math.abs(seconds)
  const format = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' })
  if (elapsed < 60) return format.format(0, 'second')
  if (elapsed < 3600) return format.format(Math.round(seconds / 60), 'minute')
  if (elapsed < 86400) return format.format(Math.round(seconds / 3600), 'hour')
  if (elapsed < 7 * 86400) return format.format(Math.round(seconds / 86400), 'day')
  return new Date(timestamp).toLocaleDateString(locale, { month: 'short', day: 'numeric' })
}

/**
 * Cancellable timeout — returns a function that cancels.
 */
export function timeout(fn: () => void, ms: number): () => void {
  const id = setTimeout(fn, ms)
  return () => clearTimeout(id)
}

/**
 * Debounce.
 */
export function debounce<T extends (...args: never[]) => void>(fn: T, ms = 200): (...args: Parameters<T>) => void {
  let id: ReturnType<typeof setTimeout> | null = null
  return (...args: Parameters<T>) => {
    if (id) clearTimeout(id)
    id = setTimeout(() => fn(...args), ms)
  }
}

/**
 * Copy text to clipboard with fallback.
 */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.top = '-9999px'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}
