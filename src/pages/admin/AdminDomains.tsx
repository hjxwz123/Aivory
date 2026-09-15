import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Globe, LockKeyhole, Plus, Users } from 'lucide-react'
import { workspacesApi } from '@/api'
import { domainsApi, type DomainUser, type RegistrationDomain } from '@/api/domains'
import type { ApiWorkspace } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Badge } from '@/components/ui/badge'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogBody, DialogFooter } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/hooks/use-toast'

export default function AdminDomains() {
  const { t } = useTranslation('admin')
  const [rows, setRows] = useState<RegistrationDomain[]>([])
  const [workspaces, setWorkspaces] = useState<ApiWorkspace[]>([])
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
      const [domains, spaces] = await Promise.all([domainsApi.list(), workspacesApi.adminList()])
      setRows(domains.domains)
      setWorkspaces(spaces.workspaces)
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
                  <td className="px-4 py-3 font-medium">{row.domain}</td>
                  <td className="max-w-56 break-words px-4 py-3">{row.workspace_name}</td>
                  <td className="px-4 py-3"><Badge variant={row.enabled ? 'success' : 'neutral'}>{t(row.enabled ? 'domains.enabled' : 'domains.paused')}</Badge></td>
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
                <div className="min-w-0"><p className="break-all text-sm font-medium">{row.domain}</p><p className="mt-1 break-words text-sm text-[var(--color-fg-muted)]">{row.workspace_name}</p></div>
                <Badge className="shrink-0" variant={row.enabled ? 'success' : 'neutral'}>{t(row.enabled ? 'domains.enabled' : 'domains.paused')}</Badge>
              </div>
              <p className="mt-3 flex items-center gap-1.5 text-sm text-[var(--color-fg-muted)]">{row.lock_personal && <LockKeyhole size={14} aria-hidden />}{t(row.lock_personal ? 'domains.locked' : 'domains.unlocked')} · {row.member_count} {t('domains.members')}</p>
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
      {editor && <DomainEditor key={typeof editor === 'string' ? 'new' : editor.domain} rule={editor} workspaces={workspaces} onClose={() => setEditor(null)} onSaved={() => { setEditor(null); void load() }} />}
      {members && <DomainMembers rule={members} onClose={() => setMembers(null)} />}
      <Dialog open={!!removing} onOpenChange={(open) => { if (!open && !busy) setRemoving(null) }}>
        <DialogContent className="max-w-md">
          <DialogHeader><DialogTitle>{t('domains.removeTitle', { domain: removing?.domain })}</DialogTitle><DialogDescription>{t('domains.removeHint')}</DialogDescription></DialogHeader>
          <DialogFooter><Button variant="ghost" disabled={busy} onClick={() => setRemoving(null)}>{t('domains.cancel')}</Button><Button variant="destructive" loading={busy} onClick={() => void remove()}>{t('domains.remove')}</Button></DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  )
}

function DomainEditor({ rule, workspaces, onClose, onSaved }: { rule: RegistrationDomain | 'new'; workspaces: ApiWorkspace[]; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation('admin')
  const isNew = rule === 'new'
  const [domain, setDomain] = useState(isNew ? '' : rule.domain)
  const [workspace, setWorkspace] = useState(isNew ? '' : rule.workspace_id)
  const [locked, setLocked] = useState(isNew ? false : rule.lock_personal)
  const [enabled, setEnabled] = useState(isNew ? true : rule.enabled)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const mutation = useRef(false)
  async function save() {
    if (mutation.current || !domain.trim() || !workspace) return
    mutation.current = true; setBusy(true); setError('')
    try {
      const body = { domain: domain.trim().toLowerCase(), workspace_id: workspace, lock_personal: locked, enabled }
      if (isNew) await domainsApi.create(body)
      else await domainsApi.update({ ...rule, ...body })
      toast.success(t('domains.saved')); onSaved()
    } catch (e) { setError(e instanceof Error ? e.message : t('domains.saveFailed')) }
    finally { mutation.current = false; setBusy(false) }
  }
  return (
    <Dialog open onOpenChange={(open) => { if (!open && !busy) onClose() }}>
      <DialogContent className="max-w-lg">
        <DialogHeader><DialogTitle>{t(isNew ? 'domains.add' : 'domains.edit')}</DialogTitle><DialogDescription>{t('domains.editorHint')}</DialogDescription></DialogHeader>
        <form onSubmit={(e) => { e.preventDefault(); void save() }}>
          <DialogBody className="space-y-5">
            <div className="space-y-2"><label htmlFor="domain-name" className="text-sm font-medium">{t('domains.domain')}</label><Input id="domain-name" autoFocus disabled={!isNew || busy} value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="example.com" maxLength={253} required /></div>
            <div className="space-y-2"><label id="domain-workspace-label" className="text-sm font-medium">{t('domains.workspace')}</label>
              <Select value={workspace} onValueChange={setWorkspace} disabled={!isNew || busy}><SelectTrigger aria-labelledby="domain-workspace-label"><SelectValue placeholder={t('domains.chooseWorkspace')} /></SelectTrigger><SelectContent>{workspaces.map((w) => <SelectItem key={w.id} value={w.id}>{w.name}</SelectItem>)}</SelectContent></Select>
            </div>
            <div className="flex items-start justify-between gap-4"><div><label htmlFor="domain-enabled" className="text-sm font-medium">{t('domains.autoJoin')}</label><p className="mt-1 text-sm text-[var(--color-fg-muted)]">{t('domains.pauseHint')}</p></div><Switch id="domain-enabled" checked={enabled} onCheckedChange={setEnabled} disabled={busy} /></div>
            <div className="flex items-start justify-between gap-4"><div><label htmlFor="domain-lock" className="text-sm font-medium">{t('domains.lockLabel')}</label><p className="mt-1 text-sm text-[var(--color-fg-muted)]">{t('domains.lockHint')}</p></div><Switch id="domain-lock" checked={locked} onCheckedChange={setLocked} disabled={busy} /></div>
            {error && <p role="alert" className="text-sm text-[var(--color-danger)]">{error}</p>}
          </DialogBody>
          <DialogFooter><Button type="button" variant="ghost" disabled={busy} onClick={onClose}>{t('domains.cancel')}</Button><Button type="submit" loading={busy} disabled={!domain.trim() || !workspace}>{t('domains.save')}</Button></DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function DomainMembers({ rule, onClose }: { rule: RegistrationDomain; onClose: () => void }) {
  const { t } = useTranslation('admin')
  const [users, setUsers] = useState<DomainUser[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)
  const [busy, setBusy] = useState(false)
  const mutation = useRef(false)
  useEffect(() => {
    let active = true
    setLoading(true); setError('')
    domainsApi.users(rule.domain).then((r) => { if (active) setUsers(r.users) }).catch((e) => { if (active) setError(e instanceof Error ? e.message : t('domains.loadFailed')) }).finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [rule.domain, attempt, t])
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
  return (
    <Dialog open onOpenChange={(open) => { if (!open && !busy) onClose() }}>
      <DialogContent className="max-w-2xl">
        <DialogHeader><DialogTitle>{rule.domain} · {t('domains.members')}</DialogTitle><DialogDescription>{t('domains.memberHint')}</DialogDescription></DialogHeader>
        <DialogBody>
          {loading ? <PanelFallback /> : error ? <div role="alert"><p>{error}</p><Button className="mt-3" onClick={() => setAttempt((a) => a + 1)}>{t('domains.retry')}</Button></div> : !users.length ? <p className="py-8 text-sm text-[var(--color-fg-muted)]">{t('domains.noMembers')}</p> : (
            <ul className="divide-y divide-[var(--color-divider)]">
              {users.map((user) => <li key={user.user_id} className="flex flex-col gap-3 py-4 sm:flex-row sm:items-center sm:justify-between">
                <div className="min-w-0"><p className="truncate text-sm font-medium">{user.name}</p><p className="truncate text-sm text-[var(--color-fg-muted)]">{user.email}</p><p className="mt-1 text-xs text-[var(--color-fg-muted)]">{t(user.locked ? 'domains.locked' : 'domains.unlocked')}</p></div>
                <Select disabled={busy} value={user.lock_override === null ? 'inherit' : user.lock_override ? 'locked' : 'unlocked'} onValueChange={(v) => void change(user, v)}><SelectTrigger className="sm:w-52" aria-label={`${t('domains.access')}: ${user.email}`}><SelectValue /></SelectTrigger><SelectContent><SelectItem value="inherit">{t('domains.inherit')}</SelectItem><SelectItem value="locked">{t('domains.locked')}</SelectItem><SelectItem value="unlocked">{t('domains.unlocked')}</SelectItem></SelectContent></Select>
              </li>)}
            </ul>
          )}
        </DialogBody>
        <DialogFooter><Button variant="ghost" disabled={busy} onClick={onClose}>{t('domains.close')}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
