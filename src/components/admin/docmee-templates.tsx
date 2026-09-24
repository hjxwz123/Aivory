/**
 * Deployment templates for the Docmee AI PPT integration (§ AI PPT, admin).
 *
 * An Api-Key upload lands in the vendor account, not in the users' own-template
 * listing: verified against the live service, a template only becomes visible to
 * every user of this deployment once it is published with `updateUserTemplate`
 * (the vendor then reports it with an empty owner id). So an administrator needs
 * both halves — upload/overwrite, and the publish switch — and this panel shows
 * which templates are already shared.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Globe, ImageOff, Loader2, Trash2, Upload } from 'lucide-react'

import { adminApi, aipptApi, ApiError } from '@/api'
import type { ApiAiPPTTemplate } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Skeleton } from '@/components/ui/skeleton'
import { toast } from '@/hooks/use-toast'

const PAGE_KEYS = 'admin:creditSettings.docmee.templates'

export function DocmeeTemplateAdmin({ enabled }: { enabled: boolean }) {
  const { t } = useTranslation(['admin', 'common'])
  const [templates, setTemplates] = useState<ApiAiPPTTemplate[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busyId, setBusyId] = useState<string | null>(null)
  const [removing, setRemoving] = useState<ApiAiPPTTemplate | null>(null)
  const [uploading, setUploading] = useState(false)
  const [uploadPercent, setUploadPercent] = useState(0)
  const [shareOnUpload, setShareOnUpload] = useState(true)
  const [brokenCovers, setBrokenCovers] = useState<Set<string>>(() => new Set())
  const fileRef = useRef<HTMLInputElement | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const page = await adminApi.aipptTemplates()
      setTemplates(page.templates)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t('admin:common.failed'))
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => {
    // Only worth reading once the integration is on: without a key the vendor
    // answers "not configured" and the panel would just show an error.
    if (enabled) void load()
    else setLoading(false)
  }, [enabled, load])

  async function toggleShared(template: ApiAiPPTTemplate) {
    setBusyId(template.id)
    try {
      const result = await adminApi.aipptSetTemplatePublic(template.id, !template.shared)
      setTemplates((current) =>
        current.map((item) => (item.id === template.id ? { ...item, shared: result.shared } : item)),
      )
      toast.success(t(result.shared ? `${PAGE_KEYS}.shared` : `${PAGE_KEYS}.unshared`))
    } catch (err) {
      toast.error(t(`${PAGE_KEYS}.shareFailed`), err instanceof ApiError ? err.message : undefined)
    } finally {
      setBusyId(null)
    }
  }

  async function remove(template: ApiAiPPTTemplate) {
    setBusyId(template.id)
    try {
      await adminApi.aipptDeleteTemplate(template.id)
      setTemplates((current) => current.filter((item) => item.id !== template.id))
      toast.success(t(`${PAGE_KEYS}.deleted`))
      setRemoving(null)
    } catch (err) {
      toast.error(t(`${PAGE_KEYS}.deleteFailed`), err instanceof ApiError ? err.message : undefined)
    } finally {
      setBusyId(null)
    }
  }

  async function upload(file: File) {
    if (!file.name.toLowerCase().endsWith('.pptx')) {
      toast.warning(t(`${PAGE_KEYS}.pptxOnly`))
      return
    }
    setUploading(true)
    setUploadPercent(0)
    try {
      const result = await adminApi.aipptUploadTemplate(file, {
        public: shareOnUpload,
        onProgress: (progress) => setUploadPercent(Math.round(progress.percent ?? 0)),
      })
      toast.success(t(result.shared ? `${PAGE_KEYS}.uploadedShared` : `${PAGE_KEYS}.uploaded`))
      await load()
    } catch (err) {
      toast.error(t(`${PAGE_KEYS}.uploadFailed`), err instanceof ApiError ? err.message : undefined)
    } finally {
      setUploading(false)
      setUploadPercent(0)
    }
  }

  const coverURL = (template: ApiAiPPTTemplate): string | null => {
    if (!template.coverUrl || brokenCovers.has(template.id)) return null
    return aipptApi.resourceUrl(template.coverUrl)
  }

  return (
    <div className="mt-3 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <span className="text-xs font-medium text-[var(--color-fg)]">{t(`${PAGE_KEYS}.title`)}</span>
          <p className="mt-0.5 text-xs text-[var(--color-fg-muted)]">{t(`${PAGE_KEYS}.lead`)}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <div className="flex items-center gap-1.5">
            <Switch
              id="docmee-template-share"
              checked={shareOnUpload}
              onCheckedChange={setShareOnUpload}
              disabled={uploading}
            />
            <Label htmlFor="docmee-template-share" className="text-xs text-[var(--color-fg-muted)]">
              {t(`${PAGE_KEYS}.shareOnUpload`)}
            </Label>
          </div>
          {uploading ? (
            <span className="inline-flex items-center gap-1.5 text-xs text-[var(--color-fg-muted)]">
              <Loader2 size={12} aria-hidden className="animate-spin" />
              {t(`${PAGE_KEYS}.uploading`, { percent: uploadPercent })}
            </span>
          ) : (
            <Button
              size="sm"
              variant="secondary"
              disabled={!enabled}
              onClick={() => fileRef.current?.click()}
            >
              <Upload size={13} aria-hidden className="mr-1.5" />
              {t(`${PAGE_KEYS}.upload`)}
            </Button>
          )}
          <input
            ref={fileRef}
            type="file"
            accept=".pptx"
            className="hidden"
            onChange={(event) => {
              const file = event.target.files?.[0]
              event.target.value = ''
              if (file) void upload(file)
            }}
          />
        </div>
      </div>

      {!enabled ? (
        <p className="mt-2 text-xs text-[var(--color-fg-muted)]">{t(`${PAGE_KEYS}.needsKey`)}</p>
      ) : error ? (
        <p className="mt-2 text-xs text-[var(--color-danger)]">{error}</p>
      ) : loading ? (
        <div className="mt-2 grid grid-cols-2 gap-2 sm:grid-cols-3 xl:grid-cols-4">
          {Array.from({ length: 4 }).map((_, index) => (
            <Skeleton key={index} className="h-24 rounded-[8px]" />
          ))}
        </div>
      ) : templates.length === 0 ? (
        <p className="mt-2 text-xs text-[var(--color-fg-muted)]">{t(`${PAGE_KEYS}.empty`)}</p>
      ) : (
        <div className="mt-2 grid grid-cols-2 gap-2 sm:grid-cols-3 xl:grid-cols-4">
          {templates.map((template) => {
            const cover = coverURL(template)
            const busy = busyId === template.id
            return (
              <div
                key={template.id}
                className="flex flex-col overflow-hidden rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)]"
              >
                <div className="aspect-[16/9] w-full bg-[var(--color-bg-muted)]">
                  {cover ? (
                    <img
                      src={cover}
                      alt=""
                      loading="lazy"
                      className="h-full w-full object-cover"
                      onError={() => setBrokenCovers((current) => new Set(current).add(template.id))}
                    />
                  ) : (
                    <div className="flex h-full w-full items-center justify-center text-[var(--color-fg-muted)]">
                      <ImageOff size={16} aria-hidden />
                    </div>
                  )}
                </div>
                <div className="flex min-w-0 items-center gap-1.5 px-2 pt-1.5">
                  <span className="min-w-0 flex-1 truncate text-xs text-[var(--color-fg)]" title={template.name}>
                    {template.name}
                  </span>
                  {template.shared ? (
                    <Badge variant="success">
                      <Globe size={10} aria-hidden className="mr-1" />
                      {t(`${PAGE_KEYS}.sharedBadge`)}
                    </Badge>
                  ) : null}
                </div>
                <div className="mt-auto flex items-center gap-1 px-1.5 py-1.5">
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 flex-1 px-1 text-[11px]"
                    loading={busy}
                    disabled={busy}
                    onClick={() => void toggleShared(template)}
                  >
                    {t(template.shared ? `${PAGE_KEYS}.unshare` : `${PAGE_KEYS}.share`)}
                  </Button>
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t(`${PAGE_KEYS}.delete`)}
                    disabled={busy}
                    onClick={() => setRemoving(template)}
                  >
                    <Trash2 size={13} aria-hidden />
                  </Button>
                </div>
              </div>
            )
          })}
        </div>
      )}

      <Dialog open={removing !== null} onOpenChange={(open) => (!open ? setRemoving(null) : null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t(`${PAGE_KEYS}.deleteTitle`)}</DialogTitle>
            <DialogDescription>{t(`${PAGE_KEYS}.deleteBody`)}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRemoving(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="destructive"
              loading={busyId !== null}
              disabled={busyId !== null}
              onClick={() => removing && void remove(removing)}
            >
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
