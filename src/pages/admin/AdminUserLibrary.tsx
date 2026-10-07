/**
 * AdminUserLibrary — read-only drill-down into a single user's projects and
 * knowledge bases, for support / triage (§8.1). Companion to
 * AdminUserConversations. Bypasses the per-user ownership filter (admin gate);
 * no edit/delete — viewing only. Tokens-only, matches the rest of /admin.
 */
import { Fragment, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { FolderClosed, Library, ChevronDown, ImageIcon } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiProject, ApiAdminKnowledgeBase, ApiDocument, ApiUser, ApiAdminImage } from '@/api/types'
import { AdminDetailHeader } from '@/components/admin/admin-detail-header'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useModels } from '@/store/models'
import { toast } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'
import { envNum } from '@/lib/env-config'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { AdminTable } from '@/components/admin/AdminTable'

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
  const [kbs, setKbs] = useState<ApiAdminKnowledgeBase[]>([])
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

      <AdminPageHeader
        title={t('users.libraryTitle', { name: headerName })}
        description={t('users.libraryLead')}
      />

      {loading ? (
        <PanelFallback />
      ) : (
        <>
          {/* Projects */}
          <section className="mt-8">
            <h2 className="flex items-center gap-2 text-lg font-medium tracking-normal text-[var(--color-fg)]">
              <FolderClosed size={15} aria-hidden className="text-[var(--color-fg-subtle)]" />
              {t('users.projectsHeading')}
              <span className="text-[12px] text-[var(--color-fg-subtle)] tabular-nums">· {projects.length}</span>
            </h2>
            {projects.length === 0 ? (
              <div className="mt-3 text-sm text-[var(--color-fg-subtle)] rounded-[12px] bg-[var(--color-surface)] px-5 py-8 text-center">
                {t('users.noProjects')}
              </div>
            ) : (
              <AdminTable
                items={projects}
                rowKey={(p) => p.id}
                label={t('users.projectsHeading')}
                className="mt-3"
                columns={[
                  { id: 'name', header: t('admin:resources.table.name'), width: 260, render: (p) => <div className="flex min-w-0 items-center gap-2"><span aria-hidden>{p.emoji || '📁'}</span><span className="truncate font-medium" title={p.name}>{p.name}</span>{p.pinned ? <Badge size="xs">{t('users.pinned')}</Badge> : null}</div> },
                  { id: 'description', header: t('admin:groups.fields.description'), width: 320, render: (p) => <span className="block truncate text-[var(--color-fg-muted)]" title={p.description}>{p.description || '—'}</span> },
                  { id: 'created', header: t('admin:redeemCodes.table.createdAt'), width: 170, render: (p) => <span className="text-[12px] tabular-nums text-[var(--color-fg-muted)]">{formatStamp(p.created_at)}</span> },
                ]}
              />
            )}
          </section>

          {/* Knowledge bases */}
          <section className="mt-10">
            <h2 className="flex items-center gap-2 text-lg font-medium tracking-normal text-[var(--color-fg)]">
              <Library size={15} aria-hidden className="text-[var(--color-fg-subtle)]" />
              {t('users.kbsHeading')}
              <span className="text-[12px] text-[var(--color-fg-subtle)] tabular-nums">· {kbs.length}</span>
            </h2>
            {kbs.length === 0 ? (
              <div className="mt-3 text-sm text-[var(--color-fg-subtle)] rounded-[12px] bg-[var(--color-surface)] px-5 py-8 text-center">
                {t('users.noKbs')}
              </div>
            ) : (
              <AdminTable
                items={kbs}
                rowKey={(k) => k.id}
                label={t('users.kbsHeading')}
                className="mt-3"
                columns={[
                  { id: 'name', header: t('admin:resources.table.name'), width: 230, render: (k) => <button type="button" className="admin-table-link font-medium" aria-expanded={openKb === k.id} onClick={() => void toggleKb(k.id)} title={k.name}>{k.name}</button> },
                  { id: 'description', header: t('admin:groups.fields.description'), width: 260, render: (k) => <span className="block truncate text-[var(--color-fg-muted)]" title={k.description}>{k.description || '—'}</span> },
                  { id: 'project', header: t('users.projectsHeading'), width: 170, render: (k) => <span className="block truncate">{k.project_id ? projectName(k.project_id) || t('users.inProject') : '—'}</span> },
                  { id: 'model', header: t('admin:resources.table.model'), width: 190, render: (k) => <div className="min-w-0"><span className="block truncate">{getModelById(k.embedding_model_id)?.label || k.embedding_model_id || '—'}</span>{k.embedding_dim ? <span className="text-[12px] text-[var(--color-fg-muted)]">{k.embedding_dim}d</span> : null}</div> },
                  { id: 'created', header: t('admin:redeemCodes.table.createdAt'), width: 150, render: (k) => <span className="text-[12px] tabular-nums text-[var(--color-fg-muted)]">{formatStamp(k.created_at)}</span> },
                  { id: 'actions', header: t('admin:common.actions'), width: 60, align: 'right', render: (k) => <Button variant="ghost" size="icon-sm" title={t('admin:resources.table.documents')} aria-label={t('admin:resources.table.documents')} aria-expanded={openKb === k.id} onClick={() => void toggleKb(k.id)}><ChevronDown size={15} className={cn('transition-transform', openKb === k.id && 'rotate-180')} aria-hidden /></Button> },
                ]}
                renderRow={(k, _index, cells) => (
                  <Fragment key={k.id}>
                    <tr>{cells}</tr>
                    {openKb === k.id ? (
                      <tr><td colSpan={6}>
                        {kbLoading === k.id ? <PanelFallback /> : (
                          <AdminTable
                            items={kbDocs[k.id] ?? []}
                            rowKey={(doc) => doc.id}
                            label={t('admin:resources.table.documents')}
                            embedded
                            emptyMessage={t('users.noDocuments')}
                            columns={[
                              { id: 'filename', header: t('admin:files.table.filename'), width: 300, render: (doc) => <span className="block truncate" title={doc.filename}>{doc.filename}</span> },
                              { id: 'status', header: t('admin:common.status'), width: 130, render: (doc) => <Badge size="xs" variant={doc.status === 'failed' ? 'danger' : doc.status === 'ready' ? 'success' : 'neutral'}>{t(`admin:resources.documentStatus.${doc.status}`, { defaultValue: doc.status })}</Badge> },
                              { id: 'size', header: t('admin:files.table.size'), width: 120, render: (doc) => <span className="tabular-nums">{formatBytes(doc.size_bytes)}</span> },
                              { id: 'chunks', header: t('admin:common.details'), width: 130, render: (doc) => t('users.chunks', { count: doc.chunk_count }) },
                            ]}
                          />
                        )}
                      </td></tr>
                    ) : null}
                  </Fragment>
                )}
              />
            )}
          </section>

          {/* Image gallery — every image the user generated (drawing mode + chat
              tool-call alike). Clicking a tile opens its source conversation. */}
          <section className="mt-10">
            <h2 className="flex items-center gap-2 text-lg font-medium tracking-normal text-[var(--color-fg)]">
              <ImageIcon size={15} aria-hidden className="text-[var(--color-fg-subtle)]" />
              {t('users.imagesHeading', { defaultValue: 'Image gallery' })}
              <span className="text-[12px] text-[var(--color-fg-subtle)] tabular-nums">· {images.length}</span>
            </h2>
            {images.length === 0 ? (
              <div className="mt-3 text-sm text-[var(--color-fg-subtle)] rounded-[12px] bg-[var(--color-surface)] px-5 py-8 text-center">
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
                    className="group relative aspect-square overflow-hidden rounded-[12px] bg-[var(--color-bg-muted)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
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
                    <span className="pointer-events-none absolute inset-x-0 bottom-0 truncate bg-gradient-to-t from-[var(--color-overlay)] to-transparent px-2 py-1.5 text-left text-[12px] text-[var(--color-fg-inverted)]">
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
