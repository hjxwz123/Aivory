import { describe, expect, it } from 'vitest'
import {
  clampSidebarWidth,
  SIDEBAR_WIDTH_DEFAULT,
  SIDEBAR_WIDTH_MAX,
  SIDEBAR_WIDTH_MIN,
  SIDEBAR_WIDTH_STEP,
  sidebarWidthForKey,
} from './sidebar-width'

describe('sidebar width', () => {
  it('sanitizes persisted and pointer-derived values', () => {
    expect(clampSidebarWidth(undefined)).toBe(SIDEBAR_WIDTH_DEFAULT)
    expect(clampSidebarWidth(Number.NaN)).toBe(SIDEBAR_WIDTH_DEFAULT)
    expect(clampSidebarWidth('320')).toBe(SIDEBAR_WIDTH_DEFAULT)
    expect(clampSidebarWidth(SIDEBAR_WIDTH_MIN - 50)).toBe(SIDEBAR_WIDTH_MIN)
    expect(clampSidebarWidth(SIDEBAR_WIDTH_MAX + 50)).toBe(SIDEBAR_WIDTH_MAX)
    expect(clampSidebarWidth(301.6)).toBe(302)
  })

  it('supports arrows plus Home and End without escaping the bounds', () => {
    expect(sidebarWidthForKey(280, 'ArrowLeft')).toBe(280 - SIDEBAR_WIDTH_STEP)
    expect(sidebarWidthForKey(280, 'ArrowRight')).toBe(280 + SIDEBAR_WIDTH_STEP)
    expect(sidebarWidthForKey(SIDEBAR_WIDTH_MIN, 'ArrowLeft')).toBe(SIDEBAR_WIDTH_MIN)
    expect(sidebarWidthForKey(SIDEBAR_WIDTH_MAX, 'ArrowRight')).toBe(SIDEBAR_WIDTH_MAX)
    expect(sidebarWidthForKey(280, 'Home')).toBe(SIDEBAR_WIDTH_MIN)
    expect(sidebarWidthForKey(280, 'End')).toBe(SIDEBAR_WIDTH_MAX)
    expect(sidebarWidthForKey(280, 'Enter')).toBeNull()
  })
})
