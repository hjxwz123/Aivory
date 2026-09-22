/**
 * Geometry for the right-edge artifact panel's drag handle.
 *
 * The panel is a docked column, so its width is the only axis the user can
 * change: dragging the divider trades conversation space for document space
 * (the HTML preview, a Word/Excel/PowerPoint document, the editor beside it).
 *
 * Everything here is pure so the bounds can be unit-tested without a DOM; the
 * persisted value is sanitized through `clampArtifactPanelWidth` on every read,
 * exactly like `sidebar-width`.
 */

/** Narrowest useful panel: a portrait page plus the document's own margins. */
export const ARTIFACT_PANEL_MIN_WIDTH = 288
/**
 * Default when the user has never dragged the divider.
 *
 * Chosen to match the width the panel actually had at common desktop sizes
 * before the divider existed (`clamp(22rem, 34vw, 36rem)` resolves to 510px at
 * 1500px, 435px at 1280px), so nobody who never touches the handle sees the
 * panel change size after upgrading.
 */
export const ARTIFACT_PANEL_DEFAULT_WIDTH = 480
/** Widest the panel may get on a very large display (~42rem). */
export const ARTIFACT_PANEL_MAX_WIDTH = 672
/**
 * Space the conversation column keeps no matter how far the divider is dragged.
 * Above the 1024px desktop breakpoint this leaves every layout a usable chat
 * column instead of letting the panel swallow the window.
 */
export const ARTIFACT_PANEL_RESERVED_SPACE = 360
/** Arrow-key nudge. */
export const ARTIFACT_PANEL_STEP = 16

/**
 * Keep a persisted or pointer-derived width inside the usable range for the
 * CURRENT window. A saved 672px panel on a 1024px-wide window has to shrink,
 * otherwise the conversation column disappears entirely.
 */
export function clampArtifactPanelWidth(
  value: unknown,
  viewportWidth: number = typeof window === 'undefined' ? 1440 : window.innerWidth,
): number {
  const usableMax = Math.max(
    ARTIFACT_PANEL_MIN_WIDTH,
    Math.min(ARTIFACT_PANEL_MAX_WIDTH, Math.floor(viewportWidth) - ARTIFACT_PANEL_RESERVED_SPACE),
  )
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return Math.min(ARTIFACT_PANEL_DEFAULT_WIDTH, usableMax)
  }
  return Math.min(usableMax, Math.max(ARTIFACT_PANEL_MIN_WIDTH, Math.round(value)))
}

/**
 * Keyboard interaction for the divider's ARIA separator.
 *
 * The handle sits on the panel's LEFT edge, so dragging left (or pressing
 * ArrowLeft) makes the panel WIDER — the arrow always moves the divider itself,
 * which matches the sidebar handle's convention.
 */
export function artifactPanelWidthForKey(current: number, key: string): number | null {
  switch (key) {
    case 'ArrowLeft':
      return clampArtifactPanelWidth(current + ARTIFACT_PANEL_STEP)
    case 'ArrowRight':
      return clampArtifactPanelWidth(current - ARTIFACT_PANEL_STEP)
    case 'Home':
      return ARTIFACT_PANEL_MIN_WIDTH
    case 'End':
      return clampArtifactPanelWidth(ARTIFACT_PANEL_MAX_WIDTH)
    default:
      return null
  }
}
