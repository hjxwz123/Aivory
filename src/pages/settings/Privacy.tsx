import { useRef, useState } from 'react'
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

export default function Privacy() {
  const [confirmClear, setConfirmClear] = useState(false)
  const [clearing, setClearing] = useState(false)
  const [exporting, setExporting] = useState(false)
  const [importing, setImporting] = useState(false)
  const importRef = useRef<HTMLInputElement>(null)
  const { t } = useTranslation(['settings', 'common'])
  const reloadConvs = useConversations((s) => s.load)

  /** Import conversations from another platform's JSON export. Only chat history
   *  + titles are kept — images/files/usage and <details> blocks are stripped
   *  client-side by the parser. The branch tree migrates to our message tree. */
  async function performImport(file: File) {
    if (importing) return
    setImporting(true)
    try {
      const text = await file.text()
      let json: unknown
      try {
        json = JSON.parse(text)
      } catch {
        throw new Error(t('settings:privacy.importBadJson', { defaultValue: 'That file is not valid JSON.' }))
      }
      const conversations = parseConversationExport(json)
      if (conversations.length === 0) {
        throw new Error(
          t('settings:privacy.importEmpty', {
            defaultValue: "No conversations were found in this file. Make sure it's a supported chat export.",
          }),
        )
      }
      const res = await conversationsApi.importConversations({ conversations })
      await reloadConvs()
      toast.success(
        t('settings:privacy.importDone', { defaultValue: 'Imported {{count}} conversation(s)', count: res.imported }),
        res.failed > 0
          ? t('settings:privacy.importPartial', { defaultValue: '{{count}} could not be imported.', count: res.failed })
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
          label={t('settings:privacy.import', { defaultValue: 'Import conversations' })}
          description={t('settings:privacy.importBody', {
            defaultValue:
              "Bring chats in from another platform's JSON export. Only history and titles are imported — images, files and usage are ignored.",
          })}
        >
          <input
            ref={importRef}
            type="file"
            accept="application/json,.json"
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
