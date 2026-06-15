/**
 * AdminAnnouncement — the global notice shown to users on load (§ announcement).
 *
 * Stored as the single `announcement` setting (JSON). An image makes it an
 * "image announcement" (rendered image-left / text-right in the popup); without
 * one it's a plain text notice. `remember_dismiss` controls whether closing it
 * is remembered (off → it re-shows every visit). Saving stamps `updated_at`,
 * which doubles as the dismiss version so an edited notice re-appears for
 * everyone who had dismissed the previous one.
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Megaphone, Upload, X } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/hooks/use-toast'
import { sanitizeHtml } from '@/lib/markdown'

interface AnnouncementConfig {
  enabled: boolean
  body: string
  image_url: string
  remember_dismiss: boolean
  updated_at: number
}

export default function AdminAnnouncement() {
  const { t } = useTranslation(['admin', 'common'])
  const [enabled, setEnabled] = useState(false)
  const [body, setBody] = useState('')
  const [imageUrl, setImageUrl] = useState('')
  const [remember, setRemember] = useState(true)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [uploading, setUploading] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)

  async function load() {
    setLoading(true)
    try {
      const s = await adminApi.settings()
      const a = (s.announcement ?? {}) as Partial<AnnouncementConfig>
      setEnabled(Boolean(a.enabled))
      setBody(typeof a.body === 'string' ? a.body : '')
      setImageUrl(typeof a.image_url === 'string' ? a.image_url : '')
      setRemember(a.remember_dismiss !== false)
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

  async function onPickImage(file: File | undefined) {
    if (!file) return
    setUploading(true)
    try {
      const res = await adminApi.uploadIcon(file)
      setImageUrl(res.url)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:announcement.uploadFailed'))
    } finally {
      setUploading(false)
      if (fileRef.current) fileRef.current.value = ''
    }
  }

  async function save() {
    setSaving(true)
    try {
      const payload: AnnouncementConfig = {
        enabled,
        body: body.trim(),
        image_url: imageUrl.trim(),
        remember_dismiss: remember,
        // Bump the version so an edited notice re-shows for users who dismissed
        // the previous one.
        updated_at: Math.floor(Date.now() / 1000),
      }
      await adminApi.updateSettings({ announcement: payload })
      toast.success(t('admin:announcement.saved'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div>
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:announcement.title')}</h1>
        <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:announcement.lead')}</p>
      </header>

      {loading ? (
        <div className="mt-8 text-sm text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</div>
      ) : (
        <section className="mt-8 flex flex-col gap-6 max-w-2xl">
          {/* Enabled */}
          <label className="flex items-center justify-between gap-4 rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3.5">
            <span>
              <span className="block text-sm font-medium text-[var(--color-fg)]">{t('admin:announcement.enabledLabel')}</span>
              <span className="mt-0.5 block text-[12.5px] text-[var(--color-fg-muted)]">{t('admin:announcement.enabledHint')}</span>
            </span>
            <Switch checked={enabled} onCheckedChange={setEnabled} />
          </label>

          {/* Remember dismiss */}
          <label className="flex items-center justify-between gap-4 rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3.5">
            <span>
              <span className="block text-sm font-medium text-[var(--color-fg)]">{t('admin:announcement.rememberLabel')}</span>
              <span className="mt-0.5 block text-[12.5px] text-[var(--color-fg-muted)]">{t('admin:announcement.rememberHint')}</span>
            </span>
            <Switch checked={remember} onCheckedChange={setRemember} />
          </label>

          {/* Image (optional → image announcement) */}
          <Field label={t('admin:announcement.imageLabel')} htmlFor="ann-img" hint={t('admin:announcement.imageHint')}>
            <div className="flex items-center gap-2">
              <Input
                id="ann-img"
                value={imageUrl}
                onChange={(e) => setImageUrl(e.target.value)}
                placeholder={t('admin:announcement.imagePlaceholder')}
              />
              <input
                ref={fileRef}
                type="file"
                accept="image/png,image/jpeg"
                className="hidden"
                onChange={(e) => void onPickImage(e.target.files?.[0])}
              />
              <Button
                type="button"
                variant="secondary"
                size="sm"
                loading={uploading}
                leadingIcon={<Upload size={13} aria-hidden />}
                onClick={() => fileRef.current?.click()}
              >
                {t('admin:announcement.upload')}
              </Button>
            </div>
            {imageUrl ? (
              <div className="mt-2 flex items-center gap-2">
                <img
                  src={imageUrl}
                  alt=""
                  className="h-16 w-auto rounded-[8px] border border-[var(--color-border)] object-cover"
                />
                <Button variant="ghost" size="sm" leadingIcon={<X size={13} aria-hidden />} onClick={() => setImageUrl('')}>
                  {t('admin:announcement.removeImage')}
                </Button>
              </div>
            ) : null}
          </Field>

          {/* Body */}
          <Field label={t('admin:announcement.bodyLabel')} htmlFor="ann-body" hint={t('admin:announcement.bodyHint')}>
            <Textarea
              id="ann-body"
              rows={6}
              value={body}
              onChange={(e) => setBody(e.target.value)}
              placeholder={t('admin:announcement.bodyPlaceholder')}
            />
          </Field>

          {/* Live preview */}
          {enabled && (body.trim() || imageUrl.trim()) ? (
            <div>
              <p className="mb-2 text-[12px] uppercase tracking-[0.08em] text-[var(--color-fg-subtle)]">
                {t('admin:announcement.preview')}
              </p>
              <div className="flex overflow-hidden rounded-[16px] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-md)]">
                {imageUrl.trim() ? (
                  <div className="w-2/5 shrink-0 bg-[var(--color-bg-muted)]">
                    <img src={imageUrl} alt="" className="h-full w-full object-cover" />
                  </div>
                ) : (
                  <span aria-hidden className="block w-1 shrink-0 self-stretch bg-[var(--color-accent)]" />
                )}
                {body.trim() ? (
                  <div
                    className="flex-1 p-5 text-[14px] leading-relaxed text-[var(--color-fg)] break-words [&_a]:text-[var(--color-accent)] [&_a]:underline [&_ul]:list-disc [&_ul]:pl-5 [&_ol]:list-decimal [&_ol]:pl-5"
                    dangerouslySetInnerHTML={{ __html: sanitizeHtml(body) }}
                  />
                ) : (
                  <div className="flex-1 p-5 text-[14px] text-[var(--color-fg-subtle)]">{t('admin:announcement.bodyPlaceholder')}</div>
                )}
              </div>
            </div>
          ) : null}

          <div className="flex items-center gap-3">
            <Button onClick={() => void save()} loading={saving} leadingIcon={<Megaphone size={14} aria-hidden />}>
              {t('common:actions.save')}
            </Button>
          </div>
        </section>
      )}
    </div>
  )
}
