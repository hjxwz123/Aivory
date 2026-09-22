import { describe, expect, it } from 'vitest'
import {
  ARTIFACT_PANEL_DEFAULT_WIDTH,
  ARTIFACT_PANEL_MAX_WIDTH,
  ARTIFACT_PANEL_MIN_WIDTH,
  ARTIFACT_PANEL_RESERVED_SPACE,
  ARTIFACT_PANEL_STEP,
  artifactPanelWidthForKey,
  clampArtifactPanelWidth,
} from '@/lib/artifact-panel-width'

describe('artifact panel width', () => {
  it('falls back to the default for unusable persisted values', () => {
    expect(clampArtifactPanelWidth(undefined, 1440)).toBe(ARTIFACT_PANEL_DEFAULT_WIDTH)
    expect(clampArtifactPanelWidth(Number.NaN, 1440)).toBe(ARTIFACT_PANEL_DEFAULT_WIDTH)
    expect(clampArtifactPanelWidth('520', 1440)).toBe(ARTIFACT_PANEL_DEFAULT_WIDTH)
    expect(clampArtifactPanelWidth(null, 1440)).toBe(ARTIFACT_PANEL_DEFAULT_WIDTH)
  })

  it('keeps pointer- and keyboard-derived widths inside the desktop range', () => {
    expect(clampArtifactPanelWidth(ARTIFACT_PANEL_MIN_WIDTH - 90, 1440)).toBe(ARTIFACT_PANEL_MIN_WIDTH)
    expect(clampArtifactPanelWidth(ARTIFACT_PANEL_MAX_WIDTH + 90, 1440)).toBe(ARTIFACT_PANEL_MAX_WIDTH)
    expect(clampArtifactPanelWidth(511.6, 1440)).toBe(512)
  })

  it('never lets the panel squeeze the conversation column out of a narrow window', () => {
    // 1024px is the desktop breakpoint: 360px reserved leaves 664px usable, so
    // only the 672px cap is out of reach.
    expect(clampArtifactPanelWidth(ARTIFACT_PANEL_MAX_WIDTH, 1024)).toBe(1024 - ARTIFACT_PANEL_RESERVED_SPACE)
    // Below the minimum + reserved the minimum still wins, because a panel the
    // user cannot read is worse than a cramped conversation column.
    expect(clampArtifactPanelWidth(600, 500)).toBe(ARTIFACT_PANEL_MIN_WIDTH)
    // A default that does not fit is shrunk rather than overflowing.
    expect(clampArtifactPanelWidth(ARTIFACT_PANEL_DEFAULT_WIDTH, 700)).toBe(700 - ARTIFACT_PANEL_RESERVED_SPACE)
  })

  it('moves the divider itself for arrow keys, Home and End', () => {
    // The handle sits on the panel's left edge: ArrowLeft widens the panel.
    expect(artifactPanelWidthForKey(400, 'ArrowLeft')).toBe(400 + ARTIFACT_PANEL_STEP)
    expect(artifactPanelWidthForKey(400, 'ArrowRight')).toBe(400 - ARTIFACT_PANEL_STEP)
    expect(artifactPanelWidthForKey(ARTIFACT_PANEL_MIN_WIDTH, 'ArrowRight')).toBe(ARTIFACT_PANEL_MIN_WIDTH)
    expect(artifactPanelWidthForKey(ARTIFACT_PANEL_MAX_WIDTH, 'ArrowLeft')).toBe(ARTIFACT_PANEL_MAX_WIDTH)
    expect(artifactPanelWidthForKey(400, 'Home')).toBe(ARTIFACT_PANEL_MIN_WIDTH)
    expect(artifactPanelWidthForKey(400, 'End')).toBeGreaterThanOrEqual(ARTIFACT_PANEL_MIN_WIDTH)
    expect(artifactPanelWidthForKey(400, 'Enter')).toBeNull()
    expect(artifactPanelWidthForKey(400, 'Tab')).toBeNull()
  })
})
