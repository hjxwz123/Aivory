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
      toast.error(e instanceof ApiError ? e.message : t('common:common.error'))
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
        <div className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-5 sm:px-8 py-8 pb-24">
          <p className="max-w-[60ch] text-[var(--color-fg-muted)] text-[15px] leading-relaxed">{t('kb:lead')}</p>

        <section className="mt-10">
          {loading ? (
            <div className="text-sm text-[var(--color-fg-subtle)]">{t('common:common.loading')}</div>
          ) : rows.length === 0 ? (
            <EmptyState
              icon={<Database size={20} aria-hidden />}
              title={t('kb:emptyTitle')}
              description={t('kb:emptyBody')}
              action={<Button variant="secondary" onClick={() => setOpen(true)}>{t('kb:createFirst')}</Button>}
            />
          ) : (
            <ul className="flex flex-col divide-y divide-[var(--color-divider)]">
              {rows.map((kb) => (
                <li key={kb.id} className="group/kb relative">
                  <Link
                    to={`/kb/${kb.id}`}
                    className="grid grid-cols-[1fr_180px] items-baseline gap-x-6 px-4 sm:px-6 -mx-4 sm:-mx-6 py-7 rounded-[12px] interactive hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    <div className="min-w-0 pr-10">
                      <h3 className="text-[22px] leading-[1.15] tracking-tight text-[var(--color-fg)] truncate">
                        {kb.name}
                      </h3>
                      {kb.description ? (
                        <p className="mt-1.5 text-[13.5px] text-[var(--color-fg-muted)] leading-relaxed line-clamp-2">
                          {kb.description}
                        </p>
                      ) : null}
                    </div>
                    <div className="text-[11.5px] text-[var(--color-fg-subtle)] tabular-nums text-right">
                      <div>{t('kb:stats.dim', { dim: kb.embedding_dim })}</div>
                      <time dateTime={new Date(kb.created_at * 1000).toISOString()}>
                        {t('kb:stats.created', { when: formatRelativeDate(kb.created_at * 1000) })}
                      </time>
                    </div>
                  </Link>
                  {/* Row actions — kept OUTSIDE the Link so a menu click never
                      navigates into the KB. Revealed on hover / focus. */}
                  <div className="absolute right-1 top-4">
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <button
                          type="button"
                          aria-label={t('common:actions.more', { defaultValue: 'More' })}
                          className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-subtle)] opacity-0 group-hover/kb:opacity-100 focus-visible:opacity-100 hover:bg-[var(--color-bg)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
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
                        {m.label} · dim {m.dim}
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
            <Button onClick={() => void create()} loading={creating}>{t('kb:dialog.create')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={toDelete !== null} onOpenChange={(o) => { if (!o) setToDelete(null) }}>
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
