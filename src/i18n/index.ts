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
import enSubscription from './locales/en/subscription.json'
import enWelcome from './locales/en/welcome.json'

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
import zhSubscription from './locales/zh/subscription.json'
import zhWelcome from './locales/zh/welcome.json'

// Traditional Chinese bundles (generated from zh via OpenCC s2twp)
import zhHantCommon from './locales/zh-Hant/common.json'
import zhHantNav from './locales/zh-Hant/nav.json'
import zhHantLanding from './locales/zh-Hant/landing.json'
import zhHantChat from './locales/zh-Hant/chat.json'
import zhHantAuth from './locales/zh-Hant/auth.json'
import zhHantSettings from './locales/zh-Hant/settings.json'
import zhHantErrors from './locales/zh-Hant/errors.json'
import zhHantProjects from './locales/zh-Hant/projects.json'
import zhHantAdmin from './locales/zh-Hant/admin.json'
import zhHantKb from './locales/zh-Hant/kb.json'
import zhHantMemory from './locales/zh-Hant/memory.json'
import zhHantSubscription from './locales/zh-Hant/subscription.json'
import zhHantWelcome from './locales/zh-Hant/welcome.json'

// Japanese bundles
import jaCommon from './locales/ja/common.json'
import jaNav from './locales/ja/nav.json'
import jaLanding from './locales/ja/landing.json'
import jaChat from './locales/ja/chat.json'
import jaAuth from './locales/ja/auth.json'
import jaSettings from './locales/ja/settings.json'
import jaErrors from './locales/ja/errors.json'
import jaProjects from './locales/ja/projects.json'
import jaAdmin from './locales/ja/admin.json'
import jaKb from './locales/ja/kb.json'
import jaMemory from './locales/ja/memory.json'
import jaSubscription from './locales/ja/subscription.json'
import jaWelcome from './locales/ja/welcome.json'

// French bundles
import frCommon from './locales/fr/common.json'
import frNav from './locales/fr/nav.json'
import frLanding from './locales/fr/landing.json'
import frChat from './locales/fr/chat.json'
import frAuth from './locales/fr/auth.json'
import frSettings from './locales/fr/settings.json'
import frErrors from './locales/fr/errors.json'
import frProjects from './locales/fr/projects.json'
import frAdmin from './locales/fr/admin.json'
import frKb from './locales/fr/kb.json'
import frMemory from './locales/fr/memory.json'
import frSubscription from './locales/fr/subscription.json'
import frWelcome from './locales/fr/welcome.json'

export const SUPPORTED_LANGUAGES = [
  { code: 'en', label: 'English', short: 'EN' },
  { code: 'zh', label: '简体中文', short: '简' },
  { code: 'zh-Hant', label: '繁體中文', short: '繁' },
  { code: 'ja', label: '日本語', short: '日' },
  { code: 'fr', label: 'Français', short: 'FR' },
] as const

export type LanguageCode = (typeof SUPPORTED_LANGUAGES)[number]['code']

export const DEFAULT_NS = 'common'
export const NAMESPACES = ['common', 'nav', 'landing', 'chat', 'auth', 'settings', 'errors', 'projects', 'admin', 'kb', 'memory', 'subscription', 'welcome'] as const

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
    subscription: enSubscription,
    welcome: enWelcome,
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
    subscription: zhSubscription,
    welcome: zhWelcome,
  },
  'zh-Hant': {
    common: zhHantCommon,
    nav: zhHantNav,
    landing: zhHantLanding,
    chat: zhHantChat,
    auth: zhHantAuth,
    settings: zhHantSettings,
    errors: zhHantErrors,
    projects: zhHantProjects,
    admin: zhHantAdmin,
    kb: zhHantKb,
    memory: zhHantMemory,
    subscription: zhHantSubscription,
    welcome: zhHantWelcome,
  },
  ja: {
    common: jaCommon,
    nav: jaNav,
    landing: jaLanding,
    chat: jaChat,
    auth: jaAuth,
    settings: jaSettings,
    errors: jaErrors,
    projects: jaProjects,
    admin: jaAdmin,
    kb: jaKb,
    memory: jaMemory,
    subscription: jaSubscription,
    welcome: jaWelcome,
  },
  fr: {
    common: frCommon,
    nav: frNav,
    landing: frLanding,
    chat: frChat,
    auth: frAuth,
    settings: frSettings,
    errors: frErrors,
    projects: frProjects,
    admin: frAdmin,
    kb: frKb,
    memory: frMemory,
    subscription: frSubscription,
    welcome: frWelcome,
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
      order: ['localStorage', 'navigator'],
      lookupLocalStorage: 'aurelia.lang',
      caches: [],
    },
    returnNull: false,
  })

export default i18n
