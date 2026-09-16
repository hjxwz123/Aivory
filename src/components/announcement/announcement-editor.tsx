import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Megaphone, Upload, X } from 'lucide-react'
import type { ApiAnnouncement } from '@/api/endpoints'
import { ApiError } from '@/api'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/hooks/use-toast'
import { sanitizeHtml } from '@/lib/markdown'
import { resizeImageForUpload } from '@/lib/resize-image'
import { PanelFallback } from '@/components/ui/panel-fallback'

interface AnnouncementEditorProps {
  load: () => Promise<ApiAnnouncement>
  save: (payload: ApiAnnouncement) => Promise<ApiAnnouncement | void>
  uploadImage: (file: File) => Promise<{ url: string }>
  title?: string
  lead?: string
  compact?: boolean
  translationNamespace?: 'admin' | 'chat'
  translationPrefix?: 'announcement' | 'workspace.announcement'
}

const emptyAnnouncement: ApiAnnouncement = {
  enabled: false,
  title: '',
  body: '',
  image_url: '',
  remember_dismiss: true,
  require_read: false,
  updated_at: 0,
  bar_enabled: false,
  bar_html: '',
  bar_updated_at: 0,
}

/** Shared controls for global and workspace announcements. */
export function AnnouncementEditor({
  load,
  save,
  uploadImage,
  title,
  lead,
  compact = false,
  translationNamespace = 'admin',
  translationPrefix = 'announcement',
}: AnnouncementEditorProps) {
  const { t } = useTranslation([translationNamespace, 'common'])
  const [value, setValue] = useState<ApiAnnouncement>(emptyAnnouncement)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [uploading, setUploading] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)
  const loadedBar = useRef({ enabled: false, html: '', updatedAt: 0 })

  useEffect(() => {
    let current = true
    setLoading(true)
    load()
      .then((next) => {
        if (!current) return
        const a = { ...emptyAnnouncement, ...next }
        setValue(a)
        loadedBar.current = { enabled: a.bar_enabled, html: a.bar_html, updatedAt: a.bar_updated_at }
      })
      .catch((e) => toast.error(e instanceof ApiError ? e.message : t('common:failed', { defaultValue: 'Operation failed' })))
      .finally(() => current && setLoading(false))
    return () => { current = false }
  }, [load, t])

  function update<K extends keyof ApiAnnouncement>(key: K, next: ApiAnnouncement[K]) {
    setValue((current) => ({ ...current, [key]: next }))
  }

  async function onPickImage(file: File | undefined) {
    if (!file) return
    setUploading(true)
    try {
      const result = await uploadImage(await resizeImageForUpload(file))
      update('image_url', result.url)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t(`${translationNamespace}:${translationPrefix}.uploadFailed`))
    } finally {
      setUploading(false)
      if (fileRef.current) fileRef.current.value = ''
    }
  }

  async function onSave() {
    setSaving(true)
    try {
      const now = Math.floor(Date.now() / 1000)
      const barHtml = value.bar_html.trim()
      const barChanged = value.bar_enabled !== loadedBar.current.enabled || barHtml !== loadedBar.current.html.trim()
      const payload: ApiAnnouncement = {
        ...value,
        title: value.title.trim(),
        body: value.body.trim(),
        image_url: value.image_url.trim(),
        bar_html: barHtml,
        updated_at: now,
        bar_updated_at: barChanged ? now : loadedBar.current.updatedAt || now,
      }
      const result = await save(payload)
      const saved = { ...payload, ...(result ?? {}) }
      setValue(saved)
      loadedBar.current = { enabled: saved.bar_enabled, html: saved.bar_html, updatedAt: saved.bar_updated_at }
      toast.success(t(`${translationNamespace}:${translationPrefix}.saved`))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common:failed', { defaultValue: 'Operation failed' }))
    } finally {
      setSaving(false)
    }
  }

  const label = (key: string, fallback: string, options?: Record<string, unknown>) =>
    t(`${translationNamespace}:${translationPrefix}.${key}`, { defaultValue: fallback, ...options })

  if (loading) return <PanelFallback />
  return (
    <div className={compact ? 'space-y-4' : 'mx-auto max-w-[76rem]'}>
      {title ? <h2 className="text-base font-semibold text-[var(--color-fg)]">{title}</h2> : null}
      {lead ? <p className="mt-1 text-[12.5px] text-[var(--color-fg-muted)]">{lead}</p> : null}
      <section className="mt-4 flex flex-col gap-4">
        <label className="flex items-center justify-between gap-4 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-3">
          <span className="min-w-0"><span className="block text-sm font-medium text-[var(--color-fg)]">{label('enabledLabel', 'Show announcement')}</span><span className="mt-0.5 block text-[12px] text-[var(--color-fg-muted)]">{label('enabledHint', 'Show this announcement to members.')}</span></span>
          <Switch checked={value.enabled} onCheckedChange={(v) => update('enabled', v)} />
        </label>
        <label className="flex items-center justify-between gap-4 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-3">
          <span className="min-w-0"><span className="block text-sm font-medium text-[var(--color-fg)]">{label('requireReadLabel', 'Require reading')}</span><span className="mt-0.5 block text-[12px] text-[var(--color-fg-muted)]">{label('requireReadHint', 'Members must wait before closing.')}</span></span>
          <Switch checked={value.require_read} onCheckedChange={(v) => update('require_read', v)} />
        </label>
        <label className="flex items-center justify-between gap-4 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-3">
          <span className="min-w-0"><span className="block text-sm font-medium text-[var(--color-fg)]">{label('rememberLabel', 'Remember dismissal')}</span><span className="mt-0.5 block text-[12px] text-[var(--color-fg-muted)]">{label('rememberHint', 'Let members hide this version.')}</span></span>
          <Switch checked={value.remember_dismiss} onCheckedChange={(v) => update('remember_dismiss', v)} />
        </label>
        <Field label={label('titleLabel', 'Title')} htmlFor="workspace-ann-title" hint={label('titleHint', 'Optional plain-text title.') }>
          <Input id="workspace-ann-title" value={value.title} maxLength={120} onChange={(e) => update('title', e.target.value)} placeholder={label('titlePlaceholder', 'Announcement title')} />
        </Field>
        <Field label={label('imageLabel', 'Image')} htmlFor="workspace-ann-image" hint={label('imageHint', 'Optional image shown beside the announcement.') }>
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <Input id="workspace-ann-image" wrapperClassName="min-w-0 flex-1" value={value.image_url} onChange={(e) => update('image_url', e.target.value)} placeholder={label('imagePlaceholder', 'https://...')} />
            <input ref={fileRef} type="file" accept="image/png,image/jpeg" className="hidden" onChange={(e) => void onPickImage(e.target.files?.[0])} />
            <Button type="button" variant="secondary" size="sm" loading={uploading} leadingIcon={<Upload size={13} aria-hidden />} onClick={() => fileRef.current?.click()} className="w-full sm:w-auto">{label('upload', 'Upload')}</Button>
          </div>
          {value.image_url ? <div className="mt-2 flex items-center gap-2"><img src={value.image_url} alt="" className="h-14 w-auto rounded-[7px] border border-[var(--color-border)] object-cover" /><Button variant="ghost" size="sm" leadingIcon={<X size={13} aria-hidden />} onClick={() => update('image_url', '')}>{label('removeImage', 'Remove image')}</Button></div> : null}
        </Field>
        <Field label={label('bodyLabel', 'Body')} htmlFor="workspace-ann-body" hint={label('bodyHint', 'HTML is supported and sanitized when displayed.') }>
          <Textarea id="workspace-ann-body" rows={compact ? 5 : 6} value={value.body} onChange={(e) => update('body', e.target.value)} placeholder={label('bodyPlaceholder', 'Announcement details')} />
        </Field>
        <div className="border-t border-[var(--color-divider)] pt-4">
          <h3 className="text-sm font-semibold text-[var(--color-fg)]">{label('barTitle', 'Top announcement bar')}</h3>
          <p className="mt-1 text-[12px] text-[var(--color-fg-muted)]">{label('barLead', 'Show a compact notice at the top of the chat.')}</p>
          <label className="mt-3 flex items-center justify-between gap-4 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-3"><span className="min-w-0"><span className="block text-sm font-medium text-[var(--color-fg)]">{label('barEnabledLabel', 'Enable top bar')}</span><span className="mt-0.5 block text-[12px] text-[var(--color-fg-muted)]">{label('barEnabledHint', 'Members see it at the top of this workspace.')}</span></span><Switch checked={value.bar_enabled} onCheckedChange={(v) => update('bar_enabled', v)} /></label>
          <div className="mt-3"><Field label={label('barHtmlLabel', 'Bar content (HTML / links)')} htmlFor="workspace-ann-bar" hint={label('barHtmlHint', 'Keep it short and single-line.') }><Textarea id="workspace-ann-bar" rows={2} value={value.bar_html} onChange={(e) => update('bar_html', e.target.value)} placeholder={label('barHtmlPlaceholder', 'A short update for members')} /></Field></div>
          {value.bar_enabled && value.bar_html.trim() ? <div className="mt-3"><p className="mb-2 text-[11px] uppercase tracking-[0.08em] text-[var(--color-fg-subtle)]">{label('preview', 'Preview')}</p><div className="flex items-center gap-2 rounded-[9px] border border-[var(--color-border)] bg-[var(--color-accent-soft)] px-3 py-2 text-[13px] text-[var(--color-fg)]"><Megaphone size={14} aria-hidden className="shrink-0 text-[var(--color-accent)]" /><div className="min-w-0 flex-1 break-words [&_a]:text-[var(--color-accent)] [&_a]:underline" dangerouslySetInnerHTML={{ __html: sanitizeHtml(value.bar_html) }} /></div></div> : null}
        </div>
        {value.enabled && (value.title.trim() || value.body.trim() || value.image_url.trim()) ? <div><p className="mb-2 text-[11px] uppercase tracking-[0.08em] text-[var(--color-fg-subtle)]">{label('preview', 'Preview')}</p><div className="flex max-h-72 flex-col overflow-hidden rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] sm:flex-row">{value.image_url.trim() ? <div className="aspect-[16/7] w-full shrink-0 bg-[var(--color-bg-muted)] sm:aspect-auto sm:w-2/5"><img src={value.image_url} alt="" className="size-full object-cover" /></div> : <span aria-hidden className="block w-1 shrink-0 self-stretch bg-[var(--color-accent)]" />}<div className="min-w-0 flex-1 space-y-2 overflow-y-auto p-3">{value.title.trim() ? <h4 className="break-words text-base font-semibold text-[var(--color-fg)]">{value.title.trim()}</h4> : null}{value.body.trim() ? <div className="break-words text-[13px] leading-relaxed text-[var(--color-fg)] [&_a]:text-[var(--color-accent)] [&_a]:underline [&_ol]:list-decimal [&_ol]:pl-5 [&_ul]:list-disc [&_ul]:pl-5" dangerouslySetInnerHTML={{ __html: sanitizeHtml(value.body) }} /> : null}</div></div></div> : null}
        <Button className="w-full sm:w-auto" onClick={() => void onSave()} loading={saving} leadingIcon={<Megaphone size={14} aria-hidden />}>{t('common:actions.save', { defaultValue: 'Save' })}</Button>
      </section>
    </div>
  )
}
