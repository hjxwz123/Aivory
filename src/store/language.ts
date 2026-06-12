import { create } from 'zustand'
import i18n, { SUPPORTED_LANGUAGES, type LanguageCode } from '@/i18n'

const STORAGE_KEY = 'aurelia.lang'

function detect(): LanguageCode {
  if (typeof window === 'undefined') return 'en'
  const stored = localStorage.getItem(STORAGE_KEY)
  if (stored === 'en' || stored === 'zh') return stored
  // i18next-browser-languagedetector already wrote to localStorage; read what it chose
  const detected = (i18n.language || 'en').toLowerCase()
  if (detected.startsWith('zh')) return 'zh'
  return 'en'
}

interface LanguageStore {
  lang: LanguageCode
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
    setLang(code) {
      try {
        localStorage.setItem(STORAGE_KEY, code)
      } catch {
        /* noop */
      }
      void i18n.changeLanguage(code)
      if (typeof document !== 'undefined') document.documentElement.lang = code
      set({ lang: code })
    },
    cycle() {
      const codes = SUPPORTED_LANGUAGES.map((l) => l.code)
      set((s) => {
        const next = codes[(codes.indexOf(s.lang) + 1) % codes.length]
        try {
          localStorage.setItem(STORAGE_KEY, next)
        } catch {
          /* noop */
        }
        void i18n.changeLanguage(next)
        if (typeof document !== 'undefined') document.documentElement.lang = next
        return { lang: next }
      })
    },
  }
})
