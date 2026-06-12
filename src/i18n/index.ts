import i18n from 'i18next'
import LanguageDetector from 'i18next-browser-languagedetector'
import { initReactI18next } from 'react-i18next'

// English bundles
import enCommon from './locales/en/common.json'
import enNav from './locales/en/nav.json'
import enLanding from './locales/en/landing.json'
import enChat from './locales/en/chat.json'
import enAuth from './locales/en/auth.json'
import enSettings from './locales/en/settings.json'
import enErrors from './locales/en/errors.json'
import enProjects from './locales/en/projects.json'
import enAdmin from './locales/en/admin.json'
import enKb from './locales/en/kb.json'
import enMemory from './locales/en/memory.json'

// Chinese bundles
import zhCommon from './locales/zh/common.json'
import zhNav from './locales/zh/nav.json'
import zhLanding from './locales/zh/landing.json'
import zhChat from './locales/zh/chat.json'
import zhAuth from './locales/zh/auth.json'
import zhSettings from './locales/zh/settings.json'
import zhErrors from './locales/zh/errors.json'
import zhProjects from './locales/zh/projects.json'
import zhAdmin from './locales/zh/admin.json'
import zhKb from './locales/zh/kb.json'
import zhMemory from './locales/zh/memory.json'

export const SUPPORTED_LANGUAGES = [
  { code: 'en', label: 'English', short: 'EN' },
  { code: 'zh', label: '中文', short: '中' },
] as const

export type LanguageCode = (typeof SUPPORTED_LANGUAGES)[number]['code']

export const DEFAULT_NS = 'common'
export const NAMESPACES = ['common', 'nav', 'landing', 'chat', 'auth', 'settings', 'errors', 'projects', 'admin', 'kb', 'memory'] as const

const resources = {
  en: {
    common: enCommon,
    nav: enNav,
    landing: enLanding,
    chat: enChat,
    auth: enAuth,
    settings: enSettings,
    errors: enErrors,
    projects: enProjects,
    admin: enAdmin,
    kb: enKb,
    memory: enMemory,
  },
  zh: {
    common: zhCommon,
    nav: zhNav,
    landing: zhLanding,
    chat: zhChat,
    auth: zhAuth,
    settings: zhSettings,
    errors: zhErrors,
    projects: zhProjects,
    admin: zhAdmin,
    kb: zhKb,
    memory: zhMemory,
  },
} as const

void i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources,
    fallbackLng: 'en',
    supportedLngs: SUPPORTED_LANGUAGES.map((l) => l.code),
    ns: NAMESPACES as unknown as string[],
    defaultNS: DEFAULT_NS,
    interpolation: { escapeValue: false }, // React already escapes
    detection: {
      order: ['localStorage', 'navigator', 'htmlTag'],
      lookupLocalStorage: 'aurelia.lang',
      caches: ['localStorage'],
    },
    returnNull: false,
  })

export default i18n
