import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ExternalLink, ImageOff } from 'lucide-react'
import type { Citation } from '@/types/chat'
import { isDocumentCitation, safeSearchImageUrl } from '@/lib/citations'
import { cn } from '@/lib/utils'
import { ImageLightbox } from './image-lightbox'

type ImageResult = { citation: Citation; original: string; thumbnail: string }

function ImageSearchResult({ result, onPreview }: { result: ImageResult; onPreview: (trigger: HTMLButtonElement) => void }) {
  const { t } = useTranslation('chat')
  const { citation, original, thumbnail } = result
  const [src, setSrc] = useState(thumbnail)
  const [loaded, setLoaded] = useState(false)
  const [failed, setFailed] = useState(false)
  const source = safeSearchImageUrl(citation.url)
  return (
    <li className="min-w-0">
      <button
        type="button"
        onClick={(event) => onPreview(event.currentTarget)}
        disabled={failed}
        aria-label={t('sources.previewImage', { title: citation.title })}
        className="relative grid aspect-[4/3] w-full min-w-0 place-items-center overflow-hidden rounded-[10px] bg-[var(--color-bg-muted)] interactive hover:bg-[var(--color-bg-subtle)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] focus-visible:ring-offset-2 disabled:cursor-default"
      >
        {failed ? (
          <span role="status" className="flex flex-col items-center gap-2 px-3 text-xs text-[var(--color-fg-muted)]">
            <ImageOff size={20} aria-hidden />
            {t('sources.imageUnavailable')}
          </span>
        ) : (
          <>
            {!loaded ? <span aria-hidden className="absolute inset-0 animate-pulse bg-[var(--color-bg-subtle)]" /> : null}
            <img
              src={src}
              alt={citation.title}
              loading="lazy"
              decoding="async"
              referrerPolicy="no-referrer"
              draggable={false}
              onLoad={() => setLoaded(true)}
              onError={() => {
                if (src !== original) {
                  setLoaded(false)
                  setSrc(original)
                } else {
                  setFailed(true)
                }
              }}
              className={cn('absolute inset-0 h-full w-full object-contain transition-opacity duration-150', loaded ? 'opacity-100' : 'opacity-0')}
            />
          </>
        )}
      </button>
      <p className="mt-2 line-clamp-2 text-xs font-medium leading-snug text-[var(--color-fg)] [overflow-wrap:anywhere]" title={citation.title}>
        {citation.title}
      </p>
      {source ? (
        <a
          href={source}
          target="_blank"
          rel="noopener noreferrer"
          aria-label={t('sources.openImageSource', { title: citation.title })}
          className="mt-1 inline-flex max-w-full items-center gap-1 rounded-[4px] text-[11px] text-[var(--color-fg-muted)] interactive hover:text-[var(--color-accent)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        >
          <span className="truncate">{citation.domain || new URL(source).hostname}</span>
          <ExternalLink size={10} aria-hidden className="shrink-0" />
        </a>
      ) : null}
    </li>
  )
}

/** Image results remain visible while the ordinary source list is collapsed. */
export function ImageSearchGallery({ citations }: { citations: Citation[] }) {
  const { t } = useTranslation('chat')
  const [selected, setSelected] = useState<ImageResult | null>(null)
  const previewTrigger = useRef<HTMLButtonElement | null>(null)
  const seen = new Set<string>()
  const images = citations.flatMap((citation): ImageResult[] => {
    if (isDocumentCitation(citation) || citation.imageDisplay === false) return []
    const original = safeSearchImageUrl(citation.imageUrl) || safeSearchImageUrl(citation.thumbnailUrl)
    if (!original || seen.has(original)) return []
    seen.add(original)
    return [{ citation, original, thumbnail: safeSearchImageUrl(citation.thumbnailUrl) || original }]
  })
  if (!images.length) return null
  return (
    <section aria-label={t('sources.images')} className="mb-4 min-w-0" data-image-search-gallery>
      <ul
        className="grid min-w-0 grid-cols-[repeat(auto-fit,minmax(min(9rem,100%),1fr))] gap-x-3 gap-y-4"
        style={{ maxWidth: `${Math.min(images.length, 4) * 11}rem` }}
      >
        {images.map((result) => (
          <ImageSearchResult key={`${result.original}|${result.thumbnail}`} result={result} onPreview={(trigger) => { previewTrigger.current = trigger; setSelected(result) }} />
        ))}
      </ul>
      {selected ? (
        <ImageLightbox
          key={selected.original}
          open
          onOpenChange={(open) => { if (!open) setSelected(null) }}
          onCloseAutoFocus={(event) => { event.preventDefault(); previewTrigger.current?.focus() }}
          src={selected.original}
          fallbackSrc={selected.thumbnail}
          downloadUrl={selected.original}
          sourceUrl={safeSearchImageUrl(selected.citation.url)}
          referrerPolicy="no-referrer"
          alt={selected.citation.title}
        />
      ) : null}
    </section>
  )
}
