import { useCallback, useEffect, useRef, useState } from 'react'
import { AlertTriangle, CheckCircle2, Download, ExternalLink, FileText, RefreshCw, ServerCog } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { systemUpdateApi, type SystemUpdateRelease, type SystemUpdateState } from '@/api/system-update'
import { Markdown } from '@/components/chat/markdown'
import { Badge } from '@/components/ui/badge'
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

const ACTIVE_STATUSES = new Set(['pulling', 'restarting', 'checking'])

export interface SystemUpdateSummary {
  currentVersion: string
  updateAvailable: boolean
  updating: boolean
}

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSummaryChange: (summary: SystemUpdateSummary) => void
}

function progressLabelKey(progress?: string): string {
  switch (progress) {
    case 'pulling_image': return 'pulling'
    case 'recreating_app': return 'restarting'
    case 'waiting_for_health': return 'checkingHealth'
    case 'completed': return 'completed'
    case 'failed': return 'failed'
    default: return 'preparing'
  }
}

export function SystemUpdateDialog({ open, onOpenChange, onSummaryChange }: Props) {
  const { t, i18n } = useTranslation('chat')
  const [state, setState] = useState<SystemUpdateState | null>(null)
  const [loading, setLoading] = useState(true)
  const [checking, setChecking] = useState(false)
  const [starting, setStarting] = useState(false)
  const [notesOpen, setNotesOpen] = useState(false)
  const [selectedVersion, setSelectedVersion] = useState('')
  const mounted = useRef(true)

  const applyState = useCallback((next: SystemUpdateState) => {
    if (!mounted.current) return
    setState(next)
    setLoading(false)
    setSelectedVersion((current) => {
      const releases = next.releases ?? []
      if (releases.some((release) => release.version === current)) return current
      return releases.find((release) => release.installable)?.version ?? releases[0]?.version ?? ''
    })
    onSummaryChange({
      currentVersion: next.current_version || 'dev',
      updateAvailable: next.update_available,
      updating: ACTIVE_STATUSES.has(next.job?.status ?? ''),
    })
  }, [onSummaryChange])

  const load = useCallback(async (foreground = false) => {
    try {
      applyState(await systemUpdateApi.state(foreground ? 'foreground' : 'background'))
      return true
    } catch {
      if (foreground && mounted.current) toast.error(t('userMenu.systemUpdate.loadFailed'))
      return false
    } finally {
      if (mounted.current) setLoading(false)
    }
  }, [applyState, t])

  useEffect(() => {
    mounted.current = true
    void load(false)
    const interval = window.setInterval(() => void load(false), 5 * 60 * 1000)
    return () => {
      mounted.current = false
      window.clearInterval(interval)
    }
  }, [load])

  const active = ACTIVE_STATUSES.has(state?.job?.status ?? '')
  useEffect(() => {
    if (!active) return
    const interval = window.setInterval(() => void load(false), 3000)
    return () => window.clearInterval(interval)
  }, [active, load])

  async function checkNow() {
    setChecking(true)
    try {
      applyState(await systemUpdateApi.check())
    } catch {
      toast.error(t('userMenu.systemUpdate.checkFailed'))
    } finally {
      if (mounted.current) setChecking(false)
    }
  }

  async function startUpdate() {
    const version = selectedVersion
    if (!version) return
    setStarting(true)
    try {
      const result = await systemUpdateApi.start(version)
      applyState({ ...state!, job: result.job })
      toast.info(t('userMenu.systemUpdate.started'), t('userMenu.systemUpdate.keepOpen'))
    } catch {
      toast.error(t('userMenu.systemUpdate.startFailed'))
    } finally {
      if (mounted.current) setStarting(false)
    }
  }

  const selectedRelease = state?.releases?.find((release) => release.version === selectedVersion)
  const published = selectedRelease?.published_at
    ? new Intl.DateTimeFormat(i18n.language, { dateStyle: 'medium' }).format(new Date(selectedRelease.published_at))
    : null
  const jobFailed = state?.job?.status === 'failed'
  const jobCompleted = state?.job?.status === 'completed'
  const canUpdate = Boolean(state?.configured && selectedRelease?.installable && !active)

  function openNotes() {
    onOpenChange(false)
    window.setTimeout(() => setNotesOpen(true), 120)
  }

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent size="md">
          <DialogHeader>
            <div className="flex items-center gap-3 pr-8">
              <span className="flex size-9 shrink-0 items-center justify-center rounded-[8px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                <ServerCog size={18} aria-hidden />
              </span>
              <div className="min-w-0">
                <DialogTitle>{t('userMenu.systemUpdate.title')}</DialogTitle>
                <DialogDescription className="mt-0.5">{t('userMenu.systemUpdate.description')}</DialogDescription>
              </div>
            </div>
          </DialogHeader>
          <DialogBody className="space-y-4">
            {loading && !state ? (
              <div className="flex min-h-32 items-center justify-center text-[var(--color-fg-muted)]">
                <RefreshCw className="animate-spin" size={18} aria-hidden />
              </div>
            ) : (
              <>
                <div className="grid grid-cols-2 gap-px overflow-hidden rounded-[8px] border border-[var(--color-border)] bg-[var(--color-divider)]">
                  <div className="bg-[var(--color-surface)] p-3.5">
                    <div className="text-xs text-[var(--color-fg-subtle)]">{t('userMenu.systemUpdate.current')}</div>
                    <div className="mt-1 font-mono text-sm font-semibold text-[var(--color-fg)]">v{state?.current_version ?? 'dev'}</div>
                  </div>
                  <div className="bg-[var(--color-surface)] p-3.5">
                    <div className="text-xs text-[var(--color-fg-subtle)]">{t('userMenu.systemUpdate.selected')}</div>
                    <div className="mt-1 flex items-center gap-2">
                      <span className="font-mono text-sm font-semibold text-[var(--color-fg)]">
                        {selectedRelease ? `v${selectedRelease.version}` : t('userMenu.systemUpdate.unknown')}
                      </span>
                      {selectedRelease?.prerelease ? (
                        <Badge size="xs" variant="info">{t('userMenu.systemUpdate.testRelease')}</Badge>
                      ) : state?.update_available && selectedRelease?.version === state.latest_version ? (
                        <Badge size="xs" variant="warning">{t('userMenu.systemUpdate.available')}</Badge>
                      ) : null}
                    </div>
                  </div>
                </div>

                {(state?.releases?.length ?? 0) > 0 && (
                  <div>
                    <div className="mb-2 text-xs font-medium text-[var(--color-fg-muted)]">{t('userMenu.systemUpdate.releaseChannel')}</div>
                    <div className="flex gap-1 rounded-[8px] bg-[var(--color-bg-muted)] p-1" role="radiogroup" aria-label={t('userMenu.systemUpdate.releaseChannel')}>
                      {state?.releases?.map((release) => (
                        <ReleaseChoice
                          key={release.version}
                          release={release}
                          selected={release.version === selectedVersion}
                          onSelect={() => setSelectedVersion(release.version)}
                          stableLabel={t('userMenu.systemUpdate.stableRelease')}
                          testLabel={t('userMenu.systemUpdate.testRelease')}
                        />
                      ))}
                    </div>
                  </div>
                )}

                {active && (
                  <div className="rounded-[8px] border border-[var(--color-info)]/20 bg-[var(--color-info-soft)] p-3.5">
                    <div className="flex items-center gap-2 text-sm font-medium text-[var(--color-info)]">
                      <RefreshCw className="animate-spin" size={15} aria-hidden />
                      {t(`userMenu.systemUpdate.progress.${progressLabelKey(state?.job?.progress)}`)}
                    </div>
                    <p className="mt-1.5 text-xs leading-relaxed text-[var(--color-fg-muted)]">{t('userMenu.systemUpdate.reconnect')}</p>
                  </div>
                )}
                {jobCompleted && (
                  <div className="flex items-start gap-2 rounded-[8px] border border-[var(--color-success)]/20 bg-[var(--color-success-soft)] p-3.5 text-sm text-[var(--color-success)]">
                    <CheckCircle2 className="mt-0.5 shrink-0" size={15} aria-hidden />
                    {t('userMenu.systemUpdate.success')}
                  </div>
                )}
                {(jobFailed || state?.check_error || state?.updater_error || !state?.configured) && (
                  <div className="flex items-start gap-2 rounded-[8px] border border-[var(--color-warning)]/25 bg-[var(--color-warning-soft)] p-3.5 text-sm text-[var(--color-fg)]">
                    <AlertTriangle className="mt-0.5 shrink-0 text-[var(--color-warning)]" size={15} aria-hidden />
                    <span>
                      {jobFailed
                        ? t('userMenu.systemUpdate.failedDetail', { error: state?.job?.error || t('userMenu.systemUpdate.unknownError') })
                        : state?.check_error
                          ? t('userMenu.systemUpdate.releaseUnavailable')
                          : state?.updater_error
                            ? t('userMenu.systemUpdate.updaterUnavailable')
                            : t('userMenu.systemUpdate.notConfigured')}
                    </span>
                  </div>
                )}

                {selectedRelease && (
                  <div className="flex items-center justify-between gap-3 border-t border-[var(--color-divider)] pt-3">
                    <div className="min-w-0">
                      <div className="truncate text-sm font-medium text-[var(--color-fg)]">{selectedRelease.name || `v${selectedRelease.version}`}</div>
                      {published && <div className="mt-0.5 text-xs text-[var(--color-fg-subtle)]">{published}</div>}
                    </div>
                    <Button variant="ghost" size="sm" leadingIcon={<FileText size={14} />} onClick={openNotes}>
                      {t('userMenu.systemUpdate.viewNotes')}
                    </Button>
                  </div>
                )}
              </>
            )}
          </DialogBody>
          <DialogFooter className="justify-between">
            <Button variant="ghost" size="sm" leadingIcon={<RefreshCw size={14} />} loading={checking} disabled={active} onClick={checkNow}>
              {t('userMenu.systemUpdate.check')}
            </Button>
            <Button leadingIcon={<Download size={15} />} loading={starting || active} disabled={!canUpdate} onClick={startUpdate}>
              {active ? t('userMenu.systemUpdate.updating') : selectedRelease?.installable ? t('userMenu.systemUpdate.updateNow') : t('userMenu.systemUpdate.upToDate')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={notesOpen} onOpenChange={setNotesOpen}>
        <DialogContent size="lg" className="h-[min(46rem,calc(100dvh-2rem))] overflow-hidden">
          <DialogHeader>
            <DialogTitle>{selectedRelease?.name || t('userMenu.systemUpdate.releaseNotes')}</DialogTitle>
            <DialogDescription>{selectedRelease ? `v${selectedRelease.version}${published ? ` · ${published}` : ''}` : ''}</DialogDescription>
          </DialogHeader>
          <DialogBody className="overscroll-contain">
            {selectedRelease?.notes ? (
              <div className="mx-auto w-full max-w-[72ch]">
                <Markdown content={selectedRelease.notes} className="prose-full text-sm" />
              </div>
            ) : (
              <p className="py-8 text-center text-sm text-[var(--color-fg-muted)]">{t('userMenu.systemUpdate.noNotes')}</p>
            )}
          </DialogBody>
          <DialogFooter>
            {selectedRelease?.url && (
              <Button asChild variant="secondary" leadingIcon={<ExternalLink size={14} />}>
                <a href={selectedRelease.url} target="_blank" rel="noreferrer">{t('userMenu.systemUpdate.openRelease')}</a>
              </Button>
            )}
            <Button onClick={() => setNotesOpen(false)}>{t('userMenu.systemUpdate.close')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

function ReleaseChoice({
  release,
  selected,
  onSelect,
  stableLabel,
  testLabel,
}: {
  release: SystemUpdateRelease
  selected: boolean
  onSelect: () => void
  stableLabel: string
  testLabel: string
}) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onSelect}
      className={`min-w-0 flex-1 rounded-[6px] px-3 py-2 text-left interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] ${
        selected
          ? 'bg-[var(--color-surface)] text-[var(--color-fg)] shadow-[var(--shadow-xs)]'
          : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]'
      }`}
    >
      <span className="block truncate text-xs font-medium">{release.prerelease ? testLabel : stableLabel}</span>
      <span className="mt-0.5 block truncate font-mono text-xs">v{release.version}</span>
    </button>
  )
}
