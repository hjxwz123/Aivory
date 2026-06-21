export type ThemePref = 'light' | 'dark' | 'system'
export type DensityPref = 'cozy' | 'comfortable'
export type FontSizePref = 'sm' | 'md' | 'lg'
export type AccentPref = 'violet' | 'lagoon' | 'ember' | 'moss' | 'indigo' | 'rose' | 'mono'
/** Body typeface preset. 'default' = Geist (brand); the rest override --font-sans. */
export type FontPref = 'default' | 'inter' | 'system' | 'serif'
/** Chat content width. 'comfortable' = centered editorial column (default);
 *  'full' = use the whole chat pane, keeping only a safe gutter. */
export type ChatWidthPref = 'comfortable' | 'full'

export const ACCENT_PRESETS: readonly AccentPref[] = ['violet', 'lagoon', 'ember', 'moss', 'indigo', 'rose', 'mono']
export const FONT_PRESETS: readonly FontPref[] = ['default', 'inter', 'system', 'serif']

export interface AppearanceSettings {
  theme: ThemePref
  density: DensityPref
  fontSize: FontSizePref
  font: FontPref
  chatWidth: ChatWidthPref
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
