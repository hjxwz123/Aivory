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
import { resizeImageForUpload } from '@/lib/resize-image'
import { PanelFallback } from '@/components/ui/panel-fallback'

interface AnnouncementConfig {
  enabled: boolean
  body: string
  image_url: string
  remember_dismiss: boolean
  updated_at: number
  // Pinned top bar (independent of the popup).
  bar_enabled: boolean
  bar_html: string
  bar_updated_at: number
}

export default function AdminAnnouncement() {
  const { t } = useTranslation(['admin', 'common'])
  const [enabled, setEnabled] = useState(false)
  const [body, setBody] = useState('')
  const [imageUrl, setImageUrl] = useState('')
  const [remember, setRemember] = useState(true)
  // Pinned top bar.
  const [barEnabled, setBarEnabled] = useState(false)
  const [barHtml, setBarHtml] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [uploading, setUploading] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)
  // Loaded bar snapshot, so we only bump bar_updated_at (the dismiss version)
  // when the bar's content actually changed — editing only the popup shouldn't
  // re-pop the bar for users who dismissed it.
  const barLoaded = useRef({ enabled: false, html: '', updatedAt: 0 })

  async function load() {
    setLoading(true)
    try {
      const s = await adminApi.settings()
      const a = (s.announcement ?? {}) as Partial<AnnouncementConfig>
      setEnabled(Boolean(a.enabled))
      setBody(typeof a.body === 'string' ? a.body : '')
      setImageUrl(typeof a.image_url === 'string' ? a.image_url : '')
      setRemember(a.remember_dismiss !== false)
      const bEnabled = Boolean(a.bar_enabled)
      const bHtml = typeof a.bar_html === 'string' ? a.bar_html : ''
      setBarEnabled(bEnabled)
      setBarHtml(bHtml)
      barLoaded.current = { enabled: bEnabled, html: bHtml, updatedAt: typeof a.bar_updated_at === 'number' ? a.bar_updated_at : 0 }
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
      // Downscale oversized images to a sane range before upload (the server
      // caps at 256 KiB and never resizes, so a big photo would just be rejected).
      const resized = await resizeImageForUpload(file)
      const res = await adminApi.uploadIcon(resized)
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
      const now = Math.floor(Date.now() / 1000)
      // Only re-version the bar (re-show to dismissers) when ITS content changed.
      const barChanged =
        barEnabled !== barLoaded.current.enabled || barHtml.trim() !== barLoaded.current.html.trim()
      const payload: AnnouncementConfig = {
        enabled,
        body: body.trim(),
        image_url: imageUrl.trim(),
        remember_dismiss: remember,
        // Bump the version so an edited notice re-shows for users who dismissed
        // the previous one.
        updated_at: now,
        bar_enabled: barEnabled,
        bar_html: barHtml.trim(),
        bar_updated_at: barChanged ? now : barLoaded.current.updatedAt || now,
      }
      await adminApi.updateSettings({ announcement: payload })
      barLoaded.current = { enabled: barEnabled, html: barHtml.trim(), updatedAt: payload.bar_updated_at }
      toast.success(t('admin:announcement.saved'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:announcement.title')}</h1>
        <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:announcement.lead')}</p>
      </header>

      {loading ? (
        <PanelFallback />
      ) : (
        <section className="mt-8 flex flex-col gap-6">
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

          {/* Pinned top bar — independent of the popup above. */}
          <div className="border-t border-[var(--color-divider)] pt-6">
            <h2 className="text-base font-semibold text-[var(--color-fg)]">
              {t('admin:announcement.barTitle', { defaultValue: '置顶公告条' })}
            </h2>
            <p className="mt-1 text-[12.5px] text-[var(--color-fg-muted)] max-w-2xl">
              {t('admin:announcement.barLead', {
                defaultValue: '在主界面顶部显示一条细公告条，支持链接（<a> 标签）。与上面的弹窗公告相互独立。',
              })}
            </p>
            <label className="mt-4 flex items-center justify-between gap-4 rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3.5">
              <span>
                <span className="block text-sm font-medium text-[var(--color-fg)]">
                  {t('admin:announcement.barEnabledLabel', { defaultValue: '启用置顶公告条' })}
                </span>
                <span className="mt-0.5 block text-[12.5px] text-[var(--color-fg-muted)]">
                  {t('admin:announcement.barEnabledHint', { defaultValue: '开启后，所有用户的主界面顶部都会显示该公告条。' })}
                </span>
              </span>
              <Switch checked={barEnabled} onCheckedChange={setBarEnabled} />
            </label>
            <div className="mt-4">
              <Field
                label={t('admin:announcement.barHtmlLabel', { defaultValue: '公告条内容（支持 HTML / 链接）' })}
                htmlFor="ann-bar"
                hint={t('admin:announcement.barHtmlHint', { defaultValue: '可含 <a href="...">链接</a>；保持简短，单行展示。' })}
              >
                <Textarea
                  id="ann-bar"
                  rows={3}
                  value={barHtml}
                  onChange={(e) => setBarHtml(e.target.value)}
                  placeholder={t('admin:announcement.barHtmlPlaceholder', {
                    defaultValue: '例如：系统将于今晚 02:00 维护，详情见 <a href="/welcome">公告</a>。',
                  })}
                />
              </Field>
            </div>
            {barEnabled && barHtml.trim() ? (
              <div className="mt-3">
                <p className="mb-2 text-[12px] uppercase tracking-[0.08em] text-[var(--color-fg-subtle)]">
                  {t('admin:announcement.preview')}
                </p>
                <div className="flex items-center gap-2.5 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-accent-soft)] px-4 py-2.5 text-[13px] text-[var(--color-fg)]">
                  <Megaphone size={14} aria-hidden className="shrink-0 text-[var(--color-accent)]" />
                  <div
                    className="flex-1 min-w-0 break-words [&_a]:text-[var(--color-accent)] [&_a]:underline [&_a]:underline-offset-2"
                    dangerouslySetInnerHTML={{ __html: sanitizeHtml(barHtml) }}
                  />
                </div>
              </div>
            ) : null}
          </div>

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
