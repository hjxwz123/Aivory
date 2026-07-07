/**
 * AdminBackup — database backup & migration (§ admin → data migration).
 *
 * Export downloads a single engine-neutral archive (every table as JSONL, plus
 * optionally the on-disk uploads/artifacts). Import REPLACES all data from such
 * an archive — destructive, gated behind a typed confirmation — and ends the
 * admin's session, so the page signs out and routes to /login afterwards.
 *
 * A second, lighter path exports/imports just the site *configuration* as a
 * portable JSON file (settings only, secrets redacted). That import is a
 * non-destructive PATCH-merge — session-safe, so it needs only a single confirm
 * dialog rather than the typed word + sign-out of the DB restore.
 */
import { useRef, useState, type ChangeEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router-dom'
import { Download, Upload, TriangleAlert, FileArchive, FileJson, Braces } from 'lucide-react'
import { adminApi, ApiError, type BackupImportResult } from '@/api'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Input } from '@/components/ui/input'
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
import { useAuth } from '@/store/auth'

// The literal an admin must type to authorise a destructive restore. Kept as a
// fixed token (not localized) so muscle memory can't fire it blind.
const CONFIRM_WORD = 'REPLACE'

// Envelope tag for the config-only JSON export. Import rejects any file whose
// top-level `format` doesn't match, so arbitrary JSON can't be applied by
// accident. Unlike the DB archive this is a non-destructive PATCH-merge of the
// site settings — the backend redacts secrets on read and skips the redaction
// sentinel on write, so a round-trip never leaks or clobbers them.
const CONFIG_FORMAT = 'aurelia-config'

interface ConfigFile {
  format: string
  settings: Record<string, unknown>
}

function isConfigFile(v: unknown): v is ConfigFile {
  if (!v || typeof v !== 'object') return false
  const o = v as Record<string, unknown>
  return o.format === CONFIG_FORMAT && typeof o.settings === 'object' && o.settings !== null
}

export default function AdminBackup() {
  const { t } = useTranslation(['admin', 'common'])
  const navigate = useNavigate()
  const logout = useAuth((s) => s.logout)

  const [includeFiles, setIncludeFiles] = useState(true)
  const [exporting, setExporting] = useState(false)

  const fileRef = useRef<HTMLInputElement>(null)
  const [picked, setPicked] = useState<File | null>(null)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [confirmText, setConfirmText] = useState('')
  const [importing, setImporting] = useState(false)
  const [result, setResult] = useState<BackupImportResult | null>(null)

  // Config-only JSON export/import — see CONFIG_FORMAT. Session-safe, so no
  // typed confirmation / sign-out dance like the DB restore above; a single
  // confirm dialog guards the overwrite.
  const [exportingConfig, setExportingConfig] = useState(false)
  const cfgFileRef = useRef<HTMLInputElement>(null)
  const [pendingConfig, setPendingConfig] = useState<{ file: string; settings: Record<string, unknown> } | null>(null)
  const [cfgConfirmOpen, setCfgConfirmOpen] = useState(false)
  const [importingConfig, setImportingConfig] = useState(false)

  async function onExport() {
    setExporting(true)
    try {
      const blob = await adminApi.backupExport(includeFiles)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      const stamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')
      a.download = `aurelia-backup-${stamp}.zip`
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
      toast.success(t('admin:backup.export.done'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setExporting(false)
    }
  }

  function onPick(e: ChangeEvent<HTMLInputElement>) {
    setPicked(e.target.files?.[0] ?? null)
    setResult(null)
  }

  async function onConfirmImport() {
    if (!picked || confirmText !== CONFIRM_WORD) return
    setImporting(true)
    try {
      const res = await adminApi.backupImport(picked)
      setResult(res)
      setConfirmOpen(false)
      toast.success(t('admin:backup.import.done'))
      // The restore replaced the users/sessions tables, so this admin's session
      // is gone. Sign out + route to login after a beat so the success state is
      // visible first.
      window.setTimeout(() => {
        void logout().finally(() => navigate('/login', { replace: true }))
      }, 2600)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setImporting(false)
    }
  }

  async function onExportConfig() {
    setExportingConfig(true)
    try {
      const settings = await adminApi.settings()
      const payload = { format: CONFIG_FORMAT, version: 1, exported_at: new Date().toISOString(), settings }
      const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      const stamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')
      a.download = `aurelia-config-${stamp}.json`
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
      toast.success(t('admin:backup.config.export.done'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setExportingConfig(false)
    }
  }

  async function onPickConfig(e: ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    e.target.value = '' // let the same file be re-picked after a rejected parse
    if (!file) return
    try {
      const parsed: unknown = JSON.parse(await file.text())
      if (!isConfigFile(parsed)) throw new Error('bad format')
      setPendingConfig({ file: file.name, settings: parsed.settings })
      setCfgConfirmOpen(true)
    } catch {
      setPendingConfig(null)
      toast.error(t('admin:backup.config.import.invalid'))
    }
  }

  async function onConfirmImportConfig() {
    if (!pendingConfig) return
    setImportingConfig(true)
    try {
      await adminApi.updateSettings(pendingConfig.settings)
      const count = Object.keys(pendingConfig.settings).length
      setCfgConfirmOpen(false)
      setPendingConfig(null)
      toast.success(t('admin:backup.config.import.done', { count }))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setImportingConfig(false)
    }
  }

  const totalRows = result ? Object.values(result.tables).reduce((a, b) => a + b, 0) : 0

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:backup.title')}</h1>
        <p className="mt-2 max-w-2xl text-sm text-[var(--color-fg-muted)]">{t('admin:backup.lead')}</p>
      </header>

      <section className="mt-8 flex flex-col gap-5">
        {/* Export ---------------------------------------------------------- */}
        <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
          <div className="flex items-center gap-2">
            <Download size={16} className="text-[var(--color-accent)]" aria-hidden />
            <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:backup.export.title')}</h2>
          </div>
          <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">{t('admin:backup.export.lead')}</p>

          <label className="mt-4 flex items-start justify-between gap-4 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4">
            <span>
              <span className="block text-sm font-medium text-[var(--color-fg)]">
                {t('admin:backup.export.includeFiles')}
              </span>
              <span className="mt-0.5 block text-xs text-[var(--color-fg-subtle)]">
                {t('admin:backup.export.includeFilesHint')}
              </span>
            </span>
            <Switch
              checked={includeFiles}
              onCheckedChange={setIncludeFiles}
              aria-label={t('admin:backup.export.includeFiles')}
            />
          </label>

          <Button
            className="mt-4"
            onClick={onExport}
            loading={exporting}
            leadingIcon={<Download size={14} aria-hidden />}
          >
            {t('admin:backup.export.action')}
          </Button>
        </div>

        {/* Import ---------------------------------------------------------- */}
        <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
          <div className="flex items-center gap-2">
            <Upload size={16} className="text-[var(--color-accent)]" aria-hidden />
            <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:backup.import.title')}</h2>
          </div>
          <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">{t('admin:backup.import.lead')}</p>

          <div className="mt-4 flex items-start gap-2.5 rounded-[10px] border border-[color-mix(in_oklch,var(--color-danger)_40%,transparent)] bg-[var(--color-danger-soft)] p-3.5">
            <TriangleAlert size={15} className="mt-0.5 shrink-0 text-[var(--color-danger)]" aria-hidden />
            <p className="text-xs leading-relaxed text-[var(--color-fg-muted)]">{t('admin:backup.import.warning')}</p>
          </div>

          <input
            ref={fileRef}
            type="file"
            accept=".zip,application/zip"
            className="sr-only"
            onChange={onPick}
          />
          <div className="mt-4 flex flex-wrap items-center gap-3">
            <Button
              variant="secondary"
              onClick={() => fileRef.current?.click()}
              leadingIcon={<FileArchive size={14} aria-hidden />}
            >
              {t('admin:backup.import.choose')}
            </Button>
            {picked && <span className="truncate text-xs text-[var(--color-fg-muted)]">{picked.name}</span>}
          </div>

          <Button
            className="mt-4"
            variant="destructive"
            disabled={!picked}
            onClick={() => {
              setConfirmText('')
              setConfirmOpen(true)
            }}
            leadingIcon={<Upload size={14} aria-hidden />}
          >
            {t('admin:backup.import.action')}
          </Button>

          {result && (
            <div className="mt-5 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4">
              <p className="text-sm font-medium text-[var(--color-fg)]">{t('admin:backup.import.successTitle')}</p>
              <p className="mt-1 text-xs text-[var(--color-fg-muted)]">
                {t('admin:backup.import.successSummary', { rows: totalRows, files: result.files_restored })}
              </p>
              <p className="mt-2 text-xs text-[var(--color-accent)]">{t('admin:backup.import.reloginNote')}</p>
            </div>
          )}
        </div>
      </section>

      {/* Configuration (JSON) — settings-only, non-destructive. Muted section
          icons + secondary buttons keep the single clay accent on the DB
          backup above (§2.4 one accent per screen). */}
      <section className="mt-10 flex flex-col gap-5">
        {/* Config export --------------------------------------------------- */}
        <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
          <div className="flex items-center gap-2">
            <FileJson size={16} className="text-[var(--color-fg-muted)]" aria-hidden />
            <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:backup.config.export.title')}</h2>
          </div>
          <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">{t('admin:backup.config.export.lead')}</p>

          <Button
            className="mt-4"
            variant="secondary"
            onClick={onExportConfig}
            loading={exportingConfig}
            leadingIcon={<Download size={14} aria-hidden />}
          >
            {t('admin:backup.config.export.action')}
          </Button>
        </div>

        {/* Config import --------------------------------------------------- */}
        <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
          <div className="flex items-center gap-2">
            <Braces size={16} className="text-[var(--color-fg-muted)]" aria-hidden />
            <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:backup.config.import.title')}</h2>
          </div>
          <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">{t('admin:backup.config.import.lead')}</p>

          <input
            ref={cfgFileRef}
            type="file"
            accept=".json,application/json"
            className="sr-only"
            onChange={onPickConfig}
          />
          <Button
            className="mt-4"
            variant="secondary"
            onClick={() => cfgFileRef.current?.click()}
            leadingIcon={<Upload size={14} aria-hidden />}
          >
            {t('admin:backup.config.import.action')}
          </Button>
        </div>
      </section>

      {/* Config import confirm ---------------------------------------------- */}
      <Dialog open={cfgConfirmOpen} onOpenChange={(o) => !importingConfig && setCfgConfirmOpen(o)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('admin:backup.config.import.confirmTitle')}</DialogTitle>
            <DialogDescription>{t('admin:backup.config.import.confirmLead')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <p className="text-sm text-[var(--color-fg-muted)]">
              {t('admin:backup.config.import.confirmDetail', {
                file: pendingConfig?.file ?? '',
                count: pendingConfig ? Object.keys(pendingConfig.settings).length : 0,
              })}
            </p>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setCfgConfirmOpen(false)} disabled={importingConfig}>
              {t('common:actions.cancel')}
            </Button>
            <Button onClick={onConfirmImportConfig} loading={importingConfig}>
              {t('admin:backup.config.import.confirmAction')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Typed confirmation -------------------------------------------------- */}
      <Dialog open={confirmOpen} onOpenChange={(o) => !importing && setConfirmOpen(o)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('admin:backup.confirm.title')}</DialogTitle>
            <DialogDescription>{t('admin:backup.confirm.lead')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <p className="text-sm text-[var(--color-fg-muted)]">
              {t('admin:backup.confirm.instruction', { word: CONFIRM_WORD })}
            </p>
            <Input
              className="mt-3"
              value={confirmText}
              onChange={(e) => setConfirmText(e.target.value)}
              placeholder={CONFIRM_WORD}
              autoFocus
              autoComplete="off"
              spellCheck={false}
            />
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmOpen(false)} disabled={importing}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="destructive"
              onClick={onConfirmImport}
              loading={importing}
              disabled={confirmText !== CONFIRM_WORD}
            >
              {t('admin:backup.confirm.action')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
