import i18n from '@/i18n'
import en from './locales/en/admin.json'
import zh from './locales/zh/admin.json'
import zhHant from './locales/zh-Hant/admin.json'
import ja from './locales/ja/admin.json'
import fr from './locales/fr/admin.json'

const resources = { en, zh, 'zh-Hant': zhHant, ja, fr } as const

for (const [language, messages] of Object.entries(resources)) {
  i18n.addResourceBundle(language, 'admin', messages, true, true)
}
