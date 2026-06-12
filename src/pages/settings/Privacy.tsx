import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { SettingsRow, SettingsSection } from './SettingsLayout'
import { useSettings } from '@/store/settings'
import { Switch } from '@/components/ui/switch'
import { Button } from '@/components/ui/button'
import { Download, Trash2 } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { toast } from '@/hooks/use-toast'
import { authApi, conversationsApi, memoriesApi } from '@/api'
import { useConversations } from '@/store/conversations'

export default function Privacy() {
  const p = useSettings((s) => s.privacy)
  const set = useSettings((s) => s.setPrivacy)
  const [confirmClear, setConfirmClear] = useState(false)
  const [clearing, setClearing] = useState(false)
  const { t } = useTranslation(['settings', 'common'])
  const reloadConvs = useConversations((s) => s.load)

  /** Server-side memory toggle. Persisted in the user's per-user settings
   *  so the orchestrator's MemoryWorker reads the SAME value (§4.16). The
   *  local store still mirrors it for instant UI feedback. */
  async function onToggleMemory(v: boolean) {
    set({ memoriesEnabled: v })
    try {
      await authApi.updateSettings({ memory_enabled: v })
    } catch (e) {
      // Revert local if server refused.
      set({ memoriesEnabled: !v })
      toast.error(t('common:actions.failed', { defaultValue: 'Failed to save' }), e instanceof Error ? e.message : undefined)
    }
  }

  /** Permanent clear: deletes every conversation + every memory of the
   *  logged-in user. Each row goes through the existing ownership-checked
   *  endpoints — we don't add a bulk DELETE because the API surface stays
   *  small + auditable that way. Reloads the local cache when done. */
  async function performClearAll() {
    if (clearing) return
    setClearing(true)
    try {
      const [convs, mems] = await Promise.all([
        conversationsApi.list(),
        memoriesApi.list(),
      ])
      await Promise.allSettled([
        ...convs.map((c) => conversationsApi.remove(c.id)),
        ...mems.map((m) => memoriesApi.remove(m.id)),
      ])
      await reloadConvs()
      toast.success(t('settings:privacy.cleared'))
    } catch (e) {
      toast.error(t('common:actions.failed', { defaultValue: 'Failed to clear' }), e instanceof Error ? e.message : undefined)
    } finally {
      setClearing(false)
      setConfirmClear(false)
    }
  }

  return (
    <div className="max-w-[44rem]">
      <header className="mb-8">
        <h1 className="font-serif tracking-tight text-3xl text-[var(--color-fg)]">{t('settings:privacy.title')}</h1>
        <p className="mt-2.5 text-sm text-[var(--color-fg-muted)]">
          {t('settings:privacy.subtitle')}
        </p>
      </header>

      <SettingsSection title={t('settings:privacy.controls')}>
        <SettingsRow
          label={t('settings:privacy.improve')}
          description={t('settings:privacy.improveBody')}
        >
          <Switch checked={!p.trainingOptOut} onCheckedChange={(v) => set({ trainingOptOut: !v })} />
        </SettingsRow>
        <SettingsRow
          label={t('settings:privacy.keep')}
          description={t('settings:privacy.keepBody')}
        >
          <Switch checked={p.retainHistory} onCheckedChange={(v) => set({ retainHistory: Boolean(v) })} />
        </SettingsRow>
        <SettingsRow
          label={t('settings:privacy.memory')}
          description={t('settings:privacy.memoryBody')}
        >
          <Switch checked={p.memoriesEnabled} onCheckedChange={(v) => void onToggleMemory(Boolean(v))} />
        </SettingsRow>
      </SettingsSection>

      <SettingsSection title={t('settings:privacy.exportPurge')}>
        <SettingsRow
          label={t('settings:privacy.exportAll')}
          description={t('settings:privacy.exportAllBody')}
        >
          <Button
            variant="secondary"
            leadingIcon={<Download size={13} aria-hidden />}
            onClick={() => toast.info(t('settings:privacy.exportQueued'))}
          >
            {t('common:actions.export')}
          </Button>
        </SettingsRow>
        <SettingsRow
          label={t('settings:privacy.clearAll')}
          description={t('settings:privacy.clearAllBody')}
        >
          <Button
            variant="destructive"
            leadingIcon={<Trash2 size={13} aria-hidden />}
            onClick={() => setConfirmClear(true)}
          >
            {t('common:actions.clear')}
          </Button>
        </SettingsRow>
      </SettingsSection>

      <Dialog open={confirmClear} onOpenChange={setConfirmClear}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('settings:privacy.clearAllConfirm')}</DialogTitle>
            <DialogDescription>
              {t('settings:privacy.clearAllConfirmBody')}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmClear(false)} disabled={clearing}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="destructive"
              onClick={() => void performClearAll()}
              disabled={clearing}
            >
              {t('settings:privacy.clearAllConfirmAction')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
