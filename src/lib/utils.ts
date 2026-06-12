import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

/**
 * Compose Tailwind class names. Resolves conflicts (e.g. p-2 + p-4 → p-4).
 */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs))
}

/**
 * Sleep utility for mock streaming.
 */
export function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms))
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
