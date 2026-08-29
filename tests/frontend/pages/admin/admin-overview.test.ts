import { describe, expect, it } from 'vitest'
import { getOverviewHealth } from '@/lib/admin-overview'

type OverviewInput = Parameters<typeof getOverviewHealth>[0]

function overviewData(patch: Partial<OverviewInput> = {}): OverviewInput {
  return {
    settings: {
      default_model_id: 'chat-default',
      task_model_id: 'task-default',
      storage_provider: 'local',
    },
    enabledChannelCount: 1,
    modelIds: new Set(['chat-default', 'task-default']),
    paymentChannelCount: 0,
    paymentMethodCount: 0,
    ...patch,
  }
}

describe('admin overview health status', () => {
  it('marks the overview ready only when all six health checks pass', () => {
    const status = getOverviewHealth(overviewData())

    expect(status).toMatchObject({
      channelReady: true,
      defaultModelReady: true,
      taskModelReady: true,
      emailReady: true,
      storageReady: true,
      paymentsReady: true,
      allReady: true,
    })
  })

  it('treats an unset task model as inheriting the current conversation model', () => {
    const status = getOverviewHealth(
      overviewData({ settings: { default_model_id: 'chat-default', task_model_id: '', storage_provider: 'local' } }),
    )

    expect(status.taskModelInherited).toBe(true)
    expect(status.taskModelReady).toBe(true)
    expect(status.allReady).toBe(true)
  })

  it.each([
    ['channel', overviewData({ enabledChannelCount: 0 })],
    ['default model', overviewData({ settings: { default_model_id: 'missing', task_model_id: 'task-default', storage_provider: 'local' } })],
    ['task model', overviewData({ settings: { default_model_id: 'chat-default', task_model_id: 'missing', storage_provider: 'local' } })],
    ['SMTP', overviewData({ settings: { default_model_id: 'chat-default', task_model_id: 'task-default', storage_provider: 'local', email_verification_required: true } })],
    ['storage', overviewData({ settings: { default_model_id: 'chat-default', task_model_id: 'task-default', storage_provider: 's3' } })],
    ['payment method', overviewData({ paymentChannelCount: 1, paymentMethodCount: 0 })],
  ])('keeps health visible when %s is not ready', (_check, input) => {
    expect(getOverviewHealth(input).allReady).toBe(false)
  })
})
