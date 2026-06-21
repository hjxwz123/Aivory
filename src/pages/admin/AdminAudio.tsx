/**
 * AdminAudio — configure the voice (speech-to-text) model used by the composer's
 * microphone. Settings are stored globally and live-reloaded on each
 * transcription call (mirrors the search / sandbox config). The backend proxies
 * audio to {base_url}/v1/audio/transcriptions with the given model.
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { toast } from '@/hooks/use-toast'

const KEYS = ['audio_transcribe_base_url', 'audio_transcribe_api_key', 'audio_transcribe_model'] as const

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
  const set = (k: string, v: string) => setDraft((d) => ({ ...d, [k]: v }))

  async function save() {
    setSaving(true)
    try {
      const patch: Record<string, unknown> = {}
      for (const k of KEYS) patch[k] = read(k)
      await adminApi.updateSettings(patch)
      toast.success(t('admin:audio.saved'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="mx-auto max-w-[60rem]">
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:audio.title')}</h1>
        <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:audio.lead')}</p>
      </header>

      <section className="mt-8 flex flex-col gap-5">
        {loading ? (
          <div className="text-sm text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</div>
        ) : (
          <>
            <Field
              label={t('admin:audio.fields.model')}
              htmlFor="a-model"
              hint={t('admin:audio.fields.modelHint')}
            >
              <Input
                id="a-model"
                value={read('audio_transcribe_model')}
                onChange={(e) => set('audio_transcribe_model', e.target.value)}
                placeholder="whisper-1"
              />
            </Field>
            <Field
              label={t('admin:audio.fields.baseUrl')}
              htmlFor="a-base"
              hint={t('admin:audio.fields.baseUrlHint')}
            >
              <Input
                id="a-base"
                value={read('audio_transcribe_base_url')}
                onChange={(e) => set('audio_transcribe_base_url', e.target.value)}
                placeholder="https://api.openai.com"
              />
            </Field>
            <Field
              label={t('admin:audio.fields.apiKey')}
              htmlFor="a-key"
              hint={t('admin:audio.fields.apiKeyHint')}
            >
              <Input
                id="a-key"
                type="password"
                value={read('audio_transcribe_api_key')}
                onChange={(e) => set('audio_transcribe_api_key', e.target.value)}
                placeholder="sk-…"
              />
            </Field>
            <div className="flex justify-end">
              <Button loading={saving} onClick={() => void save()}>
                {t('common:actions.save')}
              </Button>
            </div>
          </>
        )}
      </section>
    </div>
  )
}
