import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { LogOut } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import type { ApiWorkspace } from '@/api/types'
import { useWorkspaces } from '@/store/workspaces'
import { useSettingsModal } from '@/store/settings-modal'
import { Button } from '@/components/ui/button'
import { WorkspaceIcon } from '@/components/workspace/workspace-icon'
import { toast } from '@/hooks/use-toast'

export default function Work() {
  const workspace = useWorkspaces((s) => s.workspaces.find((w) => w.id === (s.activeId ?? s.domainAccess?.workspace_id)))
  if (!workspace) return null
  return <WorkspaceDetails key={workspace.id} workspace={workspace} />
}

function WorkspaceDetails({ workspace }: { workspace: ApiWorkspace }) {
  const { t } = useTranslation(['settings', 'chat', 'common'])
  const navigate = useNavigate()
  const lockedWorkspaceId = useWorkspaces((s) => s.lockedWorkspaceId)
  const canLeave = !workspace.is_owner && lockedWorkspaceId !== workspace.id
  const [leaving, setLeaving] = useState(false)
  const [confirmLeave, setConfirmLeave] = useState(false)
  const busy = useRef(false)
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])

  async function leave() {
    if (busy.current || !canLeave) return
    busy.current = true
    setLeaving(true)
    try {
      const wasActive = useWorkspaces.getState().activeId === workspace.id
      await useWorkspaces.getState().leave(workspace.id)
      useSettingsModal.getState().close()
      if (wasActive) navigate('/')
    } catch { if (mounted.current) toast.error(t('chat:workspace.leaveFailed')) }
    finally { busy.current = false; if (mounted.current) setLeaving(false) }
  }

  return (
    <div className="min-w-0">
      <header className="mb-5">
        <h1 className="text-lg font-semibold">{t('settings:tabs.work')}</h1>
        <p className="mt-1 text-sm text-[var(--color-fg-muted)]">{t('settings:work.subtitle')}</p>
      </header>
      <div className="mb-5 flex min-w-0 items-center gap-3">
        <WorkspaceIcon icon={workspace.icon_url} size={40} />
        <h2 className="min-w-0 break-words text-base font-semibold [overflow-wrap:anywhere]">{workspace.name}</h2>
      </div>
      <p className="whitespace-pre-wrap break-words text-sm leading-6 text-[var(--color-fg-muted)] [overflow-wrap:anywhere]">
        {workspace.description?.trim() || t('settings:work.noDescription')}
      </p>
      {canLeave ? (
        <div className="mt-6 border-t border-[var(--color-divider)] pt-4">
          {confirmLeave ? (
            <div className="space-y-3" role="group" aria-label={t('chat:workspace.leave')}>
              <p className="text-sm text-[var(--color-fg-muted)]">{t('settings:work.leaveConfirm')}</p>
              <div className="flex flex-wrap gap-2">
                <Button variant="secondary" disabled={leaving} onClick={() => setConfirmLeave(false)}>{t('common:actions.cancel')}</Button>
                <Button variant="destructive" loading={leaving} onClick={() => void leave()}>{t('chat:workspace.leave')}</Button>
              </div>
            </div>
          ) : (
            <Button variant="secondary" leadingIcon={<LogOut size={14} aria-hidden />} onClick={() => setConfirmLeave(true)}>{t('chat:workspace.leave')}</Button>
          )}
        </div>
      ) : null}
    </div>
  )
}
