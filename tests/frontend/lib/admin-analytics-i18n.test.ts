import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en/admin.json'
import fr from '@/i18n/locales/fr/admin.json'
import ja from '@/i18n/locales/ja/admin.json'
import zhHant from '@/i18n/locales/zh-Hant/admin.json'
import zh from '@/i18n/locales/zh/admin.json'

type JsonObject = Record<string, unknown>

const locales = { en, fr, ja, 'zh-Hant': zhHant, zh } as const

const requiredAnalyticsKeys = [
  'actions.refresh',
  'actions.usageRecords',
  'breakdown.count',
  'breakdown.empty',
  'breakdown.search',
  'breakdown.sort',
  'breakdown.sortOptions.calls',
  'breakdown.sortOptions.cost',
  'breakdown.sortOptions.credits',
  'breakdown.sortOptions.tokens',
  'breakdown.sortOptions.turns',
  'comparison.current',
  'comparison.delta',
  'comparison.newActivity',
  'comparison.previous',
  'comparison.previousPeriod',
  'details.cacheReadTokens',
  'details.cacheWriteTokens',
  'details.conversations',
  'details.images',
  'details.inputTokens',
  'details.outputTokens',
  'details.workspaces',
  'dimension.channel',
  'dimension.label',
  'dimension.model',
  'dimension.purpose',
  'dimension.user',
  'dimension.workspace',
  'economics.chargedTurnRate',
  'economics.chargedUsers',
  'economics.costCoverage',
  'economics.costPerTurn',
  'economics.costPerUser',
  'economics.creditsPerChargedTurn',
  'economics.includedCost',
  'economics.operationsPerTurn',
  'error.retry',
  'error.stale',
  'error.title',
  'filters.active',
  'filters.allChannels',
  'filters.allModels',
  'filters.allPurposes',
  'filters.allUsers',
  'filters.allWorkspaces',
  'filters.channel',
  'filters.clear',
  'filters.model',
  'filters.personal',
  'filters.purpose',
  'filters.unattributed',
  'filters.user',
  'filters.userPlaceholder',
  'filters.workspace',
  'labels.deletedModel',
  'labels.deletedUser',
  'labels.personal',
  'labels.unattributedChannel',
  'labels.unknown',
  'metric.cost',
  'metric.credits',
  'metric.label',
  'metric.tokens',
  'metric.turns',
  'metric.users',
  'notes.billingDefinition',
  'notes.tokenDefinition',
  'sections.breakdown',
  'sections.economics',
  'sections.tokenComposition',
  'sections.trend',
  'sections.weekly',
  'stats.cost',
  'stats.credits',
  'stats.operations',
  'stats.tokens',
  'stats.turns',
  'stats.users',
  'table.avgCostOperation',
  'table.avgCostTurn',
  'table.chargedTurns',
  'table.cost',
  'table.credits',
  'table.name',
  'table.operations',
  'table.tokens',
  'table.turns',
  'trend.empty',
  'updatedAt',
  'view.feedback',
  'view.label',
  'view.usage',
] as const

function leafEntries(value: unknown, prefix = ''): Array<[string, string]> {
  if (typeof value === 'string') return [[prefix, value]]
  if (!value || typeof value !== 'object' || Array.isArray(value)) return []
  return Object.entries(value as JsonObject).flatMap(([key, child]) =>
    leafEntries(child, prefix ? `${prefix}.${key}` : key),
  )
}

function readString(root: unknown, path: string): string | undefined {
  let value: unknown = root
  for (const segment of path.split('.')) {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
    value = (value as JsonObject)[segment]
  }
  return typeof value === 'string' ? value : undefined
}

function placeholders(value: string): string[] {
  return [...value.matchAll(/\{\{\s*([^}\s]+)\s*\}\}/g)].map((match) => match[1]).sort()
}

describe('admin analytics translations', () => {
  it('keeps the analytics key tree identical across all supported languages', () => {
    const expected = leafEntries(en.analytics).map(([path]) => path).sort()

    for (const [locale, messages] of Object.entries(locales)) {
      expect(leafEntries(messages.analytics).map(([path]) => path).sort(), locale).toEqual(expected)
    }
  })

  it('provides every usage-and-billing UI key and the annual range in every language', () => {
    for (const [locale, messages] of Object.entries(locales)) {
      for (const key of requiredAnalyticsKeys) {
        expect(readString(messages.analytics, key)?.trim(), `${locale}: analytics.${key}`).toBeTruthy()
      }
      expect(messages.usage.range['365'].trim(), `${locale}: usage.range.365`).toBeTruthy()
    }
  })

  it('preserves interpolation variables in every translated analytics message', () => {
    const english = new Map(leafEntries(en.analytics))

    for (const [locale, messages] of Object.entries(locales)) {
      const translated = new Map(leafEntries(messages.analytics))
      for (const [key, value] of english) {
        expect(placeholders(translated.get(key) ?? ''), `${locale}: analytics.${key}`).toEqual(placeholders(value))
      }
    }
  })
})
