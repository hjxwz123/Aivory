/**
 * AdminUsers — list users, create accounts, reset passwords, change roles, and
 * ban / unban (realtime via the cache kill channel). Each row links to the
 * per-user conversation drill-down used for support / abuse triage (§8.1).
 */
import { lazy, Suspense, useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { Brain, History, MessageSquare, Plus, Pencil, Trash2, Search, Info, Ban, ShieldCheck, MoreHorizontal, X } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiUser, ApiUserGroup } from '@/api/types'
import { AdminSortableList } from '@/components/admin/AdminSortableList'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Tooltip } from '@/components/ui/tooltip'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { SegmentedControl } from '@/components/ui/segmented-control'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { Pagination } from '@/components/ui/pagination'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { initials } from '@/components/ui/avatar.utils'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Sheet, SheetBody, SheetClose, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { toast } from '@/hooks/use-toast'
import { useAuth } from '@/store/auth'
import { formatDateTime, cn } from '@/lib/utils'
import { envNum } from '@/lib/env-config'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Skeleton } from '@/components/ui/skeleton'
import { AdminPageHeader } from '@/components/admin/admin-page-header'

// A user counts as online if they made an authenticated request in the last 5
// minutes (the middleware refreshes last_seen_at at most once/min).
const ONLINE_WINDOW_S = envNum('VITE_AIVORY_ONLINE_WINDOW_S', 300)

const AdminUserConversations = lazy(() => import('./AdminUserConversations'))
const AdminUserLoginHistory = lazy(() => import('./AdminUserLoginHistory'))
const AdminUserMemories = lazy(() => import('./AdminUserMemories'))

type Role = 'user' | 'admin'
type CreditOperation = 'add' | 'remove'
type UserActivityPanel = { user: ApiUser; kind: 'conversations' | 'login-history' | 'memories' }
const ACTIVITY_TITLE_KEYS = {
  conversations: 'admin:users.conversationsTitle',
  'login-history': 'admin:users.loginHistoryTitle',
  memories: 'admin:users.memoriesTitle',
} as const

export default function AdminUsers() {
  const { t } = useTranslation(['admin', 'common'])
  const me = useAuth((s) => s.user)
  const [rows, setRows] = useState<ApiUser[]>([])
  const [total, setTotal] = useState(0)
  const [groups, setGroups] = useState<ApiUserGroup[]>([])
  const [loading, setLoading] = useState(true)

  // New-user dialog
  const [createOpen, setCreateOpen] = useState(false)
  const [draft, setDraft] = useState<{ email: string; name: string; password: string; role: Role }>({
    email: '',
    name: '',
    password: '',
    role: 'user',
  })
  const [creating, setCreating] = useState(false)
  const creatingRef = useRef(false)

  // Edit-user dialog (email, role + reset password)
  const [editRow, setEditRow] = useState<ApiUser | null>(null)
  const [editEmail, setEditEmail] = useState('')
  const [editRole, setEditRole] = useState<Role>('user')
  const [editGroup, setEditGroup] = useState('')
  // Membership expiry as a yyyy-mm-dd date input value ('' = permanent).
  const [editExpiry, setEditExpiry] = useState('')
  const [editPassword, setEditPassword] = useState('')
  const [editCreditsOperation, setEditCreditsOperation] = useState<CreditOperation>('add')
  const [editCreditsAmount, setEditCreditsAmount] = useState('')
  const [editCreditsNotify, setEditCreditsNotify] = useState(false)
  const [editCreditsReason, setEditCreditsReason] = useState('')
  const [saving, setSaving] = useState(false)
  // Reset-2FA button inside the edit dialog — the dialog stays open after
  // success, so guard against re-clicks that would fire duplicate calls/toasts.
  const [resetting2fa, setResetting2fa] = useState(false)
  const resetting2faRef = useRef(false)
  // Read-only user drawers.
  const [activityPanel, setActivityPanel] = useState<UserActivityPanel | null>(null)
  const [infoRow, setInfoRow] = useState<ApiUser | null>(null)
  const [infoDetails, setInfoDetails] = useState<ApiUser | null>(null)
  const [infoLoading, setInfoLoading] = useState(false)
  const [infoLoadFailed, setInfoLoadFailed] = useState(false)
  const infoRequestRef = useRef(0)
  // Delete-user confirmation.
  const [deleteRow, setDeleteRow] = useState<ApiUser | null>(null)
  const [deleting, setDeleting] = useState(false)
  // Per-row ban/unban in-flight guard (a given row is either ban or unban, so a
  // single shared id suffices) — blocks double-submits and drives the spinner.
  const [busyId, setBusyId] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [committedQuery, setCommittedQuery] = useState('')
  const [page, setPage] = useState(1)
  const PAGE_SIZE = envNum('VITE_AIVORY_PAGE_SIZE_3', 20)
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const pageRows = rows

  // Debounce search: commit the query 400ms after the user stops typing.
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  function handleQueryChange(v: string) {
    setQuery(v)
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      setCommittedQuery(v)
      setPage(1)
    }, 400)
  }

  const groupsLoadedRef = useRef(false)

  const load = useCallback(async (search: string, p: number, opts?: { silent?: boolean }) => {
    if (!opts?.silent) setLoading(true)
    try {
      const offset = (p - 1) * PAGE_SIZE
      const activity = opts?.silent ? 'background' : 'foreground'
      if (!groupsLoadedRef.current) {
        const [resp, gs] = await Promise.all([adminApi.users(search, PAGE_SIZE, offset, activity), adminApi.userGroups()])
        setRows(resp.users)
        setTotal(resp.total)
        setGroups(gs)
        groupsLoadedRef.current = true
      } else {
        const resp = await adminApi.users(search, PAGE_SIZE, offset, activity)
        setRows(resp.users)
        setTotal(resp.total)
      }
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setLoading(false)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    void load(committedQuery, page)
  }, [committedQuery, page, load])

  useEffect(() => () => {
    infoRequestRef.current += 1
  }, [])

  // While any account is being purged in the background, refresh periodically
  // (silently — no loading flash) so the row disappears once the job completes,
  // and surface each job's progress text on the badge.
  const [deletionProgress, setDeletionProgress] = useState<Record<string, string>>({})
  const hasDeleting = rows.some((u) => u.status === 'deleting')
  useEffect(() => {
    if (!hasDeleting) return
    const tick = async () => {
      await load(committedQuery, page, { silent: true })
      try {
        const resp = await adminApi.userDeletions('background')
        setDeletionProgress(
          Object.fromEntries(resp.jobs.map((j) => [j.user_id, j.status === 'failed' ? `failed: ${j.error ?? ''}` : j.progress])),
        )
      } catch {
        /* polling is best-effort */
      }
    }
    const id = window.setInterval(() => void tick(), 4000)
    return () => window.clearInterval(id)
  }, [hasDeleting, committedQuery, page, load])

  async function reload() {
    await load(committedQuery, page)
  }

  function persistOrder(next: ApiUser[], prev: ApiUser[]) {
    void adminApi.reorderUsers(next.map((u) => u.id)).catch((e) => {
      setRows(prev)
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    })
  }

  async function ban(u: ApiUser) {
    if (busyId) return
    setBusyId(u.id)
    try {
      await adminApi.banUser(u.id)
      toast.success(t('admin:users.banned'))
      await reload()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setBusyId(null)
    }
  }
  async function unban(u: ApiUser) {
    if (busyId) return
    setBusyId(u.id)
    try {
      await adminApi.unbanUser(u.id)
      toast.success(t('admin:users.reinstated'))
      await reload()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setBusyId(null)
    }
  }
  async function remove() {
    if (!deleteRow) return
    setDeleting(true)
    try {
      await adminApi.deleteUser(deleteRow.id)
      toast.success(t('admin:users.deleteStarted', { defaultValue: 'Deletion started — cleaning up in the background' }))
      setDeleteRow(null)
      await reload()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setDeleting(false)
    }
  }

  function openCreate() {
    setDraft({ email: '', name: '', password: '', role: 'user' })
    setCreateOpen(true)
  }

  async function submitCreate() {
    if (creatingRef.current) return
    if (!draft.email.trim() || !draft.email.includes('@')) {
      toast.error(t('admin:users.errors.emailRequired'))
      return
    }
    if (draft.password.length < 8) {
      toast.error(t('admin:users.errors.passwordShort'))
      return
    }
    creatingRef.current = true
    setCreating(true)
    try {
      await adminApi.createUser({
        email: draft.email.trim(),
        name: draft.name.trim(),
        password: draft.password,
        role: draft.role,
      })
      toast.success(t('admin:users.created'))
      setCreateOpen(false)
      await reload()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      creatingRef.current = false
      setCreating(false)
    }
  }

  async function resetTwoFa() {
    if (!editRow) return
    if (resetting2faRef.current) return
    resetting2faRef.current = true
    setResetting2fa(true)
    try {
      await adminApi.disableUser2fa(editRow.id)
      setEditRow({ ...editRow, totp_enabled: false })
      toast.success(t('admin:users.twofaReset'))
      await reload()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      resetting2faRef.current = false
      setResetting2fa(false)
    }
  }

  function openEdit(u: ApiUser) {
    setEditRow(u)
    setEditEmail(u.email)
    setEditRole(u.role)
    setEditGroup(u.group_id || (groups.find((g) => g.is_default)?.id ?? ''))
    setEditExpiry(expiryToInput(u.group_expires_at ?? 0))
    setEditPassword('')
    setEditCreditsOperation('add')
    setEditCreditsAmount('')
    setEditCreditsNotify(false)
    setEditCreditsReason('')
  }

  function closeInfo() {
    infoRequestRef.current += 1
    setInfoRow(null)
    setInfoDetails(null)
    setInfoLoading(false)
    setInfoLoadFailed(false)
  }

  function openInfo(u: ApiUser) {
    setInfoRow(u)
    void loadInfo(u)
  }

  async function loadInfo(u: ApiUser, silent = false) {
    const requestID = ++infoRequestRef.current
    if (!silent) {
      setInfoDetails(null)
      setInfoLoading(true)
      setInfoLoadFailed(false)
    }
    try {
      const detail = await adminApi.user(u.id, silent ? 'background' : 'foreground')
      if (requestID !== infoRequestRef.current) return
      setInfoDetails(detail)
    } catch {
      if (requestID !== infoRequestRef.current) return
      if (!silent) setInfoLoadFailed(true)
    } finally {
      if (!silent && requestID === infoRequestRef.current) setInfoLoading(false)
    }
  }

  useEffect(() => {
    if (!infoRow) return
    const timer = window.setInterval(() => {
      void loadInfo(infoRow, true)
    }, 5000)
    return () => window.clearInterval(timer)
  }, [infoRow])

  async function submitEdit() {
    if (!editRow) return
    const normalizedEmail = editEmail.trim().toLowerCase()
    if (!normalizedEmail || !normalizedEmail.includes('@')) {
      toast.error(t('admin:users.errors.emailRequired'))
      return
    }
    if (editPassword && editPassword.length < 8) {
      toast.error(t('admin:users.errors.passwordShort'))
      return
    }
    const creditAmountText = editCreditsAmount.trim()
    const creditAmount = Number(creditAmountText)
    if (creditAmountText && (
      !Number.isFinite(creditAmount) || creditAmount <= 0 || Math.round(creditAmount * 1_000_000) <= 0
    )) {
      toast.error(t('admin:users.errors.creditAmountRequired'))
      return
    }
    if (editCreditsNotify && !creditAmountText) {
      toast.error(t('admin:users.errors.creditAmountRequired'))
      return
    }
    if (editCreditsNotify && !editCreditsReason.trim()) {
      toast.error(t('admin:users.errors.creditReasonRequired'))
      return
    }
    setSaving(true)
    try {
      if (normalizedEmail !== editRow.email.trim().toLowerCase()) {
        await adminApi.setUserEmail(editRow.id, normalizedEmail)
        toast.success(t('admin:users.emailChanged'))
      }
      if (editRole !== editRow.role) {
        await adminApi.setUserRole(editRow.id, editRole)
        toast.success(t('admin:users.roleChanged'))
      }
      const newExpiry = inputToExpiry(editExpiry)
      if (editGroup && (editGroup !== editRow.group_id || newExpiry !== (editRow.group_expires_at ?? 0))) {
        await adminApi.setUserGroup(editRow.id, editGroup, newExpiry)
        toast.success(t('admin:users.groupChanged'))
      }
      if (editPassword) {
        await adminApi.setUserPassword(editRow.id, editPassword)
        toast.success(t('admin:users.passwordSet'))
      }
      if (creditAmountText) {
        const adjustment = await adminApi.adjustUserCredits(editRow.id, {
          operation: editCreditsOperation,
          amount: creditAmount,
          notify_user: editCreditsNotify,
          reason: editCreditsNotify ? editCreditsReason.trim() : '',
        })
        toast.success(t(editCreditsOperation === 'add' ? 'admin:users.creditsAdded' : 'admin:users.creditsRemoved', {
          amount: formatCredits(adjustment.amount),
        }))
      }
      setEditRow(null)
      await reload()
    } catch (e) {
      if (e instanceof ApiError && e.message === 'email_already_registered') {
        toast.error(t('admin:users.errors.emailExists'))
      } else if (e instanceof ApiError && e.message === 'invalid_email') {
        toast.error(t('admin:users.errors.emailRequired'))
      } else if (e instanceof ApiError && e.message === 'insufficient permanent credits') {
        toast.error(t('admin:users.errors.insufficientPermanentCredits'))
      } else if (e instanceof ApiError && e.message === 'invalid credit notification') {
        toast.error(t('admin:users.errors.creditReasonRequired'))
      } else {
        toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
      }
    } finally {
      setSaving(false)
    }
  }

  return (
    <div>
      <AdminPageHeader
        title={t('admin:users.title')}
        description={t('admin:users.lead')}
        actions={(
          <Button
            size="sm"
            className="max-sm:min-h-[var(--tap-min)] max-sm:flex-1"
            leadingIcon={<Plus size={15} aria-hidden />}
            onClick={openCreate}
          >
            {t('admin:users.new')}
          </Button>
        )}
      />

      <div className="mt-5 sm:mt-6">
        <Input
          value={query}
          onChange={(e) => handleQueryChange(e.target.value)}
          leadingIcon={<Search size={16} aria-hidden />}
          placeholder={t('admin:users.searchPlaceholder')}
          wrapperClassName="h-11 w-full sm:h-10 sm:max-w-sm"
        />
      </div>

      <section className="mt-4 sm:mt-5">
        {loading ? (
          <PanelFallback />
        ) : (
          <AdminSortableList
            items={pageRows}
            onItemsChange={setRows}
            onOrderCommit={persistOrder}
            dragHandleLabel={t('admin:common.dragHandle')}
            moveUpLabel={t('admin:common.moveUp')}
            moveDownLabel={t('admin:common.moveDown')}
            tableLabel={t('admin:users.title')}
            columns={[
              { id: 'user', header: t('admin:users.fields.name'), width: 210, render: (u) => {
                const avatarUrl = (u.settings as Record<string, unknown> | undefined)?.avatar_url as string | undefined
                return (
                  <div className="flex min-w-0 items-center gap-2.5">
                    <Avatar size="md">
                      {avatarUrl ? <AvatarImage src={avatarUrl} alt={u.name || u.email} /> : null}
                      <AvatarFallback>{initials(u.name || u.email)}</AvatarFallback>
                    </Avatar>
                    <div className="min-w-0">
                      <span className="block truncate font-medium" title={u.name || u.email}>{u.name || u.email}</span>
                      {me?.id === u.id ? <Badge size="xs">{t('admin:users.you')}</Badge> : null}
                    </div>
                  </div>
                )
              } },
              { id: 'email', header: t('admin:users.fields.email'), width: 230, render: (u) => <span className="block truncate font-mono text-[12px] text-[var(--color-fg-muted)]" title={u.email}>{u.email}</span> },
              { id: 'role', header: t('admin:users.fields.role'), width: 90, render: (u) => <Badge size="xs">{t(`admin:users.role${u.role === 'admin' ? 'Admin' : 'User'}`)}</Badge> },
              { id: 'group', header: t('admin:users.fields.group'), width: 100, render: (u) => <span className="block truncate">{groups.find((g) => g.id === u.group_id)?.name ?? '—'}</span> },
              { id: 'status', header: t('admin:common.status'), width: 90, render: (u) => <Badge size="xs" variant={u.status === 'active' ? 'success' : u.status === 'deleting' ? 'warning' : 'neutral'} title={u.status === 'deleting' ? deletionProgress[u.id] : undefined}>{t(`admin:users.status.${u.status}`, { defaultValue: u.status })}</Badge> },
              { id: 'lastActive', header: t('admin:common.lastActive'), width: 170, render: (u) => {
                const lastSeen = u.last_seen_at ?? 0
                const online = lastSeen > 0 && Date.now() / 1000 - lastSeen < ONLINE_WINDOW_S
                return (
                  <div className="flex items-center gap-2 text-[12px] text-[var(--color-fg-muted)]">
                    <span aria-hidden className={cn('size-1.5 shrink-0 rounded-full', online ? 'bg-[var(--color-success)]' : 'bg-[var(--color-fg-faint)]')} />
                    <span className="tabular-nums">{online ? t('admin:users.online') : lastSeen > 0 ? formatDateTime(lastSeen * 1000) : t('admin:users.neverSeen')}</span>
                  </div>
                )
              } },
              { id: 'actions', header: t('admin:common.actions'), width: 144, align: 'right', render: (u) => {
                const isMe = me?.id === u.id
                return (
                  <div className="flex items-center justify-end gap-0.5">
                    <IconAction label={t('admin:users.viewInfo')} onClick={() => openInfo(u)}><Info size={15} aria-hidden /></IconAction>
                    <IconAction label={t('admin:common.edit')} onClick={() => openEdit(u)}><Pencil size={15} aria-hidden /></IconAction>
                    <DropdownMenu>
                      <Tooltip content={t('admin:users.more')}>
                        <DropdownMenuTrigger aria-label={t('admin:users.more')} className="inline-flex size-8 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]">
                          <MoreHorizontal size={15} aria-hidden />
                        </DropdownMenuTrigger>
                      </Tooltip>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem onSelect={() => setActivityPanel({ user: u, kind: 'conversations' })}><MessageSquare size={14} aria-hidden />{t('admin:users.viewConversations')}</DropdownMenuItem>
                        <DropdownMenuItem onSelect={() => setActivityPanel({ user: u, kind: 'memories' })}><Brain size={14} aria-hidden />{t('admin:users.viewMemories')}</DropdownMenuItem>
                        <DropdownMenuItem onSelect={() => setActivityPanel({ user: u, kind: 'login-history' })}><History size={14} aria-hidden />{t('admin:users.viewLoginHistory')}</DropdownMenuItem>
                        <DropdownMenuSeparator />
                        {u.status === 'active' ? (
                          <DropdownMenuItem disabled={isMe || busyId === u.id} onClick={() => void ban(u)}><Ban size={14} aria-hidden />{t('admin:users.ban')}</DropdownMenuItem>
                        ) : (
                          <DropdownMenuItem disabled={u.status === 'deleting' || busyId === u.id} onClick={() => void unban(u)}><ShieldCheck size={14} aria-hidden />{t('admin:users.unban')}</DropdownMenuItem>
                        )}
                        <DropdownMenuItem destructive disabled={isMe} onClick={() => setDeleteRow(u)}><Trash2 size={14} aria-hidden />{t('admin:common.delete')}</DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                )
              } },
            ]}
          />
        )}
        {!loading ? (
          <Pagination className="max-sm:[&_button]:size-11" page={page} pageCount={pageCount} onPage={setPage} />
        ) : null}
      </section>

      {/* New user */}
      <Dialog open={createOpen} onOpenChange={(next) => !creatingRef.current && setCreateOpen(next)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('admin:users.newTitle')}</DialogTitle>
            <DialogDescription>{t('admin:users.newLead')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <Field label={t('admin:users.fields.email')} htmlFor="u-email">
                <Input
                  id="u-email"
                  type="email"
                  value={draft.email}
                  onChange={(e) => setDraft({ ...draft, email: e.target.value })}
                  placeholder="user@example.com"
                />
              </Field>
              <Field label={t('admin:users.fields.name')} htmlFor="u-name">
                <Input
                  id="u-name"
                  value={draft.name}
                  onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                  placeholder="Astrid Holm"
                />
              </Field>
              <Field label={t('admin:users.fields.password')} htmlFor="u-pw" hint={t('admin:users.fields.passwordHint')}>
                <Input
                  id="u-pw"
                  type="password"
                  value={draft.password}
                  onChange={(e) => setDraft({ ...draft, password: e.target.value })}
                  placeholder="••••••••"
                />
              </Field>
              <Field label={t('admin:users.fields.role')} htmlFor="u-role">
                <Select value={draft.role} onValueChange={(v) => setDraft({ ...draft, role: v as Role })}>
                  <SelectTrigger id="u-role">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="user">{t('admin:users.roleUser')}</SelectItem>
                    <SelectItem value="admin">{t('admin:users.roleAdmin')}</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setCreateOpen(false)} disabled={creating}>
              {t('common:actions.cancel')}
            </Button>
            <Button loading={creating} onClick={() => void submitCreate()}>
              {t('admin:users.create')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Edit user — email, role + reset password */}
      <Dialog open={Boolean(editRow)} onOpenChange={(o) => !o && setEditRow(null)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{editRow ? t('admin:users.editTitle', { name: editRow.name || editRow.email }) : ''}</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <Field label={t('admin:users.fields.email')} htmlFor="e-email">
                <Input
                  id="e-email"
                  type="email"
                  value={editEmail}
                  onChange={(e) => setEditEmail(e.target.value)}
                  autoComplete="off"
                />
              </Field>
              <Field label={t('admin:users.fields.role')} htmlFor="e-role">
                <Select
                  value={editRole}
                  onValueChange={(v) => setEditRole(v as Role)}
                  disabled={Boolean(editRow && me?.id === editRow.id)}
                >
                  <SelectTrigger id="e-role">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="user">{t('admin:users.roleUser')}</SelectItem>
                    <SelectItem value="admin">{t('admin:users.roleAdmin')}</SelectItem>
                  </SelectContent>
                </Select>
                {editRow && me?.id === editRow.id ? (
                  <p className="mt-1.5 text-[12px] text-[var(--color-fg-subtle)]">{t('admin:users.selfRoleHint')}</p>
                ) : null}
              </Field>
              <Field label={t('admin:users.fields.group')} htmlFor="e-group" hint={t('admin:users.fields.groupHint')}>
                <Select value={editGroup} onValueChange={setEditGroup}>
                  <SelectTrigger id="e-group">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {groups.map((g) => (
                      <SelectItem key={g.id} value={g.id}>
                        {g.name}
                        {g.is_default ? ` · ${t('admin:groups.default')}` : ''}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field
                label={t('admin:users.fields.groupExpiry')}
                htmlFor="e-expiry"
                hint={t('admin:users.fields.groupExpiryHint')}
              >
                <Input
                  id="e-expiry"
                  type="date"
                  value={editExpiry}
                  onChange={(e) => setEditExpiry(e.target.value)}
                />
              </Field>
              {editRow?.totp_enabled ? (
                <Field label={t('admin:users.fields.twofa')} hint={t('admin:users.twofaHint')}>
                  <Button variant="secondary" loading={resetting2fa} onClick={() => void resetTwoFa()}>
                    {t('admin:users.twofaReset')}
                  </Button>
                </Field>
              ) : null}
              <Field
                label={t('admin:users.fields.newPassword')}
                htmlFor="e-pw"
                hint={t('admin:users.fields.passwordEditHint')}
              >
                <Input
                  id="e-pw"
                  type="password"
                  value={editPassword}
                  onChange={(e) => setEditPassword(e.target.value)}
                  placeholder="••••••••"
                  autoComplete="new-password"
                />
              </Field>
              <div className="flex flex-col gap-1.5">
                <p className="text-sm font-medium leading-tight text-[var(--color-fg)]">
                  {t('admin:users.fields.permanentCredits')}
                </p>
                <p className="text-xs text-[var(--color-fg-subtle)]">
                  {t('admin:users.fields.creditCurrent', {
                    amount: formatCredits(editRow?.credits_permanent ?? 0),
                  })}
                </p>
              </div>
              <Field label={t('admin:users.fields.creditOperation')}>
                <SegmentedControl
                  label={t('admin:users.fields.creditOperation')}
                  value={editCreditsOperation}
                  options={[
                    { value: 'add', label: t('admin:users.fields.creditAdd') },
                    { value: 'remove', label: t('admin:users.fields.creditRemove') },
                  ]}
                  onChange={setEditCreditsOperation}
                  fullWidthOnMobile
                />
              </Field>
              <Field
                label={t('admin:users.fields.creditAmount')}
                htmlFor="e-credits-amount"
                hint={t('admin:users.fields.creditAmountHint')}
              >
                <Input
                  id="e-credits-amount"
                  type="number"
                  min="0.000001"
                  step="any"
                  inputMode="decimal"
                  value={editCreditsAmount}
                  onChange={(e) => setEditCreditsAmount(e.target.value)}
                />
              </Field>
              <div className="flex items-center justify-between gap-4 pt-4">
                <label htmlFor="e-credits-notify" className="min-w-0 cursor-pointer">
                  <span className="block text-sm font-medium leading-tight text-[var(--color-fg)]">
                    {t('admin:users.fields.creditNotify')}
                  </span>
                  <span className="mt-1 block text-xs leading-relaxed text-[var(--color-fg-subtle)]">
                    {t('admin:users.fields.creditNotifyHint')}
                  </span>
                </label>
                <Switch
                  id="e-credits-notify"
                  checked={editCreditsNotify}
                  onCheckedChange={setEditCreditsNotify}
                  aria-label={t('admin:users.fields.creditNotify')}
                />
              </div>
              {editCreditsNotify ? (
                <Field
                  label={t('admin:users.fields.creditReason')}
                  htmlFor="e-credits-reason"
                  hint={t('admin:users.fields.creditReasonHint')}
                >
                  <Textarea
                    id="e-credits-reason"
                    rows={3}
                    maxLength={500}
                    value={editCreditsReason}
                    onChange={(e) => setEditCreditsReason(e.target.value)}
                  />
                </Field>
              ) : null}
            </div>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setEditRow(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button loading={saving} onClick={() => void submitEdit()}>
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Sheet open={Boolean(activityPanel)} onOpenChange={(open) => !open && setActivityPanel(null)}>
        <SheetContent side="right" size="lg" className="w-full max-w-[64rem] border-0 sm:w-[calc(100vw-3rem)]">
          <SheetHeader>
            <div className="flex min-w-0 items-start justify-between gap-4">
              <div className="min-w-0">
                <SheetTitle className="break-words">
                  {activityPanel ? t(ACTIVITY_TITLE_KEYS[activityPanel.kind], {
                    name: activityPanel.user.name || activityPanel.user.email,
                  }) : ''}
                </SheetTitle>
                <p className="mt-1 break-all text-[12px] text-[var(--color-fg-muted)]">
                  {activityPanel?.user.email}
                </p>
              </div>
              <SheetClose asChild>
                <Button variant="ghost" size="icon-sm" className="shrink-0" aria-label={t('common:actions.close')}>
                  <X size={16} aria-hidden />
                </Button>
              </SheetClose>
            </div>
          </SheetHeader>
          <SheetBody className="min-h-0 min-w-0 pb-5">
            <Suspense fallback={<PanelFallback />}>
              {activityPanel?.kind === 'conversations' ? <AdminUserConversations key={activityPanel.user.id} userId={activityPanel.user.id} embedded /> : null}
              {activityPanel?.kind === 'login-history' ? <AdminUserLoginHistory key={activityPanel.user.id} userId={activityPanel.user.id} embedded /> : null}
              {activityPanel?.kind === 'memories' ? <AdminUserMemories key={activityPanel.user.id} userId={activityPanel.user.id} embedded /> : null}
            </Suspense>
          </SheetBody>
        </SheetContent>
      </Sheet>

      {/* User info (read-only) */}
      <Sheet open={Boolean(infoRow)} onOpenChange={(o) => !o && closeInfo()}>
        <SheetContent side="right" size="lg">
          <SheetHeader>
            <div className="flex min-w-0 items-start justify-between gap-4">
              <div className="min-w-0">
                <SheetTitle className="truncate">
                  {infoDetails?.name || infoDetails?.email || infoRow?.name || infoRow?.email || ''}
                </SheetTitle>
                <p className="mt-1 truncate font-mono text-[12px] text-[var(--color-fg-muted)]">
                  {infoDetails?.email || infoRow?.email || ''}
                </p>
              </div>
              <SheetClose asChild>
                <Button variant="ghost" size="icon-sm" aria-label={t('common:actions.close', { defaultValue: 'Close' })}>
                  <X size={16} aria-hidden />
                </Button>
              </SheetClose>
            </div>
          </SheetHeader>
          <SheetBody className="pb-5">
            {infoLoading ? (
              <UserInfoSkeleton label={t('common:common.loading')} />
            ) : infoLoadFailed ? (
              <div className="flex min-h-40 flex-col items-center justify-center gap-3 text-center" role="alert">
                <p className="max-w-xs text-sm leading-relaxed text-[var(--color-fg-muted)]">
                  {t('admin:users.info.loadFailed')}
                </p>
                <Button variant="secondary" size="sm" onClick={() => infoRow && void loadInfo(infoRow)}>
                  {t('common:actions.tryAgain')}
                </Button>
              </div>
            ) : infoDetails ? (
              <dl className="grid grid-cols-[minmax(0,0.8fr)_minmax(0,1.2fr)] gap-x-4 gap-y-2.5 text-sm sm:gap-x-6">
                <InfoLine label={t('admin:users.fields.email')} value={infoDetails.email} mono />
                <InfoLine
                  label={t('admin:users.fields.role')}
                  value={t(`admin:users.role${infoDetails.role === 'admin' ? 'Admin' : 'User'}`)}
                />
                <InfoLine label={t('admin:users.info.status')} value={infoDetails.status} />
                <InfoLine
                  label={t('admin:users.fields.group')}
                  value={groups.find((g) => g.id === infoDetails.group_id)?.name ?? '—'}
                />
                <InfoLine
                  label={t('admin:users.info.expiry')}
                  value={
                    (infoDetails.group_expires_at ?? 0) > 0
                      ? formatDateTime((infoDetails.group_expires_at ?? 0) * 1000)
                      : t('admin:users.info.permanent')
                  }
                />
                <InfoLine
                  label={t('admin:users.fields.permanentCredits')}
                  value={formatCredits(infoDetails.credits_permanent ?? 0)}
                />
                <InfoLine
                  label={t('admin:users.info.allowance')}
                  value={
                    infoDetails.credits_timed
                      ? `${formatCredits(infoDetails.credits_timed.remaining)} / ${formatCredits(infoDetails.credits_timed.allowance)}`
                      : '—'
                  }
                />
                <InfoLine
                  label={t('admin:users.info.availableCredits')}
                  value={formatCredits(infoDetails.credits_available ?? 0)}
                />
                <InfoLine
                  label={t('admin:users.info.creditReset')}
                  value={
                    (infoDetails.credits_timed?.resets_at ?? 0) > 0
                      ? formatDateTime((infoDetails.credits_timed?.resets_at ?? 0) * 1000)
                      : '—'
                  }
                />
                <InfoLine
                  label={t('admin:users.info.twofa')}
                  value={infoDetails.totp_enabled ? t('admin:users.info.enabled') : t('admin:users.info.disabled')}
                />
                <InfoLine
                  label={t('admin:users.info.lastSeen')}
                  value={
                    infoDetails.last_seen_at || infoRow?.last_seen_at
                      ? formatDateTime((infoDetails.last_seen_at || infoRow?.last_seen_at || 0) * 1000)
                      : t('admin:users.neverSeen')
                  }
                />
                <InfoLine label={t('admin:users.info.created')} value={formatDateTime(infoDetails.created_at * 1000)} />
              </dl>
            ) : null}
          </SheetBody>
        </SheetContent>
      </Sheet>

      {/* Delete user confirmation */}
      <Dialog open={Boolean(deleteRow)} onOpenChange={(o) => !o && setDeleteRow(null)}>
        <DialogContent size="sm">
          <DialogHeader>
            {/* users.deleteTitle/deleteBody belong to the CONVERSATION delete
                dialog (AdminUserConversations, {{title}}) — reusing them here
                showed "删除对话? …{{title}}…" with a raw placeholder. */}
            <DialogTitle>{t('admin:users.deleteUserTitle', { defaultValue: 'Delete this user?' })}</DialogTitle>
            <DialogDescription>
              {t('admin:users.deleteUserBody', {
                defaultValue:
                  "Permanently deletes {{name}}'s account and all of their data (conversations, files, knowledge bases, usage records). Deletion runs in the background and cannot be undone.",
                name: deleteRow?.name || deleteRow?.email || '',
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleteRow(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" loading={deleting} onClick={() => void remove()}>
              {t('admin:common.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

// IconAction — a compact, label-only-on-hover icon button (or link) for the
// user row. Tooltip carries the accessible name so the rail stays uncluttered.
function IconAction({
  label,
  onClick,
  href,
  disabled,
  danger,
  loading,
  children,
}: {
  label: string
  onClick?: () => void
  href?: string
  disabled?: boolean
  danger?: boolean
  loading?: boolean
  children: ReactNode
}) {
  const cls = cn(
    'inline-flex items-center justify-center size-8 rounded-[8px] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:opacity-40 disabled:cursor-not-allowed',
    danger
      ? 'text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)]'
      : 'text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
  )
  const spinner = (
    <span
      className="inline-block size-3.5 rounded-full border-2 border-current border-r-transparent animate-[spin_700ms_linear_infinite]"
      aria-hidden
    />
  )
  return (
    <Tooltip content={label}>
      {href ? (
        <Link to={href} aria-label={label} className={cls}>
          {children}
        </Link>
      ) : (
        <button
          type="button"
          aria-label={label}
          aria-busy={loading || undefined}
          onClick={onClick}
          disabled={disabled || loading}
          className={cls}
        >
          {loading ? spinner : children}
        </button>
      )}
    </Tooltip>
  )
}

function InfoLine({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <>
      <dt className="text-[var(--color-fg-subtle)]">{label}</dt>
      <dd className={cn('min-w-0 break-words text-right text-[var(--color-fg)]', mono && 'font-mono text-[12.5px]')}>{value}</dd>
    </>
  )
}

function UserInfoSkeleton({ label }: { label: string }) {
  return (
    <div className="space-y-2.5 py-0.5" role="status" aria-label={label}>
      {Array.from({ length: 10 }, (_, index) => (
        <div key={index} className="flex h-4 items-center justify-between gap-6" aria-hidden>
          <Skeleton shape="line" className="h-3 w-20 sm:w-24" />
          <Skeleton shape="line" className="h-3 w-28 sm:w-36" />
        </div>
      ))}
    </div>
  )
}

function formatCredits(value: number): string {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 6 }).format(value)
}

// Membership expiry conversions between the API's unix seconds and the
// <input type="date"> yyyy-mm-dd value. '' / 0 means permanent.
function expiryToInput(sec: number): string {
  if (!sec || sec <= 0) return ''
  const d = new Date(sec * 1000)
  if (Number.isNaN(d.getTime())) return ''
  const z = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${z(d.getMonth() + 1)}-${z(d.getDate())}`
}
function inputToExpiry(s: string): number {
  if (!s) return 0
  const ms = Date.parse(`${s}T23:59:59`)
  return Number.isNaN(ms) ? 0 : Math.floor(ms / 1000)
}
