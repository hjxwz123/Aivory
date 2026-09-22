import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { PptxNativePreview } from '@/components/files/pptx-native-preview'
import { Skeleton } from '@/components/ui/skeleton'
import { decodeXml, encodeXml, loadOoxmlArchive, type OoxmlArchive } from '@/lib/ooxml/archive'
import {
  PPTX_MIME,
  parseSlideText,
  resolveSlides,
  writeSlideText,
  type SlideRef,
} from '@/lib/ooxml/pptx'
import type { DocumentEditorProps } from '@/components/files/editors/editor-types'

/**
 * PowerPoint editor with a deliberately narrow scope: it edits the TEXT of an
 * existing deck and nothing else.
 *
 * That is the honest boundary of a browser-only, zero-dependency editor. It is
 * also the safest possible one: only `<a:t>` run contents are rewritten, so the
 * theme, master, layouts, media, charts and every shape the editor does not
 * understand come back byte-identical. Adding shapes or re-laying out slides is
 * the 600–1000+ engineer-day problem this project deliberately did not take on.
 *
 * The slide preview is re-rendered from the PATCHED package, so an edit shows
 * up in place — at the cost of re-rendering the deck, which is why patching is
 * debounced rather than applied per keystroke.
 */

const PATCH_DEBOUNCE_MS = 1000

interface SlideState {
  ref: SlideRef
  source: string
  runs: string[]
  /** Run indices whose text differs from `runs`. */
  edits: Map<number, string>
}

export default function PptxEditor({ name, data, onChange, flushRef }: DocumentEditorProps) {
  const { t } = useTranslation(['chat', 'files'])
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange

  const archiveRef = useRef<OoxmlArchive | null>(null)
  const timerRef = useRef<number | null>(null)

  const [slides, setSlides] = useState<SlideState[]>([])
  const [activeIndex, setActiveIndex] = useState(0)
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading')
  /** The edited package, fed back into the renderer for a live preview. */
  const [patched, setPatched] = useState<ArrayBuffer | null>(null)

  /** Debounced callbacks read the newest slides through this, not a stale closure. */
  const slidesRef = useRef<SlideState[]>([])
  slidesRef.current = slides

  useEffect(() => {
    let cancelled = false

    void (async () => {
      try {
        const archive = await loadOoxmlArchive(data)
        const refs = resolveSlides(
          decodeXml(await archive.read('ppt/presentation.xml')),
          decodeXml(await archive.read('ppt/_rels/presentation.xml.rels')),
        )

        const loaded: SlideState[] = []
        for (const ref of refs) {
          if (!archive.has(ref.path)) continue
          const source = decodeXml(await archive.read(ref.path))
          loaded.push({ ref, source, runs: parseSlideText(source), edits: new Map() })
        }

        if (cancelled) return
        archiveRef.current = archive
        setSlides(loaded)
        setActiveIndex(0)
        setStatus(loaded.length > 0 ? 'ready' : 'error')
      } catch {
        if (!cancelled) setStatus('error')
      }
    })()

    return () => {
      cancelled = true
    }
  }, [data])

  const cancelTimer = useCallback(() => {
    if (timerRef.current === null) return
    window.clearTimeout(timerRef.current)
    timerRef.current = null
  }, [])

  useEffect(() => cancelTimer, [cancelTimer])

  /** Rebuild the package from the accumulated per-slide edits. */
  const buildBlob = useCallback(async (current: SlideState[]): Promise<Blob | null> => {
    const archive = archiveRef.current
    if (!archive) return null
    let touched = false
    for (const slide of current) {
      if (slide.edits.size === 0) continue
      const { xml, changed } = writeSlideText(slide.source, slide.edits)
      if (changed === 0) continue
      archive.write(slide.ref.path, encodeXml(xml))
      touched = true
    }
    if (!touched) return null
    return archive.build(PPTX_MIME)
  }, [])

  const patch = useCallback(
    async (current: SlideState[]) => {
      const blob = await buildBlob(current)
      if (!blob) return
      onChangeRef.current({ bytes: blob })
      setPatched(await blob.arrayBuffer())
    },
    [buildBlob],
  )

  // Serialize on demand so the panel's save/download cannot miss an edit that
  // is still inside the patch debounce window.
  useEffect(() => {
    if (!flushRef) return
    flushRef.current = async () => {
      cancelTimer()
      return buildBlob(slidesRef.current)
    }
    return () => {
      flushRef.current = null
    }
  })

  const schedulePatch = useCallback(
    (current: SlideState[]) => {
      cancelTimer()
      timerRef.current = window.setTimeout(() => {
        timerRef.current = null
        void patch(current)
      }, PATCH_DEBOUNCE_MS)
    },
    [cancelTimer, patch],
  )

  const active = slides[activeIndex]

  const applyRunEdit = (runIndex: number, text: string) => {
    const next = slides.map((slide, index) => {
      if (index !== activeIndex) return slide
      const edits = new Map(slide.edits)
      if (slide.runs[runIndex] === text) edits.delete(runIndex)
      else edits.set(runIndex, text)
      return { ...slide, edits }
    })
    setSlides(next)
    schedulePatch(next)
  }

  const previewData = useMemo(() => patched ?? data, [patched, data])

  const labels = useMemo(
    () => ({
      loading: t('files:pptx.loading'),
      error: t('files:pptx.failed'),
      tooLarge: t('files:pptx.tooLarge'),
      slideError: t('files:pptx.failed'),
    }),
    [t],
  )

  const editedCount = slides.reduce((total, slide) => total + slide.edits.size, 0)

  if (status === 'loading') {
    return (
      <div className="flex h-full min-h-0 flex-col gap-3 p-4" role="status">
        <span className="text-[13px] text-[var(--color-fg-muted)]">{t('files:pptx.loading')}</span>
        <Skeleton className="h-56 w-full rounded-[6px]" />
      </div>
    )
  }

  if (status === 'error' || !active) {
    return (
      <div className="grid h-full place-items-center p-6 text-center text-sm text-[var(--color-fg-muted)]" role="alert">
        {t('chat:filePreview.pptxFailed', {
          defaultValue: "This presentation couldn't be opened for editing.",
        })}
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col bg-[var(--color-surface)]">
      <div className="h-[45%] min-h-0 shrink-0 border-b border-[var(--color-divider)] bg-[var(--color-preview-canvas)]">
        <PptxNativePreview data={previewData} name={name} labels={labels} />
      </div>

      <div className="flex shrink-0 items-center gap-1 border-b border-[var(--color-divider)] px-2 py-1">
        <button
          type="button"
          onClick={() => setActiveIndex((index) => Math.max(0, index - 1))}
          disabled={activeIndex === 0}
          aria-label={t('chat:filePreview.pptxPrev', { defaultValue: 'Previous slide' })}
          className="interactive inline-flex size-7 items-center justify-center rounded-[7px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:pointer-events-none disabled:opacity-40"
        >
          <ChevronLeft size={14} aria-hidden />
        </button>
        <span className="min-w-0 flex-1 truncate text-center text-[12px] text-[var(--color-fg-muted)]">
          {active.ref.name} · {activeIndex + 1}/{slides.length}
          {editedCount > 0 ? ` · ${editedCount}` : ''}
        </span>
        <button
          type="button"
          onClick={() => setActiveIndex((index) => Math.min(slides.length - 1, index + 1))}
          disabled={activeIndex === slides.length - 1}
          aria-label={t('chat:filePreview.pptxNext', { defaultValue: 'Next slide' })}
          className="interactive inline-flex size-7 items-center justify-center rounded-[7px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:pointer-events-none disabled:opacity-40"
        >
          <ChevronRight size={14} aria-hidden />
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-2">
        {active.runs.length === 0 ? (
          <p className="px-1 py-3 text-[12px] text-[var(--color-fg-muted)]">
            {t('chat:filePreview.pptxNoText', { defaultValue: 'This slide has no editable text.' })}
          </p>
        ) : (
          <ul className="flex flex-col gap-2">
            {active.runs.map((run, index) => (
              <li key={index}>
                <label className="sr-only" htmlFor={`pptx-run-${activeIndex}-${index}`}>
                  {t('chat:filePreview.pptxRuns', { defaultValue: 'Text on this slide' })} {index + 1}
                </label>
                <input
                  id={`pptx-run-${activeIndex}-${index}`}
                  value={active.edits.get(index) ?? run}
                  onChange={(event) => applyRunEdit(index, event.target.value)}
                  className="w-full rounded-[7px] border border-[var(--color-border)] bg-[var(--color-bg)] px-2.5 py-1.5 text-[12.5px] text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                />
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}
