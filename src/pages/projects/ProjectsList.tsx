import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Search, FolderKanban } from 'lucide-react'
import { useProjects } from '@/store/projects'
import { useConversations } from '@/store/conversations'
import { ContentHeader } from '@/components/layout/content-header'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { ProjectRow } from '@/components/projects/project-row'
import { NewProjectDialog } from '@/components/projects/new-project-dialog'
import { cn } from '@/lib/utils'

type Filter = 'all' | 'pinned'

export default function ProjectsList() {
  const { t } = useTranslation(['projects', 'common'])
  const projects = useProjects((s) => s.projects)
  const conversations = useConversations((s) => s.conversations)
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState<Filter>('all')
  const [createOpen, setCreateOpen] = useState(false)

  const chatCounts = useMemo(() => {
    const map = new Map<string, number>()
    for (const c of conversations) {
      if (!c.projectId || c.archived) continue
      map.set(c.projectId, (map.get(c.projectId) ?? 0) + 1)
    }
    return map
  }, [conversations])

  // Sort once, then split into pinned + rest for the two-band editorial layout.
  const sorted = useMemo(() => {
    const q = query.trim().toLowerCase()
    return projects
      .filter((p) => {
        if (filter === 'pinned' && !p.pinned) return false
        if (!q) return true
        return (
          p.name.toLowerCase().includes(q) ||
          (p.description ?? '').toLowerCase().includes(q) ||
          p.instructions.toLowerCase().includes(q)
        )
      })
      .slice()
      .sort((a, b) => b.updatedAt - a.updatedAt)
  }, [projects, query, filter])

  const pinned = useMemo(() => sorted.filter((p) => p.pinned), [sorted])
  const rest = useMemo(
    () => (filter === 'pinned' ? [] : sorted.filter((p) => !p.pinned)),
    [sorted, filter],
  )
  const totalVisible = sorted.length

  return (
    <div className="flex-1 min-h-0 flex flex-col bg-[var(--color-bg)] text-[var(--color-fg)]">
      <ContentHeader
        title={t('projects:list.title')}
        actions={
          <Button
            variant="secondary"
            size="sm"
            leadingIcon={<Plus size={15} aria-hidden />}
            onClick={() => setCreateOpen(true)}
          >
            {t('projects:list.createCta')}
          </Button>
        }
      />
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-5 sm:px-8 py-8 pb-24">
          <p className="max-w-[60ch] text-[var(--color-fg-muted)] text-[15px] leading-relaxed">
            {t('projects:list.subtitle')}
          </p>

          {/* Controls strip. Lives directly above the list, separated by a
              single hairline (Section 4.4: no card containers around tools). */}
          {projects.length > 0 ? (
            <div className="mt-8 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between border-b border-[var(--color-divider)] pb-4">
            <div className="sm:max-w-xs w-full">
              <Input
                leadingIcon={<Search size={14} aria-hidden />}
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder={t('projects:list.searchPlaceholder')}
                aria-label={t('projects:list.searchPlaceholder')}
              />
            </div>
            <div
              role="tablist"
              aria-label={t('projects:list.title')}
              className="inline-flex items-center gap-4 text-[12.5px] self-start sm:self-auto"
            >
              <FilterTab
                active={filter === 'all'}
                onClick={() => setFilter('all')}
                count={projects.length}
              >
                {t('projects:list.filterAll')}
              </FilterTab>
              <FilterTab
                active={filter === 'pinned'}
                onClick={() => setFilter('pinned')}
                count={projects.filter((p) => p.pinned).length}
              >
                {t('projects:list.filterPinned')}
              </FilterTab>
            </div>
          </div>
        ) : null}

        {/* List body */}
        {totalVisible > 0 ? (
          <div className="mt-2">
            {pinned.length > 0 && filter !== 'pinned' ? (
              <Band label={t('projects:list.filterPinned')}>
                <RowList>
                  {pinned.map((p) => (
                    <ProjectRow
                      key={p.id}
                      project={p}
                      chatCount={chatCounts.get(p.id) ?? 0}
                    />
                  ))}
                </RowList>
              </Band>
            ) : null}

            {rest.length > 0 || filter === 'pinned' ? (
              <Band label={filter === 'pinned' ? undefined : t('projects:list.filterAll')}>
                <RowList>
                  {(filter === 'pinned' ? sorted : rest).map((p) => (
                    <ProjectRow
                      key={p.id}
                      project={p}
                      chatCount={chatCounts.get(p.id) ?? 0}
                    />
                  ))}
                </RowList>
              </Band>
            ) : null}
          </div>
        ) : (
          <EmptyState
            className="mt-12"
            icon={<FolderKanban size={20} aria-hidden />}
            title={t('projects:list.emptyTitle')}
            description={t('projects:list.emptyBody')}
            action={
              <Button
                variant="secondary"
                leadingIcon={<Plus size={15} aria-hidden />}
                onClick={() => setCreateOpen(true)}
              >
                {t('projects:list.createCta')}
              </Button>
            }
          />
        )}
        </div>
      </div>

      <NewProjectDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  )
}

/* Band groups a contiguous run of rows under an optional small label.
   The label is intentionally small + sentence-case (not an uppercase
   eyebrow) so it reads as a section divider, not section chrome. */
function Band({ label, children }: { label?: string; children: React.ReactNode }) {
  return (
    <section className="mt-10 first:mt-6">
      {label ? (
        <h2 className="text-[13.5px] text-[var(--color-fg-subtle)] tracking-tight mb-1 px-4 sm:px-6 -mx-4 sm:-mx-6">
          {label}
        </h2>
      ) : null}
      {children}
    </section>
  )
}

function RowList({ children }: { children: React.ReactNode }) {
  return (
    <ul className="flex flex-col divide-y divide-[var(--color-divider)]">
      {Array.isArray(children)
        ? children.map((c, i) => <li key={i}>{c}</li>)
        : <li>{children}</li>}
    </ul>
  )
}

function FilterTab({
  active,
  onClick,
  count,
  children,
}: {
  active: boolean
  onClick: () => void
  count?: number
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onClick}
      className={cn(
        'group/tab inline-flex items-baseline gap-1.5 py-1 interactive',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] rounded-[6px]',
        active
          ? 'text-[var(--color-fg)]'
          : 'text-[var(--color-fg-subtle)] hover:text-[var(--color-fg-muted)]',
      )}
    >
      <span
        className={cn(
          'relative',
          active &&
            'after:absolute after:left-0 after:right-0 after:-bottom-[5px] after:h-px after:bg-[var(--color-fg)]',
        )}
      >
        {children}
      </span>
      {typeof count === 'number' ? (
        <span className="text-[10.5px] text-[var(--color-fg-subtle)] tabular-nums">
          {count}
        </span>
      ) : null}
    </button>
  )
}
