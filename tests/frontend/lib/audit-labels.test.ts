import { createInstance } from 'i18next'
import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en/admin.json'
import zh from '@/i18n/locales/zh/admin.json'
import zhHant from '@/i18n/locales/zh-Hant/admin.json'
import ja from '@/i18n/locales/ja/admin.json'
import fr from '@/i18n/locales/fr/admin.json'
import { auditActionLabel, auditTargetLabel } from '@/lib/audit-labels'

describe.each(Object.entries({ en, zh, 'zh-Hant': zhHant, ja, fr }))('audit labels in %s', (language, messages) => {
  it('translates administrator operations, authentication and legacy workspace actions', async () => {
    const i18n = createInstance()
    await i18n.init({ lng: language, defaultNS: 'admin', resources: { [language]: { admin: messages } } })
    const t = i18n.getFixedT(language, 'admin')
    expect(auditActionLabel('admin.models.skills', t)).toBe(messages.logs.actions.skills)
    expect(auditActionLabel('admin.models.channels', t)).toBe(messages.logs.actions.channels)
    expect(auditActionLabel('admin.channels.models', t)).toBe(messages.logs.actions.models)
    expect(auditActionLabel('auth.login', t)).toBe(messages.logs.actions.auth.login)
    expect(auditActionLabel('member.role_updated', t)).toBe(messages.logs.actions.role_updated)
    expect(auditActionLabel('admin.future.operation', t)).toBe(messages.logs.actions.other)
    expect(auditTargetLabel('model', t)).toBe(messages.logs.targetTypes.model)
    expect(auditTargetLabel('unknown_resource', t)).toBe(messages.logs.targetTypes.other)
  })
})
