import { useEffect, useState } from 'react'
import { ArrowLeft, ArrowRightLeft, Download, LockKeyhole } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { domainDataApi } from '@/api/domains'
import { Button } from '@/components/ui/button'
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
import { exportDomainPersonalConversationZip } from '@/lib/conversation-export'
import { useAuth } from '@/store/auth'
import { useConversations } from '@/store/conversations'
import { useDomainData } from '@/store/domain-data'
import { useWorkspaces } from '@/store/workspaces'

export function DomainDataDialog() {
  const { t } = useTranslation(['chat', 'common'])
  const userId = useAuth((state) => state.user?.id)
  const status = useDomainData((state) => state.status)
  const open = useDomainData((state) => state.open)
  const load = useDomainData((state) => state.load)
  const markDismissed = useDomainData((state) => state.markDismissed)
  const markMigrated = useDomainData((state) => state.markMigrated)
  const reset = useDomainData((state) => state.reset)
  const [exporting, setExporting] = useState(false)
  const [migrating, setMigrating] = useState(false)
  const [dismissing, setDismissing] = useState(false)
  const [confirmMigrate, setConfirmMigrate] = useState(false)
  const busy = exporting || migrating || dismissing

  useEffect(() => {
    if (userId) void load(userId)
    else reset()
  }, [load, reset, userId])

  useEffect(() => {
    if (!open) setConfirmMigrate(false)
  }, [open])

  if (!status?.needs_action) return null
  const currentStatus = status

  async function dismiss() {
    if (busy) return
    setDismissing(true)
    try {
      await domainDataApi.dismiss()
      markDismissed()
    } catch (error) {
      toast.error(t('chat:domainData.dismissFailed'), error instanceof Error ? error.message : undefined)
    } finally {
      setDismissing(false)
    }
  }

  async function download() {
    if (busy) return
    setExporting(true)
    try {
      const count = await exportDomainPersonalConversationZip()
      toast.success(t('chat:domainData.downloaded', { count }))
    } catch (error) {
      toast.error(t('chat:domainData.downloadFailed'), error instanceof Error ? error.message : undefined)
    } finally {
      setExporting(false)
    }
  }

  async function migrate() {
    if (busy || !currentStatus.can_migrate) return
    setMigrating(true)
    try {
      const result = await domainDataApi.migrate()
      markMigrated()
      await useWorkspaces.getState().load()
      await useConversations.getState().load()
      toast.success(t('chat:domainData.migrated', { count: result.migrated_conversations }))
    } catch (error) {
      toast.error(t('chat:domainData.migrateFailed'), error instanceof Error ? error.message : undefined)
      void load(userId || '', true)
    } finally {
      setMigrating(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next && !busy) void dismiss() }}>
      <DialogContent size="lg" closeDisabled={busy} showClose={!confirmMigrate}>
        <DialogHeader>
          <DialogTitle>{confirmMigrate ? t('chat:domainData.confirmTitle') : t('chat:domainData.title')}</DialogTitle>
          <DialogDescription>
            {confirmMigrate
              ? t('chat:domainData.confirmDescription', { workspace: currentStatus.workspace_name })
              : t('chat:domainData.description', { domain: currentStatus.domain, workspace: currentStatus.workspace_name })}
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-5">
          {confirmMigrate ? (
            <>
              <div className="flex items-start gap-3 rounded-[10px] bg-[var(--color-bg-muted)] p-4">
                <LockKeyhole size={18} className="mt-0.5 shrink-0 text-[var(--color-fg-muted)]" aria-hidden />
                <div className="space-y-1 text-sm leading-6">
                  <p className="font-medium text-[var(--color-fg)]">{t('chat:domainData.privateAfterMigration')}</p>
                  <p className="text-[var(--color-fg-muted)]">{t('chat:domainData.adminVisibility')}</p>
                </div>
              </div>
              <p className="text-sm leading-6 text-[var(--color-fg-muted)]">{t('chat:domainData.linksCleared')}</p>
            </>
          ) : (
            <>
              <div className="flex items-center justify-between gap-4 border-y border-[var(--color-divider)] py-4">
                <span className="text-sm text-[var(--color-fg-muted)]">{t('chat:domainData.personalChats')}</span>
                <span className="text-base font-semibold tabular-nums text-[var(--color-fg)]">{currentStatus.personal_conversation_count}</span>
              </div>
              <div className="space-y-4">
                <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                  <div className="min-w-0">
                    <p className="text-sm font-medium text-[var(--color-fg)]">{t('chat:domainData.downloadTitle')}</p>
                    <p className="mt-1 text-sm leading-6 text-[var(--color-fg-muted)]">{t('chat:domainData.downloadDescription')}</p>
                  </div>
                  <Button className="shrink-0" variant="secondary" leadingIcon={<Download size={15} aria-hidden />} loading={exporting} disabled={migrating || dismissing} onClick={() => void download()}>
                    {t('chat:domainData.download')}
                  </Button>
                </div>
                <div className="border-t border-[var(--color-divider)] pt-4">
                  <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                    <div className="min-w-0">
                      <p className="text-sm font-medium text-[var(--color-fg)]">{t('chat:domainData.migrateTitle')}</p>
                      <p className="mt-1 text-sm leading-6 text-[var(--color-fg-muted)]">
                        {t(currentStatus.can_migrate ? 'chat:domainData.migrateDescription' : 'chat:domainData.migrateUnavailable', { workspace: currentStatus.workspace_name })}
                      </p>
                    </div>
                    {currentStatus.can_migrate ? (
                      <Button className="shrink-0" leadingIcon={<ArrowRightLeft size={15} aria-hidden />} disabled={busy} onClick={() => setConfirmMigrate(true)}>
                        {t('chat:domainData.migrate')}
                      </Button>
                    ) : null}
                  </div>
                </div>
              </div>
            </>
          )}
        </DialogBody>
        <DialogFooter>
          {confirmMigrate ? (
            <>
              <Button variant="ghost" leadingIcon={<ArrowLeft size={15} aria-hidden />} disabled={busy} onClick={() => setConfirmMigrate(false)}>{t('common:actions.back')}</Button>
              <Button leadingIcon={<ArrowRightLeft size={15} aria-hidden />} loading={migrating} onClick={() => void migrate()}>{t('chat:domainData.confirmMigrate')}</Button>
            </>
          ) : (
            <Button variant="ghost" loading={dismissing} disabled={exporting || migrating} onClick={() => void dismiss()}>{t('chat:domainData.later')}</Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
