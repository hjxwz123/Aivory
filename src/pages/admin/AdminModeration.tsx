/**
 * AdminModeration — global content-moderation config (§ moderation).
 *
 * Two screens are available, chosen per-model on the model edit page:
 *   - keyword: the prompt is matched against the keyword list below.
 *   - model:   the prompt is sent to the dedicated moderation model below for an
 *              ALLOW/BLOCK verdict before generation.
 * This page owns only the global pieces (keyword list, moderation model, block
 * message); the per-model enable/mode toggle lives on /admin/models/:id.
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import type { ApiModel } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/hooks/use-toast'

// Radix Select forbids an empty-string item value, so use a sentinel for "none".
const NONE = '__none'

export default function AdminModeration() {
  const { t } = useTranslation(['admin', 'common'])
  const [models, setModels] = useState<ApiModel[]>([])
  const [keywordsText, setKeywordsText] = useState('')
  const [modelId, setModelId] = useState('')
  const [categoriesText, setCategoriesText] = useState('')
  const [message, setMessage] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  async function load() {
    setLoading(true)
    try {
      const [s, m] = await Promise.all([adminApi.settings(), adminApi.models('chat')])
      const kw = Array.isArray(s.moderation_keywords) ? (s.moderation_keywords as string[]) : []
      setKeywordsText(kw.join('\n'))
      const cats = Array.isArray(s.moderation_categories) ? (s.moderation_categories as string[]) : []
      setCategoriesText(cats.join('\n'))
      setModelId(typeof s.moderation_model_id === 'string' ? s.moderation_model_id : '')
      setMessage(typeof s.moderation_message === 'string' ? s.moderation_message : '')
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

  const keywordCount = keywordsText
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean).length

  async function save() {
    setSaving(true)
    try {
      const keywords = keywordsText
        .split('\n')
        .map((s) => s.trim())
        .filter(Boolean)
      const categories = categoriesText
        .split('\n')
        .map((s) => s.trim())
        .filter(Boolean)
      await adminApi.updateSettings({
        moderation_keywords: keywords,
        moderation_categories: categories,
        moderation_model_id: modelId,
        moderation_message: message.trim(),
      })
      toast.success(t('admin:moderation.saved'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="mx-auto max-w-[60rem]">
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:moderation.title')}</h1>
        <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:moderation.lead')}</p>
      </header>

      {loading ? (
        <div className="mt-8 text-sm text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</div>
      ) : (
        <section className="mt-8 flex flex-col gap-6">
          {/* Keyword list */}
          <Field
            label={t('admin:moderation.keywordsLabel')}
            htmlFor="mod-kw"
            hint={t('admin:moderation.keywordsHint')}
          >
            <Textarea
              id="mod-kw"
              rows={10}
              value={keywordsText}
              onChange={(e) => setKeywordsText(e.target.value)}
              placeholder={t('admin:moderation.keywordsPlaceholder')}
              className="font-mono text-[13px]"
            />
            <p className="mt-1.5 text-[12px] text-[var(--color-fg-subtle)] tabular-nums">
              {t('admin:moderation.keywordsCount', { count: keywordCount })}
            </p>
          </Field>

          {/* Moderation model */}
          <Field
            label={t('admin:moderation.modelLabel')}
            htmlFor="mod-model"
            hint={t('admin:moderation.modelHint')}
          >
            <Select value={modelId || NONE} onValueChange={(v) => setModelId(v === NONE ? '' : v)}>
              <SelectTrigger id="mod-model">
                <SelectValue placeholder={t('admin:moderation.modelNone')} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NONE}>{t('admin:moderation.modelNone')}</SelectItem>
                {models.map((m) => (
                  <SelectItem key={m.id} value={m.id}>
                    {m.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>

          {/* Violation categories (model mode) */}
          <Field
            label={t('admin:moderation.categoriesLabel')}
            htmlFor="mod-cats"
            hint={t('admin:moderation.categoriesHint')}
          >
            <Textarea
              id="mod-cats"
              rows={6}
              value={categoriesText}
              onChange={(e) => setCategoriesText(e.target.value)}
              placeholder={t('admin:moderation.categoriesPlaceholder')}
            />
          </Field>

          {/* Block message */}
          <Field
            label={t('admin:moderation.messageLabel')}
            htmlFor="mod-msg"
            hint={t('admin:moderation.messageHint')}
          >
            <Input id="mod-msg" value={message} onChange={(e) => setMessage(e.target.value)} />
          </Field>

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
