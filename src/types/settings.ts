export type ThemePref = 'light' | 'dark' | 'system'
export type DensityPref = 'cozy' | 'comfortable'
export type FontSizePref = 'sm' | 'md' | 'lg'
export type AccentPref = 'violet' | 'lagoon' | 'ember' | 'moss' | 'indigo' | 'rose' | 'mono'
/** Body typeface preset. Each option represents a visibly distinct reading style. */
export type FontPref = 'default' | 'humanist' | 'rounded' | 'serif'
/** Chat content width preset, picked on a snapping slider. 'full' (the default
 *  for new accounts) = a wide editorial column; 'max' goes wider still but keeps
 *  a proportional gutter (never edge-to-edge); 'narrow'/'comfortable'/'wide'
 *  step down from there. */
export type ChatWidthPref = 'narrow' | 'comfortable' | 'wide' | 'full' | 'max'

export const REPLY_ANIMATION_PRESETS = ['paper-plane', 'origami', 'ink', 'sprout', 'orbit', 'prism'] as const
export type ReplyAnimationPref = (typeof REPLY_ANIMATION_PRESETS)[number]
export const DEFAULT_REPLY_ANIMATION: ReplyAnimationPref = 'paper-plane'

export function normalizeReplyAnimationPref(value: unknown): ReplyAnimationPref | undefined {
  return typeof value === 'string' && (REPLY_ANIMATION_PRESETS as readonly string[]).includes(value)
    ? value as ReplyAnimationPref
    : undefined
}

export const ACCENT_PRESETS: readonly AccentPref[] = ['violet', 'lagoon', 'ember', 'moss', 'indigo', 'rose', 'mono']
export const FONT_PRESETS: readonly FontPref[] = ['default', 'humanist', 'rounded', 'serif']

/** Accept persisted values from before the distinct typeface presets shipped. */
export function normalizeFontPref(value: unknown): FontPref | undefined {
  if (value === 'inter') return 'humanist'
  if (value === 'system') return 'rounded'
  return typeof value === 'string' && (FONT_PRESETS as readonly string[]).includes(value)
    ? (value as FontPref)
    : undefined
}

export interface AppearanceSettings {
  theme: ThemePref
  density: DensityPref
  fontSize: FontSizePref
  font: FontPref
  chatWidth: ChatWidthPref
  /** When true, user-authored message bubbles render through the same markdown pipeline as assistant messages. */
  userMessageMarkdown: boolean
  codeBlockWrap: boolean
  replyAnimation: ReplyAnimationPref
}

export interface ModelSettings {
  defaultModelId: string
  customInstructions: string
  responseLength: 'concise' | 'balanced' | 'detailed'
}

export interface PrivacySettings {
  trainingOptOut: boolean
  retainHistory: boolean
  memoriesEnabled: boolean
}
