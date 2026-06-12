/**
 * AdminSettings — global settings: default + task model, compression options,
 * signup gate, daily quotas.
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import type { ApiModel } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/hooks/use-toast'

type Settings = Record<string, unknown>

export default function AdminSettings() {
  const { t } = useTranslation(['admin', 'common'])
  const [models, setModels] = useState<ApiModel[]>([])
  const [draft, setDraft] = useState<Settings>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  async function load() {
    setLoading(true)
    try {
      const [s, m] = await Promise.all([adminApi.settings(), adminApi.models('chat')])
      setDraft(s)
      setModels(m)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function save() {
    setSaving(true)
    try {
      await adminApi.updateSettings(draft)
      toast.success(t('admin:settings.saved'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setSaving(false)
    }
  }

  function readString(key: string, fallback = ''): string {
    const v = draft[key]
    if (typeof v === 'string') return v
    return fallback
  }
  function readNumber(key: string, fallback = 0): number {
    const v = draft[key]
    if (typeof v === 'number') return v
    return fallback
  }
  function readBool(key: string, fallback = false): boolean {
    const v = draft[key]
    if (typeof v === 'boolean') return v
    return fallback
  }

  return (
    <div>
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:settings.title')}</h1>
        <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:settings.lead')}</p>
      </header>

      {loading ? (
        <div className="mt-8 text-sm text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</div>
      ) : (
        <section className="mt-8 flex flex-col gap-5 max-w-xl">
          <Field label={t('admin:settings.fields.defaultModel')} htmlFor="def-model">
            <Select
              value={readString('default_model_id')}
              onValueChange={(v) => setDraft({ ...draft, default_model_id: v })}
            >
              <SelectTrigger id="def-model">
                <SelectValue placeholder={t('admin:settings.fields.pickModel')} />
              </SelectTrigger>
              <SelectContent>
                {models.map((m) => (
                  <SelectItem key={m.id} value={m.id}>
                    {m.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>

          <Field
            label={t('admin:settings.fields.taskModel')}
            htmlFor="task-model"
            hint={t('admin:settings.fields.taskModelHint')}
          >
            <Select
              value={readString('task_model_id')}
              onValueChange={(v) => setDraft({ ...draft, task_model_id: v })}
            >
              <SelectTrigger id="task-model">
                <SelectValue placeholder={t('admin:settings.fields.pickModel')} />
              </SelectTrigger>
              <SelectContent>
                {models.map((m) => (
                  <SelectItem key={m.id} value={m.id}>
                    {m.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>

          <div className="grid grid-cols-2 gap-4">
            <Field label={t('admin:settings.fields.keep')} htmlFor="keep">
              <Input
                id="keep"
                type="number"
                value={String(readNumber('keep_recent_rounds', 6))}
                onChange={(e) => setDraft({ ...draft, keep_recent_rounds: Number(e.target.value) })}
              />
            </Field>
            <Field label={t('admin:settings.fields.sumTokens')} htmlFor="sumtokens">
              <Input
                id="sumtokens"
                type="number"
                value={String(readNumber('summary_max_tokens', 2048))}
                onChange={(e) => setDraft({ ...draft, summary_max_tokens: Number(e.target.value) })}
              />
            </Field>
          </div>

          <ToggleRow
            label={t('admin:settings.fields.compactionEnabled')}
            checked={readBool('compaction_enabled', true)}
            onChange={(v) => setDraft({ ...draft, compaction_enabled: v })}
          />
          <ToggleRow
            label={t('admin:settings.fields.memoryEnabled')}
            checked={readBool('memory_enabled', true)}
            onChange={(v) => setDraft({ ...draft, memory_enabled: v })}
          />
          <ToggleRow
            label={t('admin:settings.fields.signupOpen')}
            checked={readBool('signup_open', true)}
            onChange={(v) => setDraft({ ...draft, signup_open: v })}
          />

          <div className="grid grid-cols-2 gap-4">
            <Field label={t('admin:settings.fields.dailyMessageLimit')} htmlFor="dmsg">
              <Input
                id="dmsg"
                type="number"
                value={String(readNumber('daily_message_limit', 200))}
                onChange={(e) => setDraft({ ...draft, daily_message_limit: Number(e.target.value) })}
              />
            </Field>
            <Field label={t('admin:settings.fields.dailyImageLimit')} htmlFor="dimg">
              <Input
                id="dimg"
                type="number"
                value={String(readNumber('daily_image_limit', 30))}
                onChange={(e) => setDraft({ ...draft, daily_image_limit: Number(e.target.value) })}
              />
            </Field>
          </div>

          <div className="mt-2 border-t border-[var(--color-border)] pt-5">
            <h2 className="text-sm font-medium text-[var(--color-fg)]">
              {t('admin:settings.fields.sandboxSection')}
            </h2>
            <div className="mt-4 flex flex-col gap-5">
              <Field
                label={t('admin:settings.fields.sandboxUrl')}
                htmlFor="sandbox-url"
                hint={t('admin:settings.fields.sandboxUrlHint')}
              >
                <Input
                  id="sandbox-url"
                  type="url"
                  placeholder="http://your-server:48217"
                  value={readString('sandbox_base_url')}
                  onChange={(e) => setDraft({ ...draft, sandbox_base_url: e.target.value })}
                />
              </Field>
              <Field
                label={t('admin:settings.fields.sandboxKey')}
                htmlFor="sandbox-key"
                hint={t('admin:settings.fields.sandboxKeyHint')}
              >
                <Input
                  id="sandbox-key"
                  type="password"
                  autoComplete="off"
                  value={readString('sandbox_api_key')}
                  onChange={(e) => setDraft({ ...draft, sandbox_api_key: e.target.value })}
                />
              </Field>
            </div>
          </div>

          <div className="mt-2 border-t border-[var(--color-border)] pt-5">
            <h2 className="text-sm font-medium text-[var(--color-fg)]">
              {t('admin:settings.fields.storageSection')}
            </h2>
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
              {t('admin:settings.fields.storageLead')}
            </p>
            <div className="mt-4 flex flex-col gap-5">
              <Field
                label={t('admin:settings.fields.storageProvider')}
                htmlFor="storage-provider"
                hint={t('admin:settings.fields.storageProviderHint')}
              >
                <Select
                  value={readString('storage_provider') || 'none'}
                  onValueChange={(v) =>
                    setDraft({ ...draft, storage_provider: v === 'none' ? '' : v })
                  }
                >
                  <SelectTrigger id="storage-provider">
                    <SelectValue placeholder={t('admin:settings.fields.storageProviderPlaceholder')} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">{t('admin:settings.fields.storageNone')}</SelectItem>
                    <SelectItem value="s3">{t('admin:settings.fields.storageS3')}</SelectItem>
                    <SelectItem value="aliyun_oss">{t('admin:settings.fields.storageAliyun')}</SelectItem>
                  </SelectContent>
                </Select>
              </Field>

              <Field
                label={t('admin:settings.fields.storagePrefix')}
                htmlFor="storage-prefix"
                hint={t('admin:settings.fields.storagePrefixHint')}
              >
                <Input
                  id="storage-prefix"
                  placeholder="workspaces/"
                  value={readString('storage_prefix', 'workspaces/')}
                  onChange={(e) => setDraft({ ...draft, storage_prefix: e.target.value })}
                />
              </Field>

              {readString('storage_provider') === 's3' && (
                <div className="flex flex-col gap-5 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4">
                  <Field label={t('admin:settings.fields.s3Bucket')} htmlFor="s3-bucket">
                    <Input
                      id="s3-bucket"
                      value={readString('storage_s3_bucket')}
                      onChange={(e) => setDraft({ ...draft, storage_s3_bucket: e.target.value })}
                    />
                  </Field>
                  <div className="grid grid-cols-2 gap-4">
                    <Field label={t('admin:settings.fields.s3Region')} htmlFor="s3-region">
                      <Input
                        id="s3-region"
                        placeholder="us-east-1"
                        value={readString('storage_s3_region')}
                        onChange={(e) => setDraft({ ...draft, storage_s3_region: e.target.value })}
                      />
                    </Field>
                    <Field
                      label={t('admin:settings.fields.s3Endpoint')}
                      htmlFor="s3-endpoint"
                      hint={t('admin:settings.fields.s3EndpointHint')}
                    >
                      <Input
                        id="s3-endpoint"
                        placeholder="https://s3.amazonaws.com"
                        value={readString('storage_s3_endpoint')}
                        onChange={(e) => setDraft({ ...draft, storage_s3_endpoint: e.target.value })}
                      />
                    </Field>
                  </div>
                  <div className="grid grid-cols-2 gap-4">
                    <Field label={t('admin:settings.fields.s3AccessKey')} htmlFor="s3-ak">
                      <Input
                        id="s3-ak"
                        type="password"
                        autoComplete="off"
                        value={readString('storage_s3_access_key')}
                        onChange={(e) => setDraft({ ...draft, storage_s3_access_key: e.target.value })}
                      />
                    </Field>
                    <Field label={t('admin:settings.fields.s3SecretKey')} htmlFor="s3-sk">
                      <Input
                        id="s3-sk"
                        type="password"
                        autoComplete="off"
                        value={readString('storage_s3_secret_key')}
                        onChange={(e) => setDraft({ ...draft, storage_s3_secret_key: e.target.value })}
                      />
                    </Field>
                  </div>
                </div>
              )}

              {readString('storage_provider') === 'aliyun_oss' && (
                <div className="flex flex-col gap-5 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4">
                  <Field label={t('admin:settings.fields.ossBucket')} htmlFor="oss-bucket">
                    <Input
                      id="oss-bucket"
                      value={readString('storage_aliyun_bucket')}
                      onChange={(e) => setDraft({ ...draft, storage_aliyun_bucket: e.target.value })}
                    />
                  </Field>
                  <Field
                    label={t('admin:settings.fields.ossEndpoint')}
                    htmlFor="oss-endpoint"
                    hint={t('admin:settings.fields.ossEndpointHint')}
                  >
                    <Input
                      id="oss-endpoint"
                      placeholder="https://oss-cn-hangzhou.aliyuncs.com"
                      value={readString('storage_aliyun_endpoint')}
                      onChange={(e) => setDraft({ ...draft, storage_aliyun_endpoint: e.target.value })}
                    />
                  </Field>
                  <div className="grid grid-cols-2 gap-4">
                    <Field label={t('admin:settings.fields.ossAccessKeyId')} htmlFor="oss-akid">
                      <Input
                        id="oss-akid"
                        type="password"
                        autoComplete="off"
                        value={readString('storage_aliyun_access_key_id')}
                        onChange={(e) => setDraft({ ...draft, storage_aliyun_access_key_id: e.target.value })}
                      />
                    </Field>
                    <Field label={t('admin:settings.fields.ossAccessKeySecret')} htmlFor="oss-aks">
                      <Input
                        id="oss-aks"
                        type="password"
                        autoComplete="off"
                        value={readString('storage_aliyun_access_key_secret')}
                        onChange={(e) => setDraft({ ...draft, storage_aliyun_access_key_secret: e.target.value })}
                      />
                    </Field>
                  </div>
                </div>
              )}
            </div>
          </div>

          <div className="mt-2 border-t border-[var(--color-border)] pt-5">
            <h2 className="text-sm font-medium text-[var(--color-fg)]">
              {t('admin:settings.fields.mineruSection')}
            </h2>
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
              {t('admin:settings.fields.mineruLead')}
            </p>
            <div className="mt-4 flex flex-col gap-5">
              <Field
                label={t('admin:settings.fields.mineruBaseUrl')}
                htmlFor="mineru-url"
                hint={t('admin:settings.fields.mineruBaseUrlHint')}
              >
                <Input
                  id="mineru-url"
                  type="url"
                  placeholder="https://mineru.net"
                  value={readString('mineru_api_url')}
                  onChange={(e) => setDraft({ ...draft, mineru_api_url: e.target.value })}
                />
              </Field>
              <Field
                label={t('admin:settings.fields.mineruToken')}
                htmlFor="mineru-token"
                hint={t('admin:settings.fields.mineruTokenHint')}
              >
                <Input
                  id="mineru-token"
                  type="password"
                  autoComplete="off"
                  value={readString('mineru_api_token')}
                  onChange={(e) => setDraft({ ...draft, mineru_api_token: e.target.value })}
                />
              </Field>
            </div>
          </div>

          <div className="mt-2 border-t border-[var(--color-border)] pt-5">
            <h2 className="text-sm font-medium text-[var(--color-fg)]">
              {t('admin:settings.fields.searchSection')}
            </h2>
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
              {t('admin:settings.fields.searchLead')}
            </p>
            <div className="mt-4 flex flex-col gap-5">
              <Field
                label={t('admin:settings.fields.searchProvider')}
                htmlFor="search-provider"
                hint={t('admin:settings.fields.searchProviderHint')}
              >
                <Select
                  value={readString('search_provider') || 'none'}
                  onValueChange={(v) =>
                    setDraft({ ...draft, search_provider: v === 'none' ? '' : v })
                  }
                >
                  <SelectTrigger id="search-provider">
                    <SelectValue placeholder={t('admin:settings.fields.searchProviderPlaceholder')} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">{t('admin:settings.fields.searchNone')}</SelectItem>
                    <SelectItem value="searxng">{t('admin:settings.fields.searchSearxng')}</SelectItem>
                    <SelectItem value="serper">{t('admin:settings.fields.searchSerper')}</SelectItem>
                    <SelectItem value="brave">{t('admin:settings.fields.searchBrave')}</SelectItem>
                  </SelectContent>
                </Select>
              </Field>

              {readString('search_provider') === 'searxng' && (
                <div className="flex flex-col gap-5 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4">
                  <Field
                    label={t('admin:settings.fields.searchBaseUrl')}
                    htmlFor="search-url"
                    hint={t('admin:settings.fields.searchBaseUrlHint')}
                  >
                    <Input
                      id="search-url"
                      type="url"
                      placeholder="https://searxng.your-domain.tld"
                      value={readString('search_base_url')}
                      onChange={(e) => setDraft({ ...draft, search_base_url: e.target.value })}
                    />
                  </Field>
                </div>
              )}

              {(readString('search_provider') === 'serper' ||
                readString('search_provider') === 'brave') && (
                <div className="flex flex-col gap-5 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4">
                  <Field
                    label={t('admin:settings.fields.searchApiKey')}
                    htmlFor="search-key"
                    hint={t('admin:settings.fields.searchApiKeyHint')}
                  >
                    <Input
                      id="search-key"
                      type="password"
                      autoComplete="off"
                      value={readString('search_api_key')}
                      onChange={(e) => setDraft({ ...draft, search_api_key: e.target.value })}
                    />
                  </Field>
                </div>
              )}
            </div>
          </div>

          <div className="mt-2 border-t border-[var(--color-border)] pt-5">
            <h2 className="text-sm font-medium text-[var(--color-fg)]">
              {t('admin:settings.fields.uploadsSection')}
            </h2>
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
              {t('admin:settings.fields.uploadsLead')}
            </p>
            <div className="mt-4 flex flex-col gap-5">
              <Field
                label={t('admin:settings.fields.uploadAllowedExt')}
                htmlFor="upload-ext"
                hint={t('admin:settings.fields.uploadAllowedExtHint')}
              >
                <Input
                  id="upload-ext"
                  placeholder="pdf, docx, txt, png, jpg"
                  value={readString('upload_allowed_extensions')}
                  onChange={(e) =>
                    setDraft({ ...draft, upload_allowed_extensions: e.target.value })
                  }
                />
              </Field>
            </div>
          </div>

          <div className="flex justify-end">
            <Button loading={saving} onClick={() => void save()}>
              {t('common:actions.save')}
            </Button>
          </div>
        </section>
      )}
    </div>
  )
}

function ToggleRow({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="flex items-center justify-between rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5">
      <span className="text-sm">{label}</span>
      <Switch checked={checked} onCheckedChange={onChange} />
    </label>
  )
}
