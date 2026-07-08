import { create } from 'zustand'
import type { AppearanceSettings, ChatWidthPref, DensityPref, FontPref, FontSizePref, ModelSettings, PrivacySettings } from '@/types/settings'

const RESPONSE_LENGTHS = ['concise', 'balanced', 'detailed'] as const
const FONTS: readonly FontPref[] = ['default', 'inter', 'system', 'serif']
const CHAT_WIDTHS: readonly ChatWidthPref[] = ['narrow', 'comfortable', 'wide', 'full', 'max']
type ResponseLengthPref = ModelSettings['responseLength']

function isResponseLength(value: string): value is ResponseLengthPref {
  return (RESPONSE_LENGTHS as readonly string[]).includes(value)
}

interface SettingsState {
  appearance: AppearanceSettings
  models: ModelSettings
  privacy: PrivacySettings
  sidebarCollapsed: boolean
  setAppearance: (patch: Partial<AppearanceSettings>) => void
  syncUserSettings: (settings: Record<string, unknown>) => void
  setModels: (patch: Partial<ModelSettings>) => void
  setPrivacy: (patch: Partial<PrivacySettings>) => void
  setSidebarCollapsed: (v: boolean) => void
  toggleSidebar: () => void
}

const KEY = 'aurelia.settings'

function load(): Partial<SettingsState> {
  try {
    const raw = localStorage.getItem(KEY)
    if (!raw) return {}
    return JSON.parse(raw) as Partial<SettingsState>
  } catch {
    return {}
  }
}

function persist(s: Pick<SettingsState, 'appearance' | 'models' | 'privacy' | 'sidebarCollapsed'>) {
  try {
    localStorage.setItem(
      KEY,
      JSON.stringify({
        appearance: s.appearance,
        models: s.models,
        privacy: s.privacy,
        sidebarCollapsed: s.sidebarCollapsed,
      }),
    )
  } catch {
    /* noop */
  }
}

const initial = load()

export const useSettings = create<SettingsState>((set) => ({
  appearance: {
    theme: 'system',
    density: 'comfortable',
    fontSize: 'md',
    font: 'default',
    chatWidth: 'full',
    userMessageMarkdown: false,
    ...(initial.appearance ?? {}),
  },
  models: {
    defaultModelId: '',
    customInstructions: '',
    responseLength: 'balanced',
    ...(initial.models ?? {}),
  },
  privacy: {
    trainingOptOut: false,
    retainHistory: true,
    memoriesEnabled: true,
    ...(initial.privacy ?? {}),
  },
  sidebarCollapsed: initial.sidebarCollapsed ?? false,
  setAppearance(patch) {
    set((s) => {
      const next = { ...s, appearance: { ...s.appearance, ...patch } }
      persist(next)
      return next
    })
  },
  syncUserSettings(settings) {
    set((s) => {
      const appearancePatch: Partial<AppearanceSettings> = {}
      const modelsPatch: Partial<ModelSettings> = {}
      const privacyPatch: Partial<PrivacySettings> = {}

      appearancePatch.userMessageMarkdown = settings.user_message_markdown === true
      if (typeof settings.font_family === 'string' && (FONTS as readonly string[]).includes(settings.font_family)) {
        appearancePatch.font = settings.font_family as FontPref
      }
      if (typeof settings.chat_width === 'string' && (CHAT_WIDTHS as readonly string[]).includes(settings.chat_width)) {
        appearancePatch.chatWidth = settings.chat_width as ChatWidthPref
      }
      if (typeof settings.response_length === 'string') {
        const v = settings.response_length
        if (isResponseLength(v)) {
          modelsPatch.responseLength = v
        }
      }
      if (typeof settings.default_model_id === 'string') {
        modelsPatch.defaultModelId = settings.default_model_id
      }
      if (typeof settings.persona_custom === 'string') {
        modelsPatch.customInstructions = settings.persona_custom
      }
      if (typeof settings.memory_enabled === 'boolean') {
        privacyPatch.memoriesEnabled = settings.memory_enabled
      }

      const next = {
        ...s,
        appearance: { ...s.appearance, ...appearancePatch },
        models: { ...s.models, ...modelsPatch },
        privacy: { ...s.privacy, ...privacyPatch },
      }
      persist(next)
      return next
    })
  },
  setModels(patch) {
    set((s) => {
      const next = { ...s, models: { ...s.models, ...patch } }
      persist(next)
      return next
    })
  },
  setPrivacy(patch) {
    set((s) => {
      const next = { ...s, privacy: { ...s.privacy, ...patch } }
      persist(next)
      return next
    })
  },
  setSidebarCollapsed(v) {
    set((s) => {
      const next = { ...s, sidebarCollapsed: v }
      persist(next)
      return next
    })
  },
  toggleSidebar() {
    set((s) => {
      const next = { ...s, sidebarCollapsed: !s.sidebarCollapsed }
      persist(next)
      return next
    })
  },
}))

/* ---------- DOM side-effects for density + fontSize -------------------- */

function applyDensity(d: DensityPref) {
  if (typeof document === 'undefined') return
  document.documentElement.dataset.density = d
}

function applyFontSize(f: FontSizePref) {
  if (typeof document === 'undefined') return
  document.documentElement.dataset.fontsize = f
}

// Sets the body typeface via data-font (tokens.css swaps --font-sans). Non-brand
// faces that need their own file are lazy-loaded only when selected, so the
// default payload stays lean.
function applyFont(f: FontPref) {
  if (typeof document === 'undefined') return
  document.documentElement.dataset.font = f
  if (f === 'inter') void import('@fontsource-variable/inter')
}

// Chat content width — data-chat-width="full" overrides --layout-message-max-w
// in tokens.css so the chat pane uses its full width (keeping only the gutter).
function applyChatWidth(w: ChatWidthPref) {
  if (typeof document === 'undefined') return
  document.documentElement.dataset.chatWidth = w
}

// Apply on boot
const boot = useSettings.getState().appearance
applyDensity(boot.density)
applyFontSize(boot.fontSize)
applyFont(boot.font)
applyChatWidth(boot.chatWidth)

// Re-apply whenever appearance changes
useSettings.subscribe((state, prev) => {
  if (state.appearance.density !== prev.appearance.density) {
    applyDensity(state.appearance.density)
  }
  if (state.appearance.fontSize !== prev.appearance.fontSize) {
    applyFontSize(state.appearance.fontSize)
  }
  if (state.appearance.font !== prev.appearance.font) {
    applyFont(state.appearance.font)
  }
  if (state.appearance.chatWidth !== prev.appearance.chatWidth) {
    applyChatWidth(state.appearance.chatWidth)
  }
})
