/**
 * AdminBackup — database backup & migration (§ admin → data migration).
 *
 * Export downloads a single engine-neutral archive (every table as JSONL, plus
 * optionally the on-disk uploads/artifacts). Import REPLACES all data from such
 * an archive — destructive, gated behind a typed confirmation — and ends the
 * admin's session, so the page signs out and routes to /login afterwards.
 *
 * A second, lighter path exports/imports admin configuration tables (settings,
 * channels, models, skills, groups, OAuth providers, image styles, and admin
 * assets) as a portable archive. It upserts config rows and deliberately leaves
 * users/conversations/user uploads/logs alone, so it needs only a single confirm
 * dialog.
 */
import { useCallback, useEffect, useRef, useState, type ChangeEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router-dom'
import { CheckCircle2, Download, Upload, TriangleAlert, FileArchive, FileJson, Braces, Clock3, XCircle, Database, Wrench, Trash2 } from 'lucide-react'
import {
  adminApi,
  ApiError,
  type BackupArchiveFile,
  type BackupExportJob,
  type BackupExportState,
  type BackupImportResult,
  type VectorAuditReport,
  type VectorMaintenanceJob,
  type VectorMaintenanceState,
} from '@/api'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
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
import { envNum } from '@/lib/env-config'

// The literal an admin must type to authorise a destructive restore. Kept as a
// fixed token (not localized) so muscle memory can't fire it blind.
const CONFIRM_WORD = 'REPLACE'

const ADMIN_BACKUP_EXPORT_JOB_POLL_INTERVAL_MS = envNum('VITE_AIVORY_ADMIN_BACKUP_EXPORT_JOB_POLL_INTERVAL', 2500)
const ADMIN_VECTOR_MAINTENANCE_JOB_POLL_INTERVAL_MS = envNum(
  'VITE_AIVORY_ADMIN_VECTOR_MAINTENANCE_JOB_POLL_INTERVAL',
  2500,
)

function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let value = n
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`
}

function formatDate(unixSec: number): string {
  if (!unixSec) return '—'
  return new Date(unixSec * 1000).toLocaleString()
}

function safeArray<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : []
}

function tableRowCount(tables: Record<string, number> | null | undefined): number {
  if (!tables || typeof tables !== 'object' || Array.isArray(tables)) return 0
  return Object.values(tables).reduce((sum, value) => sum + (Number.isFinite(value) ? value : 0), 0)
}

function normalizeBackupState(state: BackupExportState | null | undefined): BackupExportState {
  return {
    running: state?.running ?? null,
    archives: safeArray(state?.archives),
    jobs: safeArray(state?.jobs),
  }
}

function normalizeVectorReport(report: VectorAuditReport | null | undefined): VectorAuditReport | undefined {
  if (!report) return undefined
  return {
    ...report,
    models: safeArray(report.models),
    issues: safeArray(report.issues),
  }
}

function normalizeVectorJob(job: VectorMaintenanceJob | null | undefined): VectorMaintenanceJob | null {
  if (!job) return null
  return {
    ...job,
    report: normalizeVectorReport(job.report),
  }
}

function normalizeVectorState(state: VectorMaintenanceState | null | undefined): VectorMaintenanceState {
  return {
    running: normalizeVectorJob(state?.running),
    jobs: safeArray(state?.jobs)
      .map(normalizeVectorJob)
      .filter((job): job is VectorMaintenanceJob => Boolean(job)),
  }
}

export default function AdminBackup() {
  const { t } = useTranslation(['admin', 'common'])
  const navigate = useNavigate()
  const logout = useAuth((s) => s.logout)

  const [includeFiles, setIncludeFiles] = useState(true)
  const [exporting, setExporting] = useState(false)
  const [loadingExports, setLoadingExports] = useState(true)
  const [runningExport, setRunningExport] = useState<BackupExportJob | null>(null)
  const [archives, setArchives] = useState<BackupArchiveFile[]>([])
  const [recentJobs, setRecentJobs] = useState<BackupExportJob[]>([])
  const [downloadingArchive, setDownloadingArchive] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<BackupArchiveFile | null>(null)
  const [deletingArchive, setDeletingArchive] = useState<string | null>(null)
  const runningExportID = runningExport?.id

  const [loadingVectors, setLoadingVectors] = useState(true)
  const [runningVector, setRunningVector] = useState<VectorMaintenanceJob | null>(null)
  const [vectorJobs, setVectorJobs] = useState<VectorMaintenanceJob[]>([])
  const [startingVectorJob, setStartingVectorJob] = useState<'check' | 'rebuild' | null>(null)
  const runningVectorID = runningVector?.id

  const fileRef = useRef<HTMLInputElement>(null)
  const [picked, setPicked] = useState<File | null>(null)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [confirmText, setConfirmText] = useState('')
  const [importing, setImporting] = useState(false)
  const [result, setResult] = useState<BackupImportResult | null>(null)

  // Configuration archive export/import. Session-safe, so no typed confirmation
  // / sign-out dance like the DB restore above; a single confirm dialog guards
  // the upsert.
  const [exportingConfig, setExportingConfig] = useState(false)
  const cfgFileRef = useRef<HTMLInputElement>(null)
  const [pendingConfig, setPendingConfig] = useState<File | null>(null)
  const [cfgConfirmOpen, setCfgConfirmOpen] = useState(false)
  const [importingConfig, setImportingConfig] = useState(false)

  const refreshExportState = useCallback(async () => {
    const state = normalizeBackupState(await adminApi.backupExportState('background'))
    setRunningExport(state.running)
    setArchives(state.archives)
    setRecentJobs(state.jobs)
  }, [])

  const refreshVectorState = useCallback(async () => {
    const state = normalizeVectorState(await adminApi.vectorMaintenanceState('background'))
    setRunningVector(state.running)
    setVectorJobs(state.jobs)
  }, [])

  useEffect(() => {
    let alive = true
    Promise.allSettled([adminApi.backupExportState(), adminApi.vectorMaintenanceState()])
      .then(([backupRes, vectorRes]) => {
        if (!alive) return
        if (backupRes.status === 'fulfilled') {
          const state = normalizeBackupState(backupRes.value)
          setRunningExport(state.running)
          setArchives(state.archives)
          setRecentJobs(state.jobs)
        }
        if (vectorRes.status === 'fulfilled') {
          const state = normalizeVectorState(vectorRes.value)
          setRunningVector(state.running)
          setVectorJobs(state.jobs)
        }
      })
      .catch(() => {
        /* The rest of the page remains usable; explicit actions will toast. */
      })
      .finally(() => {
        if (alive) {
          setLoadingExports(false)
          setLoadingVectors(false)
        }
      })
    return () => {
      alive = false
    }
  }, [])

  useEffect(() => {
    if (!runningExportID) return
    const timer = window.setInterval(() => {
      void refreshExportState().catch(() => {
        /* keep polling; transient admin requests can fail during deploys */
      })
    }, ADMIN_BACKUP_EXPORT_JOB_POLL_INTERVAL_MS)
    return () => window.clearInterval(timer)
  }, [refreshExportState, runningExportID])

  useEffect(() => {
    if (!runningVectorID) return
    const timer = window.setInterval(() => {
      void refreshVectorState().catch(() => {
        /* keep polling; the job continues server-side */
      })
    }, ADMIN_VECTOR_MAINTENANCE_JOB_POLL_INTERVAL_MS)
    return () => window.clearInterval(timer)
  }, [refreshVectorState, runningVectorID])

  async function onExport() {
    if (runningExport || runningVector) return
    setExporting(true)
    try {
      const state = normalizeBackupState(await adminApi.backupExportStart(includeFiles))
      setRunningExport(state.running)
      setArchives(state.archives)
      setRecentJobs(state.jobs)
      toast.success(t('admin:backup.export.started'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setExporting(false)
    }
  }

  async function onDownloadArchive(archive: BackupArchiveFile) {
    setDownloadingArchive(archive.name)
    try {
      const blob = await adminApi.backupArchiveDownload(archive.name)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = archive.name
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setDownloadingArchive(null)
    }
  }

  async function onDeleteArchive() {
    if (!deleteTarget) return
    const name = deleteTarget.name
    setDeletingArchive(name)
    try {
      await adminApi.backupArchiveDelete(name)
      setArchives((current) => current.filter((archive) => archive.name !== name))
      setDeleteTarget(null)
      toast.success(t('admin:backup.export.deleteDone', { name }))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setDeletingArchive(null)
    }
  }

  function onPick(e: ChangeEvent<HTMLInputElement>) {
    setPicked(e.target.files?.[0] ?? null)
    setResult(null)
  }

  async function onConfirmImport() {
    if (!picked || confirmText !== CONFIRM_WORD || runningExport || runningVector) return
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
    if (runningExport) return
    setExportingConfig(true)
    try {
      const blob = await adminApi.configExport()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      const stamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')
      a.download = `aivory-config-${stamp}.zip`
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
    if (runningExport) return
    const file = e.target.files?.[0]
    e.target.value = '' // let the same file be re-picked after a rejected parse
    if (!file) return
    setPendingConfig(file)
    setCfgConfirmOpen(true)
  }

  async function onConfirmImportConfig() {
    if (!pendingConfig || runningExport) return
    setImportingConfig(true)
    try {
      const res = await adminApi.configImport(pendingConfig)
      const count = tableRowCount(res.tables)
      setCfgConfirmOpen(false)
      setPendingConfig(null)
      toast.success(t('admin:backup.config.import.done', { count }))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setImportingConfig(false)
    }
  }

  async function onVectorCheck() {
    if (runningVector || runningExport) return
    setStartingVectorJob('check')
    try {
      const state = normalizeVectorState(await adminApi.vectorCheckStart())
      setRunningVector(state.running)
      setVectorJobs(state.jobs)
      toast.success(t('admin:backup.vectors.checkStarted'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setStartingVectorJob(null)
    }
  }

  async function onVectorRebuild() {
    if (runningVector || runningExport) return
    setStartingVectorJob('rebuild')
    try {
      const state = normalizeVectorState(await adminApi.vectorRebuildMissingStart())
      setRunningVector(state.running)
      setVectorJobs(state.jobs)
      toast.success(t('admin:backup.vectors.rebuildStarted'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setStartingVectorJob(null)
    }
  }

  const totalRows = result ? tableRowCount(result.tables) : 0
  const exportBusy = exporting || Boolean(runningExport)
  const failedExport = recentJobs[0]?.status === 'failed' ? recentJobs[0] : null
  const latestVectorJob = vectorJobs[0] ?? null
  const vectorReport = normalizeVectorReport(runningVector?.report ?? latestVectorJob?.report)
  const vectorMissing = vectorReport ? vectorReport.missing + vectorReport.empty : 0
  const vectorBusy = Boolean(runningVector)
  const fullBackupBusy = exportBusy || vectorBusy
  const failedVectorJob = !runningVector && latestVectorJob?.status === 'failed' ? latestVectorJob : null

  return (
    <div className="mx-auto max-w-[80rem]">
      <header className="max-w-3xl">
        <h1 className="text-balance font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">
          {t('admin:backup.title')}
        </h1>
        <p className="mt-2 max-w-[70ch] text-pretty text-sm leading-relaxed text-[var(--color-fg-muted)]">
          {t('admin:backup.lead')}
        </p>
      </header>

      <section
        className="mt-8 overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)]"
        aria-labelledby="backup-export-title backup-import-title"
      >
        <div className="grid min-w-0 lg:grid-cols-[minmax(0,1.55fr)_minmax(20rem,0.8fr)]">
          {/* Export -------------------------------------------------------- */}
          <div className="min-w-0 p-5 sm:p-6 lg:p-7">
            <div className="flex items-start gap-3">
              <span className="flex size-9 shrink-0 items-center justify-center rounded-[9px] bg-[var(--color-accent-soft)] text-[var(--color-accent)]">
                <Download size={17} aria-hidden />
              </span>
              <div className="min-w-0">
                <h2 id="backup-export-title" className="text-balance font-serif text-lg text-[var(--color-fg)]">
                  {t('admin:backup.export.title')}
                </h2>
                <p className="mt-1 max-w-[65ch] text-pretty text-xs leading-relaxed text-[var(--color-fg-muted)]">
                  {t('admin:backup.export.lead')}
                </p>
              </div>
            </div>

            <div className="mt-6 flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
              <label className="flex min-w-0 flex-1 items-center justify-between gap-5 rounded-[10px] bg-[var(--color-bg-muted)] px-4 py-3.5">
                <span className="min-w-0">
                  <span className="block text-sm font-medium text-[var(--color-fg)]">
                    {t('admin:backup.export.includeFiles')}
                  </span>
                  <span className="mt-0.5 block text-pretty text-xs leading-relaxed text-[var(--color-fg-muted)]">
                    {t('admin:backup.export.includeFilesHint')}
                  </span>
                </span>
                <Switch
                  checked={includeFiles}
                  onCheckedChange={setIncludeFiles}
                  disabled={fullBackupBusy}
                  aria-label={t('admin:backup.export.includeFiles')}
                />
              </label>
              <Button
                className="w-full sm:w-auto"
                onClick={onExport}
                loading={exporting || Boolean(runningExport)}
                disabled={fullBackupBusy}
                leadingIcon={<Download size={14} aria-hidden />}
              >
                {runningExport ? t('admin:backup.export.runningAction') : t('admin:backup.export.action')}
              </Button>
            </div>

            {runningExport && (
              <div className="mt-5 rounded-[10px] bg-[var(--color-bg-muted)] p-4" role="status" aria-live="polite">
                <div className="flex items-center gap-2 text-sm font-medium text-[var(--color-fg)]">
                  <Clock3 size={15} className="text-[var(--color-accent)]" aria-hidden />
                  {t('admin:backup.export.running')}
                </div>
                <p className="mt-1 text-xs leading-relaxed text-[var(--color-fg-muted)]">
                  {t('admin:backup.export.runningHint', {
                    progress: t(`admin:backup.export.progress.${runningExport.progress}`, {
                      defaultValue: runningExport.progress,
                    }),
                  })}
                </p>
                <div
                  className="mt-3 h-1.5 overflow-hidden rounded-full bg-[var(--color-border)]"
                  role="progressbar"
                  aria-label={t('admin:backup.export.running')}
                >
                  <div className="h-full w-1/2 animate-[pulse_1.2s_ease-in-out_infinite] rounded-full bg-[var(--color-accent)]" />
                </div>
              </div>
            )}

            {!runningExport && failedExport && (
              <div className="mt-5 flex items-start gap-2.5 rounded-[10px] bg-[var(--color-danger-soft)] p-3.5" role="alert">
                <XCircle size={15} className="mt-0.5 shrink-0 text-[var(--color-danger)]" aria-hidden />
                <p className="text-xs leading-relaxed text-[var(--color-fg-muted)]">
                  {t('admin:backup.export.failed', { error: failedExport.error || t('admin:common.failed') })}
                </p>
              </div>
            )}
          </div>

          {/* Import -------------------------------------------------------- */}
          <aside className="min-w-0 border-t border-[var(--color-border)] bg-[var(--color-bg-muted)] p-5 sm:p-6 lg:border-l lg:border-t-0 lg:p-7">
            <div className="flex items-start gap-3">
              <span className="flex size-9 shrink-0 items-center justify-center rounded-[9px] bg-[var(--color-danger-soft)] text-[var(--color-danger)]">
                <Upload size={17} aria-hidden />
              </span>
              <div className="min-w-0">
                <h2 id="backup-import-title" className="text-balance font-serif text-lg text-[var(--color-fg)]">
                  {t('admin:backup.import.title')}
                </h2>
                <p className="mt-1 text-pretty text-xs leading-relaxed text-[var(--color-fg-muted)]">
                  {t('admin:backup.import.lead')}
                </p>
              </div>
            </div>

            <div className="mt-5 flex items-start gap-2.5 rounded-[10px] bg-[var(--color-danger-soft)] p-3.5">
              <TriangleAlert size={15} className="mt-0.5 shrink-0 text-[var(--color-danger)]" aria-hidden />
              <p className="text-pretty text-xs leading-relaxed text-[var(--color-fg-muted)]">
                {t('admin:backup.import.warning')}
              </p>
            </div>

            <input
              ref={fileRef}
              type="file"
              accept=".zip,application/zip"
              className="sr-only"
              onChange={onPick}
            />
            <div className="mt-5 min-w-0">
              <Button
                className="w-full"
                variant="secondary"
                onClick={() => fileRef.current?.click()}
                disabled={fullBackupBusy}
                leadingIcon={<FileArchive size={14} aria-hidden />}
              >
                {t('admin:backup.import.choose')}
              </Button>
              {picked && (
                <p className="mt-2 truncate text-center text-xs text-[var(--color-fg-muted)]" title={picked.name}>
                  {picked.name}
                </p>
              )}
            </div>

            <Button
              className="mt-3 w-full"
              variant="destructive"
              disabled={!picked || fullBackupBusy}
              onClick={() => {
                setConfirmText('')
                setConfirmOpen(true)
              }}
              leadingIcon={<Upload size={14} aria-hidden />}
            >
              {t('admin:backup.import.action')}
            </Button>

            {result && (
              <div className="mt-5 rounded-[10px] bg-[var(--color-success-soft)] p-4" role="status" aria-live="polite">
                <div className="flex items-center gap-2">
                  <CheckCircle2 size={15} className="shrink-0 text-[var(--color-success)]" aria-hidden />
                  <p className="text-sm font-medium text-[var(--color-fg)]">{t('admin:backup.import.successTitle')}</p>
                </div>
                <p className="mt-2 text-xs leading-relaxed text-[var(--color-fg-muted)]">
                  {t('admin:backup.import.successSummary', { rows: totalRows, files: result.files_restored })}
                </p>
                {typeof result.qdrant_restored === 'number' && (
                  <p className="mt-1 text-xs leading-relaxed text-[var(--color-fg-muted)]">
                    {t('admin:backup.import.successQdrant', { points: result.qdrant_restored })}
                  </p>
                )}
                {result.qdrant_error && (
                  <p className="mt-2 text-xs leading-relaxed text-[var(--color-danger)]">
                    {t('admin:backup.import.qdrantWarning', { error: result.qdrant_error })}
                  </p>
                )}
                <p className="mt-2 text-xs leading-relaxed text-[var(--color-accent)]">
                  {t('admin:backup.import.reloginNote')}
                </p>
              </div>
            )}
          </aside>
        </div>

        {/* Generated archives --------------------------------------------- */}
        <div className="border-t border-[var(--color-border)]">
          <div className="flex items-center gap-2 px-5 py-4 sm:px-6 lg:px-7">
            <FileArchive size={15} className="text-[var(--color-fg-muted)]" aria-hidden />
            <h3 className="text-sm font-medium text-[var(--color-fg)]">
              {t('admin:backup.export.archivesTitle')}
            </h3>
            {!loadingExports && archives.length > 0 && (
              <span className="ml-auto rounded-full bg-[var(--color-bg-muted)] px-2 py-0.5 text-xs tabular-nums text-[var(--color-fg-muted)]">
                {archives.length}
              </span>
            )}
          </div>
          {loadingExports ? (
            <div
              className="border-t border-[var(--color-border-subtle)] px-5 py-5 sm:px-6 lg:px-7"
              role="status"
              aria-live="polite"
              aria-label={t('admin:backup.export.loading')}
            >
              <span className="sr-only">{t('admin:backup.export.loading')}</span>
              <div className="flex items-center gap-3">
                <Skeleton shape="circle" className="size-4 shrink-0" />
                <div className="min-w-0 flex-1 space-y-2">
                  <Skeleton shape="line" className="h-3.5 w-2/5 max-w-56" />
                  <Skeleton shape="line" className="h-2.5 w-1/4 max-w-32" />
                </div>
                <Skeleton className="size-9 shrink-0 rounded-[8px]" />
              </div>
            </div>
          ) : archives.length === 0 ? (
            <div className="border-t border-[var(--color-border-subtle)] px-5 py-9 text-center sm:px-6 lg:px-7">
              <FileArchive size={20} className="mx-auto text-[var(--color-fg-faint)]" aria-hidden />
              <p className="mt-2 text-xs text-[var(--color-fg-muted)]">{t('admin:backup.export.noArchives')}</p>
            </div>
          ) : (
            <div
              className="divide-y divide-[var(--color-border-subtle)] border-t border-[var(--color-border-subtle)]"
              role="list"
              aria-label={t('admin:backup.export.archivesTitle')}
            >
              {archives.map((archive) => (
                <div
                  key={archive.name}
                  className="flex min-w-0 items-center gap-3 px-5 py-3 transition-colors duration-150 hover:bg-[var(--color-bg-muted)] sm:px-6 lg:px-7"
                  role="listitem"
                >
                  <CheckCircle2 size={15} className="shrink-0 text-[var(--color-success)]" aria-hidden />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium text-[var(--color-fg)]" title={archive.name}>
                      {archive.name}
                    </p>
                    <p className="mt-0.5 text-xs text-[var(--color-fg-muted)]">
                      {formatBytes(archive.size_bytes)} · {formatDate(archive.created_at)}
                    </p>
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    <Tooltip content={t('admin:backup.export.download')}>
                      <Button
                        size="icon-lg"
                        variant="ghost"
                        loading={downloadingArchive === archive.name}
                        disabled={deletingArchive === archive.name}
                        onClick={() => void onDownloadArchive(archive)}
                        aria-label={`${t('admin:backup.export.download')} ${archive.name}`}
                      >
                        {downloadingArchive === archive.name ? (
                          <span className="sr-only">{t('admin:backup.export.download')}</span>
                        ) : (
                          <Download size={16} aria-hidden />
                        )}
                      </Button>
                    </Tooltip>
                    <Tooltip content={t('common:actions.delete')}>
                      <Button
                        size="icon-lg"
                        variant="ghost"
                        className="text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)]"
                        disabled={downloadingArchive === archive.name || deletingArchive === archive.name}
                        onClick={() => setDeleteTarget(archive)}
                        aria-label={`${t('common:actions.delete')} ${archive.name}`}
                      >
                        <Trash2 size={16} aria-hidden />
                      </Button>
                    </Tooltip>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      </section>

      {/* Generated archive delete confirm ----------------------------------- */}
      <Dialog
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => !open && !deletingArchive && setDeleteTarget(null)}
      >
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('admin:backup.export.deleteConfirmTitle')}</DialogTitle>
            <DialogDescription className="break-words [overflow-wrap:anywhere]">
              {deleteTarget ? t('admin:backup.export.deleteConfirmLead', { name: deleteTarget.name }) : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleteTarget(null)} disabled={Boolean(deletingArchive)}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="destructive"
              loading={Boolean(deletingArchive)}
              onClick={() => void onDeleteArchive()}
              leadingIcon={<Trash2 size={14} aria-hidden />}
            >
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Vector index maintenance ----------------------------------------- */}
      <section
        className="mt-12 border-t border-[var(--color-border)] pt-8 sm:mt-14 sm:pt-10"
        aria-labelledby="backup-vectors-title"
      >
        <div className="flex flex-col gap-5 sm:flex-row sm:items-end sm:justify-between">
          <div className="flex max-w-3xl items-start gap-3">
            <span className="flex size-9 shrink-0 items-center justify-center rounded-[9px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
              <Database size={17} aria-hidden />
            </span>
            <div className="min-w-0">
              <h2 id="backup-vectors-title" className="text-balance font-serif text-lg text-[var(--color-fg)]">
                {t('admin:backup.vectors.title')}
              </h2>
              <p className="mt-1 max-w-[70ch] text-pretty text-xs leading-relaxed text-[var(--color-fg-muted)]">
                {t('admin:backup.vectors.lead')}
              </p>
            </div>
          </div>
          <div className="flex w-full flex-col gap-2 sm:w-auto sm:flex-row">
            <Button
              className="w-full sm:w-auto"
              variant="secondary"
              onClick={onVectorCheck}
              loading={startingVectorJob === 'check' || runningVector?.type === 'check'}
              disabled={exportBusy || vectorBusy}
              leadingIcon={<Database size={14} aria-hidden />}
            >
              {t('admin:backup.vectors.checkAction')}
            </Button>
            <Button
              className="w-full sm:w-auto"
              variant="secondary"
              onClick={onVectorRebuild}
              loading={startingVectorJob === 'rebuild' || runningVector?.type === 'rebuild'}
              disabled={exportBusy || vectorBusy || vectorMissing === 0}
              leadingIcon={<Wrench size={14} aria-hidden />}
            >
              {t('admin:backup.vectors.rebuildAction')}
            </Button>
          </div>
        </div>

        {runningVector && (
          <div className="mt-5 rounded-[10px] bg-[var(--color-bg-muted)] p-4" role="status" aria-live="polite">
            <div className="flex items-center gap-2 text-sm font-medium text-[var(--color-fg)]">
              <Clock3 size={15} className="text-[var(--color-accent)]" aria-hidden />
              {runningVector.type === 'rebuild'
                ? t('admin:backup.vectors.rebuilding')
                : t('admin:backup.vectors.checking')}
            </div>
            <p className="mt-1 text-xs leading-relaxed text-[var(--color-fg-muted)]">
              {t('admin:backup.vectors.runningHint', {
                progress: t(`admin:backup.vectors.progress.${runningVector.progress}`, {
                  defaultValue: runningVector.progress,
                }),
                rebuilt: runningVector.rebuilt ?? 0,
                failed: runningVector.failed ?? 0,
              })}
            </p>
            <div
              className="mt-3 h-1.5 overflow-hidden rounded-full bg-[var(--color-border)]"
              role="progressbar"
              aria-label={
                runningVector.type === 'rebuild'
                  ? t('admin:backup.vectors.rebuilding')
                  : t('admin:backup.vectors.checking')
              }
            >
              <div className="h-full w-1/2 animate-[pulse_1.2s_ease-in-out_infinite] rounded-full bg-[var(--color-accent)]" />
            </div>
          </div>
        )}

        {failedVectorJob && (
          <div className="mt-5 flex items-start gap-2.5 rounded-[10px] bg-[var(--color-danger-soft)] p-3.5" role="alert">
            <XCircle size={15} className="mt-0.5 shrink-0 text-[var(--color-danger)]" aria-hidden />
            <p className="text-xs leading-relaxed text-[var(--color-fg-muted)]">
              {t('admin:backup.vectors.failed', { error: failedVectorJob.error || t('admin:common.failed') })}
            </p>
          </div>
        )}

        {loadingVectors ? (
          <div
            className="mt-5 overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)]"
            role="status"
            aria-live="polite"
            aria-label={t('admin:backup.vectors.loading')}
          >
            <span className="sr-only">{t('admin:backup.vectors.loading')}</span>
            <div className="grid grid-cols-2 gap-px bg-[var(--color-border-subtle)] sm:grid-cols-5">
              {[0, 1, 2, 3, 4].map((item) => (
                <div key={item} className="min-w-0 bg-[var(--color-surface)] px-4 py-3.5">
                  <Skeleton shape="line" className="h-2.5 w-16" />
                  <Skeleton shape="line" className="mt-2 h-5 w-10" />
                </div>
              ))}
            </div>
            <div className="divide-y divide-[var(--color-border-subtle)] border-t border-[var(--color-border-subtle)]">
              {[0, 1].map((item) => (
                <div key={item} className="flex min-w-0 items-center justify-between gap-4 px-4 py-3">
                  <Skeleton shape="line" className="h-3 w-2/5 max-w-56" />
                  <Skeleton shape="line" className="h-2.5 w-1/3 max-w-40" />
                </div>
              ))}
            </div>
          </div>
        ) : vectorReport ? (
          <div className="mt-5 overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)]">
            <div className="grid grid-cols-2 gap-px bg-[var(--color-border-subtle)] sm:grid-cols-5">
              {[
                ['total', vectorReport.total],
                ['present', vectorReport.present],
                ['missing', vectorReport.missing],
                ['empty', vectorReport.empty],
                ['skipped', vectorReport.skipped],
              ].map(([key, value]) => (
                <div key={key} className="min-w-0 bg-[var(--color-surface)] px-4 py-3.5">
                  <p className="text-xs text-[var(--color-fg-muted)]">{t(`admin:backup.vectors.stats.${key}`)}</p>
                  <p className="mt-1 text-lg font-semibold tabular-nums text-[var(--color-fg)]">{value}</p>
                </div>
              ))}
            </div>
            {latestVectorJob?.type === 'rebuild' && latestVectorJob.status === 'completed' && (
              <p className="border-t border-[var(--color-border-subtle)] px-4 py-3 text-xs leading-relaxed text-[var(--color-fg-muted)]">
                {t('admin:backup.vectors.rebuildSummary', {
                  rebuilt: latestVectorJob.rebuilt ?? 0,
                  failed: latestVectorJob.failed ?? 0,
                })}
              </p>
            )}
            {vectorReport.models.length > 0 && (
              <div className="divide-y divide-[var(--color-border-subtle)] border-t border-[var(--color-border-subtle)]">
                {vectorReport.models.slice(0, 6).map((m) => (
                  <div
                    key={`${m.embedding_model}:${m.dim}`}
                    className="flex min-w-0 flex-col gap-1 px-4 py-3 sm:flex-row sm:items-center sm:justify-between sm:gap-4"
                  >
                    <p className="truncate text-xs font-medium text-[var(--color-fg)]">
                      {m.embedding_model} · {m.dim || '—'}d
                    </p>
                    <p className="text-xs leading-relaxed text-[var(--color-fg-muted)] sm:text-right">
                      {t('admin:backup.vectors.modelSummary', {
                        total: m.total,
                        present: m.present,
                        missing: m.missing,
                        empty: m.empty,
                        skipped: m.skipped,
                      })}
                    </p>
                  </div>
                ))}
              </div>
            )}
            {vectorReport.issues.length > 0 && (
              <div className="border-t border-[var(--color-border-subtle)]">
                <p className="px-4 py-3 text-xs font-medium text-[var(--color-fg)]">
                  {t('admin:backup.vectors.issueSamples')}
                </p>
                <div className="divide-y divide-[var(--color-border-subtle)] border-t border-[var(--color-border-subtle)]">
                  {vectorReport.issues.slice(0, 5).map((issue) => (
                    <div key={`${issue.chunk_id}:${issue.reason}`} className="min-w-0 px-4 py-3">
                      <p className="truncate text-xs font-medium text-[var(--color-fg)]">
                        {issue.filename || issue.document_id}
                      </p>
                      <p className="mt-0.5 truncate text-[11px] text-[var(--color-fg-muted)]">
                        {issue.reason} · {issue.embedding_model} · {issue.dim || '—'}d · {issue.chunk_id}
                      </p>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </div>
        ) : (
          <div className="mt-5 border-y border-[var(--color-border-subtle)] py-9 text-center">
            <Database size={20} className="mx-auto text-[var(--color-fg-faint)]" aria-hidden />
            <p className="mt-2 text-xs text-[var(--color-fg-muted)]">{t('admin:backup.vectors.empty')}</p>
          </div>
        )}
      </section>

      {/* Configuration archive — config-only, non-destructive. Muted section
          icons + secondary buttons keep the single clay accent on the DB
          backup above (§2.4 one accent per screen). */}
      <section
        className="mt-12 border-t border-[var(--color-border)] pt-8 sm:mt-14 sm:pt-10"
        aria-labelledby="backup-config-export-title backup-config-import-title"
      >
        <div className="grid overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] md:grid-cols-2">
        {/* Config export --------------------------------------------------- */}
          <div className="min-w-0 p-5 sm:p-6">
            <div className="flex items-start gap-3">
              <span className="flex size-9 shrink-0 items-center justify-center rounded-[9px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                <FileJson size={17} aria-hidden />
              </span>
              <div className="min-w-0">
                <h2 id="backup-config-export-title" className="text-balance font-serif text-lg text-[var(--color-fg)]">
                  {t('admin:backup.config.export.title')}
                </h2>
                <p className="mt-1 max-w-[65ch] text-pretty text-xs leading-relaxed text-[var(--color-fg-muted)]">
                  {t('admin:backup.config.export.lead')}
                </p>
              </div>
            </div>

            <Button
              className="mt-5 w-full sm:w-auto"
              variant="secondary"
              onClick={onExportConfig}
              loading={exportingConfig}
              disabled={exportBusy}
              leadingIcon={<Download size={14} aria-hidden />}
            >
              {t('admin:backup.config.export.action')}
            </Button>
          </div>

        {/* Config import --------------------------------------------------- */}
          <div className="min-w-0 border-t border-[var(--color-border)] p-5 sm:p-6 md:border-l md:border-t-0">
            <div className="flex items-start gap-3">
              <span className="flex size-9 shrink-0 items-center justify-center rounded-[9px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                <Braces size={17} aria-hidden />
              </span>
              <div className="min-w-0">
                <h2 id="backup-config-import-title" className="text-balance font-serif text-lg text-[var(--color-fg)]">
                  {t('admin:backup.config.import.title')}
                </h2>
                <p className="mt-1 max-w-[65ch] text-pretty text-xs leading-relaxed text-[var(--color-fg-muted)]">
                  {t('admin:backup.config.import.lead')}
                </p>
              </div>
            </div>

            <input
              ref={cfgFileRef}
              type="file"
              accept=".zip,application/zip"
              className="sr-only"
              onChange={onPickConfig}
            />
            <Button
              className="mt-5 w-full sm:w-auto"
              variant="secondary"
              onClick={() => cfgFileRef.current?.click()}
              disabled={exportBusy}
              leadingIcon={<Upload size={14} aria-hidden />}
            >
              {t('admin:backup.config.import.action')}
            </Button>
          </div>
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
                file: pendingConfig?.name ?? '',
              })}
            </p>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setCfgConfirmOpen(false)} disabled={importingConfig}>
              {t('common:actions.cancel')}
            </Button>
            <Button onClick={onConfirmImportConfig} loading={importingConfig} disabled={Boolean(runningExport)}>
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
              disabled={confirmText !== CONFIRM_WORD || Boolean(runningExport) || Boolean(runningVector)}
            >
              {t('admin:backup.confirm.action')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
