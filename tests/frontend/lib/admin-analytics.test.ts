import { describe, expect, it } from 'vitest'
import type { ApiAnalytics, ApiUsageTotals, ApiUsageTrendPoint } from '@/api/types'
import {
  alignedAnalyticsTrend,
  analyticsMetricValue,
  inputOutputTokens,
  periodChange,
  processedTokens,
  retainSelectedOption,
  safeRatio,
} from '@/lib/admin-analytics'

function usageTotals(patch: Partial<ApiUsageTotals> = {}): ApiUsageTotals {
  return {
    calls: 0,
    turns: 0,
    credit_charged_turns: 0,
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    images_count: 0,
    cost: 0,
    credits: 0,
    turn_cost: 0,
    credit_charged_cost: 0,
    users: 0,
    credit_charged_users: 0,
    conversations: 0,
    workspaces: 0,
    ...patch,
  }
}

function trendPoint(bucketStart: number, patch: Partial<ApiUsageTrendPoint> = {}): ApiUsageTrendPoint {
  return {
    bucket_start: bucketStart,
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    images_count: 0,
    calls: 0,
    turns: 0,
    users: 0,
    cost: 0,
    credits: 0,
    ...patch,
  }
}

function analyticsFixture(patch: Partial<ApiAnalytics> = {}): ApiAnalytics {
  return {
    days: 1,
    bucket: 3_600,
    generated_at: 14_399,
    period_start: 3_600,
    period_end: 14_400,
    previous_period_start: -7_200,
    previous_period_end: 3_600,
    totals: usageTotals(),
    previous_totals: usageTotals(),
    trend: [],
    previous_trend: [],
    breakdowns: {
      model: [],
      user: [],
      workspace: [],
      purpose: [],
      channel: [],
    },
    filter_options: {
      model: [],
      user: [],
      workspace: [],
      purpose: [],
      channel: [],
    },
    ...patch,
  }
}

describe('admin analytics calculations', () => {
  it('keeps headline tokens separate from the full processed-token composition', () => {
    const totals = usageTotals({
      input_tokens: 120,
      output_tokens: 30,
      cache_read_tokens: 70,
      cache_write_tokens: 10,
    })
    const point = trendPoint(3_600, totals)

    expect(inputOutputTokens(totals)).toBe(150)
    expect(processedTokens(totals)).toBe(230)
    expect(analyticsMetricValue(point, 'tokens')).toBe(150)
  })

  it('returns stable values for zero denominators and periods with no baseline', () => {
    expect(safeRatio(8, 0)).toBe(0)
    expect(safeRatio(0, 0)).toBe(0)
    expect(periodChange(0, 0)).toBe(0)
    expect(periodChange(8, 0)).toBeNull()
    expect(periodChange(15, 10)).toBeCloseTo(0.5)
    expect(periodChange(5, 10)).toBeCloseTo(-0.5)
  })

  it('retains an active filter when a new period no longer returns that option', () => {
    const options = [{ value: 'model-current', label: 'Current model' }]

    expect(retainSelectedOption(options, '__all__', '__all__', 'unused')).toBe(options)
    expect(retainSelectedOption(options, 'model-current', '__all__', 'unused')).toBe(options)
    expect(retainSelectedOption(options, 'model-history', '__all__', 'Historical model')).toEqual([
      { value: 'model-history', label: 'Historical model' },
      ...options,
    ])
  })

  it('fills sparse current and previous series and aligns them by period position', () => {
    const data = analyticsFixture({
      trend: [
        trendPoint(3_600, { turns: 2 }),
        trendPoint(10_800, { turns: 6 }),
      ],
      previous_trend: [trendPoint(-3_600, { turns: 4 })],
    })

    expect(alignedAnalyticsTrend(data, 'turns')).toEqual([
      { bucketStart: 3_600, current: 2, previous: 0 },
      { bucketStart: 7_200, current: 0, previous: 4 },
      { bucketStart: 10_800, current: 6, previous: 0 },
    ])
  })

  it('anchors buckets to each period start so equal weekly windows align by relative position', () => {
    const data = analyticsFixture({
      bucket: 700,
      period_start: 1_000,
      period_end: 2_400,
      previous_period_start: -400,
      previous_period_end: 1_000,
      trend: [trendPoint(1_000, { cost: 2 }), trendPoint(1_700, { cost: 3 })],
      previous_trend: [trendPoint(-400, { cost: 4 }), trendPoint(300, { cost: 5 })],
    })

    expect(alignedAnalyticsTrend(data, 'cost')).toEqual([
      { bucketStart: 1_000, current: 2, previous: 4 },
      { bucketStart: 1_700, current: 3, previous: 5 },
    ])
  })

  it('returns no chart points when both periods contain no usage facts', () => {
    expect(alignedAnalyticsTrend(analyticsFixture(), 'turns')).toEqual([])
  })

  it('returns no chart points for an invalid bucket width', () => {
    expect(alignedAnalyticsTrend(analyticsFixture({
      bucket: 0,
      trend: [trendPoint(3_600, { cost: 1 })],
    }), 'cost')).toEqual([])
  })
})
