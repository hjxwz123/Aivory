export const SIDEBAR_WIDTH_MIN = 224
export const SIDEBAR_WIDTH_DEFAULT = 280
export const SIDEBAR_WIDTH_MAX = 400
export const SIDEBAR_WIDTH_STEP = 8

/** Keep persisted and pointer-derived widths inside the usable desktop range. */
export function clampSidebarWidth(value: unknown): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) return SIDEBAR_WIDTH_DEFAULT
  return Math.min(SIDEBAR_WIDTH_MAX, Math.max(SIDEBAR_WIDTH_MIN, Math.round(value)))
}

/** Resolve the keyboard interaction for the sidebar's ARIA separator. */
export function sidebarWidthForKey(current: number, key: string): number | null {
  switch (key) {
    case 'ArrowLeft':
      return clampSidebarWidth(current - SIDEBAR_WIDTH_STEP)
    case 'ArrowRight':
      return clampSidebarWidth(current + SIDEBAR_WIDTH_STEP)
    case 'Home':
      return SIDEBAR_WIDTH_MIN
    case 'End':
      return SIDEBAR_WIDTH_MAX
    default:
      return null
  }
}
