/**
 * Workspace UI (§workspaces) — the avatar-menu section for switching/creating
 * workspaces plus the members/invite management dialog. Users with no
 * workspaces AND no create-capability see nothing (per spec: 左下角不显示).
 */
import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, ArrowLeftRight, BarChart3, Briefcase, Check, Copy, FileClock, Home, LockKeyhole, KeyRound, LogOut, Megaphone, Plus, RefreshCw, Settings2, ShieldCheck, SlidersHorizontal, Trash2, UserPlus, UserX, Users } from 'lucide-react'
import { workspacesApi } from '@/api'
import type { ApiAnnouncement } from '@/api/endpoints'
import type {
  ApiModel,
  ApiWorkspaceAuditLog,
  ApiWorkspaceInvite,
  ApiWorkspaceMember,
  ApiWorkspaceMemberPermissions,
  ApiWorkspacePolicy,
  ApiWorkspaceRole,
  ApiWorkspaceUsageAnalytics,
} from '@/api/types'
import { useAuth } from '@/store/auth'
import { useWorkspaces } from '@/store/workspaces'
import { toast } from '@/hooks/use-toast'
import { useCopy } from '@/hooks/use-clipboard'
import { useMediaQuery } from '@/hooks/use-media-query'
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { subscribeAccessInvalidation } from '@/lib/access-events'
import { userCan } from '@/lib/user-permissions'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { WorkspaceIcon } from '@/components/workspace/workspace-icon'
import { AnnouncementEditor } from '@/components/announcement/announcement-editor'
import {
  canEditWorkspaceMemberPermissions,
  memberCanCreate,
  memberCanUse,
  workspaceCapabilities,
} from '@/lib/workspace-permissions'
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

const WorkspaceProfileEditor = lazy(() => import('@/components/workspace/workspace-profile-editor'))

const WORKSPACE_MANAGEMENT_TABS = [
  { key: 'profile', icon: Settings2, label: 'workspace.tabProfile' },
  { key: 'members', icon: Users, label: 'workspace.tabMembers' },
  { key: 'invites', icon: KeyRound, label: 'workspace.tabInvites' },
  { key: 'policy', icon: SlidersHorizontal, label: 'workspace.tabPolicy' },
  { key: 'announcement', icon: Megaphone, label: 'workspace.tabAnnouncement' },
  { key: 'audit', icon: FileClock, label: 'workspace.tabAudit' },
  { key: 'statistics', icon: BarChart3, label: 'workspace.tabStatistics' },
] as const

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
  const locked = useWorkspaces((s) => !!s.lockedWorkspaceId)

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
        <DropdownMenuLabel>{locked ? t('workspace.domainLocked') : t('workspace.switchSpace', { defaultValue: 'Switch space' })}</DropdownMenuLabel>
        <DropdownMenuItem disabled={locked} onClick={() => void switchTo(null)}>
          {activeId === null ? <Check size={13} aria-hidden /> : <Home size={13} aria-hidden className="text-[var(--color-fg-subtle)]" />}
          {t('workspace.personal', { defaultValue: 'Personal space' })}
          {locked && <LockKeyhole size={13} aria-label={t('workspace.domainLocked')} />}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        {workspaces.map((w) => (
          <DropdownMenuItem key={w.id} disabled={locked && w.id !== activeId} onClick={() => void switchTo(w.id)}>
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
  const locked = useWorkspaces((s) => !!s.lockedWorkspaceId)
  const mayCreate = !locked && canCreateWorkspaces()

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
            <DropdownMenuItem disabled={locked} onClick={() => void switchTo(null)}>
              {activeId === null ? <Check size={13} aria-hidden /> : <span className="w-[13px]" aria-hidden />}
              {t('workspace.personal', { defaultValue: 'Personal space' })}
              {locked && <LockKeyhole size={13} aria-label={t('workspace.domainLocked')} />}
            </DropdownMenuItem>
            {workspaces.map((w) => (
              <DropdownMenuItem key={w.id} disabled={locked && w.id !== activeId} onClick={() => void switchTo(w.id)}>
                {activeId === w.id ? <Check size={13} aria-hidden /> : <span className="w-[13px]" aria-hidden />}
                <span className="truncate">{w.name}</span>
              </DropdownMenuItem>
            ))}
            {activeId || mayCreate ? <DropdownMenuSeparator /> : null}
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
  const locked = useWorkspaces((s) => !!s.lockedWorkspaceId)
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)

  async function submit() {
    const n = name.trim()
    if (!n || busyRef.current || locked) return
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
          <Button onClick={() => void submit()} disabled={!name.trim() || busy || locked}>
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
  const domainLocked = useWorkspaces((s) => !!s.lockedWorkspaceId)
  const [members, setMembers] = useState<ApiWorkspaceMember[]>([])
  // Distinguish "still fetching" from "loaded, empty": without it the dialog
  // opens claiming "0 members" and the list pops in a beat later.
  const [membersLoading, setMembersLoading] = useState(true)
  const [membersLoadFailed, setMembersLoadFailed] = useState(false)
  const [membersLoadAttempt, setMembersLoadAttempt] = useState(0)
  const membersRequestRef = useRef(0)
  // The former workspace-level token is intentionally no longer accepted.
  // A quick link appears only after the manager explicitly generates one.
  const [inviteToken, setInviteToken] = useState('')
  const { copied, copy } = useCopy()
  // §workspace RBAC: role authority comes from is_owner + role, never from a
  // third "owner" role value (the backend always reports the owner as admin).
  const isOwner = ws?.is_owner ?? false
  const user = useAuth((state) => state.user)
  const isAdmin = ws?.role === 'admin'
  const canManage = isOwner || isAdmin
  // In-flight guards so slow-backend mutations can't be double-fired.
  const [busyUid, setBusyUid] = useState<string | null>(null)
  const busyUidRef = useRef<{ uid: string; epoch: number } | null>(null)
  const [rotating, setRotating] = useState(false)
  const rotatingRef = useRef<{ workspaceID: string; epoch: number } | null>(null)
  const [actioning, setActioning] = useState(false)
  const actioningRef = useRef<{ workspaceID: string; epoch: number } | null>(null)
  const [editingMember, setEditingMember] = useState<ApiWorkspaceMember | null>(null)
  const [permissionDraft, setPermissionDraft] = useState<ApiWorkspaceMemberPermissions | null>(null)
  const [permissionDraftDirty, setPermissionDraftDirty] = useState(false)
  const permissionDraftDirtyRef = useRef(false)
  const [permissionConflict, setPermissionConflict] = useState(false)
  const [savingPermissions, setSavingPermissions] = useState(false)
  const savingPermissionsRef = useRef<number | null>(null)
  const [roleBusyUid, setRoleBusyUid] = useState<string | null>(null)
  const roleBusyRef = useRef<{ uid: string; role: ApiWorkspaceRole; epoch: number } | null>(null)
  const operationEpochRef = useRef(0)
  // §workspace RBAC phases 3/4: managers switch between members, invites and
  // the capability policy; management is restricted to workspace admins.
  const [tab, setTab] = useState<'profile' | 'members' | 'invites' | 'policy' | 'announcement' | 'audit' | 'statistics'>('members')
  const desktopNavigation = useMediaQuery('(min-width: 640px)')
  const paneRef = useRef<HTMLDivElement>(null)
  const [transferOpen, setTransferOpen] = useState(false)

  useEffect(() => {
    paneRef.current?.scrollTo({ top: 0 })
  }, [tab, activeId, open, canManage])

  function roleLabel(role: ApiWorkspaceRole | undefined): string {
    switch (role) {
      case 'admin':
        return t('workspace.roleAdmin', { defaultValue: 'Admin' })
      case 'guest':
        return t('workspace.roleGuest', { defaultValue: 'Guest' })
      default:
        return t('workspace.roleMember', { defaultValue: 'Member' })
    }
  }

  useEffect(() => {
    const request = ++membersRequestRef.current
    operationEpochRef.current += 1
    busyUidRef.current = null
    roleBusyRef.current = null
    rotatingRef.current = null
    actioningRef.current = null
    savingPermissionsRef.current = null
    setBusyUid(null)
    setRoleBusyUid(null)
    setRotating(false)
    setActioning(false)
    setSavingPermissions(false)
    setEditingMember(null)
    setPermissionDraft(null)
    permissionDraftDirtyRef.current = false
    setPermissionDraftDirty(false)
    setPermissionConflict(false)
    if (!open || !activeId || !canManage) { setMembers([]); return }
    setInviteToken('')
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
  }, [open, activeId, membersLoadAttempt, canManage])

  // Each opening starts at the member overview. Keeping a previous workspace's
  // policy/audit tab selected is disorienting after switching spaces, and can
  // briefly leave a newly demoted manager looking at an empty panel.
  useEffect(() => {
    if (open) setTab('members')
  }, [activeId, open])

  useEffect(
    () =>
      subscribeAccessInvalidation((event) => {
        if (!open || (event.kind !== 'account' && event.kind !== 'workspace')) return
        // Preserve a locally edited draft when another administrator changes
        // access state. Saving stays blocked until the manager explicitly
        // reloads the authoritative member row.
        if (permissionDraftDirtyRef.current || savingPermissionsRef.current !== null) {
          setPermissionConflict(true)
          return
        }
        // A clean editor still contains a snapshot of the old member row. Close
        // it before reloading so the next edit starts from the newly committed
        // permissions instead of overwriting another administrator's changes.
        permissionDraftDirtyRef.current = false
        setPermissionDraftDirty(false)
        setPermissionConflict(false)
        setEditingMember(null)
        setPermissionDraft(null)
        setMembersLoadAttempt((attempt) => attempt + 1)
      }),
    [open],
  )

  if (!ws || !activeId || !canManage) return null
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

  // §workspace RBAC role ladder: the owner may set admin/member/guest on
  // others; ordinary admins only flip member<->guest. The backend re-checks.
  async function changeRole(uid: string, role: ApiWorkspaceRole) {
    if (roleBusyRef.current) return
    const epoch = operationEpochRef.current
    roleBusyRef.current = { uid, role, epoch }
    setRoleBusyUid(uid)
    try {
      const updated = await workspacesApi.updateMemberRole(activeId!, uid, role)
      if (epoch === operationEpochRef.current) {
        setMembers((current) => current.map((m) => (m.user_id === updated.user_id ? updated : m)))
        toast.success(t('workspace.roleUpdated', { defaultValue: 'Role updated.' }))
      }
    } catch {
      if (epoch === operationEpochRef.current) {
        toast.error(t('workspace.roleUpdateFailed', { defaultValue: 'Could not update the role.' }))
      }
    } finally {
      if (roleBusyRef.current?.uid === uid && roleBusyRef.current.epoch === epoch) {
        roleBusyRef.current = null
        if (epoch === operationEpochRef.current) setRoleBusyUid(null)
      }
    }
  }

  function editPermissions(member: ApiWorkspaceMember) {
    // Admins (including the canonical owner) always have the full workspace
    // capability ceiling. The API intentionally rejects member-permission
    // writes for them, so a stale row must not be able to open a dead editor.
    if (!canEditWorkspaceMemberPermissions(member)) return
    setEditingMember(member)
    permissionDraftDirtyRef.current = false
    setPermissionDraftDirty(false)
    setPermissionConflict(false)
    setPermissionDraft({
      can_create_projects: member.can_create_projects,
      can_private_conversations: member.can_private_conversations,
      can_create_skills_prompts: member.can_create_skills_prompts,
      can_create_prompts: memberCanCreate(member, 'prompt'),
      can_create_skills: memberCanCreate(member, 'skill'),
      can_create_mcp: memberCanCreate(member, 'mcp'),
      can_use_prompts: memberCanUse(member, 'prompt'),
      can_use_skills: memberCanUse(member, 'skill'),
      can_use_mcp: memberCanUse(member, 'mcp'),
      can_create_kb: member.can_create_kb,
      can_add_kb_files: member.can_add_kb_files,
      can_delete_kb_content: member.can_delete_kb_content,
      can_delete_conversations: member.can_delete_conversations,
      can_use_ai_ppt: member.can_use_ai_ppt ?? true,
    })
  }

  function closePermissionEditor() {
    permissionDraftDirtyRef.current = false
    setPermissionDraftDirty(false)
    setPermissionConflict(false)
    setEditingMember(null)
    setPermissionDraft(null)
  }

  async function savePermissions() {
    if (!editingMember || !permissionDraft || permissionConflict || savingPermissionsRef.current !== null) return
    const epoch = operationEpochRef.current
    const memberID = editingMember.user_id
    // Keep the legacy combined bit as a conservative compatibility mirror for
    // older API nodes. It must never broaden a granular permission: an older
    // reader should only see the aggregate bit when every independent resource
    // creation capability is enabled.
    const draft = {
      ...permissionDraft,
      can_create_skills_prompts:
        Boolean(permissionDraft.can_create_prompts) &&
        Boolean(permissionDraft.can_create_skills) &&
        Boolean(permissionDraft.can_create_mcp),
    }
    savingPermissionsRef.current = epoch
    setSavingPermissions(true)
    try {
      const updated = await workspacesApi.updateMemberPermissions(activeId!, memberID, draft)
      if (epoch !== operationEpochRef.current) return
      setMembers((current) => current.map((member) => member.user_id === updated.user_id ? updated : member))
      permissionDraftDirtyRef.current = false
      setPermissionDraftDirty(false)
      setPermissionConflict(false)
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
        size="full"
        closeDisabled={actioning || savingPermissions || busyUid !== null}
        className="h-[min(44rem,calc(100dvh-3rem))] max-w-[68rem] overflow-hidden sm:w-[min(94vw,68rem)] sm:flex-row max-sm:h-dvh max-sm:max-h-dvh max-sm:w-full max-sm:rounded-none max-sm:border-0"
      >
        <div className="flex min-h-0 shrink-0 flex-col bg-[var(--color-bg-muted)]/50 sm:w-48">
        <DialogHeader className="px-4 pb-3 pt-5 max-sm:pr-12">
          <DialogTitle className="flex min-w-0 items-center gap-2">
            <WorkspaceIcon icon={ws.icon_url} size={15} />
            <span className="truncate" title={ws.name}>{ws.name}</span>
          </DialogTitle>
          <DialogDescription>
            {membersLoading
              ? t('workspace.membersLoading', { defaultValue: 'Loading members…' })
              : membersLoadFailed
                ? t('workspace.membersLoadFailed', { defaultValue: 'Could not load workspace members.' })
                : t('workspace.membersBody', { count: members.length, defaultValue: '{{count}} members' })}
          </DialogDescription>
        </DialogHeader>

          {canManage ? (
            <div
              role="tablist"
              aria-orientation={desktopNavigation ? 'vertical' : 'horizontal'}
              aria-label={t('workspace.manageTabs', { defaultValue: 'Workspace management' })}
              className="flex min-h-0 min-w-0 gap-1 overflow-x-auto overscroll-contain px-2 pb-3 scrollbar-none sm:flex-1 sm:flex-col sm:overflow-x-hidden sm:overflow-y-auto"
            >
            {WORKSPACE_MANAGEMENT_TABS.map(({ key, icon: Icon, label }) => (
              <button
                key={key}
                type="button"
                role="tab"
                id={`workspace-management-tab-${key}`}
                aria-controls={`workspace-management-panel-${key}`}
                aria-selected={tab === key}
                tabIndex={tab === key ? 0 : -1}
                onClick={() => setTab(key)}
                onKeyDown={(event) => {
                  const previousKey = desktopNavigation ? 'ArrowUp' : 'ArrowLeft'
                  const nextKey = desktopNavigation ? 'ArrowDown' : 'ArrowRight'
                  if (![previousKey, nextKey, 'Home', 'End'].includes(event.key)) return
                  const tabs = Array.from(
                    event.currentTarget.parentElement?.querySelectorAll<HTMLButtonElement>('[role="tab"]') ?? [],
                  )
                  if (tabs.length === 0) return
                  const current = tabs.indexOf(event.currentTarget)
                  const next = event.key === 'Home'
                    ? 0
                    : event.key === 'End'
                      ? tabs.length - 1
                      : (current + (event.key === nextKey ? 1 : -1) + tabs.length) % tabs.length
                  event.preventDefault()
                  tabs[next]?.focus()
                  tabs[next]?.click()
                }}
                className={`inline-flex min-h-11 shrink-0 items-center gap-2 whitespace-nowrap rounded-[8px] px-2.5 py-1.5 text-sm font-medium interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] sm:min-h-9 sm:w-full ${
                  tab === key
                    ? 'bg-[var(--color-surface)] text-[var(--color-fg)] ring-1 ring-inset ring-[var(--color-accent)] sm:bg-[var(--color-bg-muted)]'
                    : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)]/60 hover:text-[var(--color-fg)]'
                }`}
              >
                <Icon size={15} aria-hidden className="shrink-0" />
                <span>{t(label)}</span>
              </button>
            ))}
            </div>
          ) : null}
        </div>

        <div className="flex min-h-0 min-w-0 flex-1 flex-col">
          <div className="shrink-0 border-b border-[var(--color-divider)] px-5 py-4 sm:px-6 sm:pr-14 sm:pt-5">
            <h2 className="text-lg font-medium text-[var(--color-fg)]">
              {t(WORKSPACE_MANAGEMENT_TABS.find((item) => item.key === (canManage ? tab : 'members'))!.label)}
            </h2>
          </div>
          <div ref={paneRef} data-workspace-management-scroll className="min-h-0 min-w-0 flex-1 space-y-4 overflow-x-hidden overflow-y-auto overscroll-contain px-5 py-5 scrollbar-thin sm:px-6">

        {tab === 'profile' ? (
          <div
            id="workspace-management-panel-profile"
            role="tabpanel"
            aria-labelledby="workspace-management-tab-profile"
            tabIndex={0}
            className="outline-none"
          >
            <Suspense fallback={<PanelFallback />}><WorkspaceProfileEditor key={ws.id} workspace={ws} /></Suspense>
          </div>
        ) : null}
        {tab !== 'members' && canManage ? null : (
        <div
          id="workspace-management-panel-members"
          role={canManage ? 'tabpanel' : undefined}
          aria-labelledby={canManage ? 'workspace-management-tab-members' : undefined}
          tabIndex={canManage ? 0 : undefined}
          className="space-y-4 outline-none"
        >
        {/* Invite link — admins may reset it (member-level invite); the current
            token is only exposed to the owner, so ordinary admins see the
            fresh link after rotating. */}
        {canManage ? (
          <div className="rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-2.5">
            <div className="text-[11px] font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
              {t('workspace.inviteLink', { defaultValue: 'Invite link' })}
            </div>
            {inviteToken ? (
              <div className="mt-1.5 flex items-center gap-2">
                <code className="min-w-0 flex-1 truncate text-[11.5px] text-[var(--color-fg-muted)]">{inviteURL}</code>
                <Button size="sm" variant="secondary" onClick={() => copy(inviteURL)}>
                  <Copy size={12} aria-hidden />
                  {copied ? t('actions.copied', { defaultValue: 'Copied' }) : t('actions.copy', { defaultValue: 'Copy' })}
                </Button>
              </div>
            ) : (
              <p className="mt-1.5 text-[11.5px] text-[var(--color-fg-subtle)]">
                {t('workspace.inviteOwnerOnly', { defaultValue: 'Only the owner can view the current link. Reset it to generate a new one.' })}
              </p>
            )}
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
        <ul className="space-y-1">
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
          {!membersLoading && !membersLoadFailed && members.map((m) => {
            const self = m.user_id === ws.owner_id
            const targetIsAdmin = m.is_owner || m.role === 'admin'
            // Owner manages everyone (but never self); ordinary admins manage
            // ordinary members and guests only.
            const canActOn = canManage && !self && (isOwner || !targetIsAdmin)
            const roleOptions: ApiWorkspaceRole[] = isOwner && !m.is_owner
              ? ['admin', 'member', 'guest']
              : !targetIsAdmin
                ? ['member', 'guest']
                : []
            return (
              <li key={m.user_id} className="flex items-center gap-2.5 rounded-[8px] px-1.5 py-1.5">
              <Avatar size="sm">
                {m.avatar_url ? <AvatarImage src={m.avatar_url} alt={m.name} /> : null}
                <AvatarFallback>{initials(m.name || m.email)}</AvatarFallback>
              </Avatar>
              <div className="min-w-0 flex-1">
                <div className="truncate text-[13px] font-medium text-[var(--color-fg)]">{m.name || m.email}</div>
                <div className="truncate text-[11px] text-[var(--color-fg-subtle)]">
                  {m.is_owner
                    ? t('workspace.roleOwner', { defaultValue: 'Owner' })
                    : roleLabel(m.role)}
                </div>
              </div>
              {canActOn ? (
                <div className="flex shrink-0 items-center gap-0.5">
                  {roleOptions.length > 0 ? (
                    <DropdownMenu>
                      <Tooltip content={t('workspace.changeRole', { defaultValue: 'Change role' })}>
                        <DropdownMenuTrigger asChild>
                          <button
                            type="button"
                            disabled={busyUid !== null || roleBusyUid !== null}
                            aria-label={t('workspace.changeRole', { defaultValue: 'Change role' })}
                            className="inline-flex size-7 items-center justify-center rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:pointer-events-none disabled:opacity-50 max-sm:size-10"
                          >
                            <ShieldCheck size={13} aria-hidden />
                          </button>
                        </DropdownMenuTrigger>
                      </Tooltip>
                      <DropdownMenuContent align="end">
                        {roleOptions.map((role) => (
                          <DropdownMenuItem
                            key={role}
                            disabled={m.role === role || roleBusyUid !== null}
                            onClick={() => void changeRole(m.user_id, role)}
                          >
                            {m.role === role ? <Check size={13} aria-hidden /> : <span className="w-[13px]" aria-hidden />}
                            {roleLabel(role)}
                          </DropdownMenuItem>
                        ))}
                      </DropdownMenuContent>
                    </DropdownMenu>
                  ) : null}
                  {canEditWorkspaceMemberPermissions(m) ? (
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
                  ) : null}
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
            )
          })}
        </ul>
        </div>
        )}
        {tab === 'invites' && canManage && activeId ? (
          <div
            id="workspace-management-panel-invites"
            role="tabpanel"
            aria-labelledby="workspace-management-tab-invites"
            tabIndex={0}
            className="outline-none"
          >
            <WorkspaceInvitesPanel workspaceID={activeId} isOwner={isOwner} />
          </div>
        ) : null}
        {tab === 'policy' && canManage && activeId ? (
          <div
            id="workspace-management-panel-policy"
            role="tabpanel"
            aria-labelledby="workspace-management-tab-policy"
            tabIndex={0}
            className="outline-none"
          >
            <WorkspacePolicyPanel key={activeId} workspaceID={activeId} />
          </div>
        ) : null}
        {tab === 'announcement' && canManage && activeId ? (
          <div
            id="workspace-management-panel-announcement"
            role="tabpanel"
            aria-labelledby="workspace-management-tab-announcement"
            tabIndex={0}
            className="outline-none"
          >
            <WorkspaceAnnouncementPanel workspaceID={activeId} />
          </div>
        ) : null}
        {tab === 'audit' && canManage && activeId ? (
          <div
            id="workspace-management-panel-audit"
            role="tabpanel"
            aria-labelledby="workspace-management-tab-audit"
            tabIndex={0}
            className="outline-none"
          >
            <WorkspaceAuditPanel workspaceID={activeId} />
          </div>
        ) : null}
        {tab === 'statistics' && canManage && activeId ? (
          <div
            id="workspace-management-panel-statistics"
            role="tabpanel"
            aria-labelledby="workspace-management-tab-statistics"
            tabIndex={0}
            className="outline-none"
          >
            <WorkspaceUsagePanel workspaceID={activeId} />
          </div>
        ) : null}
          </div>

        <DialogFooter className="flex-wrap justify-between gap-y-2">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            {isOwner && tab === 'members' ? (
              <Button
                variant="secondary"
                disabled={actioning}
                leadingIcon={<ArrowLeftRight size={13} aria-hidden />}
                onClick={() => setTransferOpen(true)}
              >
                {t('workspace.transfer', { defaultValue: 'Transfer ownership' })}
              </Button>
            ) : null}
            {isOwner ? (
              <Button
                variant="destructive"
                loading={actioning}
                disabled={!userCan(user, 'allow_workspace_deletion')}
                title={!userCan(user, 'allow_workspace_deletion') ? t('workspace.deleteGroupDenied') : undefined}
                onClick={() => void runFooterAction(
                  removeWs,
                  t('workspace.deleteFailed', { defaultValue: 'Could not delete the workspace.' }),
                )}
              >
                <Trash2 size={13} aria-hidden />
                {t('workspace.delete', { defaultValue: 'Delete workspace' })}
              </Button>
            ) : !domainLocked ? (
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
            ) : null}
          </div>
          <Button variant="ghost" disabled={actioning || busyUid !== null} onClick={() => onOpenChange(false)}>
            {t('actions.close', { ns: 'common', defaultValue: 'Close' })}
          </Button>
        </DialogFooter>
        </div>
      </DialogContent>

      <WorkspaceTransferDialog
        open={transferOpen}
        onOpenChange={setTransferOpen}
        workspaceID={activeId ?? ''}
        workspaceName={ws.name}
        members={members.filter((m) => !m.is_owner)}
        onTransferred={() => setMembersLoadAttempt((attempt) => attempt + 1)}
      />

      <Dialog
        open={editingMember !== null}
        onOpenChange={(next) => {
          if (!next && !savingPermissions) closePermissionEditor()
        }}
      >
        <DialogContent size="md" closeDisabled={savingPermissions}>
          <DialogHeader>
            <DialogTitle>{t('workspace.memberPermissions', { defaultValue: 'Member permissions' })}</DialogTitle>
            <DialogDescription>
              {t('workspace.memberPermissionsBody', {
                name: editingMember?.name || editingMember?.email || '',
                defaultValue: 'Set what {{name}} can use, create, and manage across this workspace.',
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogBody className="min-h-0 max-h-[min(70dvh,38rem)] overflow-y-auto py-0">
            <p className="py-3 text-[12.5px] leading-5 text-[var(--color-fg-muted)]">
              {t('workspace.groupPermissionCeiling')}
            </p>
            {permissionDraft && editingMember?.role === 'guest' ? (
              <p className="py-4 text-[12.5px] leading-5 text-[var(--color-fg-muted)]">
                {t('workspace.readOnlyAccess', { defaultValue: 'Read-only access' })}
              </p>
            ) : null}
            {permissionDraft && editingMember?.role !== 'guest' ? (
              <div className="space-y-6 py-1">
                {permissionConflict ? (
                  <div role="alert" className="flex items-start gap-3 border-b border-[var(--color-warning)]/30 bg-[var(--color-warning)]/5 px-1 py-3">
                    <AlertTriangle size={16} aria-hidden className="mt-0.5 shrink-0 text-[var(--color-warning)]" />
                    <div className="min-w-0 flex-1">
                      <p className="text-[12.5px] font-medium text-[var(--color-fg)]">
                        {t('workspace.permissionsConflict', { defaultValue: 'Member permissions changed on the server.' })}
                      </p>
                      <p className="mt-0.5 text-[11.5px] leading-4 text-[var(--color-fg-subtle)]">
                        {t('workspace.permissionsConflictHint', { defaultValue: 'Your unsaved changes are preserved. Reload before saving to avoid overwriting newer settings.' })}
                      </p>
                      <Button
                        size="sm"
                        variant="secondary"
                        className="mt-2"
                        onClick={() => {
                          closePermissionEditor()
                          setMembersLoadAttempt((attempt) => attempt + 1)
                        }}
                      >
                        {t('workspace.reloadPermissions', { defaultValue: 'Reload permissions' })}
                      </Button>
                    </div>
                  </div>
                ) : null}
                {WORKSPACE_PERMISSION_GROUPS.map((group) => (
                  <section key={group.id} aria-labelledby={`workspace-member-permissions-${group.id}`}>
                    <div className="border-b border-[var(--color-divider)] py-3">
                      <h3 id={`workspace-member-permissions-${group.id}`} className="text-[13px] font-semibold text-[var(--color-fg)]">
                        {t(`workspace.permissions.groups.${group.id}.label`, { defaultValue: group.label })}
                      </h3>
                      <p className="mt-0.5 text-[11.5px] leading-4 text-[var(--color-fg-subtle)]">
                        {t(`workspace.permissions.groups.${group.id}.description`, { defaultValue: group.description })}
                      </p>
                    </div>
                    <div className="divide-y divide-[var(--color-divider)]">
                      {group.rows.map((row) => (
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
                            checked={Boolean(permissionDraft[row.key])}
                            disabled={savingPermissions}
                            onCheckedChange={(checked) => {
                              permissionDraftDirtyRef.current = true
                              setPermissionDraftDirty(true)
                              setPermissionDraft((current) => current ? { ...current, [row.key]: checked } : current)
                            }}
                            aria-label={t(`workspace.permissions.${row.key}.label`, { defaultValue: row.label })}
                          />
                        </label>
                      ))}
                    </div>
                  </section>
                ))}
              </div>
            ) : null}
          </DialogBody>
          <DialogFooter>
            <Button
              variant="ghost"
              disabled={savingPermissions}
              onClick={closePermissionEditor}
            >
              {editingMember?.role === 'guest'
                ? t('actions.close', { ns: 'common', defaultValue: 'Close' })
                : t('common.cancel', { ns: 'common', defaultValue: 'Cancel' })}
            </Button>
            {editingMember?.role !== 'guest' ? (
              <Button
                loading={savingPermissions}
                disabled={!permissionDraftDirty || permissionConflict}
                onClick={() => void savePermissions()}
              >
                {t('common.save', { ns: 'common', defaultValue: 'Save' })}
              </Button>
            ) : null}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Dialog>
  )
}

type WorkspaceMemberPermissionRow = {
  key: keyof ApiWorkspaceMemberPermissions
  label: string
  description: string
}

type WorkspaceMemberPermissionGroup = {
  id: 'useResources' | 'createResources' | 'workspaceContent'
  label: string
  description: string
  rows: WorkspaceMemberPermissionRow[]
}

/**
 * Keep usage and creation controls adjacent but visibly separate. A member
 * can, for example, use an MCP without being allowed to register one, which is
 * why these must never share a switch or a fallback in the dialog.
 */
const WORKSPACE_PERMISSION_GROUPS: WorkspaceMemberPermissionGroup[] = [
  {
    id: 'useResources',
    label: 'Use workspace resources',
    description: 'Choose which resource families this member can use in chats and projects.',
    rows: [
      { key: 'can_use_prompts', label: 'Use prompts', description: 'Apply prompts from the workspace library.' },
      { key: 'can_use_skills', label: 'Use skills', description: 'Run skills shared in the workspace library.' },
      { key: 'can_use_mcp', label: 'Use MCP services', description: 'Call tools exposed by MCP services available in this workspace.' },
      { key: 'can_use_ai_ppt', label: 'Use AI PPT', description: 'Create and edit AI presentations when the user group and workspace both allow it.' },
    ],
  },
  {
    id: 'createResources',
    label: 'Create workspace resources',
    description: 'Creation rights are independent from usage rights above.',
    rows: [
      { key: 'can_create_prompts', label: 'Create prompts', description: 'Create new prompts in this workspace.' },
      { key: 'can_create_skills', label: 'Create skills', description: 'Create new skills in this workspace.' },
      { key: 'can_create_mcp', label: 'Create MCP services', description: 'Register new MCP services in this workspace.' },
    ],
  },
  {
    id: 'workspaceContent',
    label: 'Workspace content',
    description: 'Control projects, conversations, and knowledge-base operations.',
    rows: [
      { key: 'can_create_projects', label: 'Create projects', description: 'Create new projects and their project libraries.' },
      { key: 'can_private_conversations', label: 'Private conversations', description: 'Create conversations visible only to themselves.' },
      { key: 'can_create_kb', label: 'Create knowledge bases', description: 'Create new workspace knowledge bases.' },
      { key: 'can_add_kb_files', label: 'Add knowledge-base files', description: 'Upload and paste content into workspace knowledge bases.' },
      { key: 'can_delete_kb_content', label: 'Delete knowledge-base content', description: 'Delete or retry content when the specific library also allows it.' },
      { key: 'can_delete_conversations', label: 'Delete conversations', description: 'Delete messages and conversations in this workspace, including conversations they created.' },
    ],
  },
]

const WORKSPACE_USAGE_RANGES = ['1', '7', '30', '90', '365'] as const

function WorkspaceUsagePanel({ workspaceID }: { workspaceID: string }) {
  const { t } = useTranslation('chat')
  const [days, setDays] = useState('30')
  const [data, setData] = useState<ApiWorkspaceUsageAnalytics | null>(null)
  const [loading, setLoading] = useState(true)
  const [failed, setFailed] = useState(false)
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    let current = true
    setLoading(true)
    setFailed(false)
    workspacesApi.usage(workspaceID, Number(days))
      .then((next) => { if (current) setData(next) })
      .catch(() => { if (current) { setData(null); setFailed(true) } })
      .finally(() => { if (current) setLoading(false) })
    return () => { current = false }
  }, [attempt, days, workspaceID])

  const number = (value: number) => new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(value || 0)
  const decimal = (value: number) => new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 }).format(value || 0)
  const totals = data?.totals
  const previous = data?.previous_totals
  const trend = data?.trend ?? []
  const maxTrend = Math.max(1, ...trend.map((point) => point.input_tokens + point.output_tokens))
  const totalTokens = (totals?.input_tokens ?? 0) + (totals?.output_tokens ?? 0)
  const previousTokens = (previous?.input_tokens ?? 0) + (previous?.output_tokens ?? 0)
  const metrics = [
    { label: t('workspace.statsTurns', { defaultValue: 'Conversation turns' }), value: number(totals?.turns ?? 0), hint: `${number(totals?.calls ?? 0)} ${t('workspace.statsCalls', { defaultValue: 'calls' })}` },
    { label: t('workspace.statsTokens', { defaultValue: 'Total tokens' }), value: number(totalTokens), hint: `${number(totals?.input_tokens ?? 0)} in · ${number(totals?.output_tokens ?? 0)} out` },
    { label: t('workspace.statsUsers', { defaultValue: 'Active members' }), value: number(totals?.users ?? 0), hint: `${number(totals?.conversations ?? 0)} ${t('workspace.statsConversations', { defaultValue: 'conversations' })}` },
    { label: t('workspace.statsCredits', { defaultValue: 'Credits' }), value: decimal(totals?.credits ?? 0), hint: totals?.cost ? `${decimal(totals.cost)} USD ${t('workspace.statsCost', { defaultValue: 'cost' })}` : t('workspace.statsNoCost', { defaultValue: 'Provider cost unavailable' }) },
  ]

  if (loading) {
    return <div className="space-y-5" aria-busy="true">
      <div className="flex items-center justify-between gap-3"><div><Skeleton className="h-5 w-36" /><Skeleton className="mt-2 h-3 w-64" /></div><Skeleton className="h-9 w-32" /></div>
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">{[0, 1, 2, 3].map((key) => <Skeleton key={key} className="h-24 rounded-[10px]" />)}</div>
      <Skeleton className="h-56 rounded-[10px]" />
    </div>
  }
  if (failed || !data) {
    return <div role="alert" className="flex min-h-56 flex-col items-center justify-center rounded-[12px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-5 text-center">
      <AlertTriangle size={20} aria-hidden className="text-[var(--color-danger)]" />
      <p className="mt-2 text-[13px] font-medium text-[var(--color-fg)]">{t('workspace.statsLoadFailed', { defaultValue: 'Could not load workspace statistics.' })}</p>
      <Button size="sm" variant="secondary" className="mt-3" leadingIcon={<RefreshCw size={13} aria-hidden />} onClick={() => setAttempt((value) => value + 1)}>{t('actions.tryAgain', { ns: 'common', defaultValue: 'Try again' })}</Button>
    </div>
  }

  return <div className="space-y-5">
    <div className="flex flex-col gap-3 rounded-[12px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4 sm:flex-row sm:items-center sm:justify-between">
      <div>
        <h3 className="text-[15px] font-semibold text-[var(--color-fg)]">{t('workspace.statisticsTitle', { defaultValue: 'Workspace usage' })}</h3>
        <p className="mt-1 text-[12px] leading-5 text-[var(--color-fg-muted)]">{t('workspace.statisticsLead', { defaultValue: 'Usage from conversations and tools in this workspace.' })}</p>
      </div>
      <Select value={days} onValueChange={setDays}>
        <SelectTrigger className="w-full sm:w-36" aria-label={t('workspace.statsRange', { defaultValue: 'Time range' })}><SelectValue /></SelectTrigger>
        <SelectContent>{WORKSPACE_USAGE_RANGES.map((range) => <SelectItem key={range} value={range}>{t(`workspace.statsRange${range}`, { defaultValue: `${range} days` })}</SelectItem>)}</SelectContent>
      </Select>
    </div>
    <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
      {metrics.map((metric) => <div key={metric.label} className="rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg)] p-4">
        <div className="text-[11px] font-medium text-[var(--color-fg-subtle)]">{metric.label}</div>
        <div className="mt-2 text-[22px] font-semibold tracking-tight text-[var(--color-fg)]">{metric.value}</div>
        <div className="mt-1 truncate text-[11px] text-[var(--color-fg-muted)]">{metric.hint}</div>
      </div>)}
    </div>
    <section className="rounded-[12px] border border-[var(--color-border)] bg-[var(--color-bg)] p-4" aria-labelledby="workspace-statistics-trend">
      <div className="flex items-baseline justify-between gap-3"><div><h3 id="workspace-statistics-trend" className="text-[13px] font-semibold text-[var(--color-fg)]">{t('workspace.statsTrend', { defaultValue: 'Token trend' })}</h3><p className="mt-1 text-[11px] text-[var(--color-fg-subtle)]">{number(totalTokens)} {t('workspace.statsTokensUsed', { defaultValue: 'tokens used' })} · {number(previousTokens)} {t('workspace.statsPreviousPeriod', { defaultValue: 'previous period' })}</p></div><BarChart3 size={16} aria-hidden className="text-[var(--color-fg-subtle)]" /></div>
      {trend.length > 0 ? <div className="mt-5 flex h-36 items-end gap-1.5 overflow-hidden" aria-label={t('workspace.statsTrend', { defaultValue: 'Token trend' })}>{trend.map((point) => { const value = point.input_tokens + point.output_tokens; const height = Math.max(4, Math.round((value / maxTrend) * 100)); return <div key={point.bucket_start} className="group flex min-w-0 flex-1 flex-col items-center justify-end gap-1" title={`${number(value)} tokens`}><div className="w-full max-w-8 rounded-t-[5px] bg-[var(--color-accent)]/75 transition-[height] duration-300 group-hover:bg-[var(--color-accent)]" style={{ height: `${height}%` }} /><span className="sr-only">{number(value)} tokens</span></div> })}</div> : <p className="mt-8 text-center text-[12px] text-[var(--color-fg-subtle)]">{t('workspace.statsNoData', { defaultValue: 'No usage in this period.' })}</p>}
    </section>
    <section className="rounded-[12px] border border-[var(--color-border)] bg-[var(--color-bg)]" aria-labelledby="workspace-statistics-members">
      <div className="border-b border-[var(--color-divider)] px-4 py-3"><h3 id="workspace-statistics-members" className="text-[13px] font-semibold text-[var(--color-fg)]">{t('workspace.statsMemberUsage', { defaultValue: 'Member usage' })}</h3></div>
      <div className="overflow-x-auto"><table className="w-full min-w-[620px] text-left text-[12px]"><thead className="text-[11px] text-[var(--color-fg-subtle)]"><tr><th className="px-4 py-2.5 font-medium">{t('workspace.statsMember', { defaultValue: 'Member' })}</th><th className="px-3 py-2.5 font-medium">{t('workspace.statsTurns', { defaultValue: 'Turns' })}</th><th className="px-3 py-2.5 font-medium">{t('workspace.statsInput', { defaultValue: 'Input tokens' })}</th><th className="px-3 py-2.5 font-medium">{t('workspace.statsOutput', { defaultValue: 'Output tokens' })}</th><th className="px-3 py-2.5 font-medium">{t('workspace.statsCredits', { defaultValue: 'Credits' })}</th></tr></thead><tbody className="divide-y divide-[var(--color-divider)]">{data.usage.length > 0 ? data.usage.map((member) => <tr key={member.user_id} className="text-[var(--color-fg-muted)]"><td className="max-w-[240px] truncate px-4 py-3 font-medium text-[var(--color-fg)]">{member.name || member.email}</td><td className="px-3 py-3">{number(member.messages)}</td><td className="px-3 py-3">{number(member.input_tokens)}</td><td className="px-3 py-3">{number(member.output_tokens)}</td><td className="px-3 py-3">{decimal(member.credits)}</td></tr>) : <tr><td colSpan={5} className="px-4 py-10 text-center text-[12px] text-[var(--color-fg-subtle)]">{t('workspace.statsNoData', { defaultValue: 'No usage in this period.' })}</td></tr>}</tbody></table></div>
    </section>
  </div>
}

function WorkspaceAnnouncementPanel({ workspaceID }: { workspaceID: string }) {
  const { t } = useTranslation('chat')
  const load = useCallback(() => workspacesApi.announcement(workspaceID), [workspaceID])
  const save = useCallback((payload: ApiAnnouncement) => workspacesApi.updateAnnouncement(workspaceID, payload), [workspaceID])
  const uploadImage = useCallback((file: File) => workspacesApi.uploadAnnouncementImage(workspaceID, file), [workspaceID])
  return (
    <AnnouncementEditor
      load={load}
      save={save}
      uploadImage={uploadImage}
      compact
      title={t('workspace.announcementTitle', { defaultValue: 'Workspace announcement' })}
      lead={t('workspace.announcementLead', { defaultValue: 'Only members of this workspace will see this announcement.' })}
      translationNamespace="chat"
      translationPrefix="workspace.announcement"
    />
  )
}

/** §workspace RBAC phase 3 — invite records: create, copy link, revoke. */
function WorkspaceInvitesPanel({ workspaceID, isOwner }: { workspaceID: string; isOwner: boolean }) {
  const { t } = useTranslation('chat')
  const [invites, setInvites] = useState<ApiWorkspaceInvite[]>([])
  const [loading, setLoading] = useState(true)
  const [failed, setFailed] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const [role, setRole] = useState<ApiWorkspaceRole>('guest')
  const [email, setEmail] = useState('')
  const [expiryDays, setExpiryDays] = useState(7)
  const [maxUses, setMaxUses] = useState(1)
  const [creating, setCreating] = useState(false)
  const [revokingId, setRevokingId] = useState<string | null>(null)
  const { copied, copy } = useCopy()

  useEffect(() => {
    let current = true
    setLoading(true)
    setFailed(false)
    workspacesApi
      .listInvites(workspaceID)
      .then((r) => { if (current) setInvites(r.invites) })
      .catch(() => { if (current) { setInvites([]); setFailed(true) } })
      .finally(() => { if (current) setLoading(false) })
    return () => { current = false }
  }, [workspaceID, attempt])

  async function createInvite() {
    if (creating) return
    setCreating(true)
    try {
      await workspacesApi.createInvite(workspaceID, {
        role,
        email: email.trim() || undefined,
        expires_at: expiryDays > 0 ? Math.floor(Date.now() / 1000) + expiryDays * 86400 : 0,
        max_uses: Math.max(0, Math.floor(maxUses)),
      })
      setEmail('')
      setAttempt((v) => v + 1)
      toast.success(t('workspace.inviteCreated', { defaultValue: 'Invite created.' }))
    } catch {
      toast.error(t('workspace.inviteCreateFailed', { defaultValue: 'Could not create the invite.' }))
    } finally {
      setCreating(false)
    }
  }

  async function revoke(id: string) {
    if (revokingId) return
    setRevokingId(id)
    try {
      await workspacesApi.revokeInvite(workspaceID, id)
      setAttempt((v) => v + 1)
      toast.success(t('workspace.inviteRevoked', { defaultValue: 'Invite revoked.' }))
    } catch {
      toast.error(t('workspace.inviteRevokeFailed', { defaultValue: 'Could not revoke the invite.' }))
    } finally {
      setRevokingId(null)
    }
  }

  function inviteStatus(invite: ApiWorkspaceInvite): { label: string; dead: boolean } {
    const now = Math.floor(Date.now() / 1000)
    if (invite.revoked_at > 0) return { label: t('workspace.inviteRevokedLabel', { defaultValue: 'Revoked' }), dead: true }
    if (invite.expires_at > 0 && now > invite.expires_at) return { label: t('workspace.inviteExpired', { defaultValue: 'Expired' }), dead: true }
    if (invite.max_uses > 0 && invite.used_count >= invite.max_uses) return { label: t('workspace.inviteExhausted', { defaultValue: 'Used up' }), dead: true }
    return { label: t('workspace.inviteActive', { defaultValue: 'Active' }), dead: false }
  }

  const roleOptions: ApiWorkspaceRole[] = isOwner ? ['guest', 'member', 'admin'] : ['guest', 'member']
  function roleLabel(r: ApiWorkspaceRole): string {
    switch (r) {
      case 'admin': return t('workspace.roleAdmin', { defaultValue: 'Admin' })
      case 'guest': return t('workspace.roleGuest', { defaultValue: 'Guest' })
      default: return t('workspace.roleMember', { defaultValue: 'Member' })
    }
  }

  return (
    <div className="space-y-3">
      <div className="rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-2">
        <div className="grid min-w-0 grid-cols-[4.75rem_minmax(0,1fr)_5.25rem_2.75rem_2rem] items-center gap-1.5">
          <Select value={role} disabled={creating} onValueChange={(value) => setRole(value as ApiWorkspaceRole)}>
            <SelectTrigger
              aria-label={t('workspace.inviteRole', { defaultValue: 'Invite role' })}
              className="h-8 min-w-0 gap-1 px-2 text-[12px] [&>span:first-child]:min-w-0 [&>span:first-child]:truncate [&>span:first-child]:whitespace-nowrap"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {roleOptions.map((option) => (
                <SelectItem key={option} value={option}>{roleLabel(option)}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Input
            value={email}
            disabled={creating}
            onChange={(e) => setEmail(e.target.value)}
            placeholder={t('workspace.inviteEmailOptional', { defaultValue: 'Bind to email (optional)' })}
            aria-label={t('workspace.inviteEmailOptional', { defaultValue: 'Bind to email (optional)' })}
            wrapperClassName="h-8 min-w-0 w-full px-2.5"
            className="min-w-0 text-[12.5px]"
            inputMode="email"
          />
          <Select value={String(expiryDays)} disabled={creating} onValueChange={(value) => setExpiryDays(Number(value))}>
            <SelectTrigger
              aria-label={t('workspace.inviteExpires', { defaultValue: 'Expires' })}
              className="h-8 min-w-0 gap-1 px-2 text-[12px] [&>span:first-child]:min-w-0 [&>span:first-child]:truncate [&>span:first-child]:whitespace-nowrap"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="1">{t('workspace.inviteExpiryDay', { defaultValue: '1 day' })}</SelectItem>
              <SelectItem value="7">{t('workspace.inviteExpiryWeek', { defaultValue: '7 days' })}</SelectItem>
              <SelectItem value="30">{t('workspace.inviteExpiryMonth', { defaultValue: '30 days' })}</SelectItem>
              <SelectItem value="0">{t('workspace.inviteNeverExpires', { defaultValue: 'Never' })}</SelectItem>
            </SelectContent>
          </Select>
          <label className="flex min-w-0 items-center text-[11px] text-[var(--color-fg-subtle)]">
            <span className="sr-only">{t('workspace.inviteMaxUses', { defaultValue: 'Max uses' })}</span>
            <Input
              value={maxUses}
              disabled={creating}
              onChange={(e) => setMaxUses(Math.max(0, Number(e.target.value) || 0))}
              type="number"
              min={0}
              wrapperClassName="h-8 w-full min-w-0 px-1.5"
              className="min-w-0 appearance-none p-0 text-center text-[12.5px] [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none"
              aria-label={t('workspace.inviteMaxUses', { defaultValue: 'Max uses (0 for unlimited)' })}
              title={t('workspace.inviteMaxUses', { defaultValue: 'Max uses' })}
            />
          </label>
          <Tooltip content={t('workspace.inviteCreate', { defaultValue: 'Create invite' })}>
            <Button
              className="size-8 shrink-0 rounded-[8px]"
              size="icon-sm"
              loading={creating}
              aria-label={t('workspace.inviteCreate', { defaultValue: 'Create invite' })}
              onClick={() => void createInvite()}
            >
              {creating ? null : <UserPlus size={14} aria-hidden />}
            </Button>
          </Tooltip>
        </div>
      </div>

      {loading ? (
        <div className="space-y-2 py-2">{[0, 1, 2].map((i) => <Skeleton key={i} className="h-12 w-full" />)}</div>
      ) : failed ? (
        <div role="alert" className="flex flex-col items-center px-4 py-6 text-center">
          <AlertTriangle size={18} aria-hidden className="text-[var(--color-danger)]" />
          <p className="mt-2 text-[13px] text-[var(--color-fg-muted)]">
            {t('workspace.invitesLoadFailed', { defaultValue: 'Could not load invites.' })}
          </p>
          <Button size="sm" variant="secondary" className="mt-3" leadingIcon={<RefreshCw size={13} aria-hidden />} onClick={() => setAttempt((v) => v + 1)}>
            {t('actions.tryAgain', { ns: 'common', defaultValue: 'Try again' })}
          </Button>
        </div>
      ) : invites.length === 0 ? (
        <p className="py-6 text-center text-[13px] text-[var(--color-fg-muted)]">
          {t('workspace.invitesEmpty', { defaultValue: 'No invites yet.' })}
        </p>
      ) : (
        <ul className="space-y-1">
          {invites.map((invite) => {
            const status = inviteStatus(invite)
            const url = `${window.location.origin}/workspace/join/${invite.token}`
            return (
              <li key={invite.id} className="flex items-center gap-2.5 rounded-[8px] px-1.5 py-1.5">
                <KeyRound size={14} aria-hidden className="shrink-0 text-[var(--color-fg-subtle)]" />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[12.5px] font-medium text-[var(--color-fg)]">
                    {roleLabel(invite.role)}
                    {invite.email ? <span className="ml-1.5 font-normal text-[var(--color-fg-subtle)]">· {invite.email}</span> : null}
                  </div>
                  <div className="truncate text-[11px] text-[var(--color-fg-subtle)]">
                    {status.label}
                    {' · '}
                    {t('workspace.inviteUses', { used: invite.used_count, max: invite.max_uses > 0 ? invite.max_uses : '∞', defaultValue: '{{used}}/{{max}} uses' })}
                  </div>
                </div>
                {!status.dead ? (
                  <div className="flex shrink-0 items-center gap-0.5">
                    <Button size="sm" variant="secondary" onClick={() => copy(url)}>
                      <Copy size={12} aria-hidden />
                      {copied ? t('actions.copied', { defaultValue: 'Copied' }) : t('actions.copy', { defaultValue: 'Copy' })}
                    </Button>
                    <Tooltip content={t('workspace.inviteRevoke', { defaultValue: 'Revoke invite' })}>
                      <button
                        type="button"
                        onClick={() => void revoke(invite.id)}
                        disabled={revokingId !== null}
                        aria-label={t('workspace.inviteRevoke', { defaultValue: 'Revoke invite' })}
                        className="inline-flex size-7 items-center justify-center rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:pointer-events-none disabled:opacity-50"
                      >
                        <UserX size={13} aria-hidden />
                      </button>
                    </Tooltip>
                  </div>
                ) : null}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

/** §workspace RBAC phase 4 — capability policy: switches, model allowlist and
 *  the member monthly credit limit. */
function WorkspacePolicyPanel({ workspaceID }: { workspaceID: string }) {
  const { t } = useTranslation('chat')
  const setWorkspacePolicy = useWorkspaces((state) => state.setPolicy)
  const [policy, setPolicy] = useState<ApiWorkspacePolicy | null>(null)
  const [models, setModels] = useState<Array<Pick<ApiModel, 'id' | 'label' | 'kind'>>>([])
  const [loading, setLoading] = useState(true)
  const [failed, setFailed] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const [saving, setSaving] = useState(false)
  const savingRef = useRef(false)
  const saveEpochRef = useRef(0)
  const mountedRef = useRef(true)
  const [policyDirty, setPolicyDirty] = useState(false)
  const policyDirtyRef = useRef(false)
  const [policyConflict, setPolicyConflict] = useState(false)
  const [limitDraft, setLimitDraft] = useState('0')

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      saveEpochRef.current += 1
    }
  }, [])

  function markPolicyDirty() {
    policyDirtyRef.current = true
    setPolicyDirty(true)
  }

  function reloadPolicy() {
    policyDirtyRef.current = false
    setPolicyDirty(false)
    setPolicyConflict(false)
    setAttempt((value) => value + 1)
  }

  useEffect(() => {
    let current = true
    setLoading(true)
    setFailed(false)
    Promise.all([
      workspacesApi.getPolicy(workspaceID),
      // This manager-only catalog is independent from the operator's group and
      // current workspace policy. A narrowed public picker would make saving
      // an explicit allowlist destructive.
      workspacesApi.policyModels(workspaceID),
    ])
      .then(([p, modelCatalog]) => {
        if (current) {
          setPolicy(p)
          setWorkspacePolicy(workspaceID, p)
          setModels(modelCatalog.models)
          setLimitDraft(String(p.MemberMonthlyCreditLimit))
          policyDirtyRef.current = false
          setPolicyDirty(false)
          setPolicyConflict(false)
        }
      })
      .catch(() => { if (current) setFailed(true) })
      .finally(() => { if (current) setLoading(false) })
    return () => { current = false }
  }, [attempt, setWorkspacePolicy, workspaceID])

  // Reconcile an already-open policy editor when another tab/member changes
  // workspace capabilities. The realtime layer refreshes the global policy
  // cache and emits this event; without a local reload, clicking Save here
  // could overwrite that newer server state with an old draft.
  useEffect(() => {
    return subscribeAccessInvalidation((event) => {
      if (event.kind !== 'workspace') return
      if (policyDirtyRef.current || savingRef.current) {
        setPolicyConflict(true)
        return
      }
      setAttempt((value) => value + 1)
    })
  }, [workspaceID])

  if (loading) {
    return <div className="space-y-2 py-2">{[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-12 w-full" />)}</div>
  }
  if (failed || !policy) {
    return (
      <div role="alert" className="flex flex-col items-center px-4 py-6 text-center">
        <AlertTriangle size={18} aria-hidden className="text-[var(--color-danger)]" />
        <p className="mt-2 text-[13px] text-[var(--color-fg-muted)]">
          {t('workspace.policyLoadFailed', { defaultValue: 'Could not load the workspace policy.' })}
        </p>
        <Button size="sm" variant="secondary" className="mt-3" leadingIcon={<RefreshCw size={13} aria-hidden />} onClick={() => setAttempt((v) => v + 1)}>
          {t('actions.tryAgain', { ns: 'common', defaultValue: 'Try again' })}
        </Button>
      </div>
    )
  }

  const allModelsAllowed = policy.AllowedModelIDs.length === 0
  const capabilities = workspaceCapabilities(policy)
  type CapabilityKey = 'AllowToolCalling' | 'AllowDrawing' | 'AllowPrivateChat' | 'AllowAiPPT' | 'AllowMCP' | 'AllowSkills' | 'AllowPrompts' | 'AllowKnowledgeBases' | 'AllowFileUpload'
  const capabilityGroups: Array<{
    id: 'core' | 'resources' | 'content'
    label: string
    description: string
    rows: Array<{ key: CapabilityKey; label: string; description: string }>
  }> = [
    {
      id: 'core',
      label: t('workspace.policy.groups.core.label', { defaultValue: 'Core capabilities' }),
      description: t('workspace.policy.groups.core.description', { defaultValue: 'Workspace-wide controls apply to every member, including admins.' }),
      rows: [
        { key: 'AllowToolCalling', label: 'Tool calling', description: 'Allow model tool calls, including built-in tools and MCP services.' },
        { key: 'AllowDrawing', label: 'Drawing', description: 'Allow the dedicated drawing mode and image models.' },
        { key: 'AllowPrivateChat', label: 'Private chat', description: 'Allow temporary chats when the system user group also permits them. Applies to workspace admins too.' },
        { key: 'AllowAiPPT', label: 'AI PPT', description: 'Allow AI presentations in this workspace. User group and member restrictions still apply.' },
      ],
    },
    {
      id: 'resources',
      label: t('workspace.policy.groups.resources.label', { defaultValue: 'Resource library' }),
      description: t('workspace.policy.groups.resources.description', { defaultValue: 'Turn off a family to hide its library tab and block its use in conversations.' }),
      rows: [
        { key: 'AllowPrompts', label: 'Prompts', description: 'Show and use prompt resources in this workspace.' },
        { key: 'AllowSkills', label: 'Skills', description: 'Show and use skill resources in this workspace.' },
        { key: 'AllowMCP', label: 'MCP services', description: 'Show MCP services and allow their tools when tool calling is enabled.' },
      ],
    },
    {
      id: 'content',
      label: t('workspace.policy.groups.content.label', { defaultValue: 'Workspace content' }),
      description: t('workspace.policy.groups.content.description', { defaultValue: 'Control shared knowledge and file operations.' }),
      rows: [
        { key: 'AllowKnowledgeBases', label: 'Knowledge bases', description: 'Create and use workspace knowledge bases and projects.' },
        { key: 'AllowFileUpload', label: 'File upload', description: 'Attach files to workspace conversations.' },
      ],
    },
  ]

  async function save() {
    if (savingRef.current || policyConflict || !policyDirty) return
    const saveEpoch = ++saveEpochRef.current
    const targetWorkspaceID = workspaceID
    savingRef.current = true
    setSaving(true)
    try {
      const updated = await workspacesApi.updatePolicy(targetWorkspaceID, {
        AllowedModelIDs: policy!.AllowedModelIDs,
        AllowToolCalling: capabilities.toolCalling,
        AllowDrawing: capabilities.drawing,
        AllowMCP: capabilities.mcp,
        AllowSkills: capabilities.skills,
        AllowPrompts: capabilities.prompts,
        AllowPrivateChat: capabilities.privateChat,
        AllowAiPPT: policy!.AllowAiPPT ?? true,
        AllowKnowledgeBases: capabilities.knowledgeBases,
        AllowFileUpload: capabilities.fileUpload,
        MemberMonthlyCreditLimit: Math.max(0, Number(limitDraft) || 0),
      })
      if (!mountedRef.current || saveEpoch !== saveEpochRef.current) return
      setPolicy(updated)
      setWorkspacePolicy(targetWorkspaceID, updated)
      setLimitDraft(String(updated.MemberMonthlyCreditLimit))
      policyDirtyRef.current = false
      setPolicyDirty(false)
      setPolicyConflict(false)
      toast.success(t('workspace.policySaved', { defaultValue: 'Workspace capabilities updated.' }))
    } catch {
      if (mountedRef.current && saveEpoch === saveEpochRef.current) {
        toast.error(t('workspace.policySaveFailed', { defaultValue: 'Could not update the workspace policy.' }))
      }
    } finally {
      if (saveEpoch === saveEpochRef.current) {
        savingRef.current = false
        if (mountedRef.current) setSaving(false)
      }
    }
  }

  return (
    <div className="space-y-3">
      {policyConflict ? (
        <div role="alert" className="flex items-start gap-3 border-b border-[var(--color-warning)]/30 bg-[var(--color-warning)]/5 px-1 py-3">
          <AlertTriangle size={16} aria-hidden className="mt-0.5 shrink-0 text-[var(--color-warning)]" />
          <div className="min-w-0 flex-1">
            <p className="text-[12.5px] font-medium text-[var(--color-fg)]">
              {t('workspace.policyConflict', { defaultValue: 'Workspace capabilities changed on the server.' })}
            </p>
            <p className="mt-0.5 text-[11.5px] leading-4 text-[var(--color-fg-subtle)]">
              {t('workspace.policyConflictHint', { defaultValue: 'Your unsaved changes are preserved. Reload before saving to avoid overwriting newer settings.' })}
            </p>
            <Button size="sm" variant="secondary" className="mt-2" onClick={reloadPolicy}>
              {t('workspace.reloadPolicy', { defaultValue: 'Reload capabilities' })}
            </Button>
          </div>
        </div>
      ) : null}
      <div className="space-y-4">
        <p className="text-[12.5px] leading-5 text-[var(--color-fg-muted)]">
          {t('workspace.groupPermissionCeiling')}
        </p>
        {capabilityGroups.map((group) => (
          <section key={group.id} className="rounded-[10px] border border-[var(--color-border)] px-3">
            <div className="border-b border-[var(--color-divider)] py-3">
              <h3 className="text-[13px] font-semibold text-[var(--color-fg)]">{group.label}</h3>
              <p className="mt-0.5 text-[11.5px] leading-4 text-[var(--color-fg-subtle)]">{group.description}</p>
            </div>
            <div className="divide-y divide-[var(--color-divider)]">
              {group.rows.map((row) => {
                const capability = row.key === 'AllowToolCalling'
                  ? capabilities.toolCalling
                  : row.key === 'AllowDrawing'
                    ? capabilities.drawing
                    : row.key === 'AllowMCP'
                      ? capabilities.mcp
                      : row.key === 'AllowSkills'
                        ? capabilities.skills
                        : row.key === 'AllowPrompts'
                          ? capabilities.prompts
                          : row.key === 'AllowPrivateChat'
                            ? capabilities.privateChat
                            : row.key === 'AllowAiPPT'
                              ? policy.AllowAiPPT ?? true
                            : row.key === 'AllowKnowledgeBases'
                              ? capabilities.knowledgeBases
                              : capabilities.fileUpload
                return (
                  <label key={row.key} className="flex min-h-14 cursor-pointer items-center gap-4 py-3">
                    <span className="min-w-0 flex-1">
                      <span className="block text-[13px] font-medium text-[var(--color-fg)]">
                        {t(`workspace.policy.${row.key}.label`, { defaultValue: row.label })}
                      </span>
                      <span className="mt-0.5 block text-[11.5px] leading-4 text-[var(--color-fg-subtle)]">
                        {t(`workspace.policy.${row.key}.description`, { defaultValue: row.description })}
                      </span>
                    </span>
                    <Switch
                      checked={capability}
                      disabled={saving}
                      onCheckedChange={(checked) => {
                        markPolicyDirty()
                        setPolicy((current) => {
                          if (!current) return current
                          if (row.key === 'AllowToolCalling') return { ...current, AllowToolCalling: checked }
                          if (row.key === 'AllowDrawing') return { ...current, AllowDrawing: checked }
                          if (row.key === 'AllowMCP') return { ...current, AllowMCP: checked }
                          if (row.key === 'AllowSkills') return { ...current, AllowSkills: checked }
                          if (row.key === 'AllowPrompts') return { ...current, AllowPrompts: checked }
                          if (row.key === 'AllowPrivateChat') return { ...current, AllowPrivateChat: checked }
                          if (row.key === 'AllowAiPPT') return { ...current, AllowAiPPT: checked }
                          if (row.key === 'AllowKnowledgeBases') return { ...current, AllowKnowledgeBases: checked }
                          return { ...current, AllowFileUpload: checked }
                        })
                      }}
                      aria-label={t(`workspace.policy.${row.key}.label`, { defaultValue: row.label })}
                    />
                  </label>
                )
              })}
            </div>
          </section>
        ))}
      </div>

      <div className="rounded-[10px] border border-[var(--color-border)] p-3">
        <div className="flex items-center justify-between gap-3">
          <span className="text-[13px] font-medium text-[var(--color-fg)]">
            {t('workspace.policy.modelAllowlist', { defaultValue: 'Allowed models' })}
          </span>
          <label className="flex items-center gap-2 text-[12px] text-[var(--color-fg-muted)]">
            {t('workspace.policy.allModels', { defaultValue: 'All models' })}
            <Switch
              checked={allModelsAllowed}
              disabled={saving || (allModelsAllowed && models.length === 0)}
              onCheckedChange={(checked) => {
                markPolicyDirty()
                setPolicy((current) => current ? {
                  ...current,
                  AllowedModelIDs: checked ? [] : models.map((model) => model.id),
                } : current)
              }}
              aria-label={t('workspace.policy.allModels', { defaultValue: 'All models' })}
            />
          </label>
        </div>
        {models.length === 0 ? (
          <p className="mt-1.5 text-[11.5px] text-[var(--color-fg-subtle)]">
            {t('workspace.policy.noModels', { defaultValue: 'No models available.' })}
          </p>
        ) : !allModelsAllowed ? (
          <div className="mt-2 max-h-40 space-y-1 overflow-y-auto scrollbar-thin">
            {models.map((model) => {
              const checked = policy.AllowedModelIDs.includes(model.id)
              return (
                <label key={model.id} className="flex min-h-9 cursor-pointer items-center gap-2.5 rounded-[7px] px-1.5 py-1.5 hover:bg-[var(--color-bg-muted)] max-sm:min-h-[var(--tap-min)]">
                  <input
                    type="checkbox"
                    checked={checked}
                    disabled={saving}
                    onChange={(e) => {
                      markPolicyDirty()
                      setPolicy((current) => {
                        if (!current) return current
                        const next = e.target.checked
                          ? [...current.AllowedModelIDs, model.id]
                          : current.AllowedModelIDs.filter((id) => id !== model.id)
                        return { ...current, AllowedModelIDs: next }
                      })
                    }}
                    className="size-3.5 accent-[var(--color-accent)]"
                  />
                  <span className="min-w-0 flex-1 truncate text-[12.5px] text-[var(--color-fg)]">{model.label || model.id}</span>
                </label>
              )
            })}
          </div>
        ) : (
          <p className="mt-1.5 text-[11.5px] text-[var(--color-fg-subtle)]">
            {t('workspace.policy.allModelsHint', { defaultValue: 'Every model the platform offers is usable in this workspace.' })}
          </p>
        )}
      </div>

      <div className="rounded-[10px] border border-[var(--color-border)] p-3">
        <label className="block text-[13px] font-medium text-[var(--color-fg)]">
          {t('workspace.policy.creditLimit', { defaultValue: 'Member monthly credit limit' })}
        </label>
        <p className="mt-0.5 text-[11.5px] text-[var(--color-fg-subtle)]">
          {t('workspace.policy.creditLimitHint', { defaultValue: '0 = unlimited. Members hitting the limit cannot start new turns in this workspace.' })}
        </p>
        <Input
          value={limitDraft}
          disabled={saving}
          onChange={(e) => {
            markPolicyDirty()
            setLimitDraft(e.target.value)
          }}
          type="number"
          min={0}
          step="0.01"
          className="mt-2 h-8 w-32 text-[12.5px]"
          aria-label={t('workspace.policy.creditLimit', { defaultValue: 'Member monthly credit limit' })}
        />
      </div>

      <div className="flex justify-end">
        <Button loading={saving} disabled={!policyDirty || policyConflict} onClick={() => void save()}>
          {t('common.save', { ns: 'common', defaultValue: 'Save' })}
        </Button>
      </div>
    </div>
  )
}

/** §workspace RBAC phase 3 — ownership transfer with typed-name confirmation. */
function WorkspaceTransferDialog({
  open,
  onOpenChange,
  workspaceID,
  workspaceName,
  members,
  onTransferred,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  workspaceID: string
  workspaceName: string
  members: ApiWorkspaceMember[]
  onTransferred: () => void
}) {
  const { t } = useTranslation('chat')
  const [targetID, setTargetID] = useState('')
  const [confirmName, setConfirmName] = useState('')
  const [busy, setBusy] = useState(false)
  const eligible = members.filter((m) => !m.is_owner)
  const target = eligible.find((m) => m.user_id === targetID)

  useEffect(() => {
    if (open) {
      setTargetID('')
      setConfirmName('')
    }
  }, [open])

  async function transfer() {
    if (busy || !targetID || confirmName !== workspaceName) return
    setBusy(true)
    try {
      await workspacesApi.transferOwnership(workspaceID, targetID)
      toast.success(t('workspace.transferDone', { defaultValue: 'Ownership transferred.' }))
      onOpenChange(false)
      onTransferred()
    } catch {
      toast.error(t('workspace.transferFailed', { defaultValue: 'Could not transfer ownership.' }))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next && !busy) onOpenChange(next) }}>
      <DialogContent size="sm" closeDisabled={busy}>
        <DialogHeader>
          <DialogTitle>{t('workspace.transfer', { defaultValue: 'Transfer ownership' })}</DialogTitle>
          <DialogDescription>
            {t('workspace.transferBody', { defaultValue: 'The receiver becomes the workspace owner and an admin. You keep the admin role but lose owner-exclusive operations.' })}
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-3">
          <div className="max-h-40 space-y-1 overflow-y-auto scrollbar-thin">
            {eligible.length === 0 ? (
              <p className="py-2 text-center text-[12.5px] text-[var(--color-fg-subtle)]">
                {t('workspace.transferNoCandidates', { defaultValue: 'No other members to transfer to.' })}
              </p>
            ) : eligible.map((m) => (
              <button
                key={m.user_id}
                type="button"
                disabled={busy}
                onClick={() => setTargetID(m.user_id)}
                className={`flex w-full items-center gap-2.5 rounded-[8px] border px-2 py-2 text-left interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:opacity-50 ${
                  targetID === m.user_id
                    ? 'border-[var(--color-accent)] bg-[var(--color-bg-muted)]'
                    : 'border-transparent hover:bg-[var(--color-bg-muted)]'
                }`}
              >
                <Check size={13} aria-hidden className={targetID === m.user_id ? 'opacity-100' : 'opacity-0'} />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[13px] font-medium text-[var(--color-fg)]">{m.name || m.email}</span>
                  <span className="block truncate text-[11px] text-[var(--color-fg-subtle)]">{m.role === 'admin' ? t('workspace.roleAdmin', { defaultValue: 'Admin' }) : m.role === 'guest' ? t('workspace.roleGuest', { defaultValue: 'Guest' }) : t('workspace.roleMember', { defaultValue: 'Member' })}</span>
                </span>
              </button>
            ))}
          </div>
          {target ? (
            <div>
              <p className="text-[12px] text-[var(--color-fg-muted)]">
                {t('workspace.transferConfirmHint', { defaultValue: 'Type the workspace name "{{name}}" to confirm.', name: workspaceName })}
              </p>
              <Input
                value={confirmName}
                disabled={busy}
                onChange={(e) => setConfirmName(e.target.value)}
                placeholder={workspaceName}
                aria-label={t('workspace.transferConfirmHint', { defaultValue: 'Type the workspace name to confirm' })}
                className="mt-1.5"
              />
            </div>
          ) : null}
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" disabled={busy} onClick={() => onOpenChange(false)}>
            {t('common.cancel', { ns: 'common', defaultValue: 'Cancel' })}
          </Button>
          <Button
            variant="destructive"
            loading={busy}
            disabled={!targetID || confirmName !== workspaceName}
            onClick={() => void transfer()}
          >
            {t('workspace.transferConfirm', { defaultValue: 'Transfer' })}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** §workspace RBAC phase 5 — admin audit trail viewer. */
function WorkspaceAuditPanel({ workspaceID }: { workspaceID: string }) {
  const { t } = useTranslation('chat')
  const [logs, setLogs] = useState<ApiWorkspaceAuditLog[]>([])
  const [loading, setLoading] = useState(true)
  const [failed, setFailed] = useState(false)
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    let current = true
    setLoading(true)
    setFailed(false)
    workspacesApi
      .audit(workspaceID, 100, 0)
      .then((r) => { if (current) setLogs(r.logs) })
      .catch(() => { if (current) { setLogs([]); setFailed(true) } })
      .finally(() => { if (current) setLoading(false) })
    return () => { current = false }
  }, [workspaceID, attempt])

  function actionLabel(action: string): string {
    const key = `workspace.audit.actions.${action}`
    const fallback = action
    return t(key, { defaultValue: fallback })
  }

  function describeTarget(log: ApiWorkspaceAuditLog): string {
    if (!log.target_type) return ''
    return t('workspace.audit.target', {
      type: log.target_type,
      defaultValue: '{{type}}',
    })
  }

  return (
    <div className="space-y-2">
      {loading ? (
        <div className="space-y-2 py-2">{[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-10 w-full" />)}</div>
      ) : failed ? (
        <div role="alert" className="flex flex-col items-center px-4 py-6 text-center">
          <AlertTriangle size={18} aria-hidden className="text-[var(--color-danger)]" />
          <p className="mt-2 text-[13px] text-[var(--color-fg-muted)]">
            {t('workspace.audit.loadFailed', { defaultValue: 'Could not load the audit log.' })}
          </p>
          <Button size="sm" variant="secondary" className="mt-3" leadingIcon={<RefreshCw size={13} aria-hidden />} onClick={() => setAttempt((v) => v + 1)}>
            {t('actions.tryAgain', { ns: 'common', defaultValue: 'Try again' })}
          </Button>
        </div>
      ) : logs.length === 0 ? (
        <p className="py-6 text-center text-[13px] text-[var(--color-fg-muted)]">
          {t('workspace.audit.empty', { defaultValue: 'No audit entries yet.' })}
        </p>
      ) : (
        <ul className="space-y-1">
          {logs.map((log) => (
            <li key={log.id} className="rounded-[8px] px-2 py-2 hover:bg-[var(--color-bg-muted)]">
              <div className="flex items-baseline justify-between gap-2">
                <span className="min-w-0 truncate text-[12.5px] font-medium text-[var(--color-fg)]">
                  {actionLabel(log.action)}
                </span>
                <span className="shrink-0 text-[10.5px] tabular-nums text-[var(--color-fg-subtle)]">
                  {new Date(log.created_at * 1000).toLocaleString()}
                </span>
              </div>
              <div className="mt-0.5 truncate text-[11px] text-[var(--color-fg-subtle)]">
                {log.actor_name || log.actor_user_id}
                {log.target_type ? <span className="mx-1">·</span> : null}
                {describeTarget(log)}
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
