import { describe, expect, it } from 'vitest'
import enAdmin from '@/i18n/locales/en/admin.json'
import frAdmin from '@/i18n/locales/fr/admin.json'
import jaAdmin from '@/i18n/locales/ja/admin.json'
import zhHantAdmin from '@/i18n/locales/zh-Hant/admin.json'
import zhAdmin from '@/i18n/locales/zh/admin.json'
import enChat from '@/i18n/locales/en/chat.json'
import frChat from '@/i18n/locales/fr/chat.json'
import jaChat from '@/i18n/locales/ja/chat.json'
import zhHantChat from '@/i18n/locales/zh-Hant/chat.json'
import zhChat from '@/i18n/locales/zh/chat.json'
import enKb from '@/i18n/locales/en/kb.json'
import frKb from '@/i18n/locales/fr/kb.json'
import jaKb from '@/i18n/locales/ja/kb.json'
import zhHantKb from '@/i18n/locales/zh-Hant/kb.json'
import zhKb from '@/i18n/locales/zh/kb.json'

// Keys the embedding-model guards (embedding_guard.go) and the MinerU
// parser-not-configured hint (documentErrorCode) localize. Losing one silently
// degrades the admin/user-facing copy back to a raw snake_case code.
const REQUIRED = {
  admin: [
    'documents.embeddingModelLockedError',
    'documents.embeddingModelInUseError',
    'documents.embeddingModelDanglingHint',
  ],
  chat: ['composer.parserNotConfigured', 'composer.parserNotConfiguredToast'],
  kb: ['detail.parserNotConfigured'],
} as const

const locales = {
  en: { admin: enAdmin, chat: enChat, kb: enKb },
  fr: { admin: frAdmin, chat: frChat, kb: frKb },
  ja: { admin: jaAdmin, chat: jaChat, kb: jaKb },
  'zh-Hant': { admin: zhHantAdmin, chat: zhHantChat, kb: zhHantKb },
  zh: { admin: zhAdmin, chat: zhChat, kb: zhKb },
} as const

function lookup(messages: Record<string, unknown>, path: string): unknown {
  let current: unknown = messages
  for (const part of path.split('.')) {
    if (typeof current !== 'object' || current === null) return undefined
    current = (current as Record<string, unknown>)[part]
  }
  return current
}

describe('embedding guard + parser-not-configured translations', () => {
  it('provides every guard key in every supported language', () => {
    for (const [locale, namespaces] of Object.entries(locales)) {
      for (const ns of ['admin', 'chat', 'kb'] as const) {
        for (const key of REQUIRED[ns]) {
          const value = lookup(namespaces[ns] as unknown as Record<string, unknown>, key)
          expect(typeof value === 'string' && value.trim(), `${locale}: ${ns}:${key}`).toBeTruthy()
        }
      }
    }
  })
})
