/**
 * KnowledgeBasesList — gallery of the user's knowledge bases.
 */
import { activeWorkspaceId } from '@/store/workspaces'
import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Plus, Database } from 'lucide-react'
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
  const [rows, setRows] = useState<ApiKnowledgeBase[]>([])
  const [models, setModels] = useState<ApiModel[]>([])
  const [loading, setLoading] = useState(true)
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState({ name: '', description: '', embedding_model_id: '' })
  const [creating, setCreating] = useState(false)
  const creatingRef = useRef(false)

  async function load() {
    setLoading(true)
    try {
      const [kb, em] = await Promise.all([kbsApi.list(activeWorkspaceId()), modelsApi.listEmbedding()])
      setRows(kb)
      setModels(em.models)
      if (em.models.length > 0 && !draft.embedding_model_id) {
        setDraft((d) => ({ ...d, embedding_model_id: em.default_id || em.models[0].id }))
      }
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common:common.error'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

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
                <li key={kb.id}>
                  <Link
                    to={`/kb/${kb.id}`}
                    className="grid grid-cols-[1fr_180px] items-baseline gap-x-6 px-4 sm:px-6 -mx-4 sm:-mx-6 py-7 rounded-[12px] interactive hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    <div className="min-w-0">
                      <h3 className="font-serif text-[22px] leading-[1.15] tracking-tight text-[var(--color-fg)] truncate">
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
    </div>
  )
}
