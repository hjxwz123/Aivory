import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, ArrowLeft, Globe, LockKeyhole, Plus, Search, Trash2, UserPlus, Users } from 'lucide-react'
import { adminApi, workspacesApi } from '@/api'
import { domainsApi, type DomainUser, type DomainUserCandidate, type RegistrationDomain } from '@/api/domains'
import type { ApiUserGroup, ApiWorkspace } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Switch } from '@/components/ui/switch'
import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/checkbox'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogBody, DialogFooter } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/hooks/use-toast'

function matchedDomains(rule: RegistrationDomain): string[] {
  return rule.domains?.length ? rule.domains : [rule.domain]
}

function domainRuleLabel(rule: RegistrationDomain): string {
  const domains = matchedDomains(rule)
  return domains.length > 2 ? `${domains[0]} +${domains.length - 1}` : domains.join(', ')
}

function parseDomains(value: string): string[] {
  const seen = new Set<string>()
  return value
    .split(/[\n,]+/)
    .map((domain) => domain.trim().toLowerCase())
    .filter((domain) => {
      if (!domain || seen.has(domain)) return false
      seen.add(domain)
      return true
    })
}

export default function AdminDomains() {
  const { t } = useTranslation('admin')
  const [rows, setRows] = useState<RegistrationDomain[]>([])
  const [workspaces, setWorkspaces] = useState<ApiWorkspace[]>([])
  const [groups, setGroups] = useState<ApiUserGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [editor, setEditor] = useState<RegistrationDomain | 'new' | null>(null)
  const [members, setMembers] = useState<RegistrationDomain | null>(null)
  const [removing, setRemoving] = useState<RegistrationDomain | null>(null)
  const [busy, setBusy] = useState(false)
  const mutation = useRef(false)

  async function load() {
    setLoading(true)
    setError('')
    try {
      const [domains, spaces, userGroups] = await Promise.all([domainsApi.list(), workspacesApi.adminList(), adminApi.userGroups()])
      setRows(domains.domains)
      setWorkspaces(spaces.workspaces)
      setGroups(userGroups)
    } catch (e) { setError(e instanceof Error ? e.message : t('domains.loadFailed')) }
    finally { setLoading(false) }
  }
  useEffect(() => { void load() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  async function remove() {
    if (!removing || mutation.current) return
    mutation.current = true
    setBusy(true)
    try {
      await domainsApi.remove(removing.domain)
      setRows((current) => current.filter((r) => r.domain !== removing.domain))
      setRemoving(null)
      toast.success(t('domains.saved'))
    } catch (e) { toast.error(e instanceof Error ? e.message : t('domains.saveFailed')) }
    finally { mutation.current = false; setBusy(false) }
  }

  return (
    <section>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="font-serif text-2xl text-[var(--color-fg)] sm:text-3xl">{t('domains.title')}</h1>
          <p className="mt-1 max-w-2xl text-sm text-[var(--color-fg-muted)]">{t('domains.subtitle')}</p>
        </div>
        <Button disabled={loading || !!error || !workspaces.length} onClick={() => setEditor('new')}><Plus size={15} aria-hidden />{t('domains.add')}</Button>
      </div>
      <p className="mt-5 max-w-3xl text-sm leading-relaxed text-[var(--color-fg-muted)]">{t('domains.scopeHint')}</p>
      {loading ? <PanelFallback /> : error ? (
        <div role="alert" className="mt-8 space-y-3"><p>{error}</p><Button variant="secondary" onClick={() => void load()}>{t('domains.retry')}</Button></div>
      ) : !rows.length ? (
        <div className="py-14 text-center">
          <Globe size={24} className="mx-auto text-[var(--color-fg-muted)]" aria-hidden />
          <p className="mt-3 font-medium">{t('domains.empty')}</p>
          <p className="mx-auto mt-2 max-w-lg text-sm text-[var(--color-fg-muted)]">{t('domains.emptyHint')}</p>
          {!workspaces.length && <Button variant="secondary" className="mt-4" asChild><Link to="/admin/workspaces">{t('domains.createWorkspace')}</Link></Button>}
        </div>
      ) : (
        <>
        <div className="mt-6 hidden overflow-x-auto rounded-[12px] border border-[var(--color-border)] md:block">
          <table className="w-full min-w-[680px] text-left text-sm">
            <thead className="border-b border-[var(--color-divider)] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
              <tr>{['domain', 'workspace', 'enrollment', 'access', 'members', 'actions'].map((key) => <th key={key} scope="col" className="px-4 py-3 font-medium">{t(`domains.${key}`)}</th>)}</tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.domain} className="border-b border-[var(--color-divider)] last:border-0">
                  <td className="px-4 py-3 font-medium"><div className="flex max-w-64 flex-wrap gap-x-2 gap-y-1">{matchedDomains(row).map((domain) => <span key={domain} className="break-all">{domain}</span>)}</div></td>
                  <td className="max-w-56 break-words px-4 py-3">{row.workspace_name}</td>
                  <td className="px-4 py-3"><div className="flex flex-col items-start gap-1.5">
                    <Badge variant={row.enabled ? 'success' : 'neutral'}>{t(row.enabled ? 'domains.enabled' : 'domains.paused')}</Badge>
                    <span className="text-xs text-[var(--color-fg-muted)]">{t(row.email_verification_required ? 'domains.verificationRequired' : 'domains.verificationOptional')}</span>
                    <span className="text-xs text-[var(--color-fg-muted)]">{t('domains.initialGroupSummary', { group: row.initial_group_name || t('domains.systemDefaultGroup') })}</span>
                  </div></td>
                  <td className="px-4 py-3"><span className="inline-flex items-center gap-1.5">{row.lock_personal && <LockKeyhole size={14} aria-hidden />}{t(row.lock_personal ? 'domains.locked' : 'domains.unlocked')}</span></td>
                  <td className="px-4 py-3 tabular-nums">{row.member_count}</td>
                  <td className="px-4 py-3"><div className="flex gap-1">
                    <Button size="sm" variant="ghost" onClick={() => setMembers(row)} aria-label={`${t('domains.members')}: ${row.domain}`}><Users size={14} aria-hidden />{t('domains.members')}</Button>
                    <Button size="sm" variant="ghost" onClick={() => setEditor(row)}>{t('domains.edit')}</Button>
                    <Button size="sm" variant="ghost" onClick={() => setRemoving(row)}>{t('domains.remove')}</Button>
                  </div></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <ul className="mt-6 divide-y divide-[var(--color-divider)] rounded-[12px] border border-[var(--color-border)] md:hidden">
          {rows.map((row) => (
            <li key={row.domain} className="min-w-0 p-4">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0"><div className="flex flex-wrap gap-x-2 gap-y-1">{matchedDomains(row).map((domain) => <span key={domain} className="break-all text-sm font-medium">{domain}</span>)}</div><p className="mt-1 break-words text-sm text-[var(--color-fg-muted)]">{row.workspace_name}</p></div>
                <Badge className="shrink-0" variant={row.enabled ? 'success' : 'neutral'}>{t(row.enabled ? 'domains.enabled' : 'domains.paused')}</Badge>
              </div>
              <p className="mt-3 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-sm text-[var(--color-fg-muted)]">{row.lock_personal && <LockKeyhole size={14} aria-hidden />}{t(row.lock_personal ? 'domains.locked' : 'domains.unlocked')} · {t(row.email_verification_required ? 'domains.verificationRequired' : 'domains.verificationOptional')} · {t('domains.initialGroupSummary', { group: row.initial_group_name || t('domains.systemDefaultGroup') })} · {row.member_count} {t('domains.members')}</p>
              <div className="mt-3 flex flex-wrap gap-1">
                <Button size="sm" variant="secondary" onClick={() => setMembers(row)} aria-label={`${t('domains.members')}: ${row.domain}`}><Users size={14} aria-hidden />{t('domains.members')}</Button>
                <Button size="sm" variant="ghost" onClick={() => setEditor(row)}>{t('domains.edit')}</Button>
                <Button size="sm" variant="ghost" onClick={() => setRemoving(row)}>{t('domains.remove')}</Button>
              </div>
            </li>
          ))}
        </ul>
        </>
      )}
      {editor && <DomainEditor key={typeof editor === 'string' ? 'new' : editor.domain} rule={editor} workspaces={workspaces} groups={groups} onClose={() => setEditor(null)} onSaved={() => { setEditor(null); void load() }} />}
      {members && <DomainMembers rule={members} onClose={() => setMembers(null)} onChanged={() => void load()} />}
      <Dialog open={!!removing} onOpenChange={(open) => { if (!open && !busy) setRemoving(null) }}>
        <DialogContent className="max-w-md">
          <DialogHeader><DialogTitle>{t('domains.removeTitle', { domain: removing ? domainRuleLabel(removing) : '' })}</DialogTitle><DialogDescription>{t('domains.removeHint')}</DialogDescription></DialogHeader>
          <DialogFooter><Button variant="ghost" disabled={busy} onClick={() => setRemoving(null)}>{t('domains.cancel')}</Button><Button variant="destructive" loading={busy} onClick={() => void remove()}>{t('domains.remove')}</Button></DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  )
}

function DomainEditor({ rule, workspaces, groups, onClose, onSaved }: { rule: RegistrationDomain | 'new'; workspaces: ApiWorkspace[]; groups: ApiUserGroup[]; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation('admin')
  const isNew = rule === 'new'
  const [domainsText, setDomainsText] = useState(isNew ? '' : matchedDomains(rule).join('\n'))
  const [workspace, setWorkspace] = useState(isNew ? '' : rule.workspace_id)
  const [locked, setLocked] = useState(isNew ? false : rule.lock_personal)
  const [verifyEmail, setVerifyEmail] = useState(isNew ? true : rule.email_verification_required)
  const [initialGroup, setInitialGroup] = useState(isNew ? '' : rule.initial_group_id)
  const [enabled, setEnabled] = useState(isNew ? true : rule.enabled)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const mutation = useRef(false)
  const domains = parseDomains(domainsText)
  const canSave = domains.length > 0 && domains.length <= 50 && !!workspace
  async function save() {
    if (mutation.current || !canSave) return
    mutation.current = true; setBusy(true); setError('')
    try {
      const body = { domain: isNew ? domains[0] : rule.domain, domains, workspace_id: workspace, lock_personal: locked, email_verification_required: verifyEmail, initial_group_id: initialGroup, enabled: isNew ? true : enabled }
      if (isNew) await domainsApi.create(body)
      else await domainsApi.update({ ...rule, ...body })
      toast.success(t('domains.saved')); onSaved()
    } catch (e) { setError(e instanceof Error ? e.message : t('domains.saveFailed')) }
    finally { mutation.current = false; setBusy(false) }
  }
  return (
    <Dialog open onOpenChange={(open) => { if (!open && !busy) onClose() }}>
      <DialogContent className="h-[min(48rem,calc(100dvh-2rem))] overflow-hidden">
        <DialogHeader><DialogTitle>{t(isNew ? 'domains.add' : 'domains.edit')}</DialogTitle><DialogDescription>{t('domains.editorHint')}</DialogDescription></DialogHeader>
        <form className="flex min-h-0 flex-1 flex-col overflow-hidden" onSubmit={(e) => { e.preventDefault(); void save() }}>
          <DialogBody className="space-y-5 overscroll-contain">
            <aside aria-labelledby="domain-permissions-title" className="flex items-start gap-3 rounded-xl border border-[var(--color-warning)]/40 bg-[var(--color-warning)]/10 p-4">
              <AlertTriangle size={20} aria-hidden className="mt-0.5 shrink-0 text-[var(--color-warning)]" />
              <div className="min-w-0 space-y-1.5 text-sm leading-6">
                <p id="domain-permissions-title" className="font-semibold text-[var(--color-fg)]">{t('domains.permissionsNoticeTitle')}</p>
                <p className="text-[var(--color-fg)]">{t('domains.permissionsNotice')}</p>
                <Link to="/admin/user-groups" className="inline-flex font-medium text-[var(--color-fg)] underline underline-offset-4 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4">{t('domains.configureGroups')}</Link>
              </div>
            </aside>
            <div className="space-y-2"><label htmlFor="domain-names" className="text-sm font-medium">{t('domains.domain')}</label><Textarea id="domain-names" autoFocus disabled={busy} rows={4} value={domainsText} onChange={(e) => setDomainsText(e.target.value)} placeholder={'example.com\nexample.org'} maxLength={12699} required aria-describedby="domain-names-hint" /><p id="domain-names-hint" className="text-sm text-[var(--color-fg-muted)]">{t('domains.domainsHint')}</p></div>
            <div className="space-y-2"><label id="domain-workspace-label" className="text-sm font-medium">{t('domains.workspace')}</label>
              <Select value={workspace} onValueChange={setWorkspace} disabled={!isNew || busy}><SelectTrigger aria-labelledby="domain-workspace-label"><SelectValue placeholder={t('domains.chooseWorkspace')} /></SelectTrigger><SelectContent>{workspaces.map((w) => <SelectItem key={w.id} value={w.id}>{w.name}</SelectItem>)}</SelectContent></Select>
            </div>
            {isNew ? (
              <p className="text-sm leading-6 text-[var(--color-fg-muted)]">{t('domains.startsEnabledHint')}</p>
            ) : (
              <div className="flex items-start justify-between gap-4"><div><label htmlFor="domain-stopped" className="text-sm font-medium">{t('domains.stopEnrollment')}</label><p className="mt-1 text-sm text-[var(--color-fg-muted)]">{t('domains.stopEnrollmentHint')}</p></div><Switch id="domain-stopped" checked={!enabled} onCheckedChange={(stopped) => setEnabled(!stopped)} disabled={busy} /></div>
            )}
            <div className="flex items-start justify-between gap-4"><div><label htmlFor="domain-verification" className="text-sm font-medium">{t('domains.verifyEmailLabel')}</label><p className="mt-1 text-sm text-[var(--color-fg-muted)]">{t('domains.verifyEmailHint')}</p></div><Switch id="domain-verification" checked={verifyEmail} onCheckedChange={setVerifyEmail} disabled={busy} /></div>
            <div className="space-y-2"><label id="domain-group-label" className="text-sm font-medium">{t('domains.initialGroupLabel')}</label><p className="text-sm text-[var(--color-fg-muted)]">{t('domains.initialGroupHint')}</p>
              <Select value={initialGroup || '__system_default__'} onValueChange={(value) => setInitialGroup(value === '__system_default__' ? '' : value)} disabled={busy}><SelectTrigger aria-labelledby="domain-group-label"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="__system_default__">{t('domains.systemDefaultGroup')}</SelectItem>{groups.filter((group) => !group.is_default || group.id === initialGroup).map((group) => <SelectItem key={group.id} value={group.id}>{group.name}</SelectItem>)}</SelectContent></Select>
            </div>
            <div className="flex items-start justify-between gap-4"><div><label htmlFor="domain-lock" className="text-sm font-medium">{t('domains.lockLabel')}</label><p className="mt-1 text-sm text-[var(--color-fg-muted)]">{t('domains.lockHint')}</p></div><Switch id="domain-lock" checked={locked} onCheckedChange={setLocked} disabled={busy} /></div>
            {error && <p role="alert" className="text-sm text-[var(--color-danger)]">{error}</p>}
          </DialogBody>
          <DialogFooter><Button type="button" variant="ghost" disabled={busy} onClick={onClose}>{t('domains.cancel')}</Button><Button type="submit" loading={busy} disabled={!canSave}>{t('domains.save')}</Button></DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function DomainMembers({ rule, onClose, onChanged }: { rule: RegistrationDomain; onClose: () => void; onChanged: () => void }) {
  const { t } = useTranslation('admin')
  const [view, setView] = useState<'members' | 'candidates'>('members')
  const [users, setUsers] = useState<DomainUser[]>([])
  const [candidates, setCandidates] = useState<DomainUserCandidate[]>([])
  const [candidateSearch, setCandidateSearch] = useState('')
  const [candidateSearchDebounced, setCandidateSearchDebounced] = useState('')
  const [selected, setSelected] = useState<Set<string>>(() => new Set())
  const [removingUser, setRemovingUser] = useState<DomainUser | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [memberAttempt, setMemberAttempt] = useState(0)
  const [candidateAttempt, setCandidateAttempt] = useState(0)
  const [busy, setBusy] = useState(false)
  const mutation = useRef(false)
  useEffect(() => {
    const timer = window.setTimeout(() => setCandidateSearchDebounced(candidateSearch.trim()), 300)
    return () => window.clearTimeout(timer)
  }, [candidateSearch])
  useEffect(() => {
    if (view !== 'members') return
    let active = true
    setLoading(true); setError('')
    domainsApi.users(rule.domain).then((r) => { if (active) setUsers(r.users) }).catch((e) => { if (active) setError(e instanceof Error ? e.message : t('domains.loadFailed')) }).finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [rule.domain, memberAttempt, t, view])
  useEffect(() => {
    if (view !== 'candidates') return
    let active = true
    setLoading(true); setError('')
    domainsApi.candidates(rule.domain, candidateSearchDebounced).then((r) => { if (active) setCandidates(r.users) }).catch((e) => { if (active) setError(e instanceof Error ? e.message : t('domains.loadCandidatesFailed')) }).finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [candidateAttempt, candidateSearchDebounced, rule.domain, t, view])
  async function change(user: DomainUser, value: string) {
    if (mutation.current) return
    mutation.current = true; setBusy(true)
    const override = value === 'inherit' ? null : value === 'locked'
    try {
      await domainsApi.updateUser(rule.domain, user.user_id, override)
      setUsers((current) => current.map((u) => u.user_id === user.user_id ? { ...u, lock_override: override, locked: override ?? rule.lock_personal } : u))
      toast.success(t('domains.saved'))
    } catch (e) { toast.error(e instanceof Error ? e.message : t('domains.saveFailed')) }
    finally { mutation.current = false; setBusy(false) }
  }
  async function enrollSelected() {
    if (mutation.current || selected.size === 0) return
    mutation.current = true; setBusy(true)
    try {
      const result = await domainsApi.enrollUsers(rule.domain, [...selected])
      toast.success(t('domains.existingUsersAdded', { count: result.added }))
      setSelected(new Set())
      setView('members')
      setMemberAttempt((value) => value + 1)
      setCandidateAttempt((value) => value + 1)
      onChanged()
    } catch (e) { toast.error(e instanceof Error ? e.message : t('domains.addExistingFailed')) }
    finally { mutation.current = false; setBusy(false) }
  }
  async function removeUser() {
    if (!removingUser || mutation.current) return
    mutation.current = true; setBusy(true)
    try {
      await domainsApi.removeUser(rule.domain, removingUser.user_id)
      setUsers((current) => current.filter((user) => user.user_id !== removingUser.user_id))
      setRemovingUser(null)
      onChanged()
      toast.success(t('domains.memberRemoved'))
    } catch (e) { toast.error(e instanceof Error ? e.message : t('domains.removeMemberFailed')) }
    finally { mutation.current = false; setBusy(false) }
  }
  function toggleCandidate(userId: string, checked: boolean) {
    setSelected((current) => {
      const next = new Set(current)
      if (checked) next.add(userId)
      else next.delete(userId)
      return next
    })
  }
  const allSelected = candidates.length > 0 && candidates.every((candidate) => selected.has(candidate.user_id))
  return <>
    <Dialog open onOpenChange={(open) => { if (!open && !busy) onClose() }}>
      <DialogContent className="max-w-2xl">
        <DialogHeader><DialogTitle>{domainRuleLabel(rule)} · {t(view === 'members' ? 'domains.members' : 'domains.addExistingTitle')}</DialogTitle><DialogDescription>{t(view === 'members' ? 'domains.memberHint' : 'domains.addExistingHint')}</DialogDescription></DialogHeader>
        <DialogBody>
          {view === 'candidates' ? <Input wrapperClassName="mb-4 w-full" value={candidateSearch} onChange={(event) => setCandidateSearch(event.target.value)} leadingIcon={<Search size={15} aria-hidden />} placeholder={t('domains.searchUsers')} aria-label={t('domains.searchUsers')} disabled={busy} /> : null}
          {loading ? <PanelFallback /> : error ? <div role="alert"><p>{error}</p><Button className="mt-3" onClick={() => view === 'members' ? setMemberAttempt((a) => a + 1) : setCandidateAttempt((a) => a + 1)}>{t('domains.retry')}</Button></div> : view === 'members' ? !users.length ? <p className="py-8 text-sm text-[var(--color-fg-muted)]">{t('domains.noMembers')}</p> : (
            <ul className="divide-y divide-[var(--color-divider)]">
              {users.map((user) => <li key={user.user_id} className="flex flex-col gap-3 py-4 sm:flex-row sm:items-center sm:justify-between">
                <div className="min-w-0"><p className="truncate text-sm font-medium">{user.name}</p><p className="truncate text-sm text-[var(--color-fg-muted)]">{user.email}</p><p className="mt-1 text-xs text-[var(--color-fg-muted)]">{t(user.locked ? 'domains.locked' : 'domains.unlocked')}</p></div>
                <div className="flex items-center gap-2"><Select disabled={busy} value={user.lock_override === null ? 'inherit' : user.lock_override ? 'locked' : 'unlocked'} onValueChange={(v) => void change(user, v)}><SelectTrigger className="min-w-0 flex-1 sm:w-52" aria-label={`${t('domains.access')}: ${user.email}`}><SelectValue /></SelectTrigger><SelectContent><SelectItem value="inherit">{t('domains.inherit')}</SelectItem><SelectItem value="locked">{t('domains.locked')}</SelectItem><SelectItem value="unlocked">{t('domains.unlocked')}</SelectItem></SelectContent></Select><Button size="icon" variant="ghost" disabled={busy} onClick={() => setRemovingUser(user)} aria-label={`${t('domains.removeMember')}: ${user.email}`} title={t('domains.removeMember')}><Trash2 size={16} aria-hidden /></Button></div>
              </li>)}
            </ul>
          ) : !candidates.length ? <p className="py-8 text-sm text-[var(--color-fg-muted)]">{t('domains.noCandidates')}</p> : (
            <div>
              <label className="flex min-h-10 cursor-pointer items-center gap-3 border-b border-[var(--color-divider)] pb-3 text-sm font-medium">
                <Checkbox checked={allSelected} onChange={(event) => setSelected(event.target.checked ? new Set(candidates.map((candidate) => candidate.user_id)) : new Set())} disabled={busy} />
                {t('domains.selectAllCandidates', { count: candidates.length })}
              </label>
              <ul className="divide-y divide-[var(--color-divider)]">
                {candidates.map((candidate) => (
                  <li key={candidate.user_id}>
                    <label className="flex min-h-16 cursor-pointer items-start gap-3 py-3">
                      <Checkbox className="mt-0.5" checked={selected.has(candidate.user_id)} onChange={(event) => toggleCandidate(candidate.user_id, event.target.checked)} disabled={busy} />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-sm font-medium text-[var(--color-fg)]">{candidate.name || candidate.email}</span>
                        <span className="block truncate text-sm text-[var(--color-fg-muted)]">{candidate.email}</span>
                        <span className="mt-1 block text-xs text-[var(--color-fg-subtle)]">{t('domains.personalConversationCount', { count: candidate.personal_conversation_count })}</span>
                      </span>
                      {candidate.status === 'pending' ? <Badge variant="warning">{t('domains.pendingVerification')}</Badge> : null}
                    </label>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </DialogBody>
        <DialogFooter>
          {view === 'members' ? (
            <><Button variant="secondary" leadingIcon={<UserPlus size={15} aria-hidden />} disabled={busy} onClick={() => setView('candidates')}>{t('domains.addExisting')}</Button><Button variant="ghost" disabled={busy} onClick={onClose}>{t('domains.close')}</Button></>
          ) : (
            <><Button variant="ghost" leadingIcon={<ArrowLeft size={15} aria-hidden />} disabled={busy} onClick={() => setView('members')}>{t('domains.backToMembers')}</Button><Button leadingIcon={<UserPlus size={15} aria-hidden />} loading={busy} disabled={selected.size === 0} onClick={() => void enrollSelected()}>{t('domains.addSelected', { count: selected.size })}</Button></>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
    <Dialog open={!!removingUser} onOpenChange={(open) => { if (!open && !busy) setRemovingUser(null) }}>
      <DialogContent className="max-w-md">
        <DialogHeader><DialogTitle>{t('domains.removeMemberTitle', { user: removingUser?.name || removingUser?.email })}</DialogTitle><DialogDescription>{t('domains.removeMemberHint')}</DialogDescription></DialogHeader>
        <DialogFooter><Button variant="ghost" disabled={busy} onClick={() => setRemovingUser(null)}>{t('domains.cancel')}</Button><Button variant="destructive" loading={busy} onClick={() => void removeUser()}>{t('domains.removeMember')}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  </>
}
