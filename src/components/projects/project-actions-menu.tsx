import { useEffect, useId, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  Lock,
  MoreHorizontal,
  Pencil,
  Pin,
  PinOff,
  Sparkles,
  Trash2,
  Unlock,
} from 'lucide-react'
import type { Project, ProjectAccent } from '@/types/project'
import { useProjects } from '@/store/projects'
import { useConversations } from '@/store/conversations'
import { accentClasses, PROJECT_ACCENT_OPTIONS } from '@/lib/project-helpers'
import { cn } from '@/lib/utils'
import { toast } from '@/hooks/use-toast'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Tooltip } from '@/components/ui/tooltip'
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

interface ProjectActionsMenuProps {
  project: Project
  canUseKnowledgeBases: boolean
  canManageProject: boolean
  canChangeProjectVisibility: boolean
  canDeleteConversations: boolean
  placement?: 'header' | 'sidebar'
}

export function ProjectActionsMenu({
  project,
  canUseKnowledgeBases,
  canManageProject: canManageProjectProp,
  canChangeProjectVisibility: canChangeProjectVisibilityProp,
  canDeleteConversations,
  placement = 'header',
}: ProjectActionsMenuProps) {
  const { t } = useTranslation(['projects', 'chat', 'common'])
  const navigate = useNavigate()
  const location = useLocation()
  const currentProject = project
  const {
    loadOne,
    updateProject,
    renameProject,
    togglePin,
    deleteProject,
    setVisibility: setProjectVisibility,
  } = useProjects.getState()
  const canManageProject = canUseKnowledgeBases && canManageProjectProp
  const canUploadProjectFiles = canUseKnowledgeBases && currentProject.canUploadFiles === true
  const canChangeProjectVisibility = canUseKnowledgeBases && canChangeProjectVisibilityProp
  const fieldID = useId().replace(/:/g, '')

  const [menuOpen, setMenuOpen] = useState(false)
  const [renameOpen, setRenameOpen] = useState(false)
  const [renameDraft, setRenameDraft] = useState('')
  const [renaming, setRenaming] = useState(false)
  const [editOpen, setEditOpen] = useState(false)
  const [editDraft, setEditDraft] = useState<{
    name: string
    description: string
    accent: ProjectAccent
    emoji: string
    autoAddUploads: boolean
  }>({ name: '', description: '', accent: 'violet', emoji: '', autoAddUploads: false })
  const [savingDetails, setSavingDetails] = useState(false)
  const [visibilityBusy, setVisibilityBusy] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [deleteConversations, setDeleteConversations] = useState(false)
  const deletingRef = useRef(false)
  const [deleting, setDeleting] = useState(false)

  useEffect(() => {
    if (canUseKnowledgeBases) return
    setMenuOpen(false)
    setRenameOpen(false)
    setEditOpen(false)
    setConfirmDelete(false)
  }, [canUseKnowledgeBases])

  useEffect(() => {
    if (canManageProject) return
    setRenameOpen(false)
    setEditOpen(false)
    setConfirmDelete(false)
  }, [canManageProject])

  useEffect(() => {
    if (canDeleteConversations) return
    setDeleteConversations(false)
  }, [canDeleteConversations])

  function handleMenuOpen(next: boolean) {
    setMenuOpen(next)
    if (next && (
      currentProject.canDelete === undefined ||
      currentProject.canUploadFiles === undefined
    )) {
      void loadOne(currentProject.id)
    }
  }

  function openRename() {
    if (!canManageProject) return
    setRenameDraft(currentProject.name)
    setRenameOpen(true)
  }

  async function submitRename() {
    if (!canManageProject || renaming || !renameDraft.trim()) return
    setRenaming(true)
    try {
      if (await renameProject(currentProject.id, renameDraft)) {
        setRenameOpen(false)
        toast.success(t('projects:detail.renamed'))
      }
    } finally {
      setRenaming(false)
    }
  }

  function openEdit() {
    if (!canManageProject) return
    setEditDraft({
      name: currentProject.name,
      description: currentProject.description ?? '',
      accent: currentProject.accent,
      emoji: currentProject.emoji ?? '',
      autoAddUploads: currentProject.autoAddUploads ?? false,
    })
    setEditOpen(true)
  }

  async function submitEdit() {
    if (!canManageProject || savingDetails) return
    const patch: Parameters<typeof updateProject>[1] = {
      name: editDraft.name.trim() || currentProject.name,
      description: editDraft.description.trim(),
      accent: editDraft.accent,
      emoji: editDraft.emoji.trim().slice(0, 2),
    }
    if (canUploadProjectFiles) patch.autoAddUploads = editDraft.autoAddUploads
    setSavingDetails(true)
    try {
      if (await updateProject(currentProject.id, patch)) {
        setEditOpen(false)
        toast.success(t('projects:detail.edited'))
      }
    } finally {
      setSavingDetails(false)
    }
  }

  async function toggleVisibility() {
    if (!canChangeProjectVisibility || visibilityBusy) return
    setVisibilityBusy(true)
    try {
      const ok = await setProjectVisibility(currentProject.id, !currentProject.isPublic)
      if (ok) {
        toast.success(currentProject.isPublic
          ? t('projects:detail.menu.visibilityPrivate')
          : t('projects:detail.menu.visibilityShared'))
      }
    } finally {
      setVisibilityBusy(false)
    }
  }

  async function submitDelete() {
    if (!canManageProject || deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      if (!(await deleteProject(currentProject.id, deleteConversations))) return
      if (deleteConversations) {
        useConversations.setState((state) => ({
          conversations: state.conversations.filter(
            (conversation) => conversation.projectId !== currentProject.id,
          ),
        }))
        void useConversations.getState().load()
      } else {
        useConversations.setState((state) => ({
          conversations: state.conversations.map((conversation) =>
            conversation.projectId === currentProject.id
              ? { ...conversation, projectId: undefined }
              : conversation,
          ),
        }))
      }
      setConfirmDelete(false)
      setDeleteConversations(false)
      toast.success(t('projects:detail.deleted'))
      if (location.pathname === `/projects/${currentProject.id}`) navigate('/projects')
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  const sidebar = placement === 'sidebar'
  return (
    <>
      <DropdownMenu open={menuOpen} onOpenChange={handleMenuOpen}>
        <Tooltip content={t('chat:actions.more')}>
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              aria-label={t('chat:actions.more')}
              className={cn(
                'inline-flex shrink-0 items-center justify-center text-[var(--color-fg-muted)] interactive',
                'hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] data-[state=open]:bg-[var(--color-bg-muted)]',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                sidebar
                  ? 'mr-1 size-8 rounded-[7px] max-lg:size-[var(--tap-min)] max-sm:!size-9'
                  : 'size-9 rounded-[10px] max-lg:size-[var(--tap-min)]',
              )}
            >
              <MoreHorizontal size={sidebar ? 14 : 15} aria-hidden />
            </button>
          </DropdownMenuTrigger>
        </Tooltip>
        <DropdownMenuContent align="end">
          <DropdownMenuItem disabled={!canManageProject} onSelect={openRename}>
            <Pencil size={13} aria-hidden />
            {t('projects:detail.menu.rename')}
          </DropdownMenuItem>
          <DropdownMenuItem disabled={!canManageProject} onSelect={openEdit}>
            <Sparkles size={13} aria-hidden />
            {t('projects:detail.menu.edit')}
          </DropdownMenuItem>
          <DropdownMenuItem
            disabled={!canManageProject}
            onSelect={() => void togglePin(currentProject.id)}
          >
            {currentProject.pinned ? <PinOff size={13} aria-hidden /> : <Pin size={13} aria-hidden />}
            {currentProject.pinned ? t('projects:detail.menu.unpin') : t('projects:detail.menu.pin')}
          </DropdownMenuItem>
          {canChangeProjectVisibility ? (
            <DropdownMenuItem disabled={visibilityBusy} onSelect={() => void toggleVisibility()}>
              {currentProject.isPublic ? <Lock size={13} aria-hidden /> : <Unlock size={13} aria-hidden />}
              {currentProject.isPublic
                ? t('projects:detail.menu.makePrivate')
                : t('projects:detail.menu.makeShared')}
            </DropdownMenuItem>
          ) : null}
          {canManageProject ? (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                destructive
                onSelect={() => {
                  setDeleteConversations(false)
                  setConfirmDelete(true)
                }}
              >
                <Trash2 size={13} aria-hidden />
                {t('projects:detail.menu.delete')}
              </DropdownMenuItem>
            </>
          ) : null}
        </DropdownMenuContent>
      </DropdownMenu>

      <Dialog open={renameOpen} onOpenChange={(open) => !renaming && setRenameOpen(open)}>
        <DialogContent size="sm" closeDisabled={renaming}>
          <DialogHeader>
            <DialogTitle>{t('projects:detail.renameTitle')}</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <Input
              autoFocus
              value={renameDraft}
              onChange={(event) => setRenameDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter' && !event.nativeEvent.isComposing) {
                  event.preventDefault()
                  void submitRename()
                }
              }}
            />
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setRenameOpen(false)} disabled={renaming}>
              {t('common:actions.cancel')}
            </Button>
            <Button onClick={() => void submitRename()} loading={renaming} disabled={!renameDraft.trim()}>
              {t('projects:detail.renameSave')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={editOpen} onOpenChange={(open) => !savingDetails && setEditOpen(open)}>
        <DialogContent size="lg" closeDisabled={savingDetails}>
          <DialogHeader>
            <DialogTitle>{t('projects:detail.editTitle')}</DialogTitle>
            <DialogDescription>{t('projects:detail.editDescription')}</DialogDescription>
          </DialogHeader>
          <DialogBody className="flex flex-col gap-4">
            <Field label={t('projects:detail.editNameLabel')} htmlFor={`${fieldID}-name`}>
              <Input
                id={`${fieldID}-name`}
                value={editDraft.name}
                onChange={(event) => setEditDraft((draft) => ({ ...draft, name: event.target.value }))}
                autoFocus
              />
            </Field>
            <Field label={t('projects:detail.editDescLabel')} htmlFor={`${fieldID}-description`}>
              <Input
                id={`${fieldID}-description`}
                value={editDraft.description}
                onChange={(event) => setEditDraft((draft) => ({ ...draft, description: event.target.value }))}
                placeholder={t('projects:detail.editDescPlaceholder')}
              />
            </Field>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-[1fr_120px]">
              <Field label={t('projects:detail.editAccentLabel')}>
                <div className="flex flex-wrap gap-2">
                  {PROJECT_ACCENT_OPTIONS.map((accent) => {
                    const classes = accentClasses(accent)
                    const selected = editDraft.accent === accent
                    return (
                      <button
                        type="button"
                        key={accent}
                        onClick={() => setEditDraft((draft) => ({ ...draft, accent }))}
                        aria-pressed={selected}
                        aria-label={t(`projects:accent.${accent}`)}
                        className={cn(
                          'inline-flex items-center gap-2 rounded-[10px] border px-2.5 py-1.5 text-xs interactive',
                          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                          selected
                            ? 'border-[var(--color-border-strong)] bg-[var(--color-bg-muted)] text-[var(--color-fg)]'
                            : 'border-[var(--color-border)] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                        )}
                      >
                        <span className={cn('inline-block size-3 rounded-full', classes.bar)} aria-hidden />
                        {t(`projects:accent.${accent}`)}
                      </button>
                    )
                  })}
                </div>
              </Field>
              <Field label={t('projects:detail.editEmojiLabel')} htmlFor={`${fieldID}-emoji`}>
                <Input
                  id={`${fieldID}-emoji`}
                  value={editDraft.emoji}
                  onChange={(event) => setEditDraft((draft) => ({ ...draft, emoji: event.target.value }))}
                  placeholder={t('projects:detail.editEmojiPlaceholder')}
                  maxLength={2}
                />
              </Field>
            </div>
            {canUploadProjectFiles ? (
              <label
                htmlFor={`${fieldID}-auto-add`}
                className="flex items-start justify-between gap-4 rounded-[12px] border border-[var(--color-border)] p-3.5"
              >
                <span className="min-w-0">
                  <span className="block text-[13.5px] font-medium text-[var(--color-fg)]">
                    {t('projects:detail.editAutoAddLabel')}
                  </span>
                  <span className="mt-0.5 block text-[12px] leading-relaxed text-[var(--color-fg-subtle)]">
                    {t('projects:detail.editAutoAddHint')}
                  </span>
                </span>
                <Switch
                  id={`${fieldID}-auto-add`}
                  checked={editDraft.autoAddUploads}
                  onCheckedChange={(value) => setEditDraft((draft) => ({ ...draft, autoAddUploads: value }))}
                />
              </label>
            ) : null}
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setEditOpen(false)} disabled={savingDetails}>
              {t('common:actions.cancel')}
            </Button>
            <Button onClick={() => void submitEdit()} loading={savingDetails}>
              {t('projects:detail.editSave')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={confirmDelete && canManageProject}
        onOpenChange={(open) => {
          if (deleting) return
          setConfirmDelete(open)
          if (!open) setDeleteConversations(false)
        }}
      >
        <DialogContent size="sm" closeDisabled={deleting}>
          <DialogHeader>
            <DialogTitle>{t('projects:detail.deleteTitle')}</DialogTitle>
            <DialogDescription>
              {t(deleteConversations
                ? 'projects:detail.deleteBodyWithConversations'
                : 'projects:detail.deleteBody')}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter className="flex-wrap gap-y-2 sm:flex-nowrap">
            {canDeleteConversations ? (
              <label className="mr-auto flex min-w-0 cursor-pointer items-center gap-2 py-1 pr-3 text-[13px] text-[var(--color-fg-muted)] max-sm:w-full">
                <Checkbox
                  checked={deleteConversations}
                  disabled={deleting}
                  onChange={(event) => setDeleteConversations(event.target.checked)}
                />
                <span>{t('projects:detail.deleteConversations')}</span>
              </label>
            ) : null}
            <div className="ml-auto flex items-center gap-1.5">
              <Button variant="ghost" onClick={() => setConfirmDelete(false)} disabled={deleting}>
                {t('common:actions.cancel')}
              </Button>
              <Button variant="destructive" onClick={() => void submitDelete()} loading={deleting}>
                {t('common:actions.delete')}
              </Button>
            </div>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
