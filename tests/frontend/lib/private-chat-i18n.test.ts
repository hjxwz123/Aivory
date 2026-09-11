import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en/chat.json'
import zh from '@/i18n/locales/zh/chat.json'
import zhHant from '@/i18n/locales/zh-Hant/chat.json'
import ja from '@/i18n/locales/ja/chat.json'
import fr from '@/i18n/locales/fr/chat.json'
import { isChatShellPath } from '@/lib/app-paths'

describe('private chat isolation and copy', () => {
  it('shares the normal sidebar shell without changing the private request path', () => {
    expect(isChatShellPath('/private-chat')).toBe(true)
  })
  it('explains tab-only retention in the input placeholder', () => {
    expect(zh.private.placeholder).toBe('对话只保存在当前标签页，刷新、离开或清空即销毁。')
  })
  it.each([zh, zhHant, ja, fr])('provides all private mode labels and safe errors', (locale) => {
    expect(Object.keys(locale.private).sort()).toEqual(Object.keys(en.private).sort())
    expect(Object.keys(locale.private.errors).sort()).toEqual(Object.keys(en.private.errors).sort())
    expect(Object.values(locale.private.errors).every((label) => label.length > 0)).toBe(true)
  })
})
