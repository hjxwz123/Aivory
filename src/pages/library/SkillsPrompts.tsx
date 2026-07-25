import { type Dispatch, type ReactNode, type SetStateAction, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Check,
  FileText,
  LibraryBig,
  MoreHorizontal,
  Pencil,
  Plus,
  Search,
  Sparkles,
  Trash2,
} from 'lucide-react'
import { libraryApi, ApiError } from '@/api'
import type {
  ApiLibraryCatalog,
  ApiLibraryCatalogPrompt,
  ApiLibraryCatalogSkill,
  ApiUserPrompt,
  ApiUserSkill,
} from '@/api/types'
import { ContentHeader } from '@/components/layout/content-header'
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { EmptyState } from '@/components/ui/empty-state'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { SkillIcon } from '@/components/ui/skill-icon'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/hooks/use-toast'
import { skillDisplayDescription } from '@/lib/skill-description'
import { isValidSkillName, parseSkillDocument } from '@/lib/skill-document'
import { cn } from '@/lib/utils'

type ItemKind = 'skill' | 'prompt'
type KindFilter = 'all' | ItemKind

interface EditorState {
  open: boolean
  kind: ItemKind
  id?: string
  name: string
  description: string
  content: string
  importText: string
}

type DeleteTarget =
  | { kind: 'skill'; item: ApiUserSkill }
  | { kind: 'prompt'; item: ApiUserPrompt }

const emptyCatalog: ApiLibraryCatalog = { skills: [], prompts: [] }

function newEditor(kind: ItemKind): EditorState {
  return { open: true, kind, name: '', description: '', content: '', importText: '' }
}

export default function SkillsPrompts() {
  const { t } = useTranslation(['library', 'common'])
  const [skills, setSkills] = useState<ApiUserSkill[]>([])
  const [prompts, setPrompts] = useState<ApiUserPrompt[]>([])
  const [catalog, setCatalog] = useState<ApiLibraryCatalog>(emptyCatalog)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [query, setQuery] = useState('')
  const [kindFilter, setKindFilter] = useState<KindFilter>('all')
  const [tab, setTab] = useState<'mine' | 'catalog'>('mine')
  const [editor, setEditor] = useState<EditorState>(() => ({ ...newEditor('skill'), open: false }))
  const [deleteTarget, setDeleteTarget] = useState<DeleteTarget | null>(null)
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [addingCatalogId, setAddingCatalogId] = useState('')
  const savingRef = useRef(false)
  const deletingRef = useRef(false)

  async function load() {
    setLoading(true)
    setLoadError('')
    try {
      const [userSkills, userPrompts, publicCatalog] = await Promise.all([
        libraryApi.skills(),
        libraryApi.prompts(),
        libraryApi.catalog(),
      ])
      setSkills(userSkills)
      setPrompts(userPrompts)
      setCatalog(publicCatalog)
    } catch (error) {
      const message = error instanceof ApiError ? error.message : t('common:common.error')
      setLoadError(message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const normalizedQuery = query.trim().toLocaleLowerCase()
  const filteredSkills = useMemo(
    () =>
      skills.filter(
        (item) =>
          kindFilter !== 'prompt' &&
          (!normalizedQuery ||
            item.name.toLocaleLowerCase().includes(normalizedQuery) ||
            skillDisplayDescription(item).toLocaleLowerCase().includes(normalizedQuery)),
      ),
    [kindFilter, normalizedQuery, skills],
  )
  const filteredPrompts = useMemo(
    () =>
      prompts.filter(
        (item) =>
          kindFilter !== 'skill' &&
          (!normalizedQuery ||
            item.name.toLocaleLowerCase().includes(normalizedQuery) ||
            item.description.toLocaleLowerCase().includes(normalizedQuery)),
      ),
    [kindFilter, normalizedQuery, prompts],
  )
  const filteredCatalogSkills = useMemo(
    () =>
      catalog.skills.filter(
        (item) =>
          kindFilter !== 'prompt' &&
          (!normalizedQuery ||
            item.name.toLocaleLowerCase().includes(normalizedQuery) ||
            skillDisplayDescription(item).toLocaleLowerCase().includes(normalizedQuery)),
      ),
    [catalog.skills, kindFilter, normalizedQuery],
  )
  const filteredCatalogPrompts = useMemo(
    () =>
      catalog.prompts.filter(
        (item) =>
          kindFilter !== 'skill' &&
          (!normalizedQuery ||
            item.name.toLocaleLowerCase().includes(normalizedQuery) ||
            item.description.toLocaleLowerCase().includes(normalizedQuery)),
      ),
    [catalog.prompts, kindFilter, normalizedQuery],
  )

  function editSkill(item: ApiUserSkill) {
    setEditor({
      open: true,
      kind: 'skill',
      id: item.id,
      name: item.name,
      description: item.description,
      content: item.instructions,
      importText: '',
    })
  }

  function editPrompt(item: ApiUserPrompt) {
    setEditor({
      open: true,
      kind: 'prompt',
      id: item.id,
      name: item.name,
      description: item.description,
      content: item.content,
      importText: '',
    })
  }

  function applySkillImport() {
    const parsed = parseSkillDocument(editor.importText)
    if (!parsed.name || !parsed.description || !parsed.instructions) {
      toast.error(t('library:editor.importInvalid'))
      return
    }
    setEditor((current) => ({
      ...current,
      name: parsed.name ?? current.name,
      description: parsed.description ?? current.description,
      content: parsed.instructions || current.content,
    }))
    toast.success(t('library:editor.imported'))
  }

  async function saveEditor() {
    if (savingRef.current) return
    const name = editor.name.trim()
    const description = editor.description.trim()
    const content = editor.content.trim()
    if (!name || !description || !content) {
      toast.error(t('library:editor.missingFields'))
      return
    }
    if (editor.kind === 'skill' && !isValidSkillName(name)) {
      toast.error(t('library:editor.invalidSkillName'))
      return
    }
    savingRef.current = true
    setSaving(true)
    try {
      if (editor.kind === 'skill') {
        const body = { name, description, instructions: content }
        if (editor.id) await libraryApi.updateSkill(editor.id, body)
        else await libraryApi.createSkill(body)
      } else {
        const body = { name, description, content }
        if (editor.id) await libraryApi.updatePrompt(editor.id, body)
        else await libraryApi.createPrompt(body)
      }
      toast.success(editor.id ? t('library:editor.updated') : t('library:editor.created'))
      setEditor((current) => ({ ...current, open: false }))
      await load()
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) {
        toast.error(t('library:editor.nameExists'))
      } else {
        toast.error(error instanceof ApiError ? error.message : t('common:common.error'))
      }
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  async function removeTarget() {
    if (!deleteTarget || deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      if (deleteTarget.kind === 'skill') await libraryApi.removeSkill(deleteTarget.item.id)
      else await libraryApi.removePrompt(deleteTarget.item.id)
      toast.success(t('library:remove.done'))
      setDeleteTarget(null)
      await load()
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('common:common.error'))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  async function addCatalogItem(kind: ItemKind, id: string) {
    if (addingCatalogId) return
    setAddingCatalogId(`${kind}:${id}`)
    try {
      if (kind === 'skill') await libraryApi.addCatalogSkill(id)
      else await libraryApi.addCatalogPrompt(id)
      toast.success(t('library:catalog.added'))
      await load()
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) {
        toast.error(t('library:editor.nameExists'))
      } else {
        toast.error(error instanceof ApiError ? error.message : t('common:common.error'))
      }
    } finally {
      setAddingCatalogId('')
    }
  }

  const mineEmpty = filteredSkills.length === 0 && filteredPrompts.length === 0
  const catalogEmpty = filteredCatalogSkills.length === 0 && filteredCatalogPrompts.length === 0

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-[var(--color-bg)] text-[var(--color-fg)]">
      <ContentHeader
        title={t('library:title')}
        actions={
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                size="sm"
                variant="secondary"
                leadingIcon={<Plus size={15} aria-hidden />}
                className="max-sm:min-h-[var(--tap-min)]"
              >
                {t('library:new')}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={() => setEditor(newEditor('skill'))}>
                <Sparkles size={14} aria-hidden /> {t('library:kinds.skill')}
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={() => setEditor(newEditor('prompt'))}>
                <FileText size={14} aria-hidden /> {t('library:kinds.prompt')}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        }
      />

      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-4 pb-24 pt-4 sm:px-8 sm:pt-6">
          <Tabs value={tab} onValueChange={(value) => setTab(value as typeof tab)}>
            <div className="flex flex-col gap-3 border-b border-[var(--color-divider)] pb-4 md:flex-row md:items-center md:justify-between">
              <TabsList variant="segmented" className="self-start border-0">
                <TabsTrigger value="mine" variant="segmented">{t('library:tabs.mine')}</TabsTrigger>
                <TabsTrigger value="catalog" variant="segmented">{t('library:tabs.catalog')}</TabsTrigger>
              </TabsList>
              <div className="flex min-w-0 flex-col gap-2 sm:flex-row sm:items-center">
                <div className="min-w-0 flex-1 sm:w-64 sm:flex-none">
                  <Input
                    value={query}
                    onChange={(event) => setQuery(event.target.value)}
                    leadingIcon={<Search size={14} aria-hidden />}
                    placeholder={t('library:search')}
                    aria-label={t('library:search')}
                    wrapperClassName="max-sm:h-[var(--tap-min)]"
                  />
                </div>
                <KindFilterControl value={kindFilter} onChange={setKindFilter} t={t} />
              </div>
            </div>

            {loading ? (
              <LibrarySkeleton label={t('common:common.loading')} />
            ) : loadError ? (
              <EmptyState
                className="py-14"
                icon={<LibraryBig size={20} aria-hidden />}
                title={t('common:common.error')}
                description={loadError}
                action={<Button variant="secondary" onClick={() => void load()}>{t('common:actions.tryAgain')}</Button>}
              />
            ) : (
              <>
                <TabsContent value="mine" className="mt-0">
                  {mineEmpty ? (
                    <EmptyState
                      className="py-14"
                      icon={<LibraryBig size={20} aria-hidden />}
                      title={query ? t('library:empty.search') : t('library:empty.mine')}
                      action={
                        !query ? (
                          <Button variant="secondary" leadingIcon={<Plus size={14} aria-hidden />} onClick={() => setEditor(newEditor('skill'))}>
                            {t('library:newSkill')}
                          </Button>
                        ) : undefined
                      }
                    />
                  ) : (
                    <LibraryRows>
                      {filteredSkills.map((item) => (
                        <UserLibraryRow
                          key={`skill:${item.id}`}
                          kind="skill"
                          name={item.name}
                          description={skillDisplayDescription(item)}
                          icon={item.icon}
                          imported={Boolean(item.source_skill_id)}
                          onEdit={() => editSkill(item)}
                          onDelete={() => setDeleteTarget({ kind: 'skill', item })}
                          t={t}
                        />
                      ))}
                      {filteredPrompts.map((item) => (
                        <UserLibraryRow
                          key={`prompt:${item.id}`}
                          kind="prompt"
                          name={item.name}
                          description={item.description}
                          imported={Boolean(item.source_prompt_id)}
                          onEdit={() => editPrompt(item)}
                          onDelete={() => setDeleteTarget({ kind: 'prompt', item })}
                          t={t}
                        />
                      ))}
                    </LibraryRows>
                  )}
                </TabsContent>

                <TabsContent value="catalog" className="mt-0">
                  {catalogEmpty ? (
                    <EmptyState
                      className="py-14"
                      icon={<LibraryBig size={20} aria-hidden />}
                      title={query ? t('library:empty.search') : t('library:empty.catalog')}
                    />
                  ) : (
                    <LibraryRows>
                      {filteredCatalogSkills.map((item) => (
                        <CatalogRow
                          key={`catalog-skill:${item.id}`}
                          kind="skill"
                          item={item}
                          adding={addingCatalogId === `skill:${item.id}`}
                          onAdd={() => void addCatalogItem('skill', item.id)}
                          t={t}
                        />
                      ))}
                      {filteredCatalogPrompts.map((item) => (
                        <CatalogRow
                          key={`catalog-prompt:${item.id}`}
                          kind="prompt"
                          item={item}
                          adding={addingCatalogId === `prompt:${item.id}`}
                          onAdd={() => void addCatalogItem('prompt', item.id)}
                          t={t}
                        />
                      ))}
                    </LibraryRows>
                  )}
                </TabsContent>
              </>
            )}
          </Tabs>
        </div>
      </div>

      <LibraryEditor editor={editor} saving={saving} setEditor={setEditor} onImport={applySkillImport} onSave={() => void saveEditor()} t={t} />

      <Dialog
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => {
          if (!open && !deletingRef.current) setDeleteTarget(null)
        }}
      >
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('library:remove.title')}</DialogTitle>
            <DialogDescription>
              {deleteTarget ? t('library:remove.body', { name: deleteTarget.item.name }) : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" disabled={deleting} onClick={() => setDeleteTarget(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" loading={deleting} onClick={() => void removeTarget()}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function KindFilterControl({
  value,
  onChange,
  t,
}: {
  value: KindFilter
  onChange: (value: KindFilter) => void
  t: ReturnType<typeof useTranslation>['t']
}) {
  return (
    <div
      className="grid w-full grid-cols-3 items-center rounded-[9px] bg-[var(--color-bg-muted)] p-1 sm:inline-flex sm:w-auto sm:shrink-0"
      role="group"
      aria-label={t('library:filter.label')}
    >
      {(['all', 'skill', 'prompt'] as const).map((kind) => (
        <button
          key={kind}
          type="button"
          aria-pressed={value === kind}
          onClick={() => onChange(kind)}
          className={cn(
            'inline-flex min-w-0 items-center justify-center rounded-[7px] px-2 text-[12px] font-medium interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] h-[var(--tap-min)] sm:h-8 sm:px-2.5',
            value === kind
              ? 'bg-[var(--color-surface)] text-[var(--color-fg)] shadow-[var(--shadow-xs)]'
              : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
          )}
        >
          {t(`library:filter.${kind}`)}
        </button>
      ))}
    </div>
  )
}

function LibraryRows({ children }: { children: ReactNode }) {
  return <ul className="divide-y divide-[var(--color-divider)] border-b border-[var(--color-divider)]">{children}</ul>
}

function UserLibraryRow({
  kind,
  name,
  description,
  icon,
  imported,
  onEdit,
  onDelete,
  t,
}: {
  kind: ItemKind
  name: string
  description: string
  icon?: string
  imported: boolean
  onEdit: () => void
  onDelete: () => void
  t: ReturnType<typeof useTranslation>['t']
}) {
  return (
    <li className="group flex min-w-0 items-center gap-3 py-3 sm:px-2">
      <span className="grid size-9 shrink-0 place-items-center rounded-[9px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
        {kind === 'skill' ? <SkillIcon name={icon} size={16} aria-hidden /> : <FileText size={16} aria-hidden />}
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="min-w-0 flex-1 truncate text-[14px] font-medium text-[var(--color-fg)]" title={name}>
            {name}
          </span>
          <span className="shrink-0">
            <Badge size="xs" variant="neutral">{t(`library:kinds.${kind}`)}</Badge>
          </span>
          {imported ? <span className="hidden text-[10.5px] text-[var(--color-fg-subtle)] sm:inline">{t('library:catalog.fromAdmin')}</span> : null}
        </div>
        <p className="mt-0.5 truncate text-[12.5px] text-[var(--color-fg-muted)]">{description}</p>
      </div>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label={`${t('common:actions.more')}: ${name}`}
            className="inline-flex size-[var(--tap-min)] shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-subtle)] opacity-100 hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] sm:size-8 sm:opacity-0 sm:group-hover:opacity-100 sm:data-[state=open]:opacity-100 sm:focus-visible:opacity-100"
          >
            <MoreHorizontal size={15} aria-hidden />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onSelect={onEdit}><Pencil size={14} aria-hidden /> {t('common:actions.edit')}</DropdownMenuItem>
          <DropdownMenuItem destructive onSelect={onDelete}><Trash2 size={14} aria-hidden /> {t('common:actions.delete')}</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </li>
  )
}

function CatalogRow({
  kind,
  item,
  adding,
  onAdd,
  t,
}: {
  kind: ItemKind
  item: ApiLibraryCatalogSkill | ApiLibraryCatalogPrompt
  adding: boolean
  onAdd: () => void
  t: ReturnType<typeof useTranslation>['t']
}) {
  const description =
    kind === 'skill' ? skillDisplayDescription(item as ApiLibraryCatalogSkill) : item.description
  return (
    <li className="flex min-w-0 items-center gap-3 py-3 sm:px-2">
      <span className="grid size-9 shrink-0 place-items-center rounded-[9px] bg-[var(--color-accent-soft)] text-[var(--color-accent)]">
        {kind === 'skill' ? (
          <SkillIcon name={(item as ApiLibraryCatalogSkill).icon} size={16} aria-hidden />
        ) : (
          <FileText size={16} aria-hidden />
        )}
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span
            className="min-w-0 flex-1 truncate text-[14px] font-medium text-[var(--color-fg)]"
            title={item.name}
          >
            {item.name}
          </span>
          <span className="shrink-0">
            <Badge size="xs" variant="neutral">{t(`library:kinds.${kind}`)}</Badge>
          </span>
        </div>
        <p className="mt-0.5 truncate text-[12.5px] text-[var(--color-fg-muted)]">{description}</p>
      </div>
      {item.added ? (
        <span
          className="inline-flex h-9 shrink-0 items-center gap-1.5 px-2 text-[12px] text-[var(--color-success)]"
          aria-label={t('library:catalog.addedLabel')}
          title={t('library:catalog.addedLabel')}
        >
          <Check size={14} aria-hidden />
          <span className="hidden sm:inline">{t('library:catalog.addedLabel')}</span>
        </span>
      ) : (
        <Button
          variant="ghost"
          size="sm"
          loading={adding}
          leadingIcon={<Plus size={14} aria-hidden />}
          onClick={onAdd}
          className="max-sm:min-h-[var(--tap-min)]"
        >
          {t('library:catalog.add')}
        </Button>
      )}
    </li>
  )
}

function LibraryEditor({
  editor,
  saving,
  setEditor,
  onImport,
  onSave,
  t,
}: {
  editor: EditorState
  saving: boolean
  setEditor: Dispatch<SetStateAction<EditorState>>
  onImport: () => void
  onSave: () => void
  t: ReturnType<typeof useTranslation>['t']
}) {
  const skill = editor.kind === 'skill'
  return (
    <Dialog open={editor.open} onOpenChange={(open) => !saving && setEditor((current) => ({ ...current, open }))}>
      <DialogContent size="md">
        <DialogHeader>
          <DialogTitle>
            {editor.id
              ? t(skill ? 'library:editor.editSkill' : 'library:editor.editPrompt')
              : t(skill ? 'library:editor.newSkill' : 'library:editor.newPrompt')}
          </DialogTitle>
        </DialogHeader>
        <DialogBody>
          <div className="grid gap-4">
            {skill ? (
              <Field
                label={t('library:editor.importLabel')}
                htmlFor="library-skill-import"
                hint={t('library:editor.importHint')}
              >
                <Textarea
                  id="library-skill-import"
                  rows={4}
                  className="font-mono text-[12px]"
                  value={editor.importText}
                  onChange={(event) => setEditor((current) => ({ ...current, importText: event.target.value }))}
                  placeholder={'---\nname: meeting-follow-up\ndescription: Extract decisions and next steps from meeting notes.\n---\n\nReview the notes and return...'}
                />
                <div className="mt-2 flex justify-end">
                  <Button size="sm" variant="secondary" onClick={onImport}>{t('library:editor.importAction')}</Button>
                </div>
              </Field>
            ) : null}
            <Field label={t('library:editor.name')} htmlFor="library-item-name" hint={skill ? t('library:editor.skillNameHint') : undefined}>
              <Input
                id="library-item-name"
                autoFocus={!skill}
                value={editor.name}
                onChange={(event) => setEditor((current) => ({ ...current, name: event.target.value }))}
                placeholder={skill ? 'meeting-follow-up' : t('library:editor.promptNamePlaceholder')}
              />
            </Field>
            <Field label={t(skill ? 'library:editor.when' : 'library:editor.description')} htmlFor="library-item-description">
              <Input
                id="library-item-description"
                value={editor.description}
                onChange={(event) => setEditor((current) => ({ ...current, description: event.target.value }))}
              />
            </Field>
            <Field label={t(skill ? 'library:editor.instructions' : 'library:editor.promptContent')} htmlFor="library-item-content">
              <Textarea
                id="library-item-content"
                rows={12}
                value={editor.content}
                onChange={(event) => setEditor((current) => ({ ...current, content: event.target.value }))}
              />
            </Field>
          </div>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" disabled={saving} onClick={() => setEditor((current) => ({ ...current, open: false }))}>
            {t('common:actions.cancel')}
          </Button>
          <Button loading={saving} onClick={onSave}>{t('common:actions.save')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function LibrarySkeleton({ label }: { label: string }) {
  return (
    <div className="divide-y divide-[var(--color-divider)]" role="status" aria-label={label}>
      {Array.from({ length: 5 }, (_, index) => (
        <div key={index} className="flex items-center gap-3 py-3 sm:px-2">
          <Skeleton className="size-9 shrink-0 rounded-[9px]" />
          <div className="min-w-0 flex-1 space-y-2">
            <Skeleton shape="line" className="h-3.5 w-1/3" />
            <Skeleton shape="line" className="w-2/3" />
          </div>
          <Skeleton className="size-8 rounded-[8px]" />
        </div>
      ))}
      <span className="sr-only">{label}</span>
    </div>
  )
}
