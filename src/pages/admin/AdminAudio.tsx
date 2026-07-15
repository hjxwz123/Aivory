/**
 * AdminAudio — configure the composer microphone's speech-to-text.
 *
 * Two providers share the same live-reloaded settings bag:
 *  - "gpt": an OpenAI-compatible /v1/audio/transcriptions endpoint. The mic
 *    records a clip, then POSTs it (see audio_handlers.go).
 *  - "volcano": 火山引擎 豆包 streaming ASR. The mic streams 16 kHz PCM over a
 *    WebSocket and text appears live (see audio_stream_handler.go + volcano_asr.go).
 *
 * Secrets come back masked ("••••••") on load; the backend skips writing the
 * mask, so saving without retyping a key preserves it (mirrors AdminTools).
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/hooks/use-toast'
import { PanelFallback } from '@/components/ui/panel-fallback'

const STRING_KEYS = [
  'audio_transcribe_provider',
  'audio_transcribe_base_url',
  'audio_transcribe_api_key',
  'audio_transcribe_model',
  'volcano_asr_app_id',
  'volcano_asr_access_token',
  'volcano_asr_resource_id',
  'volcano_asr_ws_url',
  'volcano_asr_model_name',
] as const

const BOOL_KEYS = ['volcano_asr_enable_itn', 'volcano_asr_enable_punc', 'volcano_asr_enable_ddc'] as const
const BOOL_DEFAULTS: Record<string, boolean> = {
  volcano_asr_enable_itn: true,
  volcano_asr_enable_punc: true,
  volcano_asr_enable_ddc: false,
}

export default function AdminAudio() {
  const { t } = useTranslation(['admin', 'common'])
  const [draft, setDraft] = useState<Record<string, unknown>>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    adminApi
      .settings()
      .then((s) => setDraft(s))
      .catch((e) => toast.error(e instanceof ApiError ? e.message : t('admin:common.failed')))
      .finally(() => setLoading(false))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const read = (k: string, fallback = '') => (typeof draft[k] === 'string' ? (draft[k] as string) : fallback)
  const readBool = (k: string) => (typeof draft[k] === 'boolean' ? (draft[k] as boolean) : BOOL_DEFAULTS[k] ?? false)
  const set = (k: string, v: string | boolean) => setDraft((d) => ({ ...d, [k]: v }))

  const provider = read('audio_transcribe_provider') || 'gpt'

  async function save() {
    setSaving(true)
    try {
      const patch: Record<string, unknown> = {}
      for (const k of STRING_KEYS) patch[k] = k === 'audio_transcribe_provider' ? provider : read(k)
      for (const k of BOOL_KEYS) patch[k] = readBool(k)
      await adminApi.updateSettings(patch)
      toast.success(t('admin:audio.saved'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:audio.title')}</h1>
        <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:audio.lead')}</p>
      </header>

      {loading ? (
        <PanelFallback />
      ) : (
        <section className="mt-8 flex flex-col gap-5">
          {/* Provider selector -------------------------------------------- */}
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
            <Field
              label={t('admin:audio.provider.label')}
              htmlFor="a-provider"
              hint={t('admin:audio.provider.hint')}
            >
              <Select value={provider} onValueChange={(v) => set('audio_transcribe_provider', v)}>
                <SelectTrigger id="a-provider">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="gpt">{t('admin:audio.provider.gpt')}</SelectItem>
                  <SelectItem value="volcano">{t('admin:audio.provider.volcano')}</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </div>

          {/* GPT / OpenAI-compatible -------------------------------------- */}
          {provider === 'gpt' && (
            <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5 flex flex-col gap-5">
              <Field label={t('admin:audio.fields.model')} htmlFor="a-model" hint={t('admin:audio.fields.modelHint')}>
                <Input
                  id="a-model"
                  value={read('audio_transcribe_model')}
                  onChange={(e) => set('audio_transcribe_model', e.target.value)}
                  placeholder="whisper-1"
                />
              </Field>
              <Field label={t('admin:audio.fields.baseUrl')} htmlFor="a-base" hint={t('admin:audio.fields.baseUrlHint')}>
                <Input
                  id="a-base"
                  value={read('audio_transcribe_base_url')}
                  onChange={(e) => set('audio_transcribe_base_url', e.target.value)}
                  placeholder="https://api.openai.com"
                />
              </Field>
              <Field label={t('admin:audio.fields.apiKey')} htmlFor="a-key" hint={t('admin:audio.fields.apiKeyHint')}>
                <Input
                  id="a-key"
                  type="password"
                  autoComplete="off"
                  value={read('audio_transcribe_api_key')}
                  onChange={(e) => set('audio_transcribe_api_key', e.target.value)}
                  placeholder="sk-…"
                />
              </Field>
            </div>
          )}

          {/* Volcano 豆包 streaming ASR ----------------------------------- */}
          {provider === 'volcano' && (
            <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5 flex flex-col gap-5">
              <p className="text-xs text-[var(--color-fg-subtle)]">{t('admin:audio.volcano.sectionHint')}</p>
              <Field label={t('admin:audio.volcano.appId')} htmlFor="v-app" hint={t('admin:audio.volcano.appIdHint')}>
                <Input
                  id="v-app"
                  value={read('volcano_asr_app_id')}
                  onChange={(e) => set('volcano_asr_app_id', e.target.value)}
                  placeholder="1234567890"
                />
              </Field>
              <Field
                label={t('admin:audio.volcano.accessToken')}
                htmlFor="v-token"
                hint={t('admin:audio.volcano.accessTokenHint')}
              >
                <Input
                  id="v-token"
                  type="password"
                  autoComplete="off"
                  value={read('volcano_asr_access_token')}
                  onChange={(e) => set('volcano_asr_access_token', e.target.value)}
                  placeholder="••••••"
                />
              </Field>
              <Field
                label={t('admin:audio.volcano.resourceId')}
                htmlFor="v-res"
                hint={t('admin:audio.volcano.resourceIdHint')}
              >
                <Input
                  id="v-res"
                  value={read('volcano_asr_resource_id')}
                  onChange={(e) => set('volcano_asr_resource_id', e.target.value)}
                  placeholder="volc.bigasr.sauc.duration"
                />
              </Field>
              <Field label={t('admin:audio.volcano.wsUrl')} htmlFor="v-url" hint={t('admin:audio.volcano.wsUrlHint')}>
                <Input
                  id="v-url"
                  type="url"
                  value={read('volcano_asr_ws_url')}
                  onChange={(e) => set('volcano_asr_ws_url', e.target.value)}
                  placeholder="wss://openspeech.bytedance.com/api/v3/sauc/bigmodel"
                />
              </Field>
              <Field label={t('admin:audio.volcano.model')} htmlFor="v-model" hint={t('admin:audio.volcano.modelHint')}>
                <Input
                  id="v-model"
                  value={read('volcano_asr_model_name')}
                  onChange={(e) => set('volcano_asr_model_name', e.target.value)}
                  placeholder="bigmodel"
                />
              </Field>

              <div className="mt-1 flex flex-col gap-3 border-t border-[var(--color-divider)] pt-4">
                {(
                  [
                    ['volcano_asr_enable_punc', 'punc'],
                    ['volcano_asr_enable_itn', 'itn'],
                    ['volcano_asr_enable_ddc', 'ddc'],
                  ] as const
                ).map(([key, label]) => (
                  <label
                    key={key}
                    className="flex items-center justify-between gap-4 rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3"
                  >
                    <span>
                      <span className="block text-sm font-medium text-[var(--color-fg)]">
                        {t(`admin:audio.volcano.${label}`)}
                      </span>
                      <span className="mt-0.5 block text-[12.5px] text-[var(--color-fg-muted)]">
                        {t(`admin:audio.volcano.${label}Hint`)}
                      </span>
                    </span>
                    <Switch checked={readBool(key)} onCheckedChange={(v) => set(key, v)} />
                  </label>
                ))}
              </div>
            </div>
          )}

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
