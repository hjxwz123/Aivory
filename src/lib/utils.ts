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
 * Format a Date relative to now ("Today", "Yesterday", "Mon", "Mar 12").
 */
export function formatRelativeDate(date: Date | string | number): string {
  const d = typeof date === 'number' || typeof date === 'string' ? new Date(date) : date
  const now = new Date()
  const diffMs = now.getTime() - d.getTime()
  const day = 24 * 60 * 60 * 1000
  const diffDays = Math.floor(diffMs / day)
  if (diffDays === 0) return 'Today'
  if (diffDays === 1) return 'Yesterday'
  if (diffDays < 7) return d.toLocaleDateString(undefined, { weekday: 'short' })
  if (diffDays < 365) return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
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
