import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { SettingsRow, SettingsSection } from './SettingsLayout'
import { Button } from '@/components/ui/button'
import { Download, Trash2, Upload } from 'lucide-react'
import { parseConversationExport } from '@/lib/conversation-import'
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
import { useAuth } from '@/store/auth'
import { userCan } from '@/lib/user-permissions'
import {
  exportAllConversationZip,
  readConversationExportFile,
} from '@/lib/conversation-export'

export default function Privacy() {
  const user = useAuth((s) => s.user)
  const canExportConversations = userCan(user, 'allow_conversation_export')
  const canUseMemory = userCan(user, 'allow_memory') && user?.memory_available !== false
  const [confirmClear, setConfirmClear] = useState(false)
  const [clearing, setClearing] = useState(false)
  const [exporting, setExporting] = useState(false)
  const exportAttemptRef = useRef(0)
  const [importing, setImporting] = useState(false)
  const importRef = useRef<HTMLInputElement>(null)
  const { t } = useTranslation(['settings', 'common'])
  const reloadConvs = useConversations((s) => s.load)

  useEffect(() => {
    if (!canExportConversations) {
      exportAttemptRef.current += 1
      setExporting(false)
    }
  }, [canExportConversations, user?.id])

  useEffect(() => () => {
    exportAttemptRef.current += 1
  }, [])

  /** Import conversations from JSON or a ZIP export. ZIP entries are parsed
   *  independently so the archive can contain bounded conversation batches;
   *  legacy third-party and Aivory JSON formats remain supported. */
  async function performImport(file: File) {
    if (importing) return
    setImporting(true)
    try {
      let jsonValues: unknown[]
      try {
        jsonValues = await readConversationExportFile(file)
      } catch (error) {
        throw new Error(error instanceof Error ? error.message : t('settings:privacy.importBadJson', { defaultValue: 'That file is not valid JSON.' }))
      }
      const conversations = jsonValues.flatMap((json) => parseConversationExport(json))
      if (conversations.length === 0) {
        throw new Error(
          t('settings:privacy.importEmpty', {
            defaultValue: "No conversations were found in this file. Make sure it's a supported chat export.",
          }),
        )
      }
      // Keep each request well below the server's import cap. This also keeps
      // a large ZIP recoverable: completed batches remain imported if a later
      // batch encounters a malformed conversation or a transient failure.
      const IMPORT_BATCH_SIZE = 100
      let imported = 0
      let failed = 0
      for (let start = 0; start < conversations.length; start += IMPORT_BATCH_SIZE) {
        const res = await conversationsApi.importConversations({
          conversations: conversations.slice(start, start + IMPORT_BATCH_SIZE),
        })
        imported += res.imported
        failed += res.failed
      }
      await reloadConvs()
      toast.success(
        t('settings:privacy.importDone', { defaultValue: 'Imported {{count}} conversation(s)', count: imported }),
        failed > 0
          ? t('settings:privacy.importPartial', { defaultValue: '{{count}} could not be imported.', count: failed })
          : undefined,
      )
    } catch (e) {
      toast.error(
        t('settings:privacy.importFailed', { defaultValue: 'Import failed' }),
        e instanceof Error ? e.message : undefined,
      )
    } finally {
      setImporting(false)
    }
  }

  /** Export every active and archived conversation in bounded JSON batches.
   * Each batch is independently importable, and full trees are fetched so
   * inactive branches are not lost. */
  async function performExport() {
    if (exporting || !canExportConversations) return
    const userID = user?.id
    if (!userID) return
    const attempt = exportAttemptRef.current + 1
    exportAttemptRef.current = attempt
    setExporting(true)
    try {
      const result = await exportAllConversationZip(canUseMemory)
      const latestUser = useAuth.getState().user
      if (
        exportAttemptRef.current !== attempt ||
        latestUser?.id !== userID ||
        !userCan(latestUser, 'allow_conversation_export')
      ) return
      toast.success(
        t('settings:privacy.exportDone', {
          defaultValue: 'Export downloaded ({{count}} conversation(s) in one ZIP)',
          count: result.conversations,
          batches: result.batches,
        }),
      )
    } catch (e) {
      if (exportAttemptRef.current === attempt) {
        toast.error(t('common:actions.failed', { defaultValue: 'Export failed' }), e instanceof Error ? e.message : undefined)
      }
    } finally {
      if (exportAttemptRef.current === attempt) {
        setExporting(false)
      }
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
        canUseMemory ? memoriesApi.list() : Promise.resolve([]),
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
      <header className="mb-6">
        <h1 className="text-xl font-semibold tracking-normal text-[var(--color-fg)]">{t('settings:privacy.title')}</h1>
        <p className="mt-1.5 text-sm text-[var(--color-fg-muted)]">
          {t('settings:privacy.subtitle')}
        </p>
      </header>

      <SettingsSection title={t('settings:privacy.dataStorage', { defaultValue: 'Data storage' })}>
        <div className="px-4 py-3">
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
          label={t('settings:privacy.import', { defaultValue: 'Import conversations' })}
          description={t('settings:privacy.importBody', {
            defaultValue:
              "Import chats from another platform's export or this page's own export file.",
          })}
        >
          <input
            ref={importRef}
            type="file"
            accept="application/json,.json,application/zip,.zip"
            className="hidden"
            onChange={(e) => {
              const file = e.target.files?.[0]
              e.target.value = '' // allow re-picking the same file
              if (file) void performImport(file)
            }}
          />
          <Button
            variant="secondary"
            leadingIcon={<Upload size={13} aria-hidden />}
            loading={importing}
            onClick={() => importRef.current?.click()}
          >
            {t('common:actions.import', { defaultValue: 'Import' })}
          </Button>
        </SettingsRow>
        {canExportConversations ? (
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
        ) : null}
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
