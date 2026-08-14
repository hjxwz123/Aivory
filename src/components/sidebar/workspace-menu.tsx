/**
 * Workspace UI (§workspaces) — the avatar-menu section for switching/creating
 * workspaces plus the members/invite management dialog. Users with no
 * workspaces AND no create-capability see nothing (per spec: 左下角不显示).
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, ArrowLeftRight, Briefcase, Check, Copy, Home, LogOut, Plus, RefreshCw, Settings2, Trash2, UserX, Users } from 'lucide-react'
import { workspacesApi } from '@/api'
import type { ApiWorkspaceMember, ApiWorkspaceMemberPermissions } from '@/api/types'
import { useAuth } from '@/store/auth'
import { useWorkspaces } from '@/store/workspaces'
import { toast } from '@/hooks/use-toast'
import { useCopy } from '@/hooks/use-clipboard'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { initials } from '@/components/ui/avatar.utils'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { subscribeAccessInvalidation } from '@/lib/access-events'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

/** Whether the current user may create workspaces (group feature / admin). */
function canCreateWorkspaces(): boolean {
  const u = useAuth.getState().user
  return u?.role === 'admin' || (u?.features ?? []).includes('workspaces')
}

/**
 * SpaceSwitcherButton — a standalone icon button beside the sidebar avatar that
 * opens a flat space picker (Personal + every workspace, active one checked).
 * Rendered whenever the user belongs to ≥1 workspace, so it is the primary
 * switcher in BOTH the personal space (pick a workspace to enter) and inside a
 * workspace (jump to another space or back to personal). A plain top-level
 * DropdownMenu — not a nested submenu — so it never clips (§workspaces).
 */
export function SpaceSwitcherButton() {
  const { t } = useTranslation('chat')
  const workspaces = useWorkspaces((s) => s.workspaces)
  const activeId = useWorkspaces((s) => s.activeId)
  const switchTo = useWorkspaces((s) => s.switchTo)

  if (workspaces.length === 0) return null

  return (
    <DropdownMenu>
      <Tooltip content={t('workspace.switchSpace', { defaultValue: 'Switch space' })}>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label={t('workspace.switchSpace', { defaultValue: 'Switch space' })}
            className="inline-flex size-8 shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] data-[state=open]:bg-[var(--color-bg)] data-[state=open]:text-[var(--color-fg)]"
          >
            <ArrowLeftRight size={15} aria-hidden />
          </button>
        </DropdownMenuTrigger>
      </Tooltip>
      <DropdownMenuContent align="end" side="top" className="min-w-[220px]">
        <DropdownMenuLabel>{t('workspace.switchSpace', { defaultValue: 'Switch space' })}</DropdownMenuLabel>
        <DropdownMenuItem onClick={() => void switchTo(null)}>
          {activeId === null ? <Check size={13} aria-hidden /> : <Home size={13} aria-hidden className="text-[var(--color-fg-subtle)]" />}
          {t('workspace.personal', { defaultValue: 'Personal space' })}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        {workspaces.map((w) => (
          <DropdownMenuItem key={w.id} onClick={() => void switchTo(w.id)}>
            {activeId === w.id ? <Check size={13} aria-hidden /> : <Briefcase size={13} aria-hidden className="text-[var(--color-fg-subtle)]" />}
            <span className="truncate">{w.name}</span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** Dropdown-menu section rendered inside the sidebar UserMenu. */
export function WorkspaceMenuItems({
  onManage,
  onCreate,
}: {
  onManage: () => void
  onCreate: () => void
}) {
  const { t } = useTranslation('chat')
  const workspaces = useWorkspaces((s) => s.workspaces)
  const activeId = useWorkspaces((s) => s.activeId)
  const switchTo = useWorkspaces((s) => s.switchTo)
  const mayCreate = canCreateWorkspaces()

  // Spec: users with no workspaces (and no way to make one) see nothing here.
  if (workspaces.length === 0 && !mayCreate) return null

  return (
    <>
      <DropdownMenuSeparator />
      {workspaces.length > 0 ? (
        <DropdownMenuSub>
          <DropdownMenuSubTrigger>
            <Briefcase size={13} aria-hidden />
            {t('workspace.menu', { defaultValue: 'Workspaces' })}
          </DropdownMenuSubTrigger>
          <DropdownMenuSubContent>
            <DropdownMenuItem onClick={() => void switchTo(null)}>
              {activeId === null ? <Check size={13} aria-hidden /> : <span className="w-[13px]" aria-hidden />}
              {t('workspace.personal', { defaultValue: 'Personal space' })}
            </DropdownMenuItem>
            {workspaces.map((w) => (
              <DropdownMenuItem key={w.id} onClick={() => void switchTo(w.id)}>
                {activeId === w.id ? <Check size={13} aria-hidden /> : <span className="w-[13px]" aria-hidden />}
                <span className="truncate">{w.name}</span>
              </DropdownMenuItem>
            ))}
            <DropdownMenuSeparator />
            {activeId ? (
              <DropdownMenuItem onClick={onManage}>
                <Users size={13} aria-hidden />
                {t('workspace.members', { defaultValue: 'Members' })}
              </DropdownMenuItem>
            ) : null}
            {mayCreate ? (
              <DropdownMenuItem onClick={onCreate}>
                <Plus size={13} aria-hidden />
                {t('workspace.create', { defaultValue: 'Create workspace' })}
              </DropdownMenuItem>
            ) : null}
          </DropdownMenuSubContent>
        </DropdownMenuSub>
      ) : (
        <DropdownMenuItem onClick={onCreate}>
          <Briefcase size={13} aria-hidden />
          {t('workspace.create', { defaultValue: 'Create workspace' })}
        </DropdownMenuItem>
      )}
    </>
  )
}

/** Create-workspace dialog. */
export function CreateWorkspaceDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const { t } = useTranslation('chat')
  const createWs = useWorkspaces((s) => s.create)
  const switchTo = useWorkspaces((s) => s.switchTo)
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)

  async function submit() {
    const n = name.trim()
    if (!n || busyRef.current) return
    busyRef.current = true
    setBusy(true)
    try {
      const ws = await createWs(n)
      onOpenChange(false)
      setName('')
      await switchTo(ws.id)
      toast.success(t('workspace.created', { defaultValue: 'Workspace created' }))
    } catch (e) {
      const msg = e instanceof Error ? e.message : ''
      toast.error(
        msg.includes('workspace_limit')
          ? t('workspace.limitReached', { defaultValue: 'Workspace limit reached for your plan.' })
          : msg.includes('workspace_disabled')
            ? t('workspace.disabled', { defaultValue: 'Your plan cannot create workspaces.' })
            : t('workspace.createFailed', { defaultValue: 'Could not create the workspace.' }),
      )
    } finally {
      busyRef.current = false
      setBusy(false)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next && busyRef.current) return
        onOpenChange(next)
      }}
    >
      <DialogContent size="sm" closeDisabled={busy}>
        <DialogHeader>
          <DialogTitle>{t('workspace.create', { defaultValue: 'Create workspace' })}</DialogTitle>
          <DialogDescription>
            {t('workspace.createBody', {
              defaultValue: 'A separate, shared space — its conversations, projects and knowledge bases are visible to every member.',
            })}
          </DialogDescription>
        </DialogHeader>
        <DialogBody>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && void submit()}
            placeholder={t('workspace.namePlaceholder', { defaultValue: 'Workspace name' })}
            aria-label={t('workspace.namePlaceholder', { defaultValue: 'Workspace name' })}
            autoFocus
          />
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" disabled={busy} onClick={() => onOpenChange(false)}>
            {t('common.cancel', { ns: 'common', defaultValue: 'Cancel' })}
          </Button>
          <Button onClick={() => void submit()} disabled={!name.trim() || busy}>
            {t('workspace.create', { defaultValue: 'Create workspace' })}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** Members + invite management for the ACTIVE workspace. */
export function WorkspaceMembersDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const { t } = useTranslation('chat')
  const activeId = useWorkspaces((s) => s.activeId)
  const ws = useWorkspaces((s) => (s.activeId ? s.workspaces.find((w) => w.id === s.activeId) : undefined))
  const removeWs = useWorkspaces((s) => s.remove)
  const leaveWs = useWorkspaces((s) => s.leave)
  const [members, setMembers] = useState<ApiWorkspaceMember[]>([])
  // Distinguish "still fetching" from "loaded, empty": without it the dialog
  // opens claiming "0 members" and the list pops in a beat later.
  const [membersLoading, setMembersLoading] = useState(true)
  const [membersLoadFailed, setMembersLoadFailed] = useState(false)
  const [membersLoadAttempt, setMembersLoadAttempt] = useState(0)
  const membersRequestRef = useRef(0)
  const [inviteToken, setInviteToken] = useState(ws?.invite_token ?? '')
  const { copied, copy } = useCopy()
  const isOwner = ws?.role === 'owner'
  // In-flight guards so slow-backend mutations can't be double-fired.
  const [busyUid, setBusyUid] = useState<string | null>(null)
  const busyUidRef = useRef<{ uid: string; epoch: number } | null>(null)
  const [rotating, setRotating] = useState(false)
  const rotatingRef = useRef<{ workspaceID: string; epoch: number } | null>(null)
  const [actioning, setActioning] = useState(false)
  const actioningRef = useRef<{ workspaceID: string; epoch: number } | null>(null)
  const [editingMember, setEditingMember] = useState<ApiWorkspaceMember | null>(null)
  const [permissionDraft, setPermissionDraft] = useState<ApiWorkspaceMemberPermissions | null>(null)
  const [savingPermissions, setSavingPermissions] = useState(false)
  const savingPermissionsRef = useRef<number | null>(null)
  const operationEpochRef = useRef(0)

  useEffect(() => {
    const request = ++membersRequestRef.current
    operationEpochRef.current += 1
    busyUidRef.current = null
    rotatingRef.current = null
    actioningRef.current = null
    savingPermissionsRef.current = null
    setBusyUid(null)
    setRotating(false)
    setActioning(false)
    setSavingPermissions(false)
    setEditingMember(null)
    setPermissionDraft(null)
    if (!open || !activeId) return
    setInviteToken(ws?.invite_token ?? '')
    setMembersLoading(true)
    setMembersLoadFailed(false)
    workspacesApi
      .members(activeId)
      .then((r) => {
        if (request === membersRequestRef.current) setMembers(r.members)
      })
      .catch(() => {
        if (request !== membersRequestRef.current) return
        setMembers([])
        setMembersLoadFailed(true)
      })
      .finally(() => {
        if (request === membersRequestRef.current) setMembersLoading(false)
      })
  }, [open, activeId, membersLoadAttempt, ws?.invite_token])

  useEffect(
    () =>
      subscribeAccessInvalidation((event) => {
        if (!open || (event.kind !== 'account' && event.kind !== 'workspace')) return
        setMembersLoadAttempt((attempt) => attempt + 1)
      }),
    [open],
  )

  if (!ws || !activeId) return null
  const inviteURL = inviteToken ? `${window.location.origin}/workspace/join/${inviteToken}` : ''

  async function kick(uid: string) {
    if (busyUidRef.current) return
    const epoch = operationEpochRef.current
    busyUidRef.current = { uid, epoch }
    setBusyUid(uid)
    try {
      await workspacesApi.kick(activeId!, uid)
      if (epoch === operationEpochRef.current) {
        setMembers((m) => m.filter((x) => x.user_id !== uid))
      }
    } catch {
      if (epoch === operationEpochRef.current) {
        toast.error(t('workspace.kickFailed', { defaultValue: 'Could not remove the member.' }))
      }
    } finally {
      if (busyUidRef.current?.uid === uid && busyUidRef.current.epoch === epoch) {
        busyUidRef.current = null
        if (epoch === operationEpochRef.current) setBusyUid(null)
      }
    }
  }

  function editPermissions(member: ApiWorkspaceMember) {
    setEditingMember(member)
    setPermissionDraft({
      can_create_projects: member.can_create_projects,
      can_private_conversations: member.can_private_conversations,
      can_create_kb: member.can_create_kb,
      can_add_kb_files: member.can_add_kb_files,
      can_delete_kb_content: member.can_delete_kb_content,
    })
  }

  async function savePermissions() {
    if (!editingMember || !permissionDraft || savingPermissionsRef.current !== null) return
    const epoch = operationEpochRef.current
    const memberID = editingMember.user_id
    const draft = { ...permissionDraft }
    savingPermissionsRef.current = epoch
    setSavingPermissions(true)
    try {
      const updated = await workspacesApi.updateMemberPermissions(activeId!, memberID, draft)
      if (epoch !== operationEpochRef.current) return
      setMembers((current) => current.map((member) => member.user_id === updated.user_id ? updated : member))
      setEditingMember(null)
      setPermissionDraft(null)
      toast.success(t('workspace.permissionsSaved', { defaultValue: 'Member permissions updated.' }))
    } catch {
      if (epoch === operationEpochRef.current) {
        toast.error(t('workspace.permissionsSaveFailed', { defaultValue: 'Could not update member permissions.' }))
      }
    } finally {
      if (savingPermissionsRef.current === epoch) {
        savingPermissionsRef.current = null
        if (epoch === operationEpochRef.current) setSavingPermissions(false)
      }
    }
  }

  async function rotate() {
    if (rotatingRef.current) return
    const workspaceID = activeId!
    const epoch = operationEpochRef.current
    const operation = { workspaceID, epoch }
    rotatingRef.current = operation
    setRotating(true)
    try {
      const { invite_token } = await workspacesApi.rotateInvite(workspaceID)
      if (rotatingRef.current !== operation || epoch !== operationEpochRef.current) return
      setInviteToken(invite_token)
      toast.success(t('workspace.inviteRotated', { defaultValue: 'New invite link generated — the old one is dead.' }))
    } catch {
      if (rotatingRef.current === operation && epoch === operationEpochRef.current) {
        toast.error(t('workspace.inviteRotateFailed', { defaultValue: 'Could not rotate the invite link.' }))
      }
    } finally {
      if (rotatingRef.current === operation) {
        rotatingRef.current = null
        if (epoch === operationEpochRef.current) setRotating(false)
      }
    }
  }

  // Delete (owner) / Leave (non-owner) share one in-flight flag — only one is
  // ever rendered — so the destructive footer button can't be double-fired.
  async function runFooterAction(fn: (id: string) => Promise<void>, failureMessage: string) {
    if (actioningRef.current) return
    const workspaceID = activeId!
    const epoch = operationEpochRef.current
    const operation = { workspaceID, epoch }
    actioningRef.current = operation
    setActioning(true)
    try {
      await fn(workspaceID)
      if (actioningRef.current === operation && epoch === operationEpochRef.current) onOpenChange(false)
    } catch {
      if (actioningRef.current === operation && epoch === operationEpochRef.current) toast.error(failureMessage)
    } finally {
      if (actioningRef.current === operation) {
        actioningRef.current = null
        if (epoch === operationEpochRef.current) setActioning(false)
      }
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next && (actioningRef.current || savingPermissionsRef.current !== null || busyUidRef.current)) return
        onOpenChange(next)
      }}
    >
      <DialogContent
        size="md"
        closeDisabled={actioning || savingPermissions || busyUid !== null}
        className="max-sm:h-[calc(100dvh-1rem)] max-sm:max-h-[calc(100dvh-1rem)]"
      >
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Briefcase size={15} aria-hidden />
            {ws.name}
          </DialogTitle>
          <DialogDescription>
            {membersLoading
              ? t('workspace.membersLoading', { defaultValue: 'Loading members…' })
              : membersLoadFailed
                ? t('workspace.membersLoadFailed', { defaultValue: 'Could not load workspace members.' })
                : t('workspace.membersBody', { count: members.length, defaultValue: '{{count}} members' })}
          </DialogDescription>
        </DialogHeader>

        <DialogBody className="space-y-4">
        {/* Invite link — owner only */}
        {isOwner && inviteURL ? (
          <div className="rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-2.5">
            <div className="text-[11px] font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
              {t('workspace.inviteLink', { defaultValue: 'Invite link' })}
            </div>
            <div className="mt-1.5 flex items-center gap-2">
              <code className="min-w-0 flex-1 truncate text-[11.5px] text-[var(--color-fg-muted)]">{inviteURL}</code>
              <Button size="sm" variant="secondary" onClick={() => copy(inviteURL)}>
                <Copy size={12} aria-hidden />
                {copied ? t('actions.copied', { defaultValue: 'Copied' }) : t('actions.copy', { defaultValue: 'Copy' })}
              </Button>
            </div>
            <button
              type="button"
              onClick={() => void rotate()}
              disabled={rotating}
              className="mt-1.5 text-[11px] text-[var(--color-fg-subtle)] underline-offset-2 hover:underline interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] rounded-[4px] disabled:opacity-50 disabled:pointer-events-none"
            >
              {t('workspace.rotateInvite', { defaultValue: 'Reset link (invalidates the old one)' })}
            </button>
          </div>
        ) : null}

        {/* Member list — skeleton rows while the fetch is in flight so the
            dialog never opens claiming "0 members" before data lands. */}
        <ul className="max-h-64 space-y-1 overflow-y-auto scrollbar-thin">
          {membersLoading
            ? [0, 1, 2].map((i) => (
                <li key={`sk-${i}`} className="flex items-center gap-2.5 rounded-[8px] px-1.5 py-1.5" aria-hidden>
                  <Skeleton className="size-6 rounded-full" />
                  <div className="min-w-0 flex-1 space-y-1.5">
                    <Skeleton className="h-3 w-32" />
                    <Skeleton className="h-2.5 w-20" />
                  </div>
                </li>
              ))
            : null}
          {!membersLoading && membersLoadFailed ? (
            <li role="alert" className="flex flex-col items-center px-4 py-8 text-center">
              <AlertTriangle size={18} aria-hidden className="text-[var(--color-danger)]" />
              <p className="mt-2 text-[13px] text-[var(--color-fg-muted)]">
                {t('workspace.membersLoadFailed', { defaultValue: 'Could not load workspace members.' })}
              </p>
              <Button
                size="sm"
                variant="secondary"
                className="mt-3"
                leadingIcon={<RefreshCw size={13} aria-hidden />}
                onClick={() => setMembersLoadAttempt((attempt) => attempt + 1)}
              >
                {t('actions.tryAgain', { ns: 'common', defaultValue: 'Try again' })}
              </Button>
            </li>
          ) : null}
          {!membersLoading && !membersLoadFailed && members.map((m) => (
            <li key={m.user_id} className="flex items-center gap-2.5 rounded-[8px] px-1.5 py-1.5">
              <Avatar size="sm">
                {m.avatar_url ? <AvatarImage src={m.avatar_url} alt={m.name} /> : null}
                <AvatarFallback>{initials(m.name || m.email)}</AvatarFallback>
              </Avatar>
              <div className="min-w-0 flex-1">
                <div className="truncate text-[13px] font-medium text-[var(--color-fg)]">{m.name || m.email}</div>
                <div className="truncate text-[11px] text-[var(--color-fg-subtle)]">
                  {m.role === 'owner'
                    ? t('workspace.roleOwner', { defaultValue: 'Owner' })
                    : t('workspace.roleMember', { defaultValue: 'Member' })}
                </div>
              </div>
              {isOwner && m.role !== 'owner' ? (
                <div className="flex shrink-0 items-center gap-0.5">
                  <Tooltip content={t('workspace.managePermissions', { defaultValue: 'Manage permissions' })}>
                    <button
                      type="button"
                      onClick={() => editPermissions(m)}
                      disabled={busyUid !== null}
                      aria-label={t('workspace.managePermissions', { defaultValue: 'Manage permissions' })}
                      className="inline-flex size-7 items-center justify-center rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:pointer-events-none disabled:opacity-50 max-sm:size-10"
                    >
                      <Settings2 size={13} aria-hidden />
                    </button>
                  </Tooltip>
                  <Tooltip content={t('workspace.kick', { defaultValue: 'Remove member' })}>
                    <button
                      type="button"
                      onClick={() => void kick(m.user_id)}
                      disabled={busyUid !== null}
                      aria-label={t('workspace.kick', { defaultValue: 'Remove member' })}
                      className="inline-flex size-7 items-center justify-center rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:pointer-events-none disabled:opacity-50 max-sm:size-10"
                    >
                      <UserX size={13} aria-hidden />
                    </button>
                  </Tooltip>
                </div>
              ) : null}
            </li>
          ))}
        </ul>
        </DialogBody>

        <DialogFooter className="justify-between">
          {isOwner ? (
            <Button
              variant="destructive"
              loading={actioning}
              onClick={() => void runFooterAction(
                removeWs,
                t('workspace.deleteFailed', { defaultValue: 'Could not delete the workspace.' }),
              )}
            >
              <Trash2 size={13} aria-hidden />
              {t('workspace.delete', { defaultValue: 'Delete workspace' })}
            </Button>
          ) : (
            <Button
              variant="destructive"
              loading={actioning}
              onClick={() => void runFooterAction(
                leaveWs,
                t('workspace.leaveFailed', { defaultValue: 'Could not leave the workspace.' }),
              )}
            >
              <LogOut size={13} aria-hidden />
              {t('workspace.leave', { defaultValue: 'Leave workspace' })}
            </Button>
          )}
          <Button variant="ghost" disabled={actioning || busyUid !== null} onClick={() => onOpenChange(false)}>
            {t('common.close', { ns: 'common', defaultValue: 'Close' })}
          </Button>
        </DialogFooter>
      </DialogContent>

      <Dialog
        open={editingMember !== null}
        onOpenChange={(next) => {
          if (!next && !savingPermissions) {
            setEditingMember(null)
            setPermissionDraft(null)
          }
        }}
      >
        <DialogContent size="sm" closeDisabled={savingPermissions}>
          <DialogHeader>
            <DialogTitle>{t('workspace.memberPermissions', { defaultValue: 'Member permissions' })}</DialogTitle>
            <DialogDescription>
              {t('workspace.memberPermissionsBody', {
                name: editingMember?.name || editingMember?.email || '',
                defaultValue: 'Set what {{name}} can create and manage across this workspace.',
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogBody className="divide-y divide-[var(--color-divider)] py-0">
            {permissionDraft ? WORKSPACE_PERMISSION_ROWS.map((row) => (
              <label key={row.key} className="flex min-h-14 cursor-pointer items-center gap-4 py-3">
                <span className="min-w-0 flex-1">
                  <span className="block text-[13px] font-medium text-[var(--color-fg)]">
                    {t(`workspace.permissions.${row.key}.label`, { defaultValue: row.label })}
                  </span>
                  <span className="mt-0.5 block text-[11.5px] leading-4 text-[var(--color-fg-subtle)]">
                    {t(`workspace.permissions.${row.key}.description`, { defaultValue: row.description })}
                  </span>
                </span>
                <Switch
                  checked={permissionDraft[row.key]}
                  disabled={savingPermissions}
                  onCheckedChange={(checked) => setPermissionDraft((current) => current ? { ...current, [row.key]: checked } : current)}
                  aria-label={t(`workspace.permissions.${row.key}.label`, { defaultValue: row.label })}
                />
              </label>
            )) : null}
          </DialogBody>
          <DialogFooter>
            <Button
              variant="ghost"
              disabled={savingPermissions}
              onClick={() => {
                setEditingMember(null)
                setPermissionDraft(null)
              }}
            >
              {t('common.cancel', { ns: 'common', defaultValue: 'Cancel' })}
            </Button>
            <Button loading={savingPermissions} onClick={() => void savePermissions()}>
              {t('common.save', { ns: 'common', defaultValue: 'Save' })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Dialog>
  )
}

const WORKSPACE_PERMISSION_ROWS: Array<{
  key: keyof ApiWorkspaceMemberPermissions
  label: string
  description: string
}> = [
  { key: 'can_create_projects', label: 'Create projects', description: 'Create new projects and their project libraries.' },
  { key: 'can_private_conversations', label: 'Private conversations', description: 'Create conversations visible only to themselves.' },
  { key: 'can_create_kb', label: 'Create knowledge bases', description: 'Create new workspace knowledge bases.' },
  { key: 'can_add_kb_files', label: 'Add knowledge-base files', description: 'Upload and paste content into workspace knowledge bases.' },
  { key: 'can_delete_kb_content', label: 'Delete knowledge-base content', description: 'Delete or retry content when the specific library also allows it.' },
]
