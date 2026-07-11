import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ImageIcon } from 'lucide-react'
import { imageApi } from '@/api/endpoints'
import { ApiError } from '@/api'
import type { ApiAdminImage } from '@/api/types'
import { toast } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'
import { envNum } from '@/lib/env-config'

const PAGE = envNum('VITE_AIVORY_PAGE', 30)
// Varied aspect ratios so each tile's loading skeleton reserves a real masonry
// shape (the API doesn't return image dimensions).
const SKELETON_ASPECTS = ['4/5', '1/1', '3/4', '4/5', '1/1', '4/5', '3/4', '1/1', '4/5', '3/4']
const aspectFor = (i: number) => SKELETON_ASPECTS[i % SKELETON_ASPECTS.length]

const prefersReduced = () =>
  typeof window !== 'undefined' && window.matchMedia('(prefers-reduced-motion: reduce)').matches

/**
 * MyGallery — §4.20 the signed-in user's generated-image gallery, shown below the
 * composer in drawing mode. An editorial masonry "plate section".
 *
 * Loading is DEFERRED: on the drawing home it sits below the fold, so until the
 * user scrolls it into view we render only the heading + a "scroll to view" hint
 * and fetch nothing. Once activated it paginates with auto infinite-scroll (a
 * bottom sentinel), and every tile reserves space + shimmers until its own image
 * decodes (skeleton, never a blank gap). All motion honours prefers-reduced-motion.
 */
export function MyGallery() {
  const { t, i18n } = useTranslation('chat')
  const navigate = useNavigate()
  const [activated, setActivated] = useState(false)
  const [images, setImages] = useState<ApiAdminImage[]>([])
  const [loading, setLoading] = useState(false)
  const [more, setMore] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [reduce] = useState(prefersReduced)
  // Bottom sentinel: before activation it triggers the first load; afterwards it
  // drives infinite-scroll.
  const tailRef = useRef<HTMLDivElement>(null)

  // One shared observer reveals tiles (fade + rise) as they enter view.
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

  const loadMore = useCallback(async () => {
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
  }, [images.length, t])

  // First page — only once the user has scrolled the gallery into view.
  useEffect(() => {
    if (!activated) return
    let cancelled = false
    setLoading(true)
    void imageApi
      .myImages(PAGE, 0)
      .then((imgs) => {
        if (cancelled) return
        setImages(imgs)
        setMore(imgs.length === PAGE)
      })
      .catch(() => {
        /* silent — render the empty state */
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [activated])

  // Bottom sentinel observer: activate on first reach (deferred fetch), then
  // auto-load subsequent pages as it nears view.
  useEffect(() => {
    const node = tailRef.current
    if (!node) return
    const io = new IntersectionObserver(
      (entries) => {
        if (!entries[0]?.isIntersecting) return
        if (!activated) {
          setActivated(true)
        } else if (more && !loading && !loadingMore) {
          void loadMore()
        }
      },
      // No pre-load margin before activation (so nothing fetches while the
      // heading merely peeks); a generous margin once active for smooth paging.
      { rootMargin: activated ? '600px 0px' : '0px' },
    )
    io.observe(node)
    return () => io.disconnect()
  }, [activated, more, loading, loadingMore, images.length, loadMore])

  const fmtDate = (unixSec: number) => {
    if (!unixSec) return ''
    try {
      return new Intl.DateTimeFormat(i18n.language, { dateStyle: 'medium' }).format(unixSec * 1000)
    } catch {
      return ''
    }
  }

  // Editorial section header: a small tracked label, a hairline, a folio count.
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

  // Skeleton grid — the masonry shape while the first page loads.
  const skeletonGrid = (
    <div className="columns-2 gap-4 sm:columns-3 lg:columns-4">
      {SKELETON_ASPECTS.map((ar, i) => (
        <div
          key={i}
          style={{ aspectRatio: ar }}
          className="mb-4 w-full overflow-hidden rounded-[var(--radius-lg)] bg-[var(--color-surface-sunken)] bg-[length:1000px_100%] bg-gradient-to-r from-transparent via-[var(--color-fg)]/[0.05] to-transparent animate-[shimmer_1.4s_ease-in-out_infinite]"
        />
      ))}
    </div>
  )

  return (
    <div>
      {header}

      {!activated ? (
        /* Deferred: heading + a "scroll to view" hint; no fetch until scrolled in. */
        <div className="flex flex-col items-center gap-2 rounded-[var(--radius-lg)] border border-dashed border-[var(--color-border-subtle)] bg-[var(--color-surface)]/40 px-5 py-12 text-center">
          <ChevronDown
            size={20}
            strokeWidth={1.5}
            aria-hidden
            className={cn('text-[var(--color-fg-faint)]', !reduce && 'animate-[bob_1.6s_ease-in-out_infinite]')}
          />
          <p className="text-sm text-[var(--color-fg-muted)]">
            {t('gallery.scrollHint', { defaultValue: '下拉查看我的画廊' })}
          </p>
        </div>
      ) : loading ? (
        skeletonGrid
      ) : images.length === 0 ? (
        <div className="rounded-[var(--radius-lg)] border border-[var(--color-border-subtle)] bg-[var(--color-surface)] px-5 py-14 text-center">
          <ImageIcon size={24} strokeWidth={1.5} className="mx-auto text-[var(--color-fg-faint)]" aria-hidden />
          <p className="mt-3 text-sm text-[var(--color-fg-muted)]">
            {t('gallery.empty', { defaultValue: 'No images yet — draw something above.' })}
          </p>
        </div>
      ) : (
        <div className="columns-2 gap-4 sm:columns-3 lg:columns-4">
          {images.map((img, i) => (
            <GalleryTile
              key={img.id}
              img={img}
              aspect={aspectFor(i)}
              reduce={reduce}
              reveal={reveal}
              date={fmtDate(img.created_at)}
              onOpen={() => navigate(`/chat/${encodeURIComponent(img.conversation_id)}`)}
            />
          ))}
        </div>
      )}

      {/* Bottom sentinel — drives activation + infinite scroll. While more pages
          are loading it shimmers so the reveal never looks stalled. */}
      {(!activated || more) && (
        <div ref={tailRef} className="pt-6" aria-hidden>
          {loadingMore ? (
            <div className="columns-2 gap-4 sm:columns-3 lg:columns-4">
              {SKELETON_ASPECTS.slice(0, 4).map((ar, i) => (
                <div
                  key={i}
                  style={{ aspectRatio: ar }}
                  className="mb-4 w-full overflow-hidden rounded-[var(--radius-lg)] bg-[var(--color-surface-sunken)] bg-[length:1000px_100%] bg-gradient-to-r from-transparent via-[var(--color-fg)]/[0.05] to-transparent animate-[shimmer_1.4s_ease-in-out_infinite]"
                />
              ))}
            </div>
          ) : null}
        </div>
      )}
    </div>
  )
}

function GalleryTile({
  img,
  aspect,
  date,
  reduce,
  reveal,
  onOpen,
}: {
  img: ApiAdminImage
  aspect: string
  date: string
  reduce: boolean
  reveal: (el: Element | null) => void
  onOpen: () => void
}) {
  const [loaded, setLoaded] = useState(false)
  return (
    // Wrapper owns the scroll-reveal (fade + rise); the button owns hover (lift).
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
        {/* Skeleton — reserves the tile's space + shimmers until the image decodes,
            so a slow tile shows a placeholder instead of collapsing to nothing. */}
        {!loaded ? (
          <div
            style={{ aspectRatio: aspect }}
            aria-hidden
            className="w-full bg-[var(--color-surface-sunken)] bg-[length:1000px_100%] bg-gradient-to-r from-transparent via-[var(--color-fg)]/[0.05] to-transparent animate-[shimmer_1.4s_ease-in-out_infinite]"
          />
        ) : null}
        <img
          src={img.url}
          alt={img.conversation_title || ''}
          loading="lazy"
          onLoad={() => setLoaded(true)}
          onError={(e) => {
            e.currentTarget.style.visibility = 'hidden'
            setLoaded(true)
          }}
          className={cn(
            'block w-full object-cover transition-[opacity,transform] duration-[var(--duration-slow)] ease-[var(--ease-out)] group-hover:scale-[1.04]',
            loaded ? 'opacity-100' : 'absolute inset-0 opacity-0',
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
