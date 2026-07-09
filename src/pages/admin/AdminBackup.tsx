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
import { CheckCircle2, Download, Upload, TriangleAlert, FileArchive, FileJson, Braces, Clock3, XCircle, Database, Wrench } from 'lucide-react'
import { adminApi, ApiError, type BackupArchiveFile, type BackupExportJob, type BackupImportResult, type VectorMaintenanceJob } from '@/api'
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
    const state = await adminApi.backupExportState()
    setRunningExport(state.running)
    setArchives(state.archives)
    setRecentJobs(state.jobs)
  }, [])

  const refreshVectorState = useCallback(async () => {
    const state = await adminApi.vectorMaintenanceState()
    setRunningVector(state.running)
    setVectorJobs(state.jobs)
  }, [])

  useEffect(() => {
    let alive = true
    Promise.allSettled([adminApi.backupExportState(), adminApi.vectorMaintenanceState()])
      .then(([backupRes, vectorRes]) => {
        if (!alive) return
        if (backupRes.status === 'fulfilled') {
          setRunningExport(backupRes.value.running)
          setArchives(backupRes.value.archives)
          setRecentJobs(backupRes.value.jobs)
        }
        if (vectorRes.status === 'fulfilled') {
          setRunningVector(vectorRes.value.running)
          setVectorJobs(vectorRes.value.jobs)
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
    }, 2500)
    return () => window.clearInterval(timer)
  }, [refreshExportState, runningExportID])

  useEffect(() => {
    if (!runningVectorID) return
    const timer = window.setInterval(() => {
      void refreshVectorState().catch(() => {
        /* keep polling; the job continues server-side */
      })
    }, 2500)
    return () => window.clearInterval(timer)
  }, [refreshVectorState, runningVectorID])

  async function onExport() {
    if (runningExport || runningVector) return
    setExporting(true)
    try {
      const state = await adminApi.backupExportStart(includeFiles)
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
      a.download = `aurelia-config-${stamp}.zip`
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
      const count = Object.values(res.tables).reduce((a, b) => a + b, 0)
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
      const state = await adminApi.vectorCheckStart()
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
      const state = await adminApi.vectorRebuildMissingStart()
      setRunningVector(state.running)
      setVectorJobs(state.jobs)
      toast.success(t('admin:backup.vectors.rebuildStarted'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setStartingVectorJob(null)
    }
  }

  const totalRows = result ? Object.values(result.tables).reduce((a, b) => a + b, 0) : 0
  const exportBusy = exporting || Boolean(runningExport)
  const failedExport = recentJobs[0]?.status === 'failed' ? recentJobs[0] : null
  const latestVectorJob = vectorJobs[0] ?? null
  const vectorReport = runningVector?.report ?? latestVectorJob?.report
  const vectorMissing = vectorReport ? vectorReport.missing + vectorReport.empty : 0
  const vectorBusy = Boolean(runningVector)
  const fullBackupBusy = exportBusy || vectorBusy
  const failedVectorJob = !runningVector && latestVectorJob?.status === 'failed' ? latestVectorJob : null

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
              disabled={fullBackupBusy}
              aria-label={t('admin:backup.export.includeFiles')}
            />
          </label>

          {runningExport && (
            <div className="mt-4 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4">
              <div className="flex items-center gap-2 text-sm font-medium text-[var(--color-fg)]">
                <Clock3 size={15} className="text-[var(--color-accent)]" aria-hidden />
                {t('admin:backup.export.running')}
              </div>
              <p className="mt-1 text-xs text-[var(--color-fg-muted)]">
                {t('admin:backup.export.runningHint', {
                  progress: t(`admin:backup.export.progress.${runningExport.progress}`, {
                    defaultValue: runningExport.progress,
                  }),
                })}
              </p>
              <div className="mt-3 h-1.5 overflow-hidden rounded-full bg-[var(--color-border)]">
                <div className="h-full w-1/2 animate-[pulse_1.2s_ease-in-out_infinite] rounded-full bg-[var(--color-accent)]" />
              </div>
            </div>
          )}

          {!runningExport && failedExport && (
            <div className="mt-4 flex items-start gap-2.5 rounded-[10px] border border-[color-mix(in_oklch,var(--color-danger)_40%,transparent)] bg-[var(--color-danger-soft)] p-3.5">
              <XCircle size={15} className="mt-0.5 shrink-0 text-[var(--color-danger)]" aria-hidden />
              <p className="text-xs leading-relaxed text-[var(--color-fg-muted)]">
                {t('admin:backup.export.failed', { error: failedExport.error || t('admin:common.failed') })}
              </p>
            </div>
          )}

          <Button
            className="mt-4"
            onClick={onExport}
            loading={exporting || Boolean(runningExport)}
            disabled={fullBackupBusy}
            leadingIcon={<Download size={14} aria-hidden />}
          >
            {runningExport ? t('admin:backup.export.runningAction') : t('admin:backup.export.action')}
          </Button>

          <div className="mt-5 border-t border-[var(--color-border)] pt-4">
            <div className="flex items-center gap-2">
              <FileArchive size={15} className="text-[var(--color-fg-muted)]" aria-hidden />
              <h3 className="text-sm font-medium text-[var(--color-fg)]">{t('admin:backup.export.archivesTitle')}</h3>
            </div>
            {loadingExports ? (
              <p className="mt-2 text-xs text-[var(--color-fg-subtle)]">{t('admin:backup.export.loading')}</p>
            ) : archives.length === 0 ? (
              <p className="mt-2 text-xs text-[var(--color-fg-subtle)]">{t('admin:backup.export.noArchives')}</p>
            ) : (
              <div className="mt-3 divide-y divide-[var(--color-border)] overflow-hidden rounded-[10px] border border-[var(--color-border)]">
                {archives.map((archive) => (
                  <div key={archive.name} className="flex flex-col gap-3 bg-[var(--color-bg-muted)] p-3 sm:flex-row sm:items-center sm:justify-between">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2">
                        <CheckCircle2 size={14} className="shrink-0 text-[var(--color-accent)]" aria-hidden />
                        <p className="truncate text-sm font-medium text-[var(--color-fg)]">{archive.name}</p>
                      </div>
                      <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
                        {formatBytes(archive.size_bytes)} · {formatDate(archive.created_at)}
                      </p>
                    </div>
                    <Button
                      size="sm"
                      variant="secondary"
                      className="self-start sm:self-auto"
                      loading={downloadingArchive === archive.name}
                      onClick={() => void onDownloadArchive(archive)}
                      leadingIcon={<Download size={13} aria-hidden />}
                    >
                      {t('admin:backup.export.download')}
                    </Button>
                  </div>
                ))}
              </div>
            )}
          </div>
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
              disabled={fullBackupBusy}
              leadingIcon={<FileArchive size={14} aria-hidden />}
            >
              {t('admin:backup.import.choose')}
            </Button>
            {picked && <span className="truncate text-xs text-[var(--color-fg-muted)]">{picked.name}</span>}
          </div>

          <Button
            className="mt-4"
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
            <div className="mt-5 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4">
              <p className="text-sm font-medium text-[var(--color-fg)]">{t('admin:backup.import.successTitle')}</p>
              <p className="mt-1 text-xs text-[var(--color-fg-muted)]">
                {t('admin:backup.import.successSummary', { rows: totalRows, files: result.files_restored })}
              </p>
              {typeof result.qdrant_restored === 'number' && (
                <p className="mt-1 text-xs text-[var(--color-fg-muted)]">
                  {t('admin:backup.import.successQdrant', { points: result.qdrant_restored })}
                </p>
              )}
              {result.qdrant_error && (
                <p className="mt-2 text-xs text-[var(--color-danger)]">
                  {t('admin:backup.import.qdrantWarning', { error: result.qdrant_error })}
                </p>
              )}
              <p className="mt-2 text-xs text-[var(--color-accent)]">{t('admin:backup.import.reloginNote')}</p>
            </div>
          )}
        </div>
      </section>

      {/* Vector index maintenance ----------------------------------------- */}
      <section className="mt-10">
        <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
          <div className="flex items-center gap-2">
            <Database size={16} className="text-[var(--color-fg-muted)]" aria-hidden />
            <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:backup.vectors.title')}</h2>
          </div>
          <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">{t('admin:backup.vectors.lead')}</p>

          {runningVector && (
            <div className="mt-4 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4">
              <div className="flex items-center gap-2 text-sm font-medium text-[var(--color-fg)]">
                <Clock3 size={15} className="text-[var(--color-accent)]" aria-hidden />
                {runningVector.type === 'rebuild'
                  ? t('admin:backup.vectors.rebuilding')
                  : t('admin:backup.vectors.checking')}
              </div>
              <p className="mt-1 text-xs text-[var(--color-fg-muted)]">
                {t('admin:backup.vectors.runningHint', {
                  progress: t(`admin:backup.vectors.progress.${runningVector.progress}`, {
                    defaultValue: runningVector.progress,
                  }),
                  rebuilt: runningVector.rebuilt ?? 0,
                  failed: runningVector.failed ?? 0,
                })}
              </p>
              <div className="mt-3 h-1.5 overflow-hidden rounded-full bg-[var(--color-border)]">
                <div className="h-full w-1/2 animate-[pulse_1.2s_ease-in-out_infinite] rounded-full bg-[var(--color-accent)]" />
              </div>
            </div>
          )}

          {failedVectorJob && (
            <div className="mt-4 flex items-start gap-2.5 rounded-[10px] border border-[color-mix(in_oklch,var(--color-danger)_40%,transparent)] bg-[var(--color-danger-soft)] p-3.5">
              <XCircle size={15} className="mt-0.5 shrink-0 text-[var(--color-danger)]" aria-hidden />
              <p className="text-xs leading-relaxed text-[var(--color-fg-muted)]">
                {t('admin:backup.vectors.failed', { error: failedVectorJob.error || t('admin:common.failed') })}
              </p>
            </div>
          )}

          {loadingVectors ? (
            <p className="mt-4 text-xs text-[var(--color-fg-subtle)]">{t('admin:backup.vectors.loading')}</p>
          ) : vectorReport ? (
            <div className="mt-4 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4">
              <div className="grid gap-3 sm:grid-cols-5">
                {[
                  ['total', vectorReport.total],
                  ['present', vectorReport.present],
                  ['missing', vectorReport.missing],
                  ['empty', vectorReport.empty],
                  ['skipped', vectorReport.skipped],
                ].map(([key, value]) => (
                  <div key={key} className="min-w-0">
                    <p className="text-[11px] text-[var(--color-fg-subtle)]">
                      {t(`admin:backup.vectors.stats.${key}`)}
                    </p>
                    <p className="mt-1 text-lg font-semibold text-[var(--color-fg)]">{value}</p>
                  </div>
                ))}
              </div>
              {latestVectorJob?.type === 'rebuild' && latestVectorJob.status === 'completed' && (
                <p className="mt-3 text-xs text-[var(--color-fg-muted)]">
                  {t('admin:backup.vectors.rebuildSummary', {
                    rebuilt: latestVectorJob.rebuilt ?? 0,
                    failed: latestVectorJob.failed ?? 0,
                  })}
                </p>
              )}
              {vectorReport.models.length > 0 && (
                <div className="mt-4 divide-y divide-[var(--color-border)] overflow-hidden rounded-[10px] border border-[var(--color-border)]">
                  {vectorReport.models.slice(0, 6).map((m) => (
                    <div key={`${m.embedding_model}:${m.dim}`} className="flex flex-col gap-1 bg-[var(--color-surface)] px-3 py-2.5 sm:flex-row sm:items-center sm:justify-between">
                      <p className="truncate text-xs font-medium text-[var(--color-fg)]">
                        {m.embedding_model} · {m.dim || '—'}d
                      </p>
                      <p className="text-xs text-[var(--color-fg-subtle)]">
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
                <div className="mt-4">
                  <p className="text-xs font-medium text-[var(--color-fg)]">{t('admin:backup.vectors.issueSamples')}</p>
                  <div className="mt-2 flex flex-col gap-2">
                    {vectorReport.issues.slice(0, 5).map((issue) => (
                      <div key={`${issue.chunk_id}:${issue.reason}`} className="rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2">
                        <p className="truncate text-xs font-medium text-[var(--color-fg)]">{issue.filename || issue.document_id}</p>
                        <p className="mt-0.5 text-[11px] text-[var(--color-fg-subtle)]">
                          {issue.reason} · {issue.embedding_model} · {issue.dim || '—'}d · {issue.chunk_id}
                        </p>
                      </div>
                    ))}
                  </div>
                </div>
              )}
            </div>
          ) : (
            <p className="mt-4 text-xs text-[var(--color-fg-subtle)]">{t('admin:backup.vectors.empty')}</p>
          )}

          <div className="mt-4 flex flex-wrap items-center gap-3">
            <Button
              variant="secondary"
              onClick={onVectorCheck}
              loading={startingVectorJob === 'check' || (runningVector?.type === 'check')}
              disabled={exportBusy || vectorBusy}
              leadingIcon={<Database size={14} aria-hidden />}
            >
              {t('admin:backup.vectors.checkAction')}
            </Button>
            <Button
              variant="secondary"
              onClick={onVectorRebuild}
              loading={startingVectorJob === 'rebuild' || (runningVector?.type === 'rebuild')}
              disabled={exportBusy || vectorBusy || vectorMissing === 0}
              leadingIcon={<Wrench size={14} aria-hidden />}
            >
              {t('admin:backup.vectors.rebuildAction')}
            </Button>
          </div>
        </div>
      </section>

      {/* Configuration archive — config-only, non-destructive. Muted section
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
            disabled={exportBusy}
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
            accept=".zip,.json,application/zip,application/json"
            className="sr-only"
            onChange={onPickConfig}
          />
          <Button
            className="mt-4"
            variant="secondary"
            onClick={() => cfgFileRef.current?.click()}
            disabled={exportBusy}
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
