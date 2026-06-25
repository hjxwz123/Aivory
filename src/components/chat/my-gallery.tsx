import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ImageIcon } from 'lucide-react'
import { imageApi } from '@/api/endpoints'
import { ApiError } from '@/api'
import type { ApiAdminImage } from '@/api/types'
import { Button } from '@/components/ui/button'
import { toast } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'

const PAGE = 60
// Varied aspect ratios so the loading skeleton previews a real masonry shape.
const SKELETON_ASPECTS = ['4/5', '1/1', '3/4', '4/5', '1/1', '4/5', '3/4', '1/1', '4/5', '3/4']

const prefersReduced = () =>
  typeof window !== 'undefined' && window.matchMedia('(prefers-reduced-motion: reduce)').matches

/**
 * MyGallery — §4.20 the signed-in user's generated-image gallery, shown below the
 * composer in drawing mode. An editorial masonry "plate section": a hairline rule
 * that draws open on scroll, tiles that fade in as the image loads and reveal on
 * scroll (IntersectionObserver), and a hover caption (conversation title + date).
 * Clicking a tile jumps to the conversation that made it. Token-only; all motion
 * honours prefers-reduced-motion.
 */
export function MyGallery() {
  const { t, i18n } = useTranslation('chat')
  const navigate = useNavigate()
  const [images, setImages] = useState<ApiAdminImage[]>([])
  const [loading, setLoading] = useState(true)
  const [more, setMore] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [reduce] = useState(prefersReduced)

  // One shared observer reveals elements (tiles + the header rule) as they enter
  // view by flipping data-shown; each element styles its own reaction.
  const obs = useRef<IntersectionObserver | null>(null)
  useEffect(() => {
    if (reduce) return
    obs.current = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.isIntersecting) {
            e.target.setAttribute('data-shown', 'true')
            obs.current?.unobserve(e.target)
          }
        }
      },
      { rootMargin: '0px 0px -8% 0px', threshold: 0.04 },
    )
    return () => obs.current?.disconnect()
  }, [reduce])
  const reveal = (el: Element | null) => {
    if (el && obs.current) obs.current.observe(el)
  }

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const imgs = await imageApi.myImages(PAGE, 0)
        if (cancelled) return
        setImages(imgs)
        setMore(imgs.length === PAGE)
      } catch {
        /* silent — just render the empty state */
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  async function loadMore() {
    if (loadingMore) return
    setLoadingMore(true)
    try {
      const next = await imageApi.myImages(PAGE, images.length)
      setImages((cur) => [...cur, ...next])
      setMore(next.length === PAGE)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('gallery.loadFailed', { defaultValue: 'Failed to load' }))
    } finally {
      setLoadingMore(false)
    }
  }

  const fmtDate = (unixSec: number) => {
    if (!unixSec) return ''
    try {
      return new Intl.DateTimeFormat(i18n.language, { dateStyle: 'medium' }).format(unixSec * 1000)
    } catch {
      return ''
    }
  }

  // Editorial section header: a small tracked label, a hairline that draws open,
  // and a folio-style count.
  const header = (
    <div className="mb-8 flex items-center gap-4">
      <h2 className="flex shrink-0 items-center gap-2 text-[var(--text-xs)] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        <ImageIcon size={13} strokeWidth={1.5} aria-hidden />
        {t('gallery.heading', { defaultValue: 'My gallery' })}
      </h2>
      <span
        ref={reduce ? undefined : reveal}
        data-shown={reduce ? 'true' : 'false'}
        aria-hidden
        className="h-px flex-1 origin-center scale-x-0 bg-[var(--color-divider)] transition-transform duration-[var(--duration-slower)] ease-[var(--ease-out)] data-[shown=true]:scale-x-100"
      />
      {!loading && images.length > 0 ? (
        <span className="shrink-0 text-[var(--text-xs)] tabular-nums text-[var(--color-fg-faint)]">
          {images.length}
          {more ? '+' : ''}
        </span>
      ) : null}
    </div>
  )

  if (loading) {
    return (
      <div>
        {header}
        {/* Shimmer skeletons preview the masonry shape (no spinning loader). */}
        <div className="columns-2 gap-4 sm:columns-3 lg:columns-4">
          {SKELETON_ASPECTS.map((ar, i) => (
            <div
              key={i}
              style={{ aspectRatio: ar }}
              className="mb-4 w-full overflow-hidden rounded-[var(--radius-lg)] bg-[var(--color-surface-sunken)] bg-[length:1000px_100%] bg-gradient-to-r from-transparent via-[var(--color-fg)]/[0.05] to-transparent animate-[shimmer_1.4s_ease-in-out_infinite]"
            />
          ))}
        </div>
      </div>
    )
  }

  if (images.length === 0) {
    return (
      <div>
        {header}
        <div className="rounded-[var(--radius-lg)] border border-[var(--color-border-subtle)] bg-[var(--color-surface)] px-5 py-14 text-center">
          <ImageIcon size={24} strokeWidth={1.5} className="mx-auto text-[var(--color-fg-faint)]" aria-hidden />
          <p className="mt-3 text-sm text-[var(--color-fg-muted)]">
            {t('gallery.empty', { defaultValue: 'No images yet — draw something above.' })}
          </p>
        </div>
      </div>
    )
  }

  return (
    <div>
      {header}
      <div className="columns-2 gap-4 sm:columns-3 lg:columns-4">
        {images.map((img) => (
          <GalleryTile
            key={img.id}
            img={img}
            reduce={reduce}
            reveal={reveal}
            date={fmtDate(img.created_at)}
            onOpen={() => navigate(`/chat/${encodeURIComponent(img.conversation_id)}`)}
          />
        ))}
      </div>
      {more ? (
        <div className="mt-6 text-center">
          <Button variant="ghost" size="sm" loading={loadingMore} onClick={() => void loadMore()}>
            {t('gallery.loadMore', { defaultValue: 'Load more' })}
          </Button>
        </div>
      ) : null}
    </div>
  )
}

function GalleryTile({
  img,
  date,
  reduce,
  reveal,
  onOpen,
}: {
  img: ApiAdminImage
  date: string
  reduce: boolean
  reveal: (el: Element | null) => void
  onOpen: () => void
}) {
  const [loaded, setLoaded] = useState(false)
  return (
    // Wrapper owns the scroll-reveal (fade + rise); the button owns hover (lift) so
    // the two transforms never fight.
    <div
      ref={reduce ? undefined : reveal}
      data-shown={reduce ? 'true' : 'false'}
      className={cn(
        'mb-4 break-inside-avoid transition-[opacity,transform] duration-[var(--duration-slow)] ease-[var(--ease-out)]',
        !reduce && 'translate-y-3 opacity-0 data-[shown=true]:translate-y-0 data-[shown=true]:opacity-100',
      )}
    >
      <button
        type="button"
        onClick={onOpen}
        title={img.conversation_title || ''}
        className="group relative block w-full overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border-subtle)] bg-[var(--color-bg-muted)] interactive will-change-transform transition-[transform,box-shadow,border-color] duration-[var(--duration-slow)] ease-[var(--ease-out)] hover:-translate-y-[3px] hover:border-[var(--color-border-strong)] hover:shadow-[var(--shadow-md)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
      >
        <img
          src={img.url}
          alt={img.conversation_title || ''}
          loading="lazy"
          onLoad={() => setLoaded(true)}
          onError={(e) => {
            e.currentTarget.style.visibility = 'hidden'
          }}
          className={cn(
            'block w-full object-cover transition-[opacity,transform] duration-[var(--duration-slow)] ease-[var(--ease-out)] group-hover:scale-[1.04]',
            loaded ? 'opacity-100' : 'opacity-0',
          )}
        />
        {/* Hover caption — title + date, under a token ink scrim. */}
        <div className="pointer-events-none absolute inset-x-0 bottom-0 translate-y-1 bg-gradient-to-t from-[var(--color-overlay)] to-transparent p-3 pt-9 opacity-0 transition duration-[var(--duration-base)] ease-[var(--ease-out)] group-hover:translate-y-0 group-hover:opacity-100 group-focus-visible:translate-y-0 group-focus-visible:opacity-100">
          {img.conversation_title ? (
            <p className="line-clamp-2 text-[var(--text-sm)] font-medium leading-snug text-[var(--color-fg-inverted)]">
              {img.conversation_title}
            </p>
          ) : null}
          {date ? (
            <p className="mt-0.5 text-[var(--text-xs)] tabular-nums tracking-wide text-[var(--color-fg-inverted)] opacity-70">
              {date}
            </p>
          ) : null}
        </div>
      </button>
    </div>
  )
}
