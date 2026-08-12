import { useEffect, useMemo, useState } from 'react'
import { RefreshCw, Search, Wrench } from 'lucide-react'
import { toolsApi } from '@/api'
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
import { cn } from '@/lib/utils'
import { useTranslation } from 'react-i18next'

interface ToolSelectionDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  modelId: string
  /** undefined means all available tools; [] is an explicit empty selection. */
  selectedIds?: string[]
  onChange: (ids: string[] | undefined) => void
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
  selectedIds,
  onChange,
}: ToolSelectionDialogProps) {
  const { t } = useTranslation('chat')
  const [tools, setTools] = useState<ApiSelectableTool[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)
  const [query, setQuery] = useState('')
  const [draftAll, setDraftAll] = useState(selectedIds === undefined)
  const [draftIds, setDraftIds] = useState<Set<string>>(() => new Set(selectedIds ?? []))
  const [reloadKey, setReloadKey] = useState(0)

  useEffect(() => {
    if (!open || !modelId) return
    let cancelled = false
    setLoading(true)
    setError(false)
    void toolsApi
      .list(modelId)
      .then((items) => {
        if (cancelled) return
        setTools(items)
        const available = new Set(items.map((item) => item.id))
        const validSelected = selectedIds?.filter((id) => available.has(id))
        if (selectedIds !== undefined && validSelected && !sameIds(selectedIds, validSelected)) {
          onChange(validSelected)
        }
        setDraftAll(selectedIds === undefined)
        setDraftIds(new Set(validSelected ?? items.map((item) => item.id)))
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
  }, [modelId, onChange, open, reloadKey, selectedIds])

  useEffect(() => {
    if (!open) setQuery('')
  }, [open])

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

  const filtered = useMemo(() => {
    const value = query.trim().toLocaleLowerCase()
    if (!value) return presentedTools
    return presentedTools.filter((tool) =>
      `${tool.displayName}\n${tool.displayDescription}`.toLocaleLowerCase().includes(value),
    )
  }, [presentedTools, query])

  const selectedCount = draftAll ? tools.length : draftIds.size

  function toggle(id: string) {
    setDraftIds((current) => {
      const next = draftAll ? new Set(tools.map((tool) => tool.id)) : new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
    setDraftAll(false)
  }

  function applySelection() {
    onChange(committedToolSelection(tools.map((tool) => tool.id), draftIds, draftAll))
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

        <DialogBody className="flex flex-col px-4 pb-3 sm:px-6">
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
                setDraftAll(true)
                setDraftIds(new Set(tools.map((tool) => tool.id)))
              }}
              disabled={loading || tools.length === 0}
            >
              {t('composer.toolSelection.selectAll', { defaultValue: 'Select all' })}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                setDraftAll(false)
                setDraftIds(new Set())
              }}
              disabled={loading || tools.length === 0}
            >
              {t('composer.toolSelection.clear', { defaultValue: 'Clear' })}
            </Button>
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain border-y border-[var(--color-divider)] scrollbar-thin">
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
                {query
                  ? t('composer.toolSelection.noResults', { defaultValue: 'No matching tools.' })
                  : t('composer.toolSelection.empty', { defaultValue: 'No tools are available for this model.' })}
              </div>
            ) : (
              <div className="divide-y divide-[var(--color-divider)]">
                {filtered.map((tool) => {
                  const Icon = resolveLucideIcon(tool.icon) ?? Wrench
                  const checked = draftAll || draftIds.has(tool.id)
                  return (
                    <label
                      key={tool.id}
                      className={cn(
                        'flex min-h-16 cursor-pointer items-start gap-3 rounded-[7px] px-2 py-2.5 interactive focus-within:ring-2 focus-within:ring-inset focus-within:ring-[var(--color-ring)] hover:bg-[var(--color-bg-muted)]',
                        checked && 'bg-[var(--color-tool-selection-soft)]/60',
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
                      </span>
                      <Checkbox
                        checked={checked}
                        onChange={() => toggle(tool.id)}
                        aria-label={tool.displayName}
                        className="mt-1"
                      />
                    </label>
                  )
                })}
              </div>
            )}
          </div>

          {!loading && !error ? (
            <p className="shrink-0 pt-3 text-[12px] text-[var(--color-fg-subtle)]" aria-live="polite">
              {draftAll
                ? t('composer.toolSelection.allSelected', { count: tools.length, defaultValue: 'All {{count}} tools selected' })
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
          <Button onClick={applySelection} disabled={loading || error}>
            {t('composer.toolSelection.done', { defaultValue: 'Done' })}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
