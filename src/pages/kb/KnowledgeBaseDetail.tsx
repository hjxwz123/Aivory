/**
 * KnowledgeBaseDetail — list documents, add one (paste content or upload a
 * file), remove. Status shown live via polling while any doc is non-ready.
 */
import { activeWorkspaceId } from '@/store/workspaces'
import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2, Upload, FileText, Loader2, AlertTriangle } from 'lucide-react'
import { ApiError, kbsApi } from '@/api'
import type { ApiDocument, ApiKnowledgeBase } from '@/api/types'
import { api } from '@/api/client'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Badge } from '@/components/ui/badge'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import { ContentHeader } from '@/components/layout/content-header'
import { toast } from '@/hooks/use-toast'
import { formatRelativeDate, cn } from '@/lib/utils'

export default function KnowledgeBaseDetail() {
  const { t } = useTranslation(['kb', 'common'])
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [kb, setKB] = useState<ApiKnowledgeBase | null>(null)
  const [docs, setDocs] = useState<ApiDocument[]>([])
  const [loading, setLoading] = useState(true)
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState({ filename: '', content: '' })
  const [uploading, setUploading] = useState(false)
  const [tab, setTab] = useState<'paste' | 'upload'>('paste')
  const fileInput = useRef<HTMLInputElement>(null)

  // load(silent) refreshes the KB + its docs. Only the FIRST load toggles the
  // page-level skeleton; the background status poll passes silent=true so the
  // list refreshes in place without flipping the whole page to "loading…"
  // every ~2s (which read as a flicker).
  async function load(silent = false) {
    if (!id) return
    if (!silent) setLoading(true)
    try {
      const [list, d] = await Promise.all([kbsApi.list(activeWorkspaceId()), kbsApi.listDocs(id)])
      setKB(list.find((k) => k.id === id) ?? null)
      setDocs(d)
    } catch (e) {
      // A failed background poll shouldn't nag the user — only surface errors
      // on an explicit (non-silent) load.
      if (!silent) toast.error(e instanceof ApiError ? e.message : t('common:common.error'))
    } finally {
      if (!silent) setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id])

  // Poll silently while any document is mid-pipeline.
  useEffect(() => {
    if (!id) return
    const pending = docs.some(
      (d) => d.status === 'pending' || d.status === 'parsing' || d.status === 'embedding',
    )
    if (!pending) return
    const handle = setInterval(() => void load(true), 2200)
    return () => clearInterval(handle)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [docs, id])

  async function addPasted() {
    if (!id || !draft.filename.trim()) {
      toast.error(t('kb:dialog.nameRequired'))
      return
    }
    try {
      await kbsApi.addDoc(id, { filename: draft.filename, content: draft.content })
      toast.success(t('kb:detail.uploaded'))
      setOpen(false)
      setDraft({ filename: '', content: '' })
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('kb:detail.uploadFailed'))
    }
  }

  async function uploadFiles(files: FileList | null) {
    if (!files || !id) return
    setUploading(true)
    try {
      for (const file of Array.from(files)) {
        const form = new FormData()
        form.append('file', file)
        await api(`/kbs/${encodeURIComponent(id)}/documents`, { method: 'POST', body: form })
      }
      toast.success(t('kb:detail.uploaded'))
      setOpen(false)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('kb:detail.uploadFailed'))
    } finally {
      setUploading(false)
    }
  }

  async function remove(d: ApiDocument) {
    if (!id) return
    try {
      await kbsApi.removeDoc(id, d.id)
      toast.success(t('kb:detail.removed'))
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common:common.error'))
    }
  }

  if (!kb && !loading) {
    return (
      <div className="flex-1 grid place-items-center p-10">
        <EmptyState
          title={t('kb:emptyTitle')}
          description={t('kb:emptyBody')}
          action={<Button onClick={() => navigate('/kb')}>{t('common:actions.back')}</Button>}
        />
      </div>
    )
  }

  return (
    <div className="flex-1 min-h-0 flex flex-col bg-[var(--color-bg)] text-[var(--color-fg)]">
      <ContentHeader
        title={kb?.name ?? '…'}
        backTo="/kb"
        backLabel={t('kb:title')}
        actions={
          <Button
            size="sm"
            leadingIcon={<Plus size={15} aria-hidden />}
            onClick={() => setOpen(true)}
          >
            {t('kb:detail.uploadButton')}
          </Button>
        }
      />
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-5 sm:px-8 py-8 pb-24">
          {kb?.description ? (
            <p className="text-[var(--color-fg-muted)] text-[15px] leading-relaxed max-w-[60ch]">{kb.description}</p>
          ) : null}

        <section className="mt-8">
          {loading ? (
            <div className="text-sm text-[var(--color-fg-subtle)]">{t('common:common.loading')}</div>
          ) : docs.length === 0 ? (
            <EmptyState
              icon={<FileText size={20} aria-hidden />}
              title={t('kb:detail.noDocs')}
              description={t('kb:detail.noDocsBody')}
              action={<Button onClick={() => setOpen(true)}>{t('kb:detail.uploadButton')}</Button>}
            />
          ) : (
            <ul className="flex flex-col divide-y divide-[var(--color-divider)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
              {docs.map((d) => (
                <li key={d.id} className="grid grid-cols-[1fr_auto] gap-3 items-center px-5 py-4">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <FileText size={13} className="text-[var(--color-fg-subtle)] shrink-0" aria-hidden />
                      <span className="font-medium text-[var(--color-fg)] truncate">{d.filename}</span>
                      <StatusBadge status={d.status} label={t(`kb:detail.status.${d.status}`)} />
                    </div>
                    <div className="mt-1 text-[12px] text-[var(--color-fg-subtle)] font-mono">
                      {(d.size_bytes / 1024).toFixed(1)} KB · {d.chunk_count} chunks · {t('kb:stats.created', { when: formatRelativeDate(d.created_at * 1000) })}
                    </div>
                    {d.status === 'failed' ? (
                      <p className="mt-1.5 flex items-start gap-1.5 text-[12px] text-[var(--color-danger)] leading-snug">
                        <AlertTriangle size={12} className="mt-px shrink-0" aria-hidden />
                        <span>{d.error || t('kb:detail.failedReason')}</span>
                      </p>
                    ) : null}
                    {(d.status === 'parsing' || d.status === 'embedding') ? (
                      <div className="mt-1.5 h-1 w-full overflow-hidden rounded-full bg-[var(--color-bg-muted)]">
                        <div className="h-full w-1/3 bg-[var(--color-accent)] animate-[indeterminate_1400ms_linear_infinite]" />
                      </div>
                    ) : null}
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    leadingIcon={<Trash2 size={13} aria-hidden />}
                    onClick={() => void remove(d)}
                  >
                    {t('common:actions.delete')}
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </section>
        </div>
      </div>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t('kb:detail.uploadButton')}</DialogTitle>
            <DialogDescription>{t('kb:detail.noDocsBody')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <Tabs value={tab} onValueChange={(v) => setTab(v as 'paste' | 'upload')}>
              <TabsList className="mb-4">
                <TabsTrigger value="paste">
                  <FileText size={12} aria-hidden /> {t('kb:detail.tabPaste')}
                </TabsTrigger>
                <TabsTrigger value="upload">
                  <Upload size={12} aria-hidden /> {t('kb:detail.tabUpload')}
                </TabsTrigger>
              </TabsList>
              <TabsContent value="paste">
                <div className="grid gap-4">
                  <Field label={t('kb:detail.tableHeaders.filename')} htmlFor="doc-name">
                    <Input
                      id="doc-name"
                      value={draft.filename}
                      onChange={(e) => setDraft({ ...draft, filename: e.target.value })}
                      placeholder="notes.md"
                    />
                  </Field>
                  <Field label={t('kb:detail.contentLabel')} htmlFor="doc-body">
                    <Textarea
                      id="doc-body"
                      rows={10}
                      value={draft.content}
                      onChange={(e) => setDraft({ ...draft, content: e.target.value })}
                    />
                  </Field>
                </div>
              </TabsContent>
              <TabsContent value="upload">
                <div
                  className={cn(
                    'rounded-[14px] border border-dashed border-[var(--color-border-strong)] bg-[var(--color-bg-muted)] p-10 text-center interactive',
                    'hover:border-[var(--color-accent)] cursor-pointer',
                  )}
                  onClick={() => fileInput.current?.click()}
                >
                  <input
                    ref={fileInput}
                    type="file"
                    hidden
                    multiple
                    onChange={(e) => {
                      void uploadFiles(e.currentTarget.files)
                      e.currentTarget.value = ''
                    }}
                  />
                  <Upload size={24} className="mx-auto text-[var(--color-fg-subtle)]" aria-hidden />
                  <p className="mt-3 text-[var(--color-fg-muted)] text-sm">
                    {uploading ? <Loader2 className="inline-block animate-spin" size={14} aria-hidden /> : t('kb:detail.clickToChoose')}
                  </p>
                </div>
              </TabsContent>
            </Tabs>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setOpen(false)}>
              {t('common:actions.cancel')}
            </Button>
            {tab === 'paste' ? (
              <Button onClick={() => void addPasted()}>{t('common:actions.save')}</Button>
            ) : null}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function StatusBadge({ status, label }: { status: ApiDocument['status']; label: string }) {
  switch (status) {
    case 'ready':
      return <Badge size="xs" variant="sage">{label}</Badge>
    case 'failed':
      // Failed must read as an error, not just "another in-progress state".
      return <Badge size="xs" variant="danger">{label}</Badge>
    default:
      return <Badge size="xs" variant="neutral">{label}…</Badge>
  }
}
