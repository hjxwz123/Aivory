import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { SettingsRow, SettingsSection } from './SettingsLayout'
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
import { conversationsApi, memoriesApi } from '@/api'
import { useConversations } from '@/store/conversations'

export default function Privacy() {
  const [confirmClear, setConfirmClear] = useState(false)
  const [clearing, setClearing] = useState(false)
  const [exporting, setExporting] = useState(false)
  const { t } = useTranslation(['settings', 'common'])
  const reloadConvs = useConversations((s) => s.load)

  /** Export user data: fetch all conversations + messages + memories and
   *  download as a JSON file. */
  async function performExport() {
    if (exporting) return
    setExporting(true)
    try {
      const [{ conversations: convs }, mems] = await Promise.all([
        conversationsApi.list(),
        memoriesApi.list(),
      ])
      // Fetch full messages for each conversation.
      const detailed = await Promise.all(
        convs.map(async (c) => {
          try {
            const detail = await conversationsApi.get(c.id)
            return { ...c, messages: detail.messages }
          } catch {
            return { ...c, messages: [] }
          }
        }),
      )
      const blob = new Blob(
        [JSON.stringify({ conversations: detailed, memories: mems, exported_at: new Date().toISOString() }, null, 2)],
        { type: 'application/json' },
      )
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `aurelia-export-${new Date().toISOString().slice(0, 10)}.json`
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
      toast.success(t('settings:privacy.exportDone', { defaultValue: 'Export downloaded' }))
    } catch (e) {
      toast.error(t('common:actions.failed', { defaultValue: 'Export failed' }), e instanceof Error ? e.message : undefined)
    } finally {
      setExporting(false)
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
      const [{ conversations: convs }, mems] = await Promise.all([
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
    <div className="mx-auto max-w-[60rem]">
      <header className="mb-8">
        <h1 className="font-serif tracking-tight text-3xl text-[var(--color-fg)]">{t('settings:privacy.title')}</h1>
        <p className="mt-2.5 text-sm text-[var(--color-fg-muted)]">
          {t('settings:privacy.subtitle')}
        </p>
      </header>

      <SettingsSection title={t('settings:privacy.dataStorage', { defaultValue: 'Data storage' })}>
        <div className="px-5 sm:px-6 py-4">
          <p className="text-sm text-[var(--color-fg-muted)] leading-relaxed">
            {t('settings:privacy.dataStorageBody', {
              defaultValue:
                'Your conversations are stored securely on our servers. To request data deletion, please contact support.',
            })}
          </p>
        </div>
      </SettingsSection>

      <SettingsSection title={t('settings:privacy.exportPurge')}>
        <SettingsRow
          label={t('settings:privacy.exportAll')}
          description={t('settings:privacy.exportAllBody')}
        >
          <Button
            variant="secondary"
            leadingIcon={<Download size={13} aria-hidden />}
            loading={exporting}
            onClick={() => void performExport()}
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
