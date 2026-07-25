/**
 * KnowledgeBasesList — gallery of the user's knowledge bases.
 */
import { activeWorkspaceId, useWorkspaces } from '@/store/workspaces'
import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Plus, Database, MoreHorizontal, Trash2 } from 'lucide-react'
import { ApiError, kbsApi, modelsApi } from '@/api'
import type { ApiKnowledgeBase, ApiModel } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { EmptyState } from '@/components/ui/empty-state'
import { Skeleton } from '@/components/ui/skeleton'
import { ContentHeader } from '@/components/layout/content-header'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { toast } from '@/hooks/use-toast'
import { formatRelativeDate } from '@/lib/utils'

export default function KnowledgeBasesList() {
  const { t } = useTranslation(['kb', 'common'])
  // §workspaces: KBs aren't part of reloadSpaceData(), so this page re-fetches
  // itself when the active space changes (after the switch settles).
  const activeWsId = useWorkspaces((s) => s.activeId)
  const wsSwitching = useWorkspaces((s) => s.switching)
  const [rows, setRows] = useState<ApiKnowledgeBase[]>([])
  const [models, setModels] = useState<ApiModel[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState({ name: '', description: '', embedding_model_id: '' })
  const [creating, setCreating] = useState(false)
  const creatingRef = useRef(false)
  // Delete-KB confirmation (removes the KB + its documents/vectors, and
  // auto-unbinds it from any conversation that referenced it — server-side).
  const [toDelete, setToDelete] = useState<ApiKnowledgeBase | null>(null)
  const [deleting, setDeleting] = useState(false)
  // Stale-response guard for the space-switch reloads: a slow earlier space's
  // response must never overwrite the current space's rows (same epoch pattern
  // as the conversations/projects stores).
  const loadEpochRef = useRef(0)

  async function load() {
    const epoch = ++loadEpochRef.current
    setLoading(true)
    setLoadError('')
    try {
      const [kb, em] = await Promise.all([kbsApi.list(activeWorkspaceId()), modelsApi.listEmbedding()])
      if (epoch !== loadEpochRef.current) return // superseded by a space switch
      setRows(kb)
      setModels(em.models)
      if (em.models.length > 0 && !draft.embedding_model_id) {
        setDraft((d) => ({ ...d, embedding_model_id: em.default_id || em.models[0].id }))
      }
    } catch (e) {
      if (epoch !== loadEpochRef.current) return
      const message = e instanceof ApiError ? e.message : t('common:common.error')
      setLoadError(message)
      toast.error(message)
    } finally {
      if (epoch === loadEpochRef.current) setLoading(false)
    }
  }

  useEffect(() => {
    if (wsSwitching) return
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeWsId, wsSwitching])

  async function doDelete() {
    if (!toDelete) return
    setDeleting(true)
    try {
      await kbsApi.remove(toDelete.id)
      toast.success(t('kb:deleted', { defaultValue: 'Knowledge base deleted' }))
      setToDelete(null)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common:common.error'))
    } finally {
      setDeleting(false)
    }
  }

  async function create() {
    if (creatingRef.current) return
    if (!draft.name.trim()) {
      toast.error(t('kb:dialog.nameRequired'))
      return
    }
    creatingRef.current = true
    setCreating(true)
    try {
      await kbsApi.create({ ...draft, workspace_id: activeWorkspaceId() })
      toast.success(t('kb:dialog.created'))
      setOpen(false)
      setDraft({ name: '', description: '', embedding_model_id: draft.embedding_model_id })
      await load()
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : t('common:common.error')
      toast.error(
        msg === 'kb_limit_reached'
          ? t('kb:limitReached', { defaultValue: 'You’ve reached your plan’s knowledge-base limit.' })
          : msg === 'name_exists'
            ? t('kb:dialog.nameExists', { defaultValue: 'A knowledge base with this name already exists.' })
          : msg,
      )
    } finally {
      creatingRef.current = false
      setCreating(false)
    }
  }

  return (
    <div className="flex-1 min-h-0 flex flex-col bg-[var(--color-bg)] text-[var(--color-fg)]">
      <ContentHeader
        title={t('kb:title')}
        actions={
          <Button
            variant="secondary"
            size="sm"
            leadingIcon={<Plus size={15} aria-hidden />}
            onClick={() => setOpen(true)}
          >
            {t('kb:new')}
          </Button>
        }
      />
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-5 pb-24 pt-5 sm:px-8 sm:pt-6">
          <p className="max-w-[60ch] text-[13.5px] leading-relaxed text-[var(--color-fg-muted)]">
            {t('kb:lead')}
          </p>

          <section className="mt-6">
            {loading ? (
              <KnowledgeBasesSkeleton label={t('common:common.loading')} />
            ) : loadError ? (
              <EmptyState
                icon={<Database size={20} aria-hidden />}
                title={t('common:common.error')}
                description={loadError}
                action={
                  <Button variant="secondary" onClick={() => void load()}>
                    {t('common:actions.tryAgain', { defaultValue: 'Try again' })}
                  </Button>
                }
              />
            ) : rows.length === 0 ? (
              <EmptyState
                icon={<Database size={20} aria-hidden />}
                title={t('kb:emptyTitle')}
                description={t('kb:emptyBody')}
                action={
                  <Button variant="secondary" onClick={() => setOpen(true)}>
                    {t('kb:createFirst')}
                  </Button>
                }
              />
            ) : (
              <ul className="flex flex-col divide-y divide-[var(--color-divider)] border-b border-[var(--color-divider)]">
                {rows.map((kb) => (
                  <li key={kb.id} className="group/kb flex min-w-0 items-center gap-1 py-1">
                    <Link
                      to={`/kb/${kb.id}`}
                      className="flex min-h-14 min-w-0 flex-1 items-center gap-3 rounded-[10px] px-2 py-2 interactive hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                    >
                      <span
                        className="grid size-8 shrink-0 place-items-center rounded-[8px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]"
                        aria-hidden
                      >
                        <Database size={15} />
                      </span>
                      <div className="min-w-0 flex-1">
                        <h3
                          title={kb.name}
                          className="truncate text-[14.5px] font-medium leading-snug tracking-normal text-[var(--color-fg)]"
                        >
                          {kb.name}
                        </h3>
                        {kb.description ? (
                          <p className="mt-0.5 truncate text-[12px] leading-snug text-[var(--color-fg-muted)]">
                            {kb.description}
                          </p>
                        ) : null}
                      </div>
                      <time
                        className="hidden shrink-0 text-[11px] tabular-nums text-[var(--color-fg-subtle)] sm:block"
                        dateTime={new Date(kb.created_at * 1000).toISOString()}
                      >
                        {t('kb:stats.created', {
                          when: formatRelativeDate(kb.created_at * 1000),
                        })}
                      </time>
                    </Link>
                    <div className="shrink-0">
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <button
                            type="button"
                            aria-label={`${t('common:actions.more', { defaultValue: 'More' })}: ${kb.name}`}
                            className="inline-flex size-[var(--tap-min)] items-center justify-center rounded-[8px] text-[var(--color-fg-subtle)] opacity-100 hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] sm:size-8 sm:opacity-0 sm:group-hover/kb:opacity-100 sm:data-[state=open]:opacity-100 sm:focus-visible:opacity-100"
                          >
                            <MoreHorizontal size={16} aria-hidden />
                          </button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem destructive onSelect={() => setToDelete(kb)}>
                            <Trash2 size={13} aria-hidden /> {t('common:actions.delete')}
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </div>
      </div>

      <Dialog open={open} onOpenChange={(next) => !creatingRef.current && setOpen(next)}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t('kb:dialog.title')}</DialogTitle>
            <DialogDescription>{t('kb:dialog.body')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <Field label={t('kb:dialog.name')} htmlFor="kb-name">
                <Input
                  id="kb-name"
                  value={draft.name}
                  onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                  placeholder={t('kb:dialog.namePlaceholder')}
                />
              </Field>
              <Field label={t('kb:dialog.description')} htmlFor="kb-desc">
                <Textarea
                  id="kb-desc"
                  rows={3}
                  value={draft.description}
                  onChange={(e) => setDraft({ ...draft, description: e.target.value })}
                />
              </Field>
              <Field label={t('kb:dialog.embeddingModel')} htmlFor="kb-em">
                <Select
                  value={draft.embedding_model_id}
                  onValueChange={(v) => setDraft({ ...draft, embedding_model_id: v })}
                >
                  <SelectTrigger id="kb-em">
                    <SelectValue placeholder={t('kb:dialog.pickModel')} />
                  </SelectTrigger>
                  <SelectContent>
                    {models.map((m) => (
                      <SelectItem key={m.id} value={m.id}>
                        {m.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setOpen(false)} disabled={creating}>
              {t('common:actions.cancel')}
            </Button>
            <Button onClick={() => void create()} loading={creating}>
              {t('kb:dialog.create')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={toDelete !== null}
        onOpenChange={(next) => {
          if (!next && !deleting) setToDelete(null)
        }}
      >
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('kb:deleteTitle', { defaultValue: 'Delete knowledge base?' })}</DialogTitle>
            <DialogDescription>
              {t('kb:deleteBody', {
                name: toDelete?.name ?? '',
                defaultValue:
                  'This permanently deletes “{{name}}”, all its documents and their embeddings. Conversations that reference it will be unlinked. This cannot be undone.',
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setToDelete(null)} disabled={deleting}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" loading={deleting} onClick={() => void doDelete()}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function KnowledgeBasesSkeleton({ label }: { label: string }) {
  return (
    <div className="divide-y divide-[var(--color-divider)]" role="status" aria-label={label}>
      {Array.from({ length: 4 }, (_, index) => (
        <div key={index} className="flex min-h-16 items-center gap-3 px-2 py-2">
          <Skeleton className="size-8 shrink-0 rounded-[8px]" />
          <div className="min-w-0 flex-1 space-y-2">
            <Skeleton shape="line" className="h-3.5 w-2/5" />
            <Skeleton shape="line" className="w-3/5" />
          </div>
          <Skeleton shape="line" className="hidden w-24 sm:block" />
          <Skeleton className="size-8 shrink-0 rounded-[8px]" />
        </div>
      ))}
      <span className="sr-only">{label}</span>
    </div>
  )
}
