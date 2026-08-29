export interface AdminOverviewHealthInput {
  settings: Record<string, unknown>
  enabledChannelCount: number
  modelIds: ReadonlySet<string>
  paymentChannelCount: number
  paymentMethodCount: number
}

function readString(settings: Record<string, unknown>, key: string): string {
  const value = settings[key]
  return typeof value === 'string' ? value.trim() : ''
}

export function getOverviewHealth(data: AdminOverviewHealthInput) {
  const defaultModel = readString(data.settings, 'default_model_id')
  const taskModel = readString(data.settings, 'task_model_id')
  const emailVerification = data.settings.email_verification_required === true
  const smtpReady = Boolean(readString(data.settings, 'smtp_host') && readString(data.settings, 'smtp_from'))
  const storageProvider = readString(data.settings, 'storage_provider')
  const storageReady =
    storageProvider === 'local' ||
    (storageProvider === 's3' && Boolean(readString(data.settings, 'storage_s3_bucket'))) ||
    (storageProvider === 'aliyun_oss' &&
      Boolean(
        readString(data.settings, 'storage_aliyun_bucket') &&
        readString(data.settings, 'storage_aliyun_endpoint') &&
        readString(data.settings, 'storage_aliyun_access_key_id') &&
        readString(data.settings, 'storage_aliyun_access_key_secret'),
      ))

  const channelReady = data.enabledChannelCount > 0
  const defaultModelReady = Boolean(defaultModel && data.modelIds.has(defaultModel))
  const taskModelInherited = taskModel === ''
  const taskModelReady = taskModelInherited || data.modelIds.has(taskModel)
  const emailReady = !emailVerification || smtpReady
  const paymentsReady = data.paymentChannelCount === 0 || data.paymentMethodCount > 0

  return {
    emailVerification,
    smtpReady,
    storageProvider,
    storageReady,
    channelReady,
    defaultModelReady,
    taskModelInherited,
    taskModelReady,
    emailReady,
    paymentsReady,
    allReady: channelReady && defaultModelReady && taskModelReady && emailReady && storageReady && paymentsReady,
  }
}
