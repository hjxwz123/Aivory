import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  BookOpen,
  ChevronRight,
  CircleAlert,
  FileText,
  FolderKanban,
  Image as ImageIcon,
  MessageSquare,
  RefreshCw,
  Search,
  Share2,
  UserRound,
  X,
} from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type {
  ApiAdminGeneratedImagePage,
  ApiAdminGeneratedImageResource,
  ApiAdminKnowledgeBaseResource,
  ApiAdminKnowledgeBaseResourceDetail,
  ApiAdminProjectConversation,
  ApiAdminProjectResource,
  ApiAdminProjectResourceDetail,
  ApiAdminResourcePage,
  ApiDocument,
} from '@/api/types'
import { ImageLightbox } from '@/components/chat/image-lightbox'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Input } from '@/components/ui/input'
import { Pagination } from '@/components/ui/pagination'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  Sheet,
  SheetBody,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'

const PAGE_SIZE = 50
const PROJECT_CONVERSATION_PAGE_SIZE = 20
const ALL_MODELS = 'all'

type ResourceTab = 'knowledge-bases' | 'projects' | 'images'

interface ListState<T> {
  data: T | null
  loading: boolean
  error: string
}

type DetailState =
  | {
      kind: 'knowledge-bases'
      summary: ApiAdminKnowledgeBaseResource
      item: ApiAdminKnowledgeBaseResourceDetail | null
      documents: ApiDocument[]
      loading: boolean
      error: string
    }
  | {
      kind: 'projects'
      summary: ApiAdminProjectResource
      item: ApiAdminProjectResourceDetail | null
      documents: ApiDocument[]
      conversations: ApiAdminResourcePage<ApiAdminProjectConversation> | null
      conversationPage: number
      conversationsLoading: boolean
      conversationsError: string
      loading: boolean
      error: string
    }
  | {
      kind: 'images'
      summary: ApiAdminGeneratedImageResource
      item: ApiAdminGeneratedImageResource | null
      loading: boolean
      error: string
    }

function useDebouncedValue(value: string, delay = 350): string {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value.trim()), delay)
    return () => window.clearTimeout(timer)
  }, [delay, value])
  return debounced
}

function formatBytes(value: number): string {
  if (value >= 1024 * 1024 * 1024) return `${(value / (1024 * 1024 * 1024)).toFixed(1)} GB`
  if (value >= 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MB`
  if (value >= 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${value || 0} B`
}

function resourceOwner(name: string, email: string, id: string): string {
  return name || email || id || '-'
}

function ownerDetail(name: string, email: string, id: string): string {
  return [name, email, id].filter(Boolean).join(' / ') || '-'
}

function isResourceTab(value: string | null): value is ResourceTab {
  return value === 'knowledge-bases' || value === 'projects' || value === 'images'
}

export default function AdminResources() {
  const { t, i18n } = useTranslation(['admin', 'common'])
  const [searchParams, setSearchParams] = useSearchParams()
  const rawTab = searchParams.get('tab')
  const tab: ResourceTab = isResourceTab(rawTab) ? rawTab : 'knowledge-bases'

  const [kbSearch, setKbSearch] = useState('')
  const [kbUser, setKbUser] = useState('')
  const [kbPage, setKbPage] = useState(1)
  const [kbState, setKbState] = useState<ListState<ApiAdminResourcePage<ApiAdminKnowledgeBaseResource>>>(
    { data: null, loading: true, error: '' },
  )

  const [projectSearch, setProjectSearch] = useState('')
  const [projectUser, setProjectUser] = useState('')
  const [projectPage, setProjectPage] = useState(1)
  const [projectState, setProjectState] = useState<ListState<ApiAdminResourcePage<ApiAdminProjectResource>>>(
    { data: null, loading: true, error: '' },
  )

  const [imageUser, setImageUser] = useState('')
  const [imageModel, setImageModel] = useState(ALL_MODELS)
  const [imagePage, setImagePage] = useState(1)
  const [imageState, setImageState] = useState<ListState<ApiAdminGeneratedImagePage>>({
    data: null,
    loading: true,
    error: '',
  })

  const kbSearchDebounced = useDebouncedValue(kbSearch)
  const kbUserDebounced = useDebouncedValue(kbUser)
  const projectSearchDebounced = useDebouncedValue(projectSearch)
  const projectUserDebounced = useDebouncedValue(projectUser)
  const imageUserDebounced = useDebouncedValue(imageUser)

  const kbRequestRef = useRef(0)
  const projectRequestRef = useRef(0)
  const imageRequestRef = useRef(0)
  const detailRequestRef = useRef(0)
  const conversationRequestRef = useRef(0)
  const [detail, setDetail] = useState<DetailState | null>(null)
  const [lightboxOpen, setLightboxOpen] = useState(false)

  useEffect(() => {
    if (isResourceTab(rawTab)) return
    const next = new URLSearchParams(searchParams)
    next.set('tab', 'knowledge-bases')
    setSearchParams(next, { replace: true })
  }, [rawTab, searchParams, setSearchParams])

  const loadKnowledgeBases = useCallback(async () => {
    const request = ++kbRequestRef.current
    setKbState((current) => ({ ...current, loading: true, error: '' }))
    try {
      const data = await adminApi.adminKnowledgeBases({
        search: kbSearchDebounced,
        user: kbUserDebounced,
        limit: PAGE_SIZE,
        offset: (kbPage - 1) * PAGE_SIZE,
      })
      if (request !== kbRequestRef.current) return
      setKbState({ data, loading: false, error: '' })
      const lastPage = Math.max(1, Math.ceil(data.total / PAGE_SIZE))
      if (kbPage > lastPage) setKbPage(lastPage)
    } catch (cause) {
      if (request !== kbRequestRef.current) return
      setKbState((current) => ({
        ...current,
        loading: false,
        error: cause instanceof ApiError ? cause.message : t('admin:resources.loadFailed'),
      }))
    }
  }, [kbPage, kbSearchDebounced, kbUserDebounced, t])

  const loadProjects = useCallback(async () => {
    const request = ++projectRequestRef.current
    setProjectState((current) => ({ ...current, loading: true, error: '' }))
    try {
      const data = await adminApi.adminProjects({
        search: projectSearchDebounced,
        user: projectUserDebounced,
        limit: PAGE_SIZE,
        offset: (projectPage - 1) * PAGE_SIZE,
      })
      if (request !== projectRequestRef.current) return
      setProjectState({ data, loading: false, error: '' })
      const lastPage = Math.max(1, Math.ceil(data.total / PAGE_SIZE))
      if (projectPage > lastPage) setProjectPage(lastPage)
    } catch (cause) {
      if (request !== projectRequestRef.current) return
      setProjectState((current) => ({
        ...current,
        loading: false,
        error: cause instanceof ApiError ? cause.message : t('admin:resources.loadFailed'),
      }))
    }
  }, [projectPage, projectSearchDebounced, projectUserDebounced, t])

  const loadImages = useCallback(async () => {
    const request = ++imageRequestRef.current
    setImageState((current) => ({ ...current, loading: true, error: '' }))
    try {
      const data = await adminApi.adminGeneratedImages({
        user: imageUserDebounced,
        model_id: imageModel === ALL_MODELS ? '' : imageModel,
        limit: PAGE_SIZE,
        offset: (imagePage - 1) * PAGE_SIZE,
      })
      if (request !== imageRequestRef.current) return
      setImageState({ data, loading: false, error: '' })
      const lastPage = Math.max(1, Math.ceil(data.total / PAGE_SIZE))
      if (imagePage > lastPage) setImagePage(lastPage)
    } catch (cause) {
      if (request !== imageRequestRef.current) return
      setImageState((current) => ({
        ...current,
        loading: false,
        error: cause instanceof ApiError ? cause.message : t('admin:resources.loadFailed'),
      }))
    }
  }, [imageModel, imagePage, imageUserDebounced, t])

  useEffect(() => {
    if (tab === 'knowledge-bases') void loadKnowledgeBases()
    if (tab === 'projects') void loadProjects()
    if (tab === 'images') void loadImages()
  }, [loadImages, loadKnowledgeBases, loadProjects, tab])

  const dateFormatter = useMemo(
    () => new Intl.DateTimeFormat(i18n.language || undefined, { dateStyle: 'medium', timeStyle: 'short' }),
    [i18n.language],
  )

  const formatDate = useCallback(
    (value: number) => {
      if (!value) return '-'
      return dateFormatter.format(new Date(value > 1_000_000_000_000 ? value : value * 1000))
    },
    [dateFormatter],
  )

  function changeTab(value: string) {
    if (!isResourceTab(value)) return
    const next = new URLSearchParams(searchParams)
    next.set('tab', value)
    setSearchParams(next)
  }

  function closeDetail() {
    detailRequestRef.current += 1
    conversationRequestRef.current += 1
    setDetail(null)
    setLightboxOpen(false)
  }

  async function openKnowledgeBase(summary: ApiAdminKnowledgeBaseResource) {
    const request = ++detailRequestRef.current
    setDetail({
      kind: 'knowledge-bases',
      summary,
      item: null,
      documents: [],
      loading: true,
      error: '',
    })
    try {
      const [{ item }, documents] = await Promise.all([
        adminApi.adminKnowledgeBase(summary.id),
        adminApi.kbDocuments(summary.id),
      ])
      if (request !== detailRequestRef.current) return
      setDetail({ kind: 'knowledge-bases', summary, item, documents, loading: false, error: '' })
    } catch (cause) {
      if (request !== detailRequestRef.current) return
      setDetail({
        kind: 'knowledge-bases',
        summary,
        item: null,
        documents: [],
        loading: false,
        error: cause instanceof ApiError ? cause.message : t('admin:resources.detailLoadFailed'),
      })
    }
  }

  async function openProject(summary: ApiAdminProjectResource) {
    const request = ++detailRequestRef.current
    conversationRequestRef.current += 1
    setDetail({
      kind: 'projects',
      summary,
      item: null,
      documents: [],
      conversations: null,
      conversationPage: 1,
      conversationsLoading: false,
      conversationsError: '',
      loading: true,
      error: '',
    })
    try {
      const { item } = await adminApi.adminProject(summary.id)
      const [documents, conversations] = await Promise.all([
        item.kb_id ? adminApi.kbDocuments(item.kb_id) : Promise.resolve([] as ApiDocument[]),
        adminApi.adminProjectConversations(summary.id, PROJECT_CONVERSATION_PAGE_SIZE, 0),
      ])
      if (request !== detailRequestRef.current) return
      setDetail({
        kind: 'projects',
        summary,
        item,
        documents,
        conversations,
        conversationPage: 1,
        conversationsLoading: false,
        conversationsError: '',
        loading: false,
        error: '',
      })
    } catch (cause) {
      if (request !== detailRequestRef.current) return
      setDetail({
        kind: 'projects',
        summary,
        item: null,
        documents: [],
        conversations: null,
        conversationPage: 1,
        conversationsLoading: false,
        conversationsError: '',
        loading: false,
        error: cause instanceof ApiError ? cause.message : t('admin:resources.detailLoadFailed'),
      })
    }
  }

  async function loadProjectConversations(projectId: string, page: number) {
    const request = ++conversationRequestRef.current
    setDetail((current) =>
      current?.kind === 'projects' && current.summary.id === projectId
        ? { ...current, conversationPage: page, conversationsLoading: true, conversationsError: '' }
        : current,
    )
    try {
      const conversations = await adminApi.adminProjectConversations(
        projectId,
        PROJECT_CONVERSATION_PAGE_SIZE,
        (page - 1) * PROJECT_CONVERSATION_PAGE_SIZE,
      )
      if (request !== conversationRequestRef.current) return
      setDetail((current) =>
        current?.kind === 'projects' && current.summary.id === projectId
          ? { ...current, conversations, conversationPage: page, conversationsLoading: false, conversationsError: '' }
          : current,
      )
    } catch (cause) {
      if (request !== conversationRequestRef.current) return
      setDetail((current) =>
        current?.kind === 'projects' && current.summary.id === projectId
          ? {
              ...current,
              conversationsLoading: false,
              conversationsError: cause instanceof ApiError ? cause.message : t('admin:resources.loadFailed'),
            }
          : current,
      )
    }
  }

  async function openImage(summary: ApiAdminGeneratedImageResource) {
    const request = ++detailRequestRef.current
    setDetail({ kind: 'images', summary, item: null, loading: true, error: '' })
    try {
      const item = await adminApi.adminGeneratedImage(summary.id)
      if (request !== detailRequestRef.current) return
      setDetail({ kind: 'images', summary, item, loading: false, error: '' })
    } catch (cause) {
      if (request !== detailRequestRef.current) return
      setDetail({
        kind: 'images',
        summary,
        item: null,
        loading: false,
        error: cause instanceof ApiError ? cause.message : t('admin:resources.detailLoadFailed'),
      })
    }
  }

  const currentState = tab === 'knowledge-bases' ? kbState : tab === 'projects' ? projectState : imageState
  const currentTotal = currentState.data?.total ?? 0
  const currentLoading = currentState.loading

  return (
    <div className="min-w-0 pb-10">
      <header>
        <h1 className="font-serif text-2xl text-[var(--color-fg)] sm:text-3xl">
          {t('admin:resources.title')}
        </h1>
        <p className="mt-2 max-w-2xl text-sm leading-relaxed text-[var(--color-fg-muted)]">
          {t('admin:resources.lead')}
        </p>
      </header>

      <div
        role="group"
        aria-label={t('admin:resources.title')}
        className="mt-6 inline-flex max-w-full items-center gap-1 overflow-x-auto rounded-[10px] border border-[var(--color-border-subtle)] bg-[var(--color-bg-muted)] p-1"
      >
        {([
          ['knowledge-bases', BookOpen, t('admin:resources.tabs.knowledgeBases')],
          ['projects', FolderKanban, t('admin:resources.tabs.projects')],
          ['images', ImageIcon, t('admin:resources.tabs.images')],
        ] as const).map(([value, Icon, label]) => (
          <button
            key={value}
            type="button"
            aria-pressed={tab === value}
            onClick={() => changeTab(value)}
            className={cn(
              'interactive inline-flex h-8 items-center gap-2 whitespace-nowrap rounded-[8px] px-3 text-sm font-medium outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              tab === value
                ? 'bg-[var(--color-surface)] text-[var(--color-fg)] shadow-[var(--shadow-xs)]'
                : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
            )}
          >
            <Icon size={14} aria-hidden />
            {label}
          </button>
        ))}
      </div>

      <div className="mt-5 flex flex-col gap-3 sm:flex-row sm:items-center">
        {tab === 'knowledge-bases' ? (
          <>
            <Input
              value={kbSearch}
              onChange={(event) => {
                setKbSearch(event.target.value)
                setKbPage(1)
              }}
              leadingIcon={<Search size={15} aria-hidden />}
              placeholder={t('admin:resources.filters.knowledgeBaseName')}
              aria-label={t('admin:resources.filters.knowledgeBaseName')}
              wrapperClassName="w-full sm:max-w-sm"
            />
            <Input
              value={kbUser}
              onChange={(event) => {
                setKbUser(event.target.value)
                setKbPage(1)
              }}
              leadingIcon={<UserRound size={15} aria-hidden />}
              placeholder={t('admin:resources.filters.user')}
              aria-label={t('admin:resources.filters.user')}
              wrapperClassName="w-full sm:max-w-xs"
            />
          </>
        ) : tab === 'projects' ? (
          <>
            <Input
              value={projectSearch}
              onChange={(event) => {
                setProjectSearch(event.target.value)
                setProjectPage(1)
              }}
              leadingIcon={<Search size={15} aria-hidden />}
              placeholder={t('admin:resources.filters.projectName')}
              aria-label={t('admin:resources.filters.projectName')}
              wrapperClassName="w-full sm:max-w-sm"
            />
            <Input
              value={projectUser}
              onChange={(event) => {
                setProjectUser(event.target.value)
                setProjectPage(1)
              }}
              leadingIcon={<UserRound size={15} aria-hidden />}
              placeholder={t('admin:resources.filters.user')}
              aria-label={t('admin:resources.filters.user')}
              wrapperClassName="w-full sm:max-w-xs"
            />
          </>
        ) : (
          <>
            <Input
              value={imageUser}
              onChange={(event) => {
                setImageUser(event.target.value)
                setImagePage(1)
              }}
              leadingIcon={<UserRound size={15} aria-hidden />}
              placeholder={t('admin:resources.filters.user')}
              aria-label={t('admin:resources.filters.user')}
              wrapperClassName="w-full sm:max-w-sm"
            />
            <Select
              value={imageModel}
              onValueChange={(value) => {
                setImageModel(value)
                setImagePage(1)
              }}
            >
              <SelectTrigger className="w-full sm:w-64" aria-label={t('admin:resources.filters.model')}>
                <SelectValue placeholder={t('admin:resources.filters.allModels')} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL_MODELS}>{t('admin:resources.filters.allModels')}</SelectItem>
                {(imageState.data?.models ?? []).map((model) => (
                  <SelectItem key={model.id} value={model.id}>{model.label || model.id}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </>
        )}
      </div>

      <section className="mt-5 overflow-hidden rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)]">
        <div className="flex min-h-11 items-center justify-between gap-3 border-b border-[var(--color-divider)] px-4 py-2.5 sm:px-5">
          <span className="text-[12.5px] tabular-nums text-[var(--color-fg-subtle)]">
            {t('admin:resources.total', { count: currentTotal })}
          </span>
          {currentLoading && currentState.data ? (
            <span className="inline-flex items-center gap-1.5 text-[12px] text-[var(--color-fg-subtle)]" role="status">
              <RefreshCw size={12} className="animate-spin" aria-hidden />
              {t('admin:resources.updating')}
            </span>
          ) : null}
        </div>

        {tab === 'knowledge-bases' ? (
          <KnowledgeBaseList
            state={kbState}
            page={kbPage}
            onPage={setKbPage}
            onRetry={() => void loadKnowledgeBases()}
            onOpen={(item) => void openKnowledgeBase(item)}
            filtered={Boolean(kbSearchDebounced || kbUserDebounced)}
            formatDate={formatDate}
            t={t}
          />
        ) : tab === 'projects' ? (
          <ProjectList
            state={projectState}
            page={projectPage}
            onPage={setProjectPage}
            onRetry={() => void loadProjects()}
            onOpen={(item) => void openProject(item)}
            filtered={Boolean(projectSearchDebounced || projectUserDebounced)}
            formatDate={formatDate}
            t={t}
          />
        ) : (
          <ImageList
            state={imageState}
            page={imagePage}
            onPage={setImagePage}
            onRetry={() => void loadImages()}
            onOpen={(item) => void openImage(item)}
            filtered={Boolean(imageUserDebounced || imageModel !== ALL_MODELS)}
            formatDate={formatDate}
            t={t}
          />
        )}
      </section>

      <Sheet open={detail !== null} onOpenChange={(open) => !open && closeDetail()}>
        <SheetContent
          side="right"
          size="lg"
          className="w-[min(100vw,40rem)]"
        >
          <SheetHeader className="relative border-b border-[var(--color-divider)] pr-14">
            <SheetTitle className="break-words">{detailTitle(detail)}</SheetTitle>
            <SheetDescription>{detail ? t(`admin:resources.tabs.${detail.kind === 'knowledge-bases' ? 'knowledgeBases' : detail.kind}`) : ''}</SheetDescription>
            <SheetClose asChild>
              <Button variant="ghost" size="icon" aria-label={t('common:actions.close')} className="absolute right-4 top-4">
                <X size={17} aria-hidden />
              </Button>
            </SheetClose>
          </SheetHeader>
          <SheetBody className="py-5">
            {detail?.loading ? (
              <DetailSkeleton />
            ) : detail?.error ? (
              <DetailError
                message={detail.error}
                retryLabel={t('admin:resources.retry')}
                onRetry={() => {
                  if (detail.kind === 'knowledge-bases') void openKnowledgeBase(detail.summary)
                  if (detail.kind === 'projects') void openProject(detail.summary)
                  if (detail.kind === 'images') void openImage(detail.summary)
                }}
              />
            ) : detail?.kind === 'knowledge-bases' && detail.item ? (
              <KnowledgeBaseDetails item={detail.item} documents={detail.documents} formatDate={formatDate} t={t} />
            ) : detail?.kind === 'projects' && detail.item ? (
              <ProjectDetails
                item={detail.item}
                documents={detail.documents}
                conversations={detail.conversations}
                conversationPage={detail.conversationPage}
                conversationsLoading={detail.conversationsLoading}
                conversationsError={detail.conversationsError}
                onConversationPage={(page) => void loadProjectConversations(detail.item!.id, page)}
                onRetryConversations={() => void loadProjectConversations(detail.item!.id, detail.conversationPage)}
                formatDate={formatDate}
                t={t}
              />
            ) : detail?.kind === 'images' && detail.item ? (
              <ImageDetails item={detail.item} formatDate={formatDate} onPreview={() => setLightboxOpen(true)} t={t} />
            ) : null}
          </SheetBody>
        </SheetContent>
      </Sheet>

      <ImageLightbox
        open={lightboxOpen}
        onOpenChange={setLightboxOpen}
        src={detail?.kind === 'images' ? (detail.item ?? detail.summary).url : ''}
        alt={detail?.kind === 'images' ? (detail.item ?? detail.summary).filename : ''}
      />
    </div>
  )
}

function KnowledgeBaseList({
  state,
  page,
  onPage,
  onRetry,
  onOpen,
  filtered,
  formatDate,
  t,
}: {
  state: ListState<ApiAdminResourcePage<ApiAdminKnowledgeBaseResource>>
  page: number
  onPage: (page: number) => void
  onRetry: () => void
  onOpen: (item: ApiAdminKnowledgeBaseResource) => void
  filtered: boolean
  formatDate: (value: number) => string
  t: (key: string, options?: Record<string, unknown>) => string
}) {
  if (state.loading && !state.data) return <ListSkeleton />
  if (state.error && !state.data) return <ListError message={state.error} onRetry={onRetry} t={t} />
  if (!state.data?.items.length) {
    return (
      <EmptyState
        className="py-20"
        icon={<BookOpen size={21} aria-hidden />}
        title={t(filtered ? 'admin:resources.empty.filtered' : 'admin:resources.empty.knowledgeBases')}
        description={filtered ? undefined : t('admin:resources.empty.knowledgeBasesLead')}
      />
    )
  }
  return (
    <div className={cn('transition-opacity', state.loading && 'pointer-events-none opacity-60')}>
      {state.error ? <InlineError message={state.error} onRetry={onRetry} t={t} /> : null}
      <div className="hidden grid-cols-[minmax(0,2fr)_minmax(0,1.25fr)_8rem_minmax(0,1fr)_10rem_1.25rem] gap-4 border-b border-[var(--color-divider)] px-5 py-2.5 text-[11px] font-medium uppercase text-[var(--color-fg-subtle)] md:grid">
        <span>{t('admin:resources.table.name')}</span>
        <span>{t('admin:resources.table.user')}</span>
        <span>{t('admin:resources.table.documents')}</span>
        <span>{t('admin:resources.table.model')}</span>
        <span>{t('admin:resources.table.updated')}</span>
        <span />
      </div>
      <ul className="divide-y divide-[var(--color-divider)]">
        {state.data.items.map((item) => (
          <li key={item.id}>
            <button
              type="button"
              onClick={() => onOpen(item)}
              className="group grid w-full grid-cols-[minmax(0,1fr)_auto] items-center gap-3 px-4 py-4 text-left interactive hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)] md:grid-cols-[minmax(0,2fr)_minmax(0,1.25fr)_8rem_minmax(0,1fr)_10rem_1.25rem] md:gap-4 md:px-5"
            >
              <span className="min-w-0">
                <span className="block truncate text-sm font-medium text-[var(--color-fg)]">{item.name}</span>
                <span className="mt-1 block line-clamp-1 text-[12px] text-[var(--color-fg-subtle)]">
                  {item.description || t('admin:resources.noDescription')}
                </span>
                <span className="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-[var(--color-fg-subtle)] md:hidden">
                  <span>{resourceOwner(item.creator_name, item.creator_email, item.creator_id)}</span>
                  <span>{t('admin:resources.counts.documents', { count: item.document_count })}</span>
                  <span>{formatDate(item.last_activity_at)}</span>
                </span>
              </span>
              <span className="hidden min-w-0 md:block">
                <span className="block truncate text-[12.5px] text-[var(--color-fg)]">{resourceOwner(item.creator_name, item.creator_email, item.creator_id)}</span>
                <span className="mt-0.5 block truncate text-[11px] text-[var(--color-fg-subtle)]">{item.creator_email}</span>
              </span>
              <DocumentSummary item={item} t={t} />
              <span className="hidden min-w-0 md:block">
                <span className="block truncate text-[12.5px] text-[var(--color-fg)]">{item.embedding_model_label || item.embedding_model_id || '-'}</span>
                <span className="mt-0.5 block text-[11px] text-[var(--color-fg-subtle)]">{item.embedding_dim ? `${item.embedding_dim}d` : '-'}</span>
              </span>
              <span className="hidden text-[12px] text-[var(--color-fg-subtle)] md:block">{formatDate(item.last_activity_at)}</span>
              <ChevronRight size={16} className="text-[var(--color-fg-faint)] transition-transform group-hover:translate-x-0.5" aria-hidden />
            </button>
          </li>
        ))}
      </ul>
      <Pagination page={page} pageCount={Math.ceil(state.data.total / PAGE_SIZE)} onPage={onPage} className="pb-4" />
    </div>
  )
}

function ProjectList({
  state,
  page,
  onPage,
  onRetry,
  onOpen,
  filtered,
  formatDate,
  t,
}: {
  state: ListState<ApiAdminResourcePage<ApiAdminProjectResource>>
  page: number
  onPage: (page: number) => void
  onRetry: () => void
  onOpen: (item: ApiAdminProjectResource) => void
  filtered: boolean
  formatDate: (value: number) => string
  t: (key: string, options?: Record<string, unknown>) => string
}) {
  if (state.loading && !state.data) return <ListSkeleton />
  if (state.error && !state.data) return <ListError message={state.error} onRetry={onRetry} t={t} />
  if (!state.data?.items.length) {
    return (
      <EmptyState
        className="py-20"
        icon={<FolderKanban size={21} aria-hidden />}
        title={t(filtered ? 'admin:resources.empty.filtered' : 'admin:resources.empty.projects')}
        description={filtered ? undefined : t('admin:resources.empty.projectsLead')}
      />
    )
  }
  return (
    <div className={cn('transition-opacity', state.loading && 'pointer-events-none opacity-60')}>
      {state.error ? <InlineError message={state.error} onRetry={onRetry} t={t} /> : null}
      <div className="hidden grid-cols-[minmax(0,2fr)_minmax(0,1.25fr)_8rem_8rem_10rem_1.25rem] gap-4 border-b border-[var(--color-divider)] px-5 py-2.5 text-[11px] font-medium uppercase text-[var(--color-fg-subtle)] md:grid">
        <span>{t('admin:resources.table.name')}</span>
        <span>{t('admin:resources.table.user')}</span>
        <span>{t('admin:resources.table.conversations')}</span>
        <span>{t('admin:resources.table.documents')}</span>
        <span>{t('admin:resources.table.updated')}</span>
        <span />
      </div>
      <ul className="divide-y divide-[var(--color-divider)]">
        {state.data.items.map((item) => (
          <li key={item.id}>
            <button
              type="button"
              onClick={() => onOpen(item)}
              className="group grid w-full grid-cols-[minmax(0,1fr)_auto] items-center gap-3 px-4 py-4 text-left interactive hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)] md:grid-cols-[minmax(0,2fr)_minmax(0,1.25fr)_8rem_8rem_10rem_1.25rem] md:gap-4 md:px-5"
            >
              <span className="flex min-w-0 items-start gap-2.5">
                <span className="shrink-0 text-lg leading-5" aria-hidden>{item.emoji || '\u{1F4C1}'}</span>
                <span className="min-w-0">
                  <span className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-sm font-medium text-[var(--color-fg)]">{item.name}</span>
                    {item.pinned ? <Badge size="xs">{t('admin:resources.details.pinned')}</Badge> : null}
                  </span>
                  <span className="mt-1 block line-clamp-1 text-[12px] text-[var(--color-fg-subtle)]">
                    {item.description || t('admin:resources.noDescription')}
                  </span>
                  <span className="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-[var(--color-fg-subtle)] md:hidden">
                    <span>{resourceOwner(item.creator_name, item.creator_email, item.creator_id)}</span>
                    <span>{t('admin:resources.counts.conversations', { count: item.conversation_count })}</span>
                    <span>{t('admin:resources.counts.documents', { count: item.document_count })}</span>
                  </span>
                </span>
              </span>
              <span className="hidden min-w-0 md:block">
                <span className="block truncate text-[12.5px] text-[var(--color-fg)]">{resourceOwner(item.creator_name, item.creator_email, item.creator_id)}</span>
                <span className="mt-0.5 block truncate text-[11px] text-[var(--color-fg-subtle)]">{item.creator_email}</span>
              </span>
              <span className="hidden text-[12.5px] tabular-nums text-[var(--color-fg)] md:block">{item.conversation_count}</span>
              <DocumentSummary item={item} t={t} />
              <span className="hidden text-[12px] text-[var(--color-fg-subtle)] md:block">{formatDate(item.last_activity_at)}</span>
              <ChevronRight size={16} className="text-[var(--color-fg-faint)] transition-transform group-hover:translate-x-0.5" aria-hidden />
            </button>
          </li>
        ))}
      </ul>
      <Pagination page={page} pageCount={Math.ceil(state.data.total / PAGE_SIZE)} onPage={onPage} className="pb-4" />
    </div>
  )
}

function ImageList({
  state,
  page,
  onPage,
  onRetry,
  onOpen,
  filtered,
  formatDate,
  t,
}: {
  state: ListState<ApiAdminGeneratedImagePage>
  page: number
  onPage: (page: number) => void
  onRetry: () => void
  onOpen: (item: ApiAdminGeneratedImageResource) => void
  filtered: boolean
  formatDate: (value: number) => string
  t: (key: string, options?: Record<string, unknown>) => string
}) {
  if (state.loading && !state.data) return <ImageSkeleton />
  if (state.error && !state.data) return <ListError message={state.error} onRetry={onRetry} t={t} />
  if (!state.data?.items.length) {
    return (
      <EmptyState
        className="py-20"
        icon={<ImageIcon size={21} aria-hidden />}
        title={t(filtered ? 'admin:resources.empty.filtered' : 'admin:resources.empty.images')}
        description={filtered ? undefined : t('admin:resources.empty.imagesLead')}
      />
    )
  }
  return (
    <div className={cn('transition-opacity', state.loading && 'pointer-events-none opacity-60')}>
      {state.error ? <InlineError message={state.error} onRetry={onRetry} t={t} /> : null}
      <ul className="grid grid-cols-1 gap-px bg-[var(--color-divider)] sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
        {state.data.items.map((item) => (
          <li key={item.id} className="min-w-0 bg-[var(--color-surface)]">
            <button
              type="button"
              onClick={() => onOpen(item)}
              className="group block h-full w-full p-3 text-left interactive hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)]"
            >
              <span className="block aspect-[4/3] overflow-hidden rounded-[8px] bg-[var(--color-surface-sunken)]">
                <ResourceImage src={item.url} alt={item.filename} className="group-hover:scale-[1.02]" />
              </span>
              <span className="mt-3 block line-clamp-2 min-h-10 text-sm leading-5 text-[var(--color-fg)]">
                {item.prompt || t('admin:resources.details.promptMissing')}
              </span>
              <span className="mt-2 flex min-w-0 items-center justify-between gap-3 text-[11px] text-[var(--color-fg-subtle)]">
                <span className="min-w-0 truncate">{resourceOwner(item.user_name, item.user_email, item.user_id)}</span>
                <span className="shrink-0">{formatDate(item.created_at)}</span>
              </span>
              <span className="mt-1 block truncate text-[11px] text-[var(--color-fg-subtle)]">{item.model_label || item.model_id || '-'}</span>
            </button>
          </li>
        ))}
      </ul>
      <Pagination page={page} pageCount={Math.ceil(state.data.total / PAGE_SIZE)} onPage={onPage} className="pb-4" />
    </div>
  )
}

function KnowledgeBaseDetails({
  item,
  documents,
  formatDate,
  t,
}: {
  item: ApiAdminKnowledgeBaseResourceDetail
  documents: ApiDocument[]
  formatDate: (value: number) => string
  t: (key: string, options?: Record<string, unknown>) => string
}) {
  return (
    <div className="space-y-7">
      <DetailSection title={t('admin:resources.details.basic')}>
        <p className="whitespace-pre-wrap break-words text-sm leading-relaxed text-[var(--color-fg)]">
          {item.description || t('admin:resources.noDescription')}
        </p>
        <MetaList>
          <MetaRow label={t('admin:resources.details.resourceId')} value={item.id} mono />
          <MetaRow label={t('admin:resources.details.created')} value={formatDate(item.created_at)} />
          <MetaRow label={t('admin:resources.details.lastActivity')} value={formatDate(item.last_activity_at)} />
        </MetaList>
      </DetailSection>

      <DetailSection title={t('admin:resources.details.owner')}>
        <MetaList>
          <MetaRow label={t('admin:resources.details.user')} value={ownerDetail(item.creator_name, item.creator_email, item.creator_id)} />
          <MetaRow label={t('admin:resources.details.workspace')} value={item.workspace_name || t('admin:resources.details.personal')} />
          {item.workspace_id ? <MetaRow label={t('admin:resources.details.workspaceId')} value={item.workspace_id} mono /> : null}
          {item.workspace_owner_id ? (
            <MetaRow label={t('admin:resources.details.workspaceOwner')} value={ownerDetail(item.workspace_owner_name ?? '', item.workspace_owner_email ?? '', item.workspace_owner_id)} />
          ) : null}
        </MetaList>
      </DetailSection>

      <DetailSection title={t('admin:resources.details.index')}>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <Stat label={t('admin:resources.details.documents')} value={item.document_count} />
          <Stat label={t('admin:resources.details.ready')} value={item.ready_document_count} tone="success" />
          <Stat label={t('admin:resources.details.processing')} value={item.processing_document_count} tone="warning" />
          <Stat label={t('admin:resources.details.failed')} value={item.failed_document_count} tone="danger" />
          <Stat label={t('admin:resources.details.chunks')} value={item.chunk_count} />
          <Stat label={t('admin:resources.details.size')} value={formatBytes(item.total_size_bytes)} />
          <Stat label={t('admin:resources.details.shares')} value={item.share_count} />
          <Stat label={t('admin:resources.details.dimension')} value={item.embedding_dim || '-'} />
        </div>
        <MetaList>
          <MetaRow label={t('admin:resources.details.embeddingModel')} value={item.embedding_model_label || item.embedding_model_id || '-'} />
          <MetaRow label={t('admin:resources.details.modelId')} value={item.embedding_model_id || '-'} mono />
          <MetaRow
            label={t('admin:resources.details.modelStatus')}
            value={t(item.embedding_model_enabled ? 'admin:resources.details.enabled' : 'admin:resources.details.disabled')}
          />
        </MetaList>
      </DetailSection>

      <DetailSection title={t('admin:resources.details.sharedMembers')} icon={<Share2 size={14} aria-hidden />}>
        {item.shares.length ? (
          <ul className="divide-y divide-[var(--color-divider)] border-y border-[var(--color-divider)]">
            {item.shares.map((share) => (
              <li key={share.user_id} className="flex items-center justify-between gap-3 py-3 text-sm">
                <span className="min-w-0">
                  <span className="block truncate text-[var(--color-fg)]">{resourceOwner(share.name, share.email, share.user_id)}</span>
                  <span className="mt-0.5 block truncate text-[11px] text-[var(--color-fg-subtle)]">
                    {[share.email, share.created_at ? formatDate(share.created_at) : ''].filter(Boolean).join(' · ')}
                  </span>
                </span>
                <Badge size="xs">{share.role || 'read'}</Badge>
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-sm text-[var(--color-fg-muted)]">{t('admin:resources.details.noShares')}</p>
        )}
      </DetailSection>

      <DocumentList documents={documents} formatDate={formatDate} t={t} />
    </div>
  )
}

function ProjectDetails({
  item,
  documents,
  conversations,
  conversationPage,
  conversationsLoading,
  conversationsError,
  onConversationPage,
  onRetryConversations,
  formatDate,
  t,
}: {
  item: ApiAdminProjectResourceDetail
  documents: ApiDocument[]
  conversations: ApiAdminResourcePage<ApiAdminProjectConversation> | null
  conversationPage: number
  conversationsLoading: boolean
  conversationsError: string
  onConversationPage: (page: number) => void
  onRetryConversations: () => void
  formatDate: (value: number) => string
  t: (key: string, options?: Record<string, unknown>) => string
}) {
  return (
    <div className="space-y-7">
      <DetailSection title={t('admin:resources.details.basic')}>
        <p className="whitespace-pre-wrap break-words text-sm leading-relaxed text-[var(--color-fg)]">
          {item.description || t('admin:resources.noDescription')}
        </p>
        <MetaList>
          <MetaRow label={t('admin:resources.details.resourceId')} value={item.id} mono />
          <MetaRow label={t('admin:resources.details.emoji')} value={item.emoji || '-'} />
          <MetaRow label={t('admin:resources.details.accent')} value={item.accent || '-'} />
          <MetaRow label={t('admin:resources.details.pinned')} value={t(item.pinned ? 'admin:resources.details.yes' : 'admin:resources.details.no')} />
          <MetaRow label={t('admin:resources.details.autoUploads')} value={t(item.auto_add_uploads ? 'admin:resources.details.enabled' : 'admin:resources.details.disabled')} />
          <MetaRow label={t('admin:resources.details.created')} value={formatDate(item.created_at)} />
          <MetaRow label={t('admin:resources.details.updated')} value={formatDate(item.updated_at)} />
          <MetaRow label={t('admin:resources.details.lastActivity')} value={formatDate(item.last_activity_at)} />
        </MetaList>
      </DetailSection>

      <DetailSection title={t('admin:resources.details.instructions')}>
        <div className="rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3.5 py-3 text-sm leading-relaxed text-[var(--color-fg)]">
          <p className="whitespace-pre-wrap break-words">{item.instructions || t('admin:resources.details.noInstructions')}</p>
        </div>
      </DetailSection>

      <DetailSection title={t('admin:resources.details.owner')}>
        <MetaList>
          <MetaRow label={t('admin:resources.details.user')} value={ownerDetail(item.creator_name, item.creator_email, item.creator_id)} />
          <MetaRow label={t('admin:resources.details.workspace')} value={item.workspace_name || t('admin:resources.details.personal')} />
          {item.workspace_id ? <MetaRow label={t('admin:resources.details.workspaceId')} value={item.workspace_id} mono /> : null}
          {item.workspace_owner_id ? (
            <MetaRow label={t('admin:resources.details.workspaceOwner')} value={ownerDetail(item.workspace_owner_name ?? '', item.workspace_owner_email ?? '', item.workspace_owner_id)} />
          ) : null}
        </MetaList>
      </DetailSection>

      <DetailSection title={t('admin:resources.details.knowledgeBase')} icon={<BookOpen size={14} aria-hidden />}>
        {item.kb_description ? (
          <p className="mb-3 whitespace-pre-wrap break-words text-sm leading-relaxed text-[var(--color-fg-muted)]">
            {item.kb_description}
          </p>
        ) : null}
        <MetaList>
          <MetaRow label={t('admin:resources.details.name')} value={item.kb_name || t('admin:resources.details.none')} />
          <MetaRow label={t('admin:resources.details.knowledgeBaseId')} value={item.kb_id || '-'} mono />
          <MetaRow label={t('admin:resources.details.embeddingModel')} value={item.embedding_model_label || item.embedding_model_id || '-'} />
          <MetaRow label={t('admin:resources.details.modelId')} value={item.embedding_model_id || '-'} mono />
          <MetaRow
            label={t('admin:resources.details.modelStatus')}
            value={item.embedding_model_id ? t(item.embedding_model_enabled ? 'admin:resources.details.enabled' : 'admin:resources.details.disabled') : '-'}
          />
          <MetaRow label={t('admin:resources.details.dimension')} value={item.embedding_dim ? String(item.embedding_dim) : '-'} />
        </MetaList>
        <div className="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-4">
          <Stat label={t('admin:resources.details.documents')} value={item.document_count} />
          <Stat label={t('admin:resources.details.ready')} value={item.ready_document_count} tone="success" />
          <Stat label={t('admin:resources.details.processing')} value={item.processing_document_count} tone="warning" />
          <Stat label={t('admin:resources.details.failed')} value={item.failed_document_count} tone="danger" />
          <Stat label={t('admin:resources.details.chunks')} value={item.chunk_count} />
          <Stat label={t('admin:resources.details.size')} value={formatBytes(item.total_size_bytes)} />
        </div>
      </DetailSection>

      <DocumentList documents={documents} formatDate={formatDate} t={t} />

      <DetailSection title={t('admin:resources.details.conversations')} icon={<MessageSquare size={14} aria-hidden />}>
        <div className="grid grid-cols-3 gap-2">
          <Stat label={t('admin:resources.details.total')} value={item.conversation_count} />
          <Stat label={t('admin:resources.details.active')} value={item.active_conversation_count} tone="success" />
          <Stat label={t('admin:resources.details.archived')} value={item.archived_conversation_count} />
        </div>
        <div className={cn('mt-3 transition-opacity', conversationsLoading && conversations && 'pointer-events-none opacity-60')}>
          {conversationsError && !conversations ? (
            <InlineError message={conversationsError} onRetry={onRetryConversations} t={t} />
          ) : conversations?.items.length ? (
            <>
              {conversationsError ? <InlineError message={conversationsError} onRetry={onRetryConversations} t={t} /> : null}
              <ul className="divide-y divide-[var(--color-divider)] border-y border-[var(--color-divider)]">
                {conversations.items.map((conversation) => (
                  <li key={conversation.id}>
                    <Link
                      to={`/admin/users/${encodeURIComponent(conversation.creator_id)}/conversations/${encodeURIComponent(conversation.id)}`}
                      className="group flex items-center gap-3 py-3 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                    >
                      <MessageSquare size={14} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-sm text-[var(--color-fg)]">{conversation.title || t('admin:resources.details.untitled')}</span>
                        <span className="mt-0.5 block truncate text-[11px] text-[var(--color-fg-subtle)]">
                          {[conversation.model_label || conversation.model_id, resourceOwner(conversation.creator_name, conversation.creator_email, conversation.creator_id), formatDate(conversation.updated_at)].filter(Boolean).join(' · ')}
                        </span>
                      </span>
                      {conversation.archived ? <Badge size="xs">{t('admin:resources.details.archived')}</Badge> : null}
                      <ChevronRight size={14} className="text-[var(--color-fg-faint)] transition-transform group-hover:translate-x-0.5" aria-hidden />
                    </Link>
                  </li>
                ))}
              </ul>
              <Pagination
                page={conversationPage}
                pageCount={Math.ceil(conversations.total / PROJECT_CONVERSATION_PAGE_SIZE)}
                onPage={onConversationPage}
              />
            </>
          ) : conversationsLoading ? (
            <div className="space-y-2 py-2" role="status">
              <Skeleton className="h-14" />
              <Skeleton className="h-14" />
            </div>
          ) : (
            <p className="text-sm text-[var(--color-fg-muted)]">{t('admin:resources.details.noConversations')}</p>
          )}
        </div>
      </DetailSection>
    </div>
  )
}

function ImageDetails({
  item,
  formatDate,
  onPreview,
  t,
}: {
  item: ApiAdminGeneratedImageResource
  formatDate: (value: number) => string
  onPreview: () => void
  t: (key: string, options?: Record<string, unknown>) => string
}) {
  return (
    <div className="space-y-7">
      <button
        type="button"
        onClick={onPreview}
        className="block aspect-[4/3] w-full overflow-hidden rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface-sunken)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        aria-label={t('admin:resources.details.openImage')}
      >
        <ResourceImage src={item.url} alt={item.filename} contain />
      </button>

      <DetailSection title={t('admin:resources.details.prompt')}>
        <p className="whitespace-pre-wrap break-words text-sm leading-relaxed text-[var(--color-fg)]">
          {item.prompt || t('admin:resources.details.promptMissing')}
        </p>
      </DetailSection>

      <DetailSection title={t('admin:resources.details.metadata')}>
        <MetaList>
          <MetaRow label={t('admin:resources.details.user')} value={ownerDetail(item.user_name, item.user_email, item.user_id)} />
          <MetaRow label={t('admin:resources.details.model')} value={item.model_label || item.model_id || '-'} />
          <MetaRow label={t('admin:resources.details.modelId')} value={item.model_id || '-'} mono />
          <MetaRow label={t('admin:resources.details.workspace')} value={item.workspace_name || t('admin:resources.details.personal')} />
          {item.workspace_id ? <MetaRow label={t('admin:resources.details.workspaceId')} value={item.workspace_id} mono /> : null}
          <MetaRow label={t('admin:resources.details.filename')} value={item.filename || '-'} />
          <MetaRow label={t('admin:resources.details.fileType')} value={item.mime_type || '-'} />
          <MetaRow label={t('admin:resources.details.size')} value={formatBytes(item.size_bytes)} />
          <MetaRow label={t('admin:resources.details.generated')} value={formatDate(item.created_at)} />
          <MetaRow label={t('admin:resources.details.artifactId')} value={item.id} mono />
          <MetaRow label={t('admin:resources.details.messageId')} value={item.message_id || '-'} mono />
          <MetaRow label={t('admin:resources.details.conversationId')} value={item.conversation_id || '-'} mono />
        </MetaList>
      </DetailSection>

      {item.conversation_id && item.user_id ? (
        <Button variant="secondary" size="sm" asChild trailingIcon={<ChevronRight size={14} aria-hidden />}>
          <Link to={`/admin/users/${encodeURIComponent(item.user_id)}/conversations/${encodeURIComponent(item.conversation_id)}`}>
            {item.conversation_title || t('admin:resources.details.openConversation')}
          </Link>
        </Button>
      ) : null}
    </div>
  )
}

function DocumentList({
  documents,
  formatDate,
  t,
}: {
  documents: ApiDocument[]
  formatDate: (value: number) => string
  t: (key: string, options?: Record<string, unknown>) => string
}) {
  return (
    <DetailSection title={t('admin:resources.details.documentList')} icon={<FileText size={14} aria-hidden />}>
      {documents.length ? (
        <ul className="divide-y divide-[var(--color-divider)] border-y border-[var(--color-divider)]">
          {documents.map((document) => (
            <li key={document.id} className="py-3">
              <div className="flex min-w-0 items-start justify-between gap-3">
                <div className="min-w-0">
                  <p className="truncate text-sm text-[var(--color-fg)]">{document.filename}</p>
                  <p className="mt-1 text-[11px] text-[var(--color-fg-subtle)]">
                    {[
                      document.mime_type,
                      formatBytes(document.size_bytes),
                      t('admin:resources.counts.chunks', { count: document.chunk_count }),
                      document.uploaded_by_name || document.uploaded_by_email
                        ? resourceOwner(document.uploaded_by_name ?? '', document.uploaded_by_email ?? '', document.uploaded_by_user_id ?? '')
                        : '',
                      formatDate(document.created_at),
                    ].filter(Boolean).join(' · ')}
                  </p>
                </div>
                <DocumentStatus status={document.status} t={t} />
              </div>
              {document.error ? <p className="mt-2 break-words text-[11px] text-[var(--color-danger)]">{document.error}</p> : null}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-sm text-[var(--color-fg-muted)]">{t('admin:resources.details.noDocuments')}</p>
      )}
    </DetailSection>
  )
}

function ResourceImage({
  src,
  alt,
  contain = false,
  className,
}: {
  src: string
  alt: string
  contain?: boolean
  className?: string
}) {
  const { t } = useTranslation('admin')
  const [loaded, setLoaded] = useState(false)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    setLoaded(false)
    setFailed(false)
  }, [src])

  return (
    <span className="relative block h-full w-full overflow-hidden">
      {!loaded && !failed ? <Skeleton className="absolute inset-0 h-full w-full rounded-none" /> : null}
      {failed ? (
        <span className="absolute inset-0 flex flex-col items-center justify-center gap-2 px-3 text-center text-xs text-[var(--color-fg-subtle)]">
          <ImageIcon size={20} aria-hidden />
          {t('resources.details.imageUnavailable')}
        </span>
      ) : (
        <img
          src={src}
          alt={alt}
          loading="lazy"
          onLoad={() => setLoaded(true)}
          onError={() => setFailed(true)}
          className={cn(
            'h-full w-full transition-[opacity,transform] duration-200',
            contain ? 'object-contain' : 'object-cover',
            loaded ? 'opacity-100' : 'opacity-0',
            className,
          )}
        />
      )}
    </span>
  )
}

function DocumentSummary({
  item,
  t,
}: {
  item: Pick<ApiAdminKnowledgeBaseResource, 'document_count' | 'ready_document_count' | 'failed_document_count' | 'processing_document_count'>
  t: (key: string, options?: Record<string, unknown>) => string
}) {
  return (
    <span className="hidden text-[12px] tabular-nums md:block">
      <span className="text-[var(--color-fg)]">{item.document_count}</span>
      {item.failed_document_count ? (
        <span className="ml-1.5 text-[var(--color-danger)]" title={t('admin:resources.details.failed')}>+{item.failed_document_count}</span>
      ) : item.processing_document_count ? (
        <span className="ml-1.5 text-[var(--color-warning)]" title={t('admin:resources.details.processing')}>+{item.processing_document_count}</span>
      ) : null}
    </span>
  )
}

function DocumentStatus({ status, t }: { status: ApiDocument['status']; t: (key: string) => string }) {
  const variant = status === 'ready' ? 'success' : status === 'failed' ? 'danger' : 'warning'
  return <Badge size="xs" variant={variant}>{t(`admin:resources.documentStatus.${status}`)}</Badge>
}

function DetailSection({ title, icon, children }: { title: string; icon?: ReactNode; children: ReactNode }) {
  return (
    <section>
      <h3 className="flex items-center gap-1.5 text-xs font-medium uppercase text-[var(--color-fg-subtle)]">
        {icon}
        {title}
      </h3>
      <div className="mt-2.5">{children}</div>
    </section>
  )
}

function MetaList({ children }: { children: ReactNode }) {
  return <dl className="mt-3 divide-y divide-[var(--color-divider)] border-y border-[var(--color-divider)] text-[12.5px]">{children}</dl>
}

function MetaRow({ label, value, mono = false }: { label: string; value: ReactNode; mono?: boolean }) {
  return (
    <div className="grid grid-cols-[8rem_minmax(0,1fr)] gap-3 py-2.5">
      <dt className="text-[var(--color-fg-subtle)]">{label}</dt>
      <dd className={cn('min-w-0 break-words text-right text-[var(--color-fg)]', mono && 'font-mono text-[11px]')}>{value}</dd>
    </div>
  )
}

function Stat({ label, value, tone = 'neutral' }: { label: string; value: ReactNode; tone?: 'neutral' | 'success' | 'warning' | 'danger' }) {
  return (
    <div className="min-w-0 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2.5">
      <p className="truncate text-[11px] text-[var(--color-fg-subtle)]">{label}</p>
      <p className={cn(
        'mt-1 truncate text-base font-medium tabular-nums text-[var(--color-fg)]',
        tone === 'success' && 'text-[var(--color-success)]',
        tone === 'warning' && 'text-[var(--color-warning)]',
        tone === 'danger' && 'text-[var(--color-danger)]',
      )}>{value}</p>
    </div>
  )
}

function ListSkeleton() {
  return (
    <div className="space-y-1 p-2" role="status">
      {Array.from({ length: 6 }, (_, index) => (
        <div key={index} className="flex min-h-20 items-center gap-4 px-3 py-3">
          <div className="min-w-0 flex-1 space-y-2">
            <Skeleton shape="line" className="w-2/5" />
            <Skeleton shape="line" className="w-4/5" />
          </div>
          <Skeleton className="hidden h-8 w-24 md:block" />
          <Skeleton className="size-4" />
        </div>
      ))}
    </div>
  )
}

function ImageSkeleton() {
  return (
    <div className="grid grid-cols-1 gap-px bg-[var(--color-divider)] p-px sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4" role="status">
      {Array.from({ length: 8 }, (_, index) => (
        <div key={index} className="bg-[var(--color-surface)] p-3">
          <Skeleton className="aspect-[4/3]" />
          <Skeleton shape="line" className="mt-3 w-4/5" />
          <Skeleton shape="line" className="mt-2 w-2/5" />
        </div>
      ))}
    </div>
  )
}

function DetailSkeleton() {
  return (
    <div className="space-y-7" role="status">
      <div className="space-y-3">
        <Skeleton shape="line" className="w-28" />
        <Skeleton className="h-24" />
      </div>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {Array.from({ length: 4 }, (_, index) => <Skeleton key={index} className="h-16" />)}
      </div>
      <div className="space-y-2">
        {Array.from({ length: 5 }, (_, index) => <Skeleton key={index} className="h-10" />)}
      </div>
    </div>
  )
}

function ListError({ message, onRetry, t }: { message: string; onRetry: () => void; t: (key: string) => string }) {
  return (
    <div className="flex flex-col items-center px-6 py-16 text-center" role="alert">
      <CircleAlert size={24} className="text-[var(--color-danger)]" aria-hidden />
      <p className="mt-3 max-w-md text-sm text-[var(--color-fg-muted)]">{message}</p>
      <Button variant="secondary" size="sm" className="mt-4" onClick={onRetry}>{t('admin:resources.retry')}</Button>
    </div>
  )
}

function InlineError({ message, onRetry, t }: { message: string; onRetry: () => void; t: (key: string) => string }) {
  return (
    <div className="flex items-center justify-between gap-3 border-b border-[var(--color-danger)]/20 bg-[var(--color-danger-soft)] px-4 py-2.5 text-xs text-[var(--color-danger)]" role="alert">
      <span className="min-w-0 break-words">{message}</span>
      <Button variant="ghost" size="xs" onClick={onRetry} className="shrink-0 text-[var(--color-danger)]">{t('admin:resources.retry')}</Button>
    </div>
  )
}

function DetailError({ message, retryLabel, onRetry }: { message: string; retryLabel: string; onRetry: () => void }) {
  return (
    <div className="flex min-h-64 flex-col items-center justify-center px-6 text-center" role="alert">
      <CircleAlert size={24} className="text-[var(--color-danger)]" aria-hidden />
      <p className="mt-3 max-w-md text-sm text-[var(--color-fg-muted)]">{message}</p>
      <Button variant="secondary" size="sm" className="mt-4" onClick={onRetry}>{retryLabel}</Button>
    </div>
  )
}

function detailTitle(detail: DetailState | null): string {
  if (!detail) return ''
  if (detail.kind === 'images') return detail.summary.filename
  return detail.summary.name
}
