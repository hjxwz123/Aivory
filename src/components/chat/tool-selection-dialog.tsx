import { useEffect, useMemo, useRef, useState } from 'react'
import { RefreshCw, Search, Wrench } from 'lucide-react'
import { toolsApi } from '@/api'
import { useWorkspaces } from '@/store/workspaces'
import {
  workspaceCapabilitiesForScope,
  workspaceMemberCanUse,
  workspacePolicyResolvedForScope,
} from '@/lib/workspace-permissions'
import type { ApiSelectableTool } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { resolveLucideIcon } from '@/lib/lucide-icons'
import { committedToolSelection } from '@/lib/tool-selection'
import {
  countSelectedTools,
  toolSegmentOf,
  toolsInSegment,
  type ToolSegment,
} from '@/lib/tool-segments'
import { subscribeAccessInvalidation } from '@/lib/access-events'
import { cn } from '@/lib/utils'
import { useTranslation } from 'react-i18next'

interface ToolSelectionDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  modelId: string
  /** Workspace scope for the catalog request. Omit to follow the active
   * workspace; pass `null` for a personal conversation. */
  workspaceId?: string | null
  /** undefined means the model defaults; [] is an explicit empty selection. */
  selectedIds?: string[]
  onChange: (ids: string[] | undefined) => void
  /** Runs only when the user confirms the dialog, not during policy cleanup. */
  onApply?: (ids: string[] | undefined) => void
}

function sameIds(left: readonly string[], right: readonly string[]): boolean {
  if (left.length !== right.length) return false
  const values = new Set(left)
  return right.every((id) => values.has(id))
}

export function ToolSelectionDialog({
  open,
  onOpenChange,
  modelId,
  workspaceId: scopedWorkspaceId,
  selectedIds,
  onChange,
  onApply,
}: ToolSelectionDialogProps) {
  const { t } = useTranslation('chat')
  const activeWorkspaceId = useWorkspaces((state) => state.activeId ?? undefined)
  const workspaceId = scopedWorkspaceId !== undefined ? scopedWorkspaceId ?? undefined : activeWorkspaceId
  const workspacesLoaded = useWorkspaces((state) => state.loaded)
  const workspacePolicyLoading = useWorkspaces((state) =>
    workspaceId ? state.policyLoading[workspaceId] === true : false,
  )
  const workspaceSwitching = useWorkspaces((state) => state.switching)
  const workspacePolicyError = useWorkspaces((state) =>
    workspaceId ? state.policyErrors[workspaceId] : null,
  )
  const activeWorkspace = useWorkspaces((state) =>
    workspaceId ? state.workspaces.find((workspace) => workspace.id === workspaceId) : undefined,
  )
  const workspacePolicy = useWorkspaces((state) =>
    workspaceId ? state.policies[workspaceId] : undefined,
  )
  const workspaceCaps = workspaceCapabilitiesForScope(workspaceId, workspacePolicy, {
    workspacesLoaded,
    policyLoading: workspacePolicyLoading,
    switching: workspaceSwitching,
    policyError: workspacePolicyError,
  })
  const workspacePolicyResolved = workspacePolicyResolvedForScope(workspaceId, workspacePolicy, {
    workspacesLoaded,
    policyLoading: workspacePolicyLoading,
    switching: workspaceSwitching,
    policyError: workspacePolicyError,
  })
  const workspaceToolCallingExplicitlyDisabled =
    workspacePolicyResolved && !workspaceCaps.toolCalling
  const canUseWorkspaceMCP = !workspaceId || workspaceMemberCanUse(activeWorkspace, 'mcp')
  const canShowMineSegment = workspaceCaps.toolCalling && workspaceCaps.mcp && canUseWorkspaceMCP
  const [tools, setTools] = useState<ApiSelectableTool[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)
  const [query, setQuery] = useState('')
  const [segment, setSegment] = useState<ToolSegment>('official')
  const [usingDefaults, setUsingDefaults] = useState(selectedIds === undefined)
  const [draftIds, setDraftIds] = useState<Set<string>>(() => new Set(selectedIds ?? []))
  const [reloadKey, setReloadKey] = useState(0)
  const backgroundReconcileRequestRef = useRef(0)

  useEffect(() => {
    if (!open || !modelId) return
    if (!workspaceCaps.toolCalling) {
      setTools([])
      setLoading(false)
      setError(false)
      setUsingDefaults(false)
      setDraftIds(new Set())
      return
    }
    let cancelled = false
    setLoading(true)
    setError(false)
    void toolsApi
      .list(modelId, workspaceId)
      .then((items) => {
        if (cancelled) return
        setTools(items)
        const allowed = new Set(items
          .filter((item) => item.allowed !== false)
          .filter((item) => canShowMineSegment || toolSegmentOf(item.id) !== 'mine')
          .map((item) => item.id))
        const defaults = items
          .filter((item) => item.allowed !== false && item.default_selected === true)
          .map((item) => item.id)
        const validSelected = selectedIds?.filter((id) => allowed.has(id))
        if (selectedIds !== undefined && validSelected && !sameIds(selectedIds, validSelected)) {
          onChange(validSelected)
        }
        setUsingDefaults(selectedIds === undefined)
        setDraftIds(new Set(validSelected ?? defaults))
      })
      .catch(() => {
        if (!cancelled) setError(true)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [canShowMineSegment, modelId, onChange, open, reloadKey, selectedIds, workspaceCaps.toolCalling, workspaceId])

  useEffect(() => {
    if (!open) {
      setQuery('')
      setSegment('official')
    }
  }, [open])

  useEffect(() => {
    if (!canShowMineSegment && segment === 'mine') setSegment('official')
  }, [canShowMineSegment, segment])

  useEffect(() => {
    // A workspace switch or policy revocation can happen while the dialog is
    // open. Drop the old segment/draft immediately so a later confirm cannot
    // submit ids from the previous scope.
    setSegment('official')
    if (workspaceToolCallingExplicitlyDisabled && selectedIds !== undefined) onChange(undefined)
    if (!workspaceCaps.toolCalling) setDraftIds(new Set())
  }, [
    onChange,
    selectedIds,
    workspaceCaps.toolCalling,
    workspaceId,
    workspaceToolCallingExplicitlyDisabled,
  ])

  useEffect(
    () => {
      const unsubscribe = subscribeAccessInvalidation((event) => {
        // §workspace RBAC: workspace policy changes reshape the tool catalog
        // alongside account-level permission changes.
        if ((event.kind !== 'account' && event.kind !== 'workspace') || !modelId) return
        if (open) {
          backgroundReconcileRequestRef.current += 1
          setReloadKey((value) => value + 1)
          return
        }
        // Persisted selections can outlive a group policy or a global tool
        // switch. Trim them in the background even when the dialog is closed.
        const requestID = ++backgroundReconcileRequestRef.current
        if (!workspaceCaps.toolCalling) return
        void toolsApi.list(modelId, workspaceId).then((items) => {
          if (requestID !== backgroundReconcileRequestRef.current || selectedIds === undefined) return
          const allowed = new Set(items
            .filter((item) => item.allowed !== false)
            .filter((item) => canShowMineSegment || toolSegmentOf(item.id) !== 'mine')
            .map((item) => item.id))
          const valid = selectedIds.filter((id) => allowed.has(id))
          if (!sameIds(selectedIds, valid)) onChange(valid)
        }).catch(() => undefined)
      })
      return () => {
        backgroundReconcileRequestRef.current += 1
        unsubscribe()
      }
    },
    [canShowMineSegment, modelId, onChange, open, selectedIds, workspaceCaps.toolCalling, workspaceId],
  )

  const presentedTools = useMemo(
    () =>
      tools.map((tool) => {
        const separator = tool.id.indexOf(':')
        const source = separator > 0 ? tool.id.slice(0, separator) : ''
        const key = separator > 0 ? tool.id.slice(separator + 1) : ''
        if ((source === 'builtin' || source === 'hosted') && key) {
          return {
            ...tool,
            displayName: t(`tools.${key}`, { defaultValue: tool.name }),
            displayDescription: t(`toolDescriptions.${key}`, { defaultValue: tool.description }),
          }
        }
        return { ...tool, displayName: tool.name, displayDescription: tool.description }
      }),
    [t, tools],
  )

  // Segment filters the VISIBLE list only; draftIds / allowedIDs / commit stay
  // on the global merged set so nothing outside the segment is ever pruned.
  const filtered = useMemo(() => {
    const inSegment = toolsInSegment(presentedTools, segment)
    const value = query.trim().toLocaleLowerCase()
    if (!value) return inSegment
    return inSegment.filter((tool) =>
      `${tool.displayName}\n${tool.displayDescription}`.toLocaleLowerCase().includes(value),
    )
  }, [presentedTools, query, segment])

  const allowedTools = useMemo(
    () => tools
      .filter((tool) => tool.allowed !== false)
      .filter((tool) => canShowMineSegment || toolSegmentOf(tool.id) !== 'mine'),
    [canShowMineSegment, tools],
  )
  const allowedIDs = useMemo(() => allowedTools.map((tool) => tool.id), [allowedTools])
  const defaultIDs = useMemo(
    () => allowedTools.filter((tool) => tool.default_selected === true).map((tool) => tool.id),
    [allowedTools],
  )
  const selectedCount = countSelectedTools(draftIds, allowedIDs)

  function toggle(id: string) {
    if (!allowedIDs.includes(id)) return
    setDraftIds((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
    setUsingDefaults(false)
  }

  function applySelection() {
    if (!workspaceCaps.toolCalling) {
      // A missing/refreshing workspace policy is intentionally rendered as
      // unavailable, but it is not an administrator decision. Closing the
      // dialog must not turn that temporary fail-closed state into a persisted
      // "use model defaults" preference. A settled tool ban is different: its
      // stale selection must be removed before the next turn.
      if (workspaceToolCallingExplicitlyDisabled) {
        onChange(undefined)
        onApply?.(undefined)
      }
      onOpenChange(false)
      return
    }
    const selection = committedToolSelection(allowedIDs, defaultIDs, draftIds)
    onChange(selection)
    onApply?.(selection)
    onOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="md" className="max-sm:h-[calc(100dvh-1rem)] max-sm:max-h-[calc(100dvh-1rem)] max-sm:w-[calc(100vw-1rem)]">
        <DialogHeader className="pr-14">
          <DialogTitle>{t('composer.toolSelection.title', { defaultValue: 'Choose tools' })}</DialogTitle>
          <DialogDescription>
            {t('composer.toolSelection.description', {
              defaultValue: 'The model will only use tools selected here.',
            })}
          </DialogDescription>
        </DialogHeader>

        <DialogBody className="flex min-h-0 flex-col overflow-hidden px-4 pb-3 sm:px-6">
          {canShowMineSegment ? (
            <div className="shrink-0 pb-2">
              <div
                className="grid w-full grid-cols-2 items-center rounded-[9px] bg-[var(--color-bg-muted)] p-1 sm:inline-flex sm:w-auto sm:shrink-0"
                role="group"
                aria-label={t('composer.toolSelection.title', { defaultValue: 'Choose tools' })}
              >
                {(['official', 'mine'] as const).map((segmentOption) => (
                  <button
                    key={segmentOption}
                    type="button"
                    aria-pressed={segment === segmentOption}
                    onClick={() => setSegment(segmentOption)}
                    className={cn(
                      'inline-flex min-w-0 items-center justify-center rounded-[7px] px-2 text-[12px] font-medium interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] h-[var(--tap-min)] sm:h-8 sm:px-2.5',
                      segment === segmentOption
                        ? 'bg-[var(--color-surface)] text-[var(--color-fg)] shadow-[var(--shadow-xs)]'
                        : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
                    )}
                  >
                    {t(`composer.toolSelection.segment.${segmentOption}`, {
                      defaultValue: segmentOption === 'official' ? 'Official tools' : 'My tools',
                    })}
                  </button>
                ))}
              </div>
            </div>
          ) : null}

          <div className="flex shrink-0 flex-wrap items-center gap-2 pb-3">
            <div className="relative min-w-[12rem] flex-1">
              <Search
                size={14}
                aria-hidden
                className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-fg-faint)]"
              />
              <Input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder={t('composer.toolSelection.search', { defaultValue: 'Search tools' })}
                aria-label={t('composer.toolSelection.search', { defaultValue: 'Search tools' })}
                className="pl-9"
              />
            </div>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                setUsingDefaults(false)
                setDraftIds(new Set(allowedIDs))
              }}
              disabled={loading || allowedTools.length === 0}
            >
              {t('composer.toolSelection.selectAll', { defaultValue: 'Select all' })}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                setUsingDefaults(false)
                setDraftIds(new Set())
              }}
              disabled={loading || allowedTools.length === 0}
            >
              {t('composer.toolSelection.clear', { defaultValue: 'Clear' })}
            </Button>
          </div>

          <div
            className="min-h-0 max-h-[min(56dvh,30rem)] flex-1 overflow-y-auto overscroll-contain border-y border-[var(--color-divider)] scrollbar-thin"
            role="region"
            aria-label={t('composer.toolSelection.title', { defaultValue: 'Choose tools' })}
          >
            {loading ? (
              <div className="space-y-1 py-2" aria-label={t('composer.toolSelection.loading', { defaultValue: 'Loading tools' })}>
                {[0, 1, 2].map((item) => (
                  <div key={item} className="flex min-h-16 animate-pulse items-center gap-3 px-2 py-2">
                    <span className="size-8 rounded-[7px] bg-[var(--color-bg-muted)]" />
                    <span className="flex-1 space-y-2">
                      <span className="block h-3 w-32 rounded bg-[var(--color-bg-muted)]" />
                      <span className="block h-2.5 w-4/5 rounded bg-[var(--color-bg-muted)]" />
                    </span>
                  </div>
                ))}
              </div>
            ) : error ? (
              <div className="flex min-h-48 flex-col items-center justify-center px-5 py-8 text-center">
                <p className="text-sm text-[var(--color-fg-muted)]">
                  {t('composer.toolSelection.loadFailed', { defaultValue: 'Tools could not be loaded.' })}
                </p>
                <Button
                  variant="ghost"
                  size="sm"
                  className="mt-2"
                  leadingIcon={<RefreshCw size={14} aria-hidden />}
                  onClick={() => setReloadKey((value) => value + 1)}
                >
                  {t('composer.toolSelection.retry', { defaultValue: 'Try again' })}
                </Button>
              </div>
            ) : filtered.length === 0 ? (
              <div className="flex min-h-48 items-center justify-center px-5 py-8 text-center text-sm text-[var(--color-fg-muted)]">
                {query ? (
                  t('composer.toolSelection.noResults', { defaultValue: 'No matching tools.' })
                ) : segment === 'mine' ? (
                  t('composer.toolSelection.segmentEmpty', {
                    defaultValue: 'No MCP services yet — add one in the Library.',
                  })
                ) : (
                  t('composer.toolSelection.empty', { defaultValue: 'No tools are available for this model.' })
                )}
              </div>
            ) : (
              <div className="divide-y divide-[var(--color-divider)]">
                {filtered.map((tool) => {
                  const Icon = resolveLucideIcon(tool.icon) ?? Wrench
                  const allowed = tool.allowed !== false
                  const checked = allowed && draftIds.has(tool.id)
                  return (
                    <label
                      key={tool.id}
                      className={cn(
                        'flex min-h-16 cursor-pointer items-start gap-3 rounded-[7px] px-2 py-2.5 interactive focus-within:ring-2 focus-within:ring-inset focus-within:ring-[var(--color-ring)] hover:bg-[var(--color-bg-muted)]',
                        checked && 'bg-[var(--color-tool-selection-soft)]/60',
                        !allowed && 'cursor-not-allowed opacity-55 hover:bg-transparent',
                      )}
                    >
                      <span
                        className={cn(
                          'mt-0.5 inline-flex size-8 shrink-0 items-center justify-center rounded-[7px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]',
                          checked && 'text-[var(--color-tool-selection-text)]',
                        )}
                      >
                        <Icon size={16} aria-hidden />
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className="block break-words text-[13px] font-medium text-[var(--color-fg)]">
                          {tool.displayName}
                        </span>
                        {tool.displayDescription ? (
                          <span className="mt-0.5 block break-words text-[11.5px] leading-[1.45] text-[var(--color-fg-subtle)]">
                            {tool.displayDescription}
                          </span>
                        ) : null}
                        {!allowed ? (
                          <span className="mt-1 block text-[11.5px] text-[var(--color-danger)]">
                            {t('composer.toolSelection.groupRestricted', {
                              defaultValue: 'Not available to your user group',
                            })}
                          </span>
                        ) : null}
                      </span>
                      <span className="mt-1 inline-flex shrink-0">
                        <Checkbox
                          checked={checked}
                          disabled={!allowed}
                          onChange={() => toggle(tool.id)}
                          aria-label={tool.displayName}
                        />
                      </span>
                    </label>
                  )
                })}
              </div>
            )}
          </div>

          {!loading && !error ? (
            <p className="shrink-0 pt-3 text-[12px] text-[var(--color-fg-subtle)]" aria-live="polite">
              {usingDefaults
                ? t('composer.toolSelection.defaultSelected', { count: selectedCount, defaultValue: 'Model default · {{count}} selected' })
                : t('composer.toolSelection.selectedCount', {
                    count: selectedCount,
                    defaultValue: '{{count}} tools selected',
                  })}
            </p>
          ) : null}
        </DialogBody>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t('composer.toolSelection.cancel', { defaultValue: 'Cancel' })}
          </Button>
          <Button onClick={applySelection} disabled={loading || error || allowedTools.length === 0}>
            {t('composer.toolSelection.done', { defaultValue: 'Done' })}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
