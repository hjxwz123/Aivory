/**
 * SharedConversation — the public, unauthenticated read-only view of a shared
 * conversation (§ sharing). Renders a frozen snapshot served by
 * /api/public/shared/:token. No chat stores, no auth — safe to render to anyone
 * with the link. The owner can revoke the share at any time, which 404s this.
 */
import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { sharedApi, ApiError } from '@/api'
import type { ApiAttachment, ApiBlock, ApiSharedConversation } from '@/api/types'
import { Logo } from '@/components/brand/logo'
import { Markdown } from '@/components/chat/markdown'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { toast } from '@/hooks/use-toast'
import { useAuth } from '@/store/auth'
import { useConversations } from '@/store/conversations'
import { Copy, FileText, Ghost, Loader2 } from 'lucide-react'

function messageText(blocks: ApiBlock[]): string {
  return blocks
    .filter((b) => b.kind === 'text' && b.text)
    .map((b) => b.text as string)
    .join('\n\n')
}

// The snapshot's asset URLs point at the OWNER-authenticated routes
// (/api/files/:id, /api/artifacts/:id) which 401 for anonymous viewers. Rewrite
// them to the share-scoped public routes — the backend authorises by checking
// the id against this share's frozen snapshot (§ sharing).
function shareAssetUrl(token: string, url: string): string {
  const tok = encodeURIComponent(token)
  const file = url.match(/^\/api\/files\/([^/?#]+)$/)
  if (file) return `/api/public/shared/${tok}/files/${file[1]}`
  const art = url.match(/^\/api\/artifacts\/([^/?#]+)$/)
  if (art) return `/api/public/shared/${tok}/artifacts/${art[1]}`
  return url
}

function isImageAttachment(a: ApiAttachment): boolean {
  return a.kind === 'image' || (a.mime_type ?? '').startsWith('image/')
}

// Generated files ride in `artifact` blocks; `summary` carries the mime type
// (same field toLocalMessage reads). Fall back to the filename extension for
// older blocks that predate the mime backfill.
function isImageArtifact(b: ApiBlock): boolean {
  if ((b.summary ?? '').startsWith('image/')) return true
  return /\.(png|jpe?g|gif|webp|avif)$/i.test(b.title ?? b.url ?? '')
}

export default function SharedConversation() {
  const { token = '' } = useParams<{ token: string }>()
  const navigate = useNavigate()
  const { t } = useTranslation(['subscription', 'chat', 'common'])
  const authStatus = useAuth((s) => s.status)
  const user = useAuth((s) => s.user)
  const adoptConversation = useConversations((s) => s.adoptConversation)
  const [data, setData] = useState<ApiSharedConversation | null>(null)
  const [status, setStatus] = useState<'loading' | 'ready' | 'missing'>('loading')
  const [cloning, setCloning] = useState(false)
  const isAuthenticated = authStatus === 'authenticated' && Boolean(user)

  useEffect(() => {
    let active = true
    sharedApi
      .get(token)
      .then((d) => {
        if (!active) return
        setData(d)
        setStatus('ready')
      })
      .catch((e) => {
        if (!active) return
        setStatus(e instanceof ApiError && e.status === 404 ? 'missing' : 'missing')
      })
    return () => {
      active = false
    }
  }, [token])

  async function cloneToMyChats() {
    if (!token || cloning) return
    setCloning(true)
    try {
      const conv = await sharedApi.clone(token)
      adoptConversation(conv)
      toast.success(t('chat:share.cloneDone'))
      navigate(`/chat/${conv.id}`)
    } catch (e) {
      toast.error(t('chat:share.failed'), e instanceof ApiError ? e.message : undefined)
    } finally {
      setCloning(false)
    }
  }

  return (
    <div className="min-h-svh w-full bg-[var(--color-bg)] text-[var(--color-fg)]">
      <header className="sticky top-0 z-10 border-b border-[var(--color-divider)] bg-[var(--color-bg)]/85 backdrop-blur">
        <div className="mx-auto flex h-14 max-w-[72rem] items-center justify-between gap-3 px-5 sm:px-8">
          <Link to={isAuthenticated ? '/' : '/welcome'} aria-label="Aivory" className="inline-flex items-center">
            <Logo />
          </Link>
          <div className="flex min-w-0 items-center gap-2">
            {isAuthenticated ? (
              <Button
                type="button"
                variant="secondary"
                size="sm"
                onClick={cloneToMyChats}
                disabled={cloning || status !== 'ready' || !data}
                className="shrink-0"
              >
                {cloning ? (
                  <Loader2 size={14} aria-hidden className="animate-spin" />
                ) : (
                  <Copy size={14} aria-hidden />
                )}
                <span className="hidden sm:inline">{t('chat:share.cloneCta')}</span>
                <span className="sm:hidden">{t('chat:share.cloneShortCta')}</span>
              </Button>
            ) : (
              <Button asChild variant="secondary" size="sm">
                <Link to="/welcome">{t('chat:share.tryCta')}</Link>
              </Button>
            )}
          </div>
        </div>
      </header>

      <main className="mx-auto w-full max-w-[72rem] px-5 pt-10 pb-24 sm:px-8">
        {status === 'loading' ? (
          <div className="text-sm text-[var(--color-fg-subtle)]">{t('common:common.loading')}</div>
        ) : status === 'missing' || !data ? (
          <div className="pt-16">
            <EmptyState
              icon={<Ghost size={22} aria-hidden />}
              title={t('chat:share.missingTitle')}
              description={t('chat:share.missingBody')}
              action={
                <Button asChild variant="secondary">
                  <Link to="/welcome">{t('chat:share.tryCta')}</Link>
                </Button>
              }
            />
          </div>
        ) : (
          <>
            <div className="mb-2 text-[12px] uppercase tracking-[0.08em] text-[var(--color-fg-subtle)]">
              {t('chat:share.eyebrow')}
            </div>
            <h1 className="font-serif text-3xl sm:text-4xl tracking-tight text-[var(--color-fg)] text-balance">
              {data.title || t('chat:share.untitled')}
            </h1>
            <div className="mt-10 flex flex-col gap-8">
              {data.messages.map((m, i) => {
                const text = messageText(m.blocks)
                const atts = m.attachments ?? []
                const artifacts = m.blocks.filter((b) => b.kind === 'artifact' && b.url)
                // A message can be attachment-only (an uploaded image) or a pure
                // generated-image reply — those must still render (§ sharing).
                if (!text && atts.length === 0 && artifacts.length === 0) return null
                return (
                  <article key={i} className="flex flex-col gap-1.5">
                    <div className="text-[12px] font-medium uppercase tracking-[0.06em] text-[var(--color-fg-subtle)]">
                      {m.role === 'user' ? t('chat:share.roleUser') : t('chat:share.roleAssistant')}
                    </div>
                    {atts.length > 0 ? (
                      <div className="flex flex-wrap gap-2">
                        {atts.map((a) =>
                          isImageAttachment(a) ? (
                            <img
                              key={a.id}
                              src={shareAssetUrl(token, a.url)}
                              alt={a.filename}
                              loading="lazy"
                              className="max-h-64 max-w-full rounded-[12px] border border-[var(--color-border)] object-contain"
                            />
                          ) : (
                            <a
                              key={a.id}
                              href={shareAssetUrl(token, a.url)}
                              target="_blank"
                              rel="noopener noreferrer"
                              className="inline-flex items-center gap-1.5 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] px-2.5 py-1.5 text-[12.5px] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] [overflow-wrap:anywhere]"
                            >
                              <FileText size={13} aria-hidden className="shrink-0 text-[var(--color-fg-subtle)]" />
                              {a.filename}
                            </a>
                          ),
                        )}
                      </div>
                    ) : null}
                    {m.role === 'user' ? (
                      text ? (
                        <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3 text-[15px] leading-relaxed whitespace-pre-wrap">
                          {text}
                        </div>
                      ) : null
                    ) : (
                      <>
                        {text ? (
                          <div className="text-[15px] leading-relaxed">
                            <Markdown content={text} blockKeyPrefix={`share-${i}`} />
                          </div>
                        ) : null}
                        {artifacts.length > 0 ? (
                          <div className="flex flex-wrap gap-2">
                            {artifacts.map((b, j) =>
                              isImageArtifact(b) ? (
                                <img
                                  key={`${i}-art-${j}`}
                                  src={shareAssetUrl(token, b.url ?? '')}
                                  alt={b.title || 'image'}
                                  loading="lazy"
                                  className="max-h-96 max-w-full rounded-[12px] border border-[var(--color-border)] object-contain"
                                />
                              ) : (
                                <a
                                  key={`${i}-art-${j}`}
                                  href={shareAssetUrl(token, b.url ?? '')}
                                  target="_blank"
                                  rel="noopener noreferrer"
                                  className="inline-flex items-center gap-1.5 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] px-2.5 py-1.5 text-[12.5px] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] [overflow-wrap:anywhere]"
                                >
                                  <FileText size={13} aria-hidden className="shrink-0 text-[var(--color-fg-subtle)]" />
                                  {b.title || b.url}
                                </a>
                              ),
                            )}
                          </div>
                        ) : null}
                      </>
                    )}
                  </article>
                )
              })}
            </div>
            <footer className="mt-16 border-t border-[var(--color-divider)] pt-6 text-center text-[12px] text-[var(--color-fg-subtle)]">
              {t('chat:share.footer')}
            </footer>
          </>
        )}
      </main>
    </div>
  )
}
