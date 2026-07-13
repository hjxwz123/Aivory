/**
 * Workspace UI (§workspaces) — the avatar-menu section for switching/creating
 * workspaces plus the members/invite management dialog. Users with no
 * workspaces AND no create-capability see nothing (per spec: 左下角不显示).
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowLeftRight, Briefcase, Check, Copy, Home, LogOut, Plus, Trash2, UserX, Users } from 'lucide-react'
import { workspacesApi } from '@/api'
import type { ApiWorkspaceMember } from '@/api/types'
import { useAuth } from '@/store/auth'
import { useWorkspaces } from '@/store/workspaces'
import { toast } from '@/hooks/use-toast'
import { useCopy } from '@/hooks/use-clipboard'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { initials } from '@/components/ui/avatar.utils'
import { Button } from '@/components/ui/button'
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
export function canCreateWorkspaces(): boolean {
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

  async function submit() {
    const n = name.trim()
    if (!n || busy) return
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
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="sm">
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
            autoFocus
          />
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
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
  const [inviteToken, setInviteToken] = useState(ws?.invite_token ?? '')
  const { copied, copy } = useCopy()
  const isOwner = ws?.role === 'owner'
  // In-flight guards so slow-backend mutations can't be double-fired.
  const [busyUid, setBusyUid] = useState<string | null>(null)
  const [rotating, setRotating] = useState(false)
  const rotatingRef = useRef(false)
  const [actioning, setActioning] = useState(false)
  const actioningRef = useRef(false)

  useEffect(() => {
    if (!open || !activeId) return
    setInviteToken(ws?.invite_token ?? '')
    workspacesApi
      .members(activeId)
      .then((r) => setMembers(r.members))
      .catch(() => setMembers([]))
  }, [open, activeId, ws?.invite_token])

  if (!ws || !activeId) return null
  const inviteURL = inviteToken ? `${window.location.origin}/workspace/join/${inviteToken}` : ''

  async function kick(uid: string) {
    if (busyUid) return
    setBusyUid(uid)
    try {
      await workspacesApi.kick(activeId!, uid)
      setMembers((m) => m.filter((x) => x.user_id !== uid))
    } catch {
      toast.error(t('workspace.kickFailed', { defaultValue: 'Could not remove the member.' }))
    } finally {
      setBusyUid(null)
    }
  }

  async function rotate() {
    if (rotatingRef.current) return
    rotatingRef.current = true
    setRotating(true)
    try {
      const { invite_token } = await workspacesApi.rotateInvite(activeId!)
      setInviteToken(invite_token)
      toast.success(t('workspace.inviteRotated', { defaultValue: 'New invite link generated — the old one is dead.' }))
    } catch {
      toast.error(t('workspace.inviteRotateFailed', { defaultValue: 'Could not rotate the invite link.' }))
    } finally {
      rotatingRef.current = false
      setRotating(false)
    }
  }

  // Delete (owner) / Leave (non-owner) share one in-flight flag — only one is
  // ever rendered — so the destructive footer button can't be double-fired.
  async function runFooterAction(fn: (id: string) => Promise<void>) {
    if (actioningRef.current) return
    actioningRef.current = true
    setActioning(true)
    try {
      await fn(activeId!)
      onOpenChange(false)
    } finally {
      actioningRef.current = false
      setActioning(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Briefcase size={15} aria-hidden />
            {ws.name}
          </DialogTitle>
          <DialogDescription>
            {t('workspace.membersBody', { count: members.length, defaultValue: '{{count}} members' })}
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

        {/* Member list */}
        <ul className="max-h-64 space-y-1 overflow-y-auto scrollbar-thin">
          {members.map((m) => (
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
                <button
                  type="button"
                  onClick={() => void kick(m.user_id)}
                  disabled={busyUid === m.user_id}
                  aria-label={t('workspace.kick', { defaultValue: 'Remove member' })}
                  className="inline-flex size-7 items-center justify-center rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:opacity-50 disabled:pointer-events-none"
                >
                  <UserX size={13} aria-hidden />
                </button>
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
              onClick={() => void runFooterAction(removeWs)}
            >
              <Trash2 size={13} aria-hidden />
              {t('workspace.delete', { defaultValue: 'Delete workspace' })}
            </Button>
          ) : (
            <Button
              variant="destructive"
              loading={actioning}
              onClick={() => void runFooterAction(leaveWs)}
            >
              <LogOut size={13} aria-hidden />
              {t('workspace.leave', { defaultValue: 'Leave workspace' })}
            </Button>
          )}
          <Button variant="ghost" disabled={actioning} onClick={() => onOpenChange(false)}>
            {t('common.close', { ns: 'common', defaultValue: 'Close' })}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}