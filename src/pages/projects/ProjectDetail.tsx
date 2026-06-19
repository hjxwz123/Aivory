import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  MoreHorizontal,
  Pencil,
  Pin,
  PinOff,
  Plus,
  Sparkles,
  Trash2,
  Save,
  Upload,
  Loader2,
  X,
} from 'lucide-react'
import type { Attachment, Conversation } from '@/types/chat'
import type { ProjectAccent } from '@/types/project'
import { useProjects } from '@/store/projects'
import { useConversations } from '@/store/conversations'
import { useModels } from '@/store/models'
import { useSettings } from '@/store/settings'
import { accentClasses, fileKindIcon, formatFileSize, PROJECT_ACCENT_OPTIONS } from '@/lib/project-helpers'
import { Composer } from '@/components/chat/composer'
import { ContentHeader } from '@/components/layout/content-header'
import { MoveToProjectSub } from '@/components/projects/move-to-project-menu'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Tooltip } from '@/components/ui/tooltip'
import { Switch } from '@/components/ui/switch'
import { EmptyState } from '@/components/ui/empty-state'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
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
import { cn, formatRelativeDate, truncate } from '@/lib/utils'

export default function ProjectDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const { t } = useTranslation(['projects', 'chat', 'common'])

  const project = useProjects((s) => s.projects.find((p) => p.id === id))
  const projectsLoaded = useProjects((s) => s.loaded)
  const loadOne = useProjects((s) => s.loadOne)
  const updateProject = useProjects((s) => s.updateProject)
  const renameProject = useProjects((s) => s.renameProject)
  const togglePin = useProjects((s) => s.togglePin)
  const deleteProject = useProjects((s) => s.deleteProject)
  const uploadFile = useProjects((s) => s.uploadFile)
  const removeFile = useProjects((s) => s.removeFile)
  const renameFile = useProjects((s) => s.renameFile)

  // Project documents come from the project's knowledge library and are only
  // returned by GET /projects/:id — hydrate them whenever the id changes so the
  // file list (and count) is correct on a fresh load.
  useEffect(() => {
    if (id) void loadOne(id)
  }, [id, loadOne])

  const allConversations = useConversations((s) => s.conversations)
  const createConversation = useConversations((s) => s.createConversation)
  const defaultModelId = useModels((s) => s.defaultId)

  const projectChats = useMemo<Conversation[]>(
    () =>
      project
        ? allConversations
            .filter((c) => c.projectId === project.id && !c.archived)
            .slice()
            .sort((a, b) => b.updatedAt - a.updatedAt)
        : [],
    [allConversations, project],
  )

  const [editingInstructions, setEditingInstructions] = useState(false)
  const [instructionsDraft, setInstructionsDraft] = useState('')
  const [savingInstructions, setSavingInstructions] = useState(false)

  const [renameOpen, setRenameOpen] = useState(false)
  const [renameDraft, setRenameDraft] = useState('')
  const [editOpen, setEditOpen] = useState(false)
  const [editDraft, setEditDraft] = useState<{
    name: string
    description: string
    accent: ProjectAccent
    emoji: string
    autoAddUploads: boolean
  }>({ name: '', description: '', accent: 'violet', emoji: '', autoAddUploads: false })
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [addFileOpen, setAddFileOpen] = useState(false)
  const [renameFileState, setRenameFileState] = useState<{ id: string; draft: string } | null>(null)

  if (!project && !projectsLoaded) {
    // Still hydrating — show a spinner rather than a premature 404.
    return (
      <div className="flex-1 grid place-items-center">
        <div className="flex flex-col items-center gap-4 text-[var(--color-fg-muted)]">
          <Loader2 size={24} className="animate-spin" aria-hidden />
          <span className="text-sm">{t('common:common.loading')}</span>
        </div>
      </div>
    )
  }

  if (!project) {
    return (
      <div className="flex-1 grid place-items-center">
        <EmptyState
          title={t('projects:detail.notFoundTitle')}
          description={t('projects:detail.notFoundBody')}
          action={<Button onClick={() => navigate('/projects')}>{t('projects:detail.goToProjects')}</Button>}
        />
      </div>
    )
  }

  const accent = accentClasses(project.accent)

  function startInstructionsEdit() {
    if (!project) return
    setInstructionsDraft(project.instructions)
    setEditingInstructions(true)
  }
  async function saveInstructions() {
    if (!project) return
    setSavingInstructions(true)
    try {
      await updateProject(project.id, { instructions: instructionsDraft })
      setEditingInstructions(false)
      toast.success(t('projects:detail.instructionsSaved'))
    } catch {
      toast.error(t('projects:detail.instructionsSaveFailed', { defaultValue: 'Failed to save instructions' }))
    } finally {
      setSavingInstructions(false)
    }
  }

  function openRename() {
    if (!project) return
    setRenameDraft(project.name)
    setRenameOpen(true)
  }
  function submitRename() {
    if (!project) return
    renameProject(project.id, renameDraft)
    setRenameOpen(false)
    toast.success(t('projects:detail.renamed'))
  }

  function openEdit() {
    if (!project) return
    setEditDraft({
      name: project.name,
      description: project.description ?? '',
      accent: project.accent,
      emoji: project.emoji ?? '',
      autoAddUploads: project.autoAddUploads ?? false,
    })
    setEditOpen(true)
  }
  function submitEdit() {
    if (!project) return
    // Send empty strings (not undefined) so clearing the description/marker is
    // actually transmitted — JSON.stringify drops undefined fields, which would
    // silently keep the old value on the backend.
    updateProject(project.id, {
      name: editDraft.name.trim() || project.name,
      description: editDraft.description.trim(),
      accent: editDraft.accent,
      emoji: editDraft.emoji.trim().slice(0, 2),
      autoAddUploads: editDraft.autoAddUploads,
    })
    setEditOpen(false)
    toast.success(t('projects:detail.edited'))
  }

  async function submitDelete() {
    if (!project) return
    const setProj = useConversations.getState().setProject
    for (const c of allConversations) {
      if (c.projectId === project.id) await setProj(c.id, undefined)
    }
    await deleteProject(project.id)
    setConfirmDelete(false)
    toast.success(t('projects:detail.deleted'))
    navigate('/projects')
  }

  async function startProjectChat(
    text: string,
    attachments: Attachment[],
    opts: { mode?: 'default' | 'deep-research' | 'canvas'; params?: Record<string, unknown> } = {},
  ) {
    if (!project) return
    const conv = await createConversation(defaultModelId, project.id)
    if (!conv) return
    navigate(`/chat/${conv.id}`)
    void useConversations.getState().sendMessage({
      conversationId: conv.id,
      text,
      modelId: conv.modelId || defaultModelId,
      attachments,
      mode: opts.mode,
      params: opts.params,
    })
  }

  return (
    <div className="flex-1 min-h-0 flex flex-col bg-[var(--color-bg)] text-[var(--color-fg)]">
      <ContentHeader
        title={project.name}
        backTo="/projects"
        backLabel={t('projects:detail.back')}
        actions={
          <DropdownMenu>
            <Tooltip content={t('chat:actions.more')}>
              <DropdownMenuTrigger asChild>
                <button
                  type="button"
                  aria-label={t('chat:actions.more')}
                  className="inline-flex items-center justify-center size-9 rounded-[10px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                >
                  <MoreHorizontal size={15} aria-hidden />
                </button>
              </DropdownMenuTrigger>
            </Tooltip>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={openRename}>
                <Pencil size={13} aria-hidden />
                {t('projects:detail.menu.rename')}
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={openEdit}>
                <Sparkles size={13} aria-hidden />
                {t('projects:detail.menu.edit')}
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={() => togglePin(project.id)}>
                {project.pinned ? <PinOff size={13} aria-hidden /> : <Pin size={13} aria-hidden />}
                {project.pinned ? t('projects:detail.menu.unpin') : t('projects:detail.menu.pin')}
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem destructive onSelect={() => setConfirmDelete(true)}>
                <Trash2 size={13} aria-hidden />
                {t('projects:detail.menu.delete')}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        }
      />
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-5 sm:px-8 py-8 pb-24">
          {/* Identity strip: a slim accent rule, the description, and metadata.
              The name itself lives in the header above, so it isn't repeated. */}
          <div className="min-w-0">
            <span className={cn('block h-1 w-10 rounded-full', accent.bar)} aria-hidden />
            {project.description ? (
              <p className="mt-4 text-[var(--color-fg-muted)] text-[15px] sm:text-[17px] leading-relaxed max-w-[60ch]">
                {project.description}
              </p>
            ) : null}
            <dl className="mt-4 flex flex-wrap items-center gap-x-4 gap-y-1.5 text-[11.5px] text-[var(--color-fg-subtle)] tabular-nums">
              <Meta>{t('projects:card.files', { count: project.files.length })}</Meta>
              <Meta>{t('projects:card.chats', { count: projectChats.length })}</Meta>
              <Meta>{t('projects:card.updated', { when: formatRelativeDate(project.updatedAt) })}</Meta>
              {project.pinned ? (
                <Meta>
                  <Pin size={10} className="inline -translate-y-px mr-1" aria-hidden />
                  {t('projects:list.filterPinned')}
                </Meta>
              ) : null}
            </dl>
          </div>

        {/* Composer. Centered, with a small inline label so the project
            context is unmistakable. */}
        <section className="mt-12 sm:mt-16">
          <div className="mx-auto max-w-[44rem]">
            <p className="mb-3 text-[12px] text-[var(--color-fg-subtle)]">
              {t('projects:detail.newChat')}
            </p>
            <Composer
              modelId={defaultModelId}
              onModelChange={(modelId) =>
                useSettings.getState().setModels({ defaultModelId: modelId })
              }
              onSubmit={(text, atts, opts) => void startProjectChat(text, atts, opts)}
            />
          </div>
        </section>

        {/* Instructions + Files. Asymmetric split: instructions are the
            voice of the project (1fr, serif body), files are the supporting
            library (360px, hairline-divided list). No tinted chips. */}
        <section className="mt-16 sm:mt-20 grid grid-cols-1 lg:grid-cols-[1fr_360px] gap-10 lg:gap-14">
          {/* Instructions */}
          <div className="min-w-0">
            <SectionHeader
              title={t('projects:detail.instructionsSection')}
              hint={t('projects:detail.instructionsHint')}
              action={
                !editingInstructions && project.instructions ? (
                  <button
                    type="button"
                    onClick={startInstructionsEdit}
                    className="inline-flex items-center gap-1.5 text-[12px] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] interactive rounded-[6px] px-1.5 py-0.5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    <Pencil size={12} aria-hidden />
                    {t('projects:detail.instructionsEdit')}
                  </button>
                ) : null
              }
            />

            {editingInstructions ? (
              <div className="mt-4 flex flex-col gap-3">
                <Textarea
                  value={instructionsDraft}
                  onChange={(e) => setInstructionsDraft(e.target.value)}
                  placeholder={t('projects:detail.instructionsPlaceholder')}
                  rows={9}
                  className="font-serif text-[15px] leading-relaxed"
                />
                <div className="flex items-center justify-end gap-2">
                  <Button
                    size="sm"
                    variant="ghost"
                    leadingIcon={<X size={13} aria-hidden />}
                    onClick={() => setEditingInstructions(false)}
                  >
                    {t('common:actions.cancel')}
                  </Button>
                  <Button size="sm" variant="secondary" leadingIcon={<Save size={13} aria-hidden />} onClick={() => void saveInstructions()} loading={savingInstructions} disabled={savingInstructions}>
                    {t('projects:detail.instructionsSave')}
                  </Button>
                </div>
              </div>
            ) : project.instructions ? (
              <div className="mt-4 font-serif text-[15.5px] leading-[1.7] text-[var(--color-fg)] whitespace-pre-wrap max-w-[62ch]">
                {project.instructions}
              </div>
            ) : (
              <button
                type="button"
                onClick={startInstructionsEdit}
                className={cn(
                  'mt-4 w-full text-left rounded-[12px] border border-dashed border-[var(--color-border)] p-5',
                  'text-[13.5px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg-muted)] interactive',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                )}
              >
                <span className="block text-[var(--color-fg-muted)] mb-1.5 max-w-[60ch]">
                  {t('projects:detail.instructionsEmpty')}
                </span>
                <span className="inline-flex items-center gap-1 text-[12px] text-[var(--color-accent)]">
                  <Plus size={12} aria-hidden /> {t('projects:detail.instructionsAddCta')}
                </span>
              </button>
            )}
          </div>

          {/* Files */}
          <aside className="lg:sticky lg:top-6 lg:self-start">
            <SectionHeader
              title={t('projects:detail.filesSection')}
              count={project.files.length}
              action={
                <Button
                  size="xs"
                  variant="ghost"
                  leadingIcon={<Plus size={12} aria-hidden />}
                  onClick={() => setAddFileOpen(true)}
                >
                  {t('projects:detail.filesAdd')}
                </Button>
              }
            />

            {project.files.length === 0 ? (
              <button
                type="button"
                onClick={() => setAddFileOpen(true)}
                className={cn(
                  'mt-4 w-full rounded-[12px] border border-dashed border-[var(--color-border)] p-5 text-left',
                  'text-[12.5px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] interactive',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                )}
              >
                <p className="text-[var(--color-fg-muted)] font-medium">
                  {t('projects:detail.filesEmpty')}
                </p>
                <p className="mt-1.5 leading-relaxed">{t('projects:detail.filesEmptyBody')}</p>
                <span className="mt-3 inline-flex items-center gap-1 text-[12px] text-[var(--color-accent)]">
                  <Plus size={12} aria-hidden /> {t('projects:detail.filesAdd')}
                </span>
              </button>
            ) : (
              <ul className="mt-3 flex flex-col divide-y divide-[var(--color-divider)] border-t border-[var(--color-divider)]">
                {project.files.map((f) => {
                  const Icon = fileKindIcon(f.kind)
                  return (
                    <li key={f.id}>
                      <div className="group/file relative flex items-start gap-3 py-3.5 pr-1">
                        <Icon
                          size={14}
                          className="mt-1 shrink-0 text-[var(--color-fg-subtle)]"
                          aria-hidden
                        />
                        <div className="flex-1 min-w-0">
                          <div className="font-serif text-[14px] leading-snug text-[var(--color-fg)]">
                            {f.name}
                          </div>
                          <div className="mt-1 text-[10.5px] text-[var(--color-fg-subtle)] tabular-nums">
                            {t(`projects:detail.kinds.${f.kind}`)}
                            {f.size > 0 ? (
                              <>
                                <span aria-hidden className="mx-1.5 opacity-50">·</span>
                                {formatFileSize(f.size)}
                              </>
                            ) : null}
                          </div>
                          {f.excerpt ? (
                            <p className="mt-1.5 text-[12px] text-[var(--color-fg-muted)] leading-snug line-clamp-2">
                              {f.excerpt}
                            </p>
                          ) : null}
                        </div>
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <button
                              type="button"
                              aria-label={t('chat:actions.more')}
                              className="inline-flex items-center justify-center size-7 rounded-[6px] text-[var(--color-fg-faint)] opacity-0 group-hover/file:opacity-100 data-[state=open]:opacity-100 hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg-muted)] interactive focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                            >
                              <MoreHorizontal size={13} aria-hidden />
                            </button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem
                              onSelect={() => setRenameFileState({ id: f.id, draft: f.name })}
                            >
                              <Pencil size={13} aria-hidden /> {t('chat:sidebar.rename')}
                            </DropdownMenuItem>
                            <DropdownMenuSeparator />
                            <DropdownMenuItem
                              destructive
                              onSelect={() => {
                                removeFile(project.id, f.id)
                                toast.success(t('projects:detail.filesRemoved'))
                              }}
                            >
                              <Trash2 size={13} aria-hidden /> {t('common:actions.delete')}
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </div>
                    </li>
                  )
                })}
              </ul>
            )}
          </aside>
        </section>

        {/* Conversations TOC */}
        <section className="mt-16 sm:mt-20">
          <SectionHeader
            title={t('projects:detail.chatsSection')}
            count={projectChats.length}
          />

          {projectChats.length === 0 ? (
            <p className="mt-4 max-w-[60ch] text-[13.5px] text-[var(--color-fg-subtle)] leading-relaxed">
              {t('projects:detail.chatsEmptyBody')}
            </p>
          ) : (
            <ul className="mt-3 flex flex-col divide-y divide-[var(--color-divider)] border-t border-[var(--color-divider)]">
              {projectChats.map((c) => (
                <li key={c.id}>
                  <div className="group/chatrow relative -mx-2 rounded-[10px] interactive hover:bg-[var(--color-bg-muted)]">
                    <Link
                      to={`/chat/${c.id}`}
                      aria-label={t('projects:detail.openChatAria', { title: c.title })}
                      className={cn(
                        'grid items-baseline grid-cols-[1fr_auto] gap-x-5 py-4 pl-2 pr-11 rounded-[10px]',
                        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                      )}
                    >
                      <span className="font-serif text-[16px] sm:text-[17px] leading-snug text-[var(--color-fg)] truncate">
                        {truncate(c.title, 90)}
                      </span>
                      <time
                        className="text-[11.5px] text-[var(--color-fg-subtle)] tabular-nums shrink-0"
                        dateTime={new Date(c.updatedAt).toISOString()}
                      >
                        {formatRelativeDate(c.updatedAt)}
                      </time>
                    </Link>
                    <div className="absolute right-1 top-1/2 -translate-y-1/2">
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <button
                            type="button"
                            aria-label={t('chat:actions.more')}
                            className="inline-flex items-center justify-center size-7 rounded-[6px] text-[var(--color-fg-faint)] opacity-0 group-hover/chatrow:opacity-100 data-[state=open]:opacity-100 hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg-muted)] interactive focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                          >
                            <MoreHorizontal size={13} aria-hidden />
                          </button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <MoveToProjectSub conversationId={c.id} currentProjectId={project.id} />
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </div>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </section>
        </div>
      </div>

      {/* Rename dialog */}
      <Dialog open={renameOpen} onOpenChange={setRenameOpen}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('projects:detail.renameTitle')}</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <Input
              autoFocus
              value={renameDraft}
              onChange={(e) => setRenameDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
                  e.preventDefault()
                  submitRename()
                }
              }}
            />
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setRenameOpen(false)}>
              {t('common:actions.cancel')}
            </Button>
            <Button onClick={submitRename}>{t('projects:detail.renameSave')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Edit dialog */}
      <Dialog open={editOpen} onOpenChange={setEditOpen}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>{t('projects:detail.editTitle')}</DialogTitle>
            <DialogDescription>{t('projects:detail.editDescription')}</DialogDescription>
          </DialogHeader>
          <DialogBody className="flex flex-col gap-4">
            <Field label={t('projects:detail.editNameLabel')} htmlFor="ep-name">
              <Input
                id="ep-name"
                value={editDraft.name}
                onChange={(e) => setEditDraft((d) => ({ ...d, name: e.target.value }))}
                autoFocus
              />
            </Field>
            <Field label={t('projects:detail.editDescLabel')} htmlFor="ep-desc">
              <Input
                id="ep-desc"
                value={editDraft.description}
                onChange={(e) => setEditDraft((d) => ({ ...d, description: e.target.value }))}
                placeholder={t('projects:detail.editDescPlaceholder')}
              />
            </Field>
            <div className="grid grid-cols-1 sm:grid-cols-[1fr_120px] gap-4">
              <Field label={t('projects:detail.editAccentLabel')}>
                <div className="flex flex-wrap gap-2">
                  {PROJECT_ACCENT_OPTIONS.map((a) => {
                    const cls = accentClasses(a)
                    const selected = editDraft.accent === a
                    return (
                      <button
                        type="button"
                        key={a}
                        onClick={() => setEditDraft((d) => ({ ...d, accent: a }))}
                        aria-pressed={selected}
                        aria-label={t(`projects:accent.${a}`)}
                        className={cn(
                          'inline-flex items-center gap-2 rounded-[10px] px-2.5 py-1.5 text-xs interactive border',
                          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                          selected
                            ? 'border-[var(--color-border-strong)] bg-[var(--color-bg-muted)] text-[var(--color-fg)]'
                            : 'border-[var(--color-border)] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                        )}
                      >
                        <span className={cn('inline-block size-3 rounded-full', cls.bar)} aria-hidden />
                        {t(`projects:accent.${a}`)}
                      </button>
                    )
                  })}
                </div>
              </Field>
              <Field label={t('projects:detail.editEmojiLabel')} htmlFor="ep-emoji">
                <Input
                  id="ep-emoji"
                  value={editDraft.emoji}
                  onChange={(e) => setEditDraft((d) => ({ ...d, emoji: e.target.value }))}
                  placeholder={t('projects:detail.editEmojiPlaceholder')}
                  maxLength={2}
                />
              </Field>
            </div>
            <label
              htmlFor="ep-auto-add"
              className="flex items-start justify-between gap-4 rounded-[12px] border border-[var(--color-border)] p-3.5"
            >
              <span className="min-w-0">
                <span className="block text-[13.5px] font-medium text-[var(--color-fg)]">
                  {t('projects:detail.editAutoAddLabel')}
                </span>
                <span className="mt-0.5 block text-[12px] text-[var(--color-fg-subtle)] leading-relaxed">
                  {t('projects:detail.editAutoAddHint')}
                </span>
              </span>
              <Switch
                id="ep-auto-add"
                checked={editDraft.autoAddUploads}
                onCheckedChange={(v) => setEditDraft((d) => ({ ...d, autoAddUploads: v }))}
              />
            </label>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setEditOpen(false)}>
              {t('common:actions.cancel')}
            </Button>
            <Button onClick={submitEdit}>{t('projects:detail.editSave')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Confirm delete */}
      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('projects:detail.deleteTitle')}</DialogTitle>
            <DialogDescription>{t('projects:detail.deleteBody')}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(false)}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" onClick={submitDelete}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Add file */}
      <AddFileDialog
        open={addFileOpen}
        onOpenChange={setAddFileOpen}
        projectName={project.name}
        onUpload={async (files) => {
          let ok = 0
          for (const file of files) {
            const res = await uploadFile(project.id, file)
            if (res) ok += 1
          }
          // Re-pull the document list so freshly-uploaded docs (and their
          // server-assigned size / status) replace the optimistic entries.
          void loadOne(project.id)
          if (ok > 0) toast.success(t('projects:detail.filesAdded', { count: ok }))
          if (ok < files.length) toast.error(t('projects:detail.filesAddFailed'))
        }}
      />

      {/* Rename file */}
      <Dialog
        open={Boolean(renameFileState)}
        onOpenChange={(open) => {
          if (!open) setRenameFileState(null)
        }}
      >
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('chat:sidebar.rename')}</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <Input
              autoFocus
              value={renameFileState?.draft ?? ''}
              onChange={(e) =>
                setRenameFileState((s) => (s ? { ...s, draft: e.target.value } : s))
              }
            />
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setRenameFileState(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              onClick={() => {
                if (renameFileState) {
                  renameFile(project.id, renameFileState.id, renameFileState.draft)
                  setRenameFileState(null)
                  toast.success(t('projects:detail.filesRenamed'))
                }
              }}
            >
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/* Small primitives kept local: they're page-specific micro-typography
   patterns and don't earn a shared module yet. */
function Meta({ children }: { children: React.ReactNode }) {
  return (
    <span className="inline-flex items-center after:content-['/'] after:ml-4 after:text-[var(--color-fg-faint)] last:after:hidden">
      {children}
    </span>
  )
}

function SectionHeader({
  title,
  hint,
  count,
  action,
}: {
  title: string
  hint?: string
  count?: number
  action?: React.ReactNode
}) {
  return (
    <div className="flex items-baseline gap-3">
      <h2 className="font-serif text-[20px] sm:text-[22px] tracking-tight text-[var(--color-fg)]">
        {title}
      </h2>
      {typeof count === 'number' ? (
        <span className="text-[12px] text-[var(--color-fg-subtle)] tabular-nums">
          {count}
        </span>
      ) : null}
      {hint ? (
        <p className="hidden sm:block text-[12px] text-[var(--color-fg-subtle)] leading-relaxed flex-1 min-w-0 max-w-[44ch]">
          {hint}
        </p>
      ) : null}
      {action ? <div className="ml-auto shrink-0">{action}</div> : null}
    </div>
  )
}

function AddFileDialog({
  open,
  onOpenChange,
  projectName,
  onUpload,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  projectName: string
  onUpload: (files: File[]) => Promise<void>
}) {
  const { t } = useTranslation(['projects', 'common'])
  const fileInput = useRef<HTMLInputElement>(null)
  const [uploading, setUploading] = useState(false)

  async function handleFiles(list: FileList | null) {
    if (!list || list.length === 0) return
    setUploading(true)
    try {
      await onUpload(Array.from(list))
      onOpenChange(false)
    } finally {
      setUploading(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="md">
        <DialogHeader>
          <DialogTitle>{t('projects:detail.filesAddTitle', { name: projectName })}</DialogTitle>
          <DialogDescription>{t('projects:detail.filesAddDescription')}</DialogDescription>
        </DialogHeader>
        <DialogBody>
          <button
            type="button"
            onClick={() => fileInput.current?.click()}
            disabled={uploading}
            className={cn(
              'w-full rounded-[14px] border border-dashed border-[var(--color-border-strong)] bg-[var(--color-bg-muted)] p-10 text-center interactive',
              'hover:border-[var(--color-accent)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              uploading && 'opacity-60 cursor-not-allowed',
            )}
          >
            <input
              ref={fileInput}
              type="file"
              hidden
              multiple
              onChange={(e) => {
                void handleFiles(e.currentTarget.files)
                e.currentTarget.value = ''
              }}
            />
            {uploading ? (
              <Loader2 size={24} className="mx-auto animate-spin text-[var(--color-fg-subtle)]" aria-hidden />
            ) : (
              <Upload size={24} className="mx-auto text-[var(--color-fg-subtle)]" aria-hidden />
            )}
            <p className="mt-3 text-[var(--color-fg-muted)] text-sm">
              {uploading ? t('projects:detail.filesUploading') : t('projects:detail.filesClickToChoose')}
            </p>
          </button>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={uploading}>
            {t('common:actions.cancel')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
