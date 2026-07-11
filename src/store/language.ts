import { create } from 'zustand'
import i18n, { SUPPORTED_LANGUAGES, type LanguageCode } from '@/i18n'
import { persistUserSettings } from '@/lib/user-settings'

const STORAGE_KEY = 'aivory.lang'

function normalizeLanguage(code: unknown): LanguageCode | null {
  if (typeof code !== 'string' || !code) return null
  const codes = SUPPORTED_LANGUAGES.map((l) => l.code) as readonly string[]
  if (codes.includes(code)) return code as LanguageCode
  const lower = code.toLowerCase().replace('_', '-')
  if (lower === 'zh-tw' || lower === 'zh-hk' || lower === 'zh-mo' || lower === 'zh-hant') return 'zh-Hant'
  if (lower === 'zh-cn' || lower === 'zh-sg' || lower === 'zh-hans') return 'zh'
  const base = lower.split('-')[0]
  const found = codes.find((c) => c.toLowerCase().split('-')[0] === base)
  return (found as LanguageCode) ?? null
}

function detect(): LanguageCode {
  if (typeof window === 'undefined') return 'en'
  const stored = normalizeLanguage(localStorage.getItem(STORAGE_KEY))
  if (stored) return stored
  const detected = normalizeLanguage(i18n.language)
  if (detected) return detected
  return detectBrowserLanguage() ?? 'en'
}

function applyLanguage(code: LanguageCode) {
  try {
    localStorage.setItem(STORAGE_KEY, code)
  } catch {
    /* noop */
  }
  void i18n.changeLanguage(code)
  if (typeof document !== 'undefined') document.documentElement.lang = code
}

interface LanguageStore {
  lang: LanguageCode
  applyLang: (code: LanguageCode) => void
  setLang: (code: LanguageCode) => void
  cycle: () => void
}

export const useLanguage = create<LanguageStore>((set) => {
  const initial = detect()
  if (typeof document !== 'undefined') {
    document.documentElement.lang = initial
  }
  // Sync i18next on first load so consumers don't see a brief default-lang flash.
  if (i18n.language !== initial) void i18n.changeLanguage(initial)

  return {
    lang: initial,
    applyLang(code) {
      applyLanguage(code)
      set({ lang: code })
    },
    setLang(code) {
      applyLanguage(code)
      set({ lang: code })
      void persistUserSettings({ language: code }).catch(() => {})
    },
    cycle() {
      const codes = SUPPORTED_LANGUAGES.map((l) => l.code)
      set((s) => {
        const next = codes[(codes.indexOf(s.lang) + 1) % codes.length]
        applyLanguage(next)
        void persistUserSettings({ language: next }).catch(() => {})
        return { lang: next }
      })
    },
  }
})

export function toSupportedLanguage(code: unknown): LanguageCode | null {
  return normalizeLanguage(code)
}

export function detectBrowserLanguage(): LanguageCode | null {
  if (typeof navigator === 'undefined') return null
  for (const code of navigator.languages ?? []) {
    const normalized = normalizeLanguage(code)
    if (normalized) return normalized
  }
  return normalizeLanguage(navigator.language)
}
