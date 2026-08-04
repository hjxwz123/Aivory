import type { ApiAnalytics, ApiUsageTotals, ApiUsageTrendPoint } from '@/api/types'

export type AnalyticsMetric = 'turns' | 'tokens' | 'cost' | 'credits' | 'users'

export interface AlignedAnalyticsPoint {
  bucketStart: number
  current: number
  previous: number
}

export interface AnalyticsFilterOption {
  value: string
  label: string
}

export function inputOutputTokens(value: Pick<ApiUsageTotals, 'input_tokens' | 'output_tokens'>): number {
  return value.input_tokens + value.output_tokens
}

export function processedTokens(
  value: Pick<ApiUsageTotals, 'input_tokens' | 'output_tokens' | 'cache_read_tokens' | 'cache_write_tokens'>,
): number {
  return value.input_tokens + value.output_tokens + value.cache_read_tokens + value.cache_write_tokens
}

export function safeRatio(numerator: number, denominator: number): number {
  return denominator > 0 ? numerator / denominator : 0
}

export function periodChange(current: number, previous: number): number | null {
  if (previous === 0) return current === 0 ? 0 : null
  return (current - previous) / Math.abs(previous)
}

export function retainSelectedOption(
  options: AnalyticsFilterOption[],
  selected: string,
  allValue: string,
  fallbackLabel: string,
): AnalyticsFilterOption[] {
  if (selected === allValue || options.some((option) => option.value === selected)) return options
  return [{ value: selected, label: fallbackLabel }, ...options]
}

export function analyticsMetricValue(point: ApiUsageTrendPoint, metric: AnalyticsMetric): number {
  if (metric === 'tokens') return point.input_tokens + point.output_tokens
  return point[metric]
}

function bucketStarts(start: number, end: number, width: number): number[] {
  if (width <= 0 || end <= start) return []
  const result: number[] = []
  for (let value = start; value < end; value += width) result.push(value)
  return result
}

export function alignedAnalyticsTrend(data: ApiAnalytics, metric: AnalyticsMetric): AlignedAnalyticsPoint[] {
  if (data.trend.length === 0 && data.previous_trend.length === 0) return []
  const currentBuckets = bucketStarts(data.period_start, data.period_end, data.bucket)
  const previousBuckets = bucketStarts(data.previous_period_start, data.previous_period_end, data.bucket)
  const currentByBucket = new Map(data.trend.map((point) => [point.bucket_start, analyticsMetricValue(point, metric)]))
  const previousByBucket = new Map(
    data.previous_trend.map((point) => [point.bucket_start, analyticsMetricValue(point, metric)]),
  )
  const length = Math.max(currentBuckets.length, previousBuckets.length)
  return Array.from({ length }, (_, index) => ({
    bucketStart: currentBuckets[index] ?? currentBuckets.at(-1) ?? data.period_start,
    current: currentByBucket.get(currentBuckets[index]) ?? 0,
    previous: previousByBucket.get(previousBuckets[index]) ?? 0,
  }))
}
