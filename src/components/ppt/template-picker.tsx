/**
 * Template gallery for the self-built AI PPT flow (§ AI PPT API mode).
 *
 * Covers are vendor-hosted and 403 without Docmee's temporary token, so every
 * image is loaded through our own resource proxy (`aipptApi.resourceUrl`) — the
 * browser never sees the token.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, ImageOff, Loader2, Pencil, Trash2, Upload } from 'lucide-react'

import { aipptApi, ApiError } from '@/api'
import type { ApiAiPPTTemplate } from '@/api/types'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { SegmentedControl } from '@/components/ui/segmented-control'
import { toast } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'

interface TemplatePickerProps {
  selectedId: string | null
  onSelect: (template: ApiAiPPTTemplate) => void
  disabled?: boolean
  /** Called after one of the user's own templates is deleted. */
  onDeleted?: (id: string) => void
}

const PAGE_SIZE = 24

export function TemplatePicker({ selectedId, onSelect, disabled = false, onDeleted }: TemplatePickerProps) {
  const { t } = useTranslation('ppt')
  const [templates, setTemplates] = useState<ApiAiPPTTemplate[]>([])
  const [type, setType] = useState<1 | 4>(1)
  const [category, setCategory] = useState<string>('')
  const [categories, setCategories] = useState<string[]>([])
  const [page, setPage] = useState(1)
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [brokenCovers, setBrokenCovers] = useState<Set<string>>(() => new Set())
  const [uploading, setUploading] = useState(false)
  const [uploadPercent, setUploadPercent] = useState(0)
  const [renaming, setRenaming] = useState<ApiAiPPTTemplate | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [removing, setRemoving] = useState<ApiAiPPTTemplate | null>(null)
  const [managing, setManaging] = useState(false)
  const fileRef = useRef<HTMLInputElement | null>(null)
  const requestRef = useRef(0)

  const load = useCallback(
    async (nextPage: number, append: boolean) => {
      const request = ++requestRef.current
      setLoading(true)
      setError(null)
      try {
        const result = await aipptApi.templates({
          type,
          page: nextPage,
          size: PAGE_SIZE,
          category: category || undefined,
        })
        if (request !== requestRef.current) return
        setTemplates((current) => (append ? [...current, ...result.templates] : result.templates))
        setCategories((current) => [...new Set([...current, ...result.templates.map((item) => (item.category ?? '').trim()).filter(Boolean)])].slice(0, 12))
        setHasMore(Boolean(result.has_more))
        setPage(nextPage)
      } catch (err) {
        if (request !== requestRef.current) return
        setError(err instanceof ApiError ? err.message : t('errors.upstream'))
      } finally {
        if (request === requestRef.current) setLoading(false)
      }
    },
    [category, t, type],
  )

  useEffect(() => {
    void load(1, false)
    return () => { requestRef.current += 1 }
  }, [load])

  /**
   * Upload a custom template. Docmee learns the .pptx server-side (type=4), so the
   * new template shows up under "Mine" a moment later — we switch to that tab,
   * reload and select it.
   */
  const upload = useCallback(
    async (file: File) => {
      if (!file.name.toLowerCase().endsWith('.pptx')) {
        toast.warning(t('template.uploadPptxOnly'))
        return
      }
      setUploading(true)
      setUploadPercent(0)
      try {
        const result = await aipptApi.uploadTemplate(file, {
          onProgress: (progress) => setUploadPercent(Math.round(progress.percent ?? 0)),
        })
        toast.success(t('template.uploaded'))
        setType(4)
        setCategory('')
        const page = await aipptApi.templates({ type: 4, page: 1, size: PAGE_SIZE })
        setTemplates(page.templates)
        setHasMore(Boolean(page.has_more))
        setPage(1)
        const created = page.templates.find((item) => item.id === result.template_id)
        if (created) onSelect(created)
      } catch (err) {
        toast.error(
          err instanceof ApiError && err.status === 413 ? t('template.uploadTooLarge') : t('template.uploadFailed'),
          err instanceof ApiError ? err.message : undefined,
        )
      } finally {
        setUploading(false)
        setUploadPercent(0)
      }
    },
    [onSelect, t],
  )

  /**
   * Rename a custom template. Only the caller's own uploads may be renamed: the
   * vendor lists the deployment's shared templates alongside them, and those
   * belong to the administrator (the server refuses them).
   */
  const submitRename = useCallback(async () => {
    if (!renaming) return
    const name = renameValue.trim()
    if (!name || name === renaming.name) {
      setRenaming(null)
      return
    }
    setManaging(true)
    try {
      await aipptApi.renameTemplate(renaming.id, name)
      setTemplates((current) => current.map((item) => (item.id === renaming.id ? { ...item, name } : item)))
      toast.success(t('template.renamed'))
      setRenaming(null)
    } catch (err) {
      toast.error(t('template.renameFailed'), err instanceof ApiError ? err.message : undefined)
    } finally {
      setManaging(false)
    }
  }, [renameValue, renaming, t])

  const confirmDelete = useCallback(async () => {
    if (!removing) return
    const id = removing.id
    setManaging(true)
    try {
      await aipptApi.deleteTemplate(id)
      setTemplates((current) => current.filter((item) => item.id !== id))
      toast.success(t('template.deleted'))
      onDeleted?.(id)
      setRemoving(null)
    } catch (err) {
      toast.error(t('template.deleteFailed'), err instanceof ApiError ? err.message : undefined)
    } finally {
      setManaging(false)
    }
  }, [onDeleted, removing, t])

  const coverURL = (template: ApiAiPPTTemplate): string | null => {
    if (!template.coverUrl || brokenCovers.has(template.id)) return null
    return aipptApi.resourceUrl(template.coverUrl)
  }

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col gap-4">
      <div className="flex shrink-0 flex-wrap items-center gap-3 border-b border-[var(--color-divider)] pb-3">
        <fieldset disabled={disabled || uploading || managing} className="min-w-0">
          <SegmentedControl
            label={t('template.title')}
            value={String(type)}
            options={[{ value: '1', label: t('template.system') }, { value: '4', label: t('template.mine') }]}
            onChange={(value) => {
              if (String(type) === value) return
              setType(value === '4' ? 4 : 1)
              setCategory('')
              setCategories([])
              setTemplates([])
            }}
          />
        </fieldset>
        {categories.length > 0 ? (
          <Select value={category || '__all'} disabled={disabled || uploading} onValueChange={(value) => setCategory(value === '__all' ? '' : value)}>
            <SelectTrigger aria-label={t('template.category')} className="h-8 w-auto min-w-28 max-w-48 bg-transparent text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__all">{t('template.all')}</SelectItem>
              {categories.map((value) => <SelectItem key={value} value={value}>{value}</SelectItem>)}
            </SelectContent>
          </Select>
        ) : null}

        {/* Custom templates: Docmee learns a .pptx server-side (type=4), so the
            upload lands in the account's own catalogue. */}
        <div className="ml-auto flex items-center gap-2">
          {uploading ? (
            <span className="inline-flex items-center gap-1.5 text-xs text-[var(--color-fg-muted)]">
              <Loader2 size={12} aria-hidden className="animate-spin" />
              {t('template.uploading', { percent: uploadPercent })}
            </span>
          ) : (
            <Button size="sm" variant="secondary" disabled={disabled} onClick={() => fileRef.current?.click()}>
              <Upload size={13} aria-hidden className="mr-1.5" />
              {t('template.upload')}
            </Button>
          )}
          <span className="hidden text-xs text-[var(--color-fg-muted)] xl:inline">
            {t('template.uploadHint')}
          </span>
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

      {error ? (
        <div role="alert" className="flex shrink-0 flex-wrap items-center justify-between gap-2 rounded-[8px] bg-[var(--color-bg-muted)] px-3 py-2 text-sm text-[var(--color-fg-muted)]">
          <p>{error}</p>
          <Button size="sm" variant="secondary" onClick={() => void load(page, page > 1)}>{t('common:actions.tryAgain')}</Button>
        </div>
      ) : null}

      <div className="min-h-0 flex-1 overflow-y-auto pr-1">
        {loading && templates.length === 0 ? (
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-4">
            {Array.from({ length: 8 }).map((_, index) => (
              <Skeleton key={index} className="aspect-video rounded-[10px]" />
            ))}
          </div>
        ) : templates.length === 0 ? (
          <p className="py-10 text-center text-sm text-[var(--color-fg-muted)]">{t('template.empty')}</p>
        ) : (
          <>
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-4">
              {templates.map((template) => {
                const cover = coverURL(template)
                const selected = selectedId === template.id
                return (
                  <div
                    key={template.id}
                    className={cn(
                      'flex min-w-0 flex-col overflow-hidden rounded-[10px] border bg-[var(--color-surface)] interactive',
                      selected
                        ? 'border-[var(--color-accent)] ring-1 ring-[var(--color-accent)]'
                        : 'border-[var(--color-border)] hover:border-[var(--color-border-strong)]',
                    )}
                  >
                    <button
                      type="button"
                      disabled={disabled}
                      onClick={() => onSelect(template)}
                      aria-pressed={selected}
                      className="block min-w-0 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)] disabled:opacity-60"
                    >
                      <div className="aspect-video w-full bg-[var(--color-bg-muted)]">
                        {cover ? (
                          <img
                            src={cover}
                            alt=""
                            loading="lazy"
                            className="h-full w-full object-contain"
                            onError={() => setBrokenCovers((current) => new Set(current).add(template.id))}
                          />
                        ) : (
                          <div className="flex h-full w-full items-center justify-center text-[var(--color-fg-muted)]">
                            <ImageOff size={18} aria-hidden />
                          </div>
                        )}
                      </div>
                      <div className="flex min-h-12 items-center gap-2 px-3 py-2.5">
                        <span className="line-clamp-2 min-w-0 flex-1 break-words text-[13px] text-[var(--color-fg)]">{template.name}</span>
                        {selected ? (
                          <Check size={13} aria-hidden className="shrink-0 text-[var(--color-accent)]" />
                        ) : null}
                      </div>
                    </button>
                    {/* The vendor also lists the deployment's shared templates
                        here; only the user's own uploads may be managed. */}
                    {template.owned ? (
                      <div className="mt-auto flex items-center gap-0.5 border-t border-[var(--color-border)] px-1 py-0.5">
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={disabled}
                          className="h-6 flex-1 px-1 text-[11px]"
                          onClick={() => {
                            setRenameValue(template.name)
                            setRenaming(template)
                          }}
                        >
                          <Pencil size={11} aria-hidden className="mr-1" />
                          {t('template.rename')}
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={disabled}
                          className="h-6 flex-1 px-1 text-[11px] text-[var(--color-danger)]"
                          onClick={() => setRemoving(template)}
                        >
                          <Trash2 size={11} aria-hidden className="mr-1" />
                          {t('template.delete')}
                        </Button>
                      </div>
                    ) : null}
                  </div>
                )
              })}
            </div>
            {hasMore ? (
              <div className="mt-4 flex justify-center">
                <Button
                  variant="outline"
                  size="sm"
                  disabled={disabled || loading}
                  onClick={() => void load(page + 1, true)}
                >
                  {loading ? <Loader2 size={14} aria-hidden className="mr-1.5 animate-spin" /> : null}
                  {t('template.loadMore')}
                </Button>
              </div>
            ) : null}
          </>
        )}
      </div>

      <Dialog open={renaming !== null} onOpenChange={(open) => (!open ? setRenaming(null) : null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('template.renameTitle')}</DialogTitle>
            <DialogDescription>{t('template.renameLead')}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="aippt-template-name">{t('template.renameLabel')}</Label>
            <Input
              id="aippt-template-name"
              value={renameValue}
              maxLength={60}
              onChange={(event) => setRenameValue(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter') void submitRename()
              }}
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRenaming(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button loading={managing} disabled={managing || !renameValue.trim()} onClick={() => void submitRename()}>
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={removing !== null} onOpenChange={(open) => (!open ? setRemoving(null) : null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('template.deleteTitle')}</DialogTitle>
            <DialogDescription>{t('template.deleteBody')}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRemoving(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" loading={managing} disabled={managing} onClick={() => void confirmDelete()}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
