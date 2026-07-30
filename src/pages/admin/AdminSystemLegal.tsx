import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Field } from '@/components/ui/label'
import { toast } from '@/hooks/use-toast'
import { PanelFallback } from '@/components/ui/panel-fallback'

type Settings = Record<string, unknown>

const OWNED_KEYS = ['contact_email', 'terms_text', 'privacy_text'] as const

export default function AdminSystemLegal() {
  const { t } = useTranslation(['admin', 'common'])
  const [draft, setDraft] = useState<Settings>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    adminApi.settings()
      .then(setDraft)
      .catch((error) => toast.error(error instanceof ApiError ? error.message : t('admin:common.failed')))
      .finally(() => setLoading(false))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function readString(key: string): string {
    return typeof draft[key] === 'string' ? draft[key] : ''
  }

  async function save() {
    const contactEmail = readString('contact_email').trim()
    if (contactEmail && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(contactEmail)) {
      toast.error(t('admin:settings.fields.contactEmailInvalid'))
      return
    }

    setSaving(true)
    try {
      const patch: Settings = {}
      for (const key of OWNED_KEYS) {
        if (key in draft) patch[key] = draft[key]
      }
      patch.contact_email = contactEmail
      await adminApi.updateSettings(patch)
      setDraft((current) => ({ ...current, contact_email: contactEmail }))
      toast.success(t('admin:settings.saved'))
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">
          {t('admin:menu.legalContact', { defaultValue: 'Legal and contact information' })}
        </h1>
        <p className="mt-2 max-w-3xl text-sm text-[var(--color-fg-muted)]">
          {t('admin:settings.fields.legalLead')}
        </p>
      </header>

      {loading ? (
        <PanelFallback />
      ) : (
        <section className="mt-8 flex flex-col gap-5">
          <Field
            label={t('admin:settings.fields.contactEmail')}
            htmlFor="contact-email"
            hint={t('admin:settings.fields.contactEmailHint')}
          >
            <Input
              id="contact-email"
              type="email"
              maxLength={320}
              value={readString('contact_email')}
              placeholder="admin@aivory.local"
              onChange={(event) => setDraft((current) => ({ ...current, contact_email: event.target.value }))}
            />
          </Field>

          <Field
            label={t('admin:settings.fields.termsText')}
            htmlFor="terms-text"
            hint={t('admin:settings.fields.termsTextHint')}
          >
            <Textarea
              id="terms-text"
              value={readString('terms_text')}
              maxLength={100000}
              className="min-h-56 resize-y font-mono text-[13px]"
              placeholder={t('admin:settings.fields.policyTextPlaceholder')}
              onChange={(event) => setDraft((current) => ({ ...current, terms_text: event.target.value }))}
            />
          </Field>

          <Field
            label={t('admin:settings.fields.privacyText')}
            htmlFor="privacy-text"
            hint={t('admin:settings.fields.privacyTextHint')}
          >
            <Textarea
              id="privacy-text"
              value={readString('privacy_text')}
              maxLength={100000}
              className="min-h-56 resize-y font-mono text-[13px]"
              placeholder={t('admin:settings.fields.policyTextPlaceholder')}
              onChange={(event) => setDraft((current) => ({ ...current, privacy_text: event.target.value }))}
            />
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
