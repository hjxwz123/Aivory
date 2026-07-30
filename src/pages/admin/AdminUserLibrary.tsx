/**
 * AdminUserLibrary — read-only drill-down into a single user's projects and
 * knowledge bases, for support / triage (§8.1). Companion to
 * AdminUserConversations. Bypasses the per-user ownership filter (admin gate);
 * no edit/delete — viewing only. Tokens-only, matches the rest of /admin.
 */
import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { FolderClosed, Library, ChevronDown, FileText, ImageIcon } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiProject, ApiKnowledgeBase, ApiDocument, ApiUser, ApiAdminImage } from '@/api/types'
import { AdminDetailHeader } from '@/components/admin/admin-detail-header'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useModels } from '@/store/models'
import { toast } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'
import { envNum } from '@/lib/env-config'
import { PanelFallback } from '@/components/ui/panel-fallback'

function formatStamp(unixSec: number): string {
  if (!unixSec) return ''
  try {
    return new Date(unixSec * 1000).toLocaleDateString()
  } catch {
    return String(unixSec)
  }
}

const IMAGES_PAGE = envNum('VITE_AIVORY_IMAGES_PAGE', 60)

function formatBytes(n: number): string {
  if (!n) return ''
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

export default function AdminUserLibrary() {
  const { t } = useTranslation(['admin', 'common'])
  const navigate = useNavigate()
  const { id = '' } = useParams<{ id: string }>()
  const [user, setUser] = useState<ApiUser | null>(null)
  const [projects, setProjects] = useState<ApiProject[]>([])
  const [kbs, setKbs] = useState<ApiKnowledgeBase[]>([])
  const [images, setImages] = useState<ApiAdminImage[]>([])
  const [imagesMore, setImagesMore] = useState(false)
  const [imagesLoadingMore, setImagesLoadingMore] = useState(false)
  const [loading, setLoading] = useState(true)
  // Lazy-loaded documents per KB (expand a KB row to view its files).
  const [openKb, setOpenKb] = useState<string | null>(null)
  const [kbDocs, setKbDocs] = useState<Record<string, ApiDocument[]>>({})
  const [kbLoading, setKbLoading] = useState<string | null>(null)

  async function toggleKb(kbId: string) {
    if (openKb === kbId) {
      setOpenKb(null)
      return
    }
    setOpenKb(kbId)
    if (!kbDocs[kbId]) {
      setKbLoading(kbId)
      try {
        const docs = await adminApi.kbDocuments(kbId)
        setKbDocs((m) => ({ ...m, [kbId]: docs }))
      } catch (e) {
        toast.error(e instanceof ApiError ? e.message : t('common.failed'))
      } finally {
        setKbLoading(null)
      }
    }
  }

  async function loadMoreImages() {
    if (imagesLoadingMore) return
    setImagesLoadingMore(true)
    try {
      const next = await adminApi.userImages(id, IMAGES_PAGE, images.length)
      setImages((cur) => [...cur, ...next])
      setImagesMore(next.length === IMAGES_PAGE)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common.failed'))
    } finally {
      setImagesLoadingMore(false)
    }
  }

  // Resolve a KB's embedding model id → label (the raw m_… id is opaque).
  const getModelById = useModels((s) => s.getById)
  const modelsLoaded = useModels((s) => s.loaded)
  const loadModels = useModels((s) => s.load)
  useEffect(() => {
    if (!modelsLoaded) void loadModels()
  }, [modelsLoaded, loadModels])

  useEffect(() => {
    let cancelled = false
    async function load() {
      setLoading(true)
      try {
        const [users, ps, ks, imgs] = await Promise.all([
          adminApi.users('', 200, 0).then((r) => r.users),
          adminApi.userProjects(id),
          adminApi.userKbs(id),
          adminApi.userImages(id, IMAGES_PAGE, 0).catch(() => [] as ApiAdminImage[]),
        ])
        if (cancelled) return
        setUser(users.find((u) => u.id === id) ?? null)
        setProjects(ps)
        setKbs(ks)
        setImages(imgs)
        setImagesMore(imgs.length === IMAGES_PAGE)
      } catch (e) {
        if (!cancelled) toast.error(e instanceof ApiError ? e.message : t('common.failed'))
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [id, t])

  const headerName = useMemo(() => user?.name || user?.email || id, [user, id])
  const projectName = (pid: string) => projects.find((p) => p.id === pid)?.name

  return (
    <div>
      <AdminDetailHeader backTo="/admin/users" backLabel={t('users.backToUsers')} />

      <header>
        <h1 className="break-words font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">
          {t('users.libraryTitle', { name: headerName })}
        </h1>
        <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('users.libraryLead')}</p>
      </header>

      {loading ? (
        <PanelFallback />
      ) : (
        <>
          {/* Projects */}
          <section className="mt-8">
            <h2 className="flex items-center gap-2 font-serif text-lg text-[var(--color-fg)]">
              <FolderClosed size={15} aria-hidden className="text-[var(--color-fg-subtle)]" />
              {t('users.projectsHeading')}
              <span className="text-[12px] text-[var(--color-fg-subtle)] tabular-nums">· {projects.length}</span>
            </h2>
            {projects.length === 0 ? (
              <div className="mt-3 text-sm text-[var(--color-fg-subtle)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-5 py-8 text-center">
                {t('users.noProjects')}
              </div>
            ) : (
              <ul className="mt-3 flex flex-col divide-y divide-[var(--color-divider)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
                {projects.map((p) => (
                  <li key={p.id} className="grid grid-cols-[auto_minmax(0,1fr)] items-start gap-x-3 gap-y-1 px-3 py-3 sm:grid-cols-[auto_minmax(0,1fr)_auto] sm:items-center sm:px-5 sm:py-4">
                    <span aria-hidden className="text-lg">{p.emoji || '📁'}</span>
                    <div className="min-w-0">
                      <div className="flex min-w-0 flex-wrap items-center gap-1.5 sm:gap-2">
                        <span className="line-clamp-2 min-w-0 break-words font-medium text-[var(--color-fg)] sm:line-clamp-1">{p.name}</span>
                        {p.pinned ? <Badge size="xs" variant="neutral">{t('users.pinned')}</Badge> : null}
                      </div>
                      {p.description ? (
                        <div className="mt-0.5 line-clamp-2 text-[12px] text-[var(--color-fg-subtle)] sm:line-clamp-1">{p.description}</div>
                      ) : null}
                    </div>
                    <span className="col-start-2 text-[11.5px] font-mono text-[var(--color-fg-subtle)] sm:col-auto sm:row-auto sm:shrink-0">
                      {formatStamp(p.created_at)}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </section>

          {/* Knowledge bases */}
          <section className="mt-10">
            <h2 className="flex items-center gap-2 font-serif text-lg text-[var(--color-fg)]">
              <Library size={15} aria-hidden className="text-[var(--color-fg-subtle)]" />
              {t('users.kbsHeading')}
              <span className="text-[12px] text-[var(--color-fg-subtle)] tabular-nums">· {kbs.length}</span>
            </h2>
            {kbs.length === 0 ? (
              <div className="mt-3 text-sm text-[var(--color-fg-subtle)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-5 py-8 text-center">
                {t('users.noKbs')}
              </div>
            ) : (
              <ul className="mt-3 flex flex-col divide-y divide-[var(--color-divider)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
                {kbs.map((k) => {
                  const model = k.embedding_model_id ? getModelById(k.embedding_model_id)?.label : ''
                  const meta = [
                    model || k.embedding_model_id,
                    k.embedding_dim ? `${k.embedding_dim}d` : '',
                    formatStamp(k.created_at),
                  ].filter(Boolean)
                  const open = openKb === k.id
                  const docs = kbDocs[k.id]
                  return (
                    <li key={k.id}>
                      <button
                        type="button"
                        onClick={() => void toggleKb(k.id)}
                        aria-expanded={open}
                        className="grid w-full grid-cols-[auto_minmax(0,1fr)_auto] items-start gap-2.5 px-3 py-3 text-left interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] sm:items-center sm:gap-3 sm:px-5 sm:py-4"
                      >
                        <Library size={14} aria-hidden className="mt-1 text-[var(--color-fg-subtle)] sm:mt-0" />
                        <div className="min-w-0">
                          <div className="flex min-w-0 flex-wrap items-center gap-1.5 sm:gap-2">
                            <span className="line-clamp-2 min-w-0 break-words font-medium text-[var(--color-fg)] sm:line-clamp-1">{k.name}</span>
                            {k.project_id ? (
                              <Badge size="xs" variant="neutral">
                                {projectName(k.project_id) || t('users.inProject')}
                              </Badge>
                            ) : null}
                          </div>
                          {k.description ? (
                            <div className="mt-0.5 line-clamp-2 text-[12px] text-[var(--color-fg-subtle)] sm:line-clamp-1">{k.description}</div>
                          ) : null}
                          <div className="mt-1 flex flex-wrap gap-x-2 gap-y-0.5 text-[11.5px] font-mono text-[var(--color-fg-subtle)] sm:mt-0.5">
                            {meta.map((part, index) => (
                              <span key={`${part}-${index}`} className="flex min-w-0 items-center gap-2 break-all sm:break-normal">
                                {index > 0 ? <span aria-hidden className="hidden sm:inline">·</span> : null}
                                {part}
                              </span>
                            ))}
                          </div>
                        </div>
                        <ChevronDown
                          size={15}
                          aria-hidden
                          className={cn('mt-1 text-[var(--color-fg-subtle)] transition-transform sm:mt-0', open && 'rotate-180')}
                        />
                      </button>
                      {open ? (
                        <div className="border-t border-[var(--color-divider)] bg-[var(--color-bg-muted)]/40 px-3 py-3 sm:px-5">
                          {kbLoading === k.id ? (
                            <PanelFallback />
                          ) : docs && docs.length > 0 ? (
                            <ul className="flex flex-col gap-1.5">
                              {docs.map((doc) => (
                                <li key={doc.id} className="grid grid-cols-[auto_minmax(0,1fr)] items-start gap-x-2.5 gap-y-1 py-1 text-[13px] sm:grid-cols-[auto_minmax(0,1fr)_auto_auto] sm:items-center sm:py-0">
                                  <FileText size={13} aria-hidden className="mt-0.5 shrink-0 text-[var(--color-fg-subtle)] sm:mt-0" />
                                  <span className="line-clamp-2 min-w-0 break-all text-[var(--color-fg)] sm:line-clamp-1">{doc.filename}</span>
                                  {doc.status !== 'ready' ? (
                                    <span className="col-start-2 sm:col-auto">
                                      <Badge size="xs" variant={doc.status === 'failed' ? 'danger' : 'neutral'}>
                                        {doc.status}
                                      </Badge>
                                    </span>
                                  ) : null}
                                  <span className="col-start-2 flex flex-wrap gap-x-2 text-[11px] font-mono tabular-nums text-[var(--color-fg-subtle)] sm:col-auto sm:shrink-0">
                                    {[doc.chunk_count ? t('users.chunks', { count: doc.chunk_count }) : '', formatBytes(doc.size_bytes)]
                                      .filter(Boolean)
                                      .map((part, index) => (
                                        <span key={`${part}-${index}`} className="flex items-center gap-2">
                                          {index > 0 ? <span aria-hidden className="hidden sm:inline">·</span> : null}
                                          {part}
                                        </span>
                                      ))}
                                  </span>
                                </li>
                              ))}
                            </ul>
                          ) : (
                            <div className="text-[12px] text-[var(--color-fg-subtle)]">{t('users.noDocuments')}</div>
                          )}
                        </div>
                      ) : null}
                    </li>
                  )
                })}
              </ul>
            )}
          </section>

          {/* Image gallery — every image the user generated (drawing mode + chat
              tool-call alike). Clicking a tile opens its source conversation. */}
          <section className="mt-10">
            <h2 className="flex items-center gap-2 font-serif text-lg text-[var(--color-fg)]">
              <ImageIcon size={15} aria-hidden className="text-[var(--color-fg-subtle)]" />
              {t('users.imagesHeading', { defaultValue: 'Image gallery' })}
              <span className="text-[12px] text-[var(--color-fg-subtle)] tabular-nums">· {images.length}</span>
            </h2>
            {images.length === 0 ? (
              <div className="mt-3 text-sm text-[var(--color-fg-subtle)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-5 py-8 text-center">
                {t('users.noImages', { defaultValue: 'No generated images.' })}
              </div>
            ) : (
              <div className="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
                {images.map((img) => (
                  <button
                    key={img.id}
                    type="button"
                    onClick={() =>
                      navigate(`/admin/users/${encodeURIComponent(id)}/conversations/${encodeURIComponent(img.conversation_id)}`)
                    }
                    title={img.conversation_title || t('users.viewConversations')}
                    className="group relative aspect-square overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    <img
                      src={img.url}
                      alt={img.conversation_title || img.filename}
                      loading="lazy"
                      onError={(e) => {
                        // Artifact row exists but the blob is gone (404) → hide the
                        // broken-image glyph; the muted tile + caption remain.
                        e.currentTarget.style.display = 'none'
                      }}
                      className="size-full object-cover transition-transform duration-200 group-hover:scale-[1.03]"
                    />
                    <span className="pointer-events-none absolute inset-x-0 bottom-0 truncate bg-gradient-to-t from-[var(--color-overlay)] to-transparent px-2 py-1.5 text-left text-[11px] text-[var(--color-fg-inverted)]">
                      {img.conversation_title || formatStamp(img.created_at)}
                    </span>
                  </button>
                ))}
              </div>
            )}
            {imagesMore ? (
              <div className="mt-4 text-center">
                <Button variant="ghost" size="sm" onClick={() => void loadMoreImages()} loading={imagesLoadingMore}>
                  {t('users.loadMore', { defaultValue: 'Load more' })}
                </Button>
              </div>
            ) : null}
          </section>
        </>
      )}

      <div className="mt-8">
        <Button asChild variant="ghost" size="sm">
          <Link to={`/admin/users/${encodeURIComponent(id)}/conversations`}>{t('users.viewConversations')}</Link>
        </Button>
      </div>
    </div>
  )
}
