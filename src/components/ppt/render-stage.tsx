/**
 * Rendering stage for AI PPT (§ AI PPT).
 *
 * Docmee's own UI shows a "making your deck" animation between picking a template
 * and seeing the result; our flow needs the same beat, because `generatePptx`
 * takes a couple of seconds and a bare spinner explains nothing.
 *
 * The vendor does not expose per-page progress for that call, so the bar is
 * staged honestly (content → layout → render → save) and driven by the caller:
 * it creeps towards 92% while the request is in flight and snaps to 100% when the
 * render returns. Motion is suppressed under `prefers-reduced-motion`.
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, Loader2 } from 'lucide-react'

import { aipptApi } from '@/api'
import type { ApiAiPPTTemplate } from '@/api/types'
import { cn } from '@/lib/utils'

export type RenderStageKey = 'content' | 'layout' | 'render' | 'save'

export const RENDER_STAGES: RenderStageKey[] = ['content', 'layout', 'render', 'save']

interface RenderStageProps {
  template: ApiAiPPTTemplate | null
  subject: string
  progress: number
  reduceMotion?: boolean
}

export function RenderStage({ template, subject, progress, reduceMotion = false }: RenderStageProps) {
  const { t } = useTranslation('ppt')
  const [shown, setShown] = useState(false)

  // Stagger the slide cards in on mount (and skip it entirely when the user asked
  // for reduced motion).
  useEffect(() => {
    if (reduceMotion) {
      setShown(true)
      return
    }
    const frame = window.requestAnimationFrame(() => setShown(true))
    return () => window.cancelAnimationFrame(frame)
  }, [reduceMotion])

  const activeStage = Math.min(RENDER_STAGES.length - 1, Math.floor((progress / 100) * RENDER_STAGES.length))

  return (
    <div className="relative flex min-h-0 flex-1 flex-col items-center justify-center overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-8">
      {/* Template backdrop: a hint of the chosen look while the deck renders. */}
      {template?.coverUrl ? (
        <div
          aria-hidden
          className="pointer-events-none absolute inset-0 opacity-[0.07] blur-2xl"
          style={{
            backgroundImage: `url("${aipptApi.resourceUrl(template.coverUrl ?? '')}")`,
            backgroundSize: 'cover',
            backgroundPosition: 'center',
          }}
        />
      ) : null}

      <div className="relative z-10 flex w-full max-w-3xl flex-col items-center gap-6">
        {/* Fanned slides: a quick "deck taking shape" read. */}
        <div className="relative h-[190px] w-full max-w-[520px] [perspective:1200px]">
          {[0, 1, 2].map((index) => (
            <div
              key={index}
              className={cn(
                'absolute left-1/2 top-0 aspect-[16/9] w-[68%] -translate-x-1/2 overflow-hidden rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] shadow-[var(--shadow-md)]',
                !reduceMotion && 'transition-[transform,opacity] duration-[var(--duration-slow)] ease-[var(--ease-out)]',
              )}
              style={{
                transform: shown
                  ? `translateX(-50%) translateY(${index * 16}px) rotate(${(index - 1) * 4}deg) scale(${1 - index * 0.06})`
                  : 'translateX(-50%) translateY(28px) scale(0.94)',
                opacity: shown ? 1 : 0,
                transitionDelay: `${index * 90}ms`,
                zIndex: 10 - index,
              }}
            >
              {/* Shimmer stands in for the pages being laid out. */}
              <div
                className={cn(
                  'absolute inset-0 bg-[length:1000px_100%] bg-gradient-to-r from-transparent via-[var(--color-fg)]/[0.05] to-transparent',
                  !reduceMotion && 'animate-[shimmer_1.6s_ease-in-out_infinite]',
                )}
                style={{ animationDelay: `${index * 220}ms` }}
              />
              {index === 0 ? (
                <div className="absolute inset-0 flex flex-col justify-center gap-2 px-6">
                  <div className="h-3 w-2/3 rounded-full bg-[var(--color-fg)]/10" />
                  <div className="h-2 w-1/2 rounded-full bg-[var(--color-fg)]/[0.07]" />
                  <div className="mt-2 h-2 w-5/6 rounded-full bg-[var(--color-fg)]/[0.06]" />
                  <div className="h-2 w-4/6 rounded-full bg-[var(--color-fg)]/[0.06]" />
                </div>
              ) : null}
              {index === 1 && template?.coverUrl ? (
                <img src={aipptApi.resourceUrl(template.coverUrl ?? '')} alt="" className="h-full w-full object-cover opacity-40" />
              ) : null}
            </div>
          ))}
        </div>

        <div className="flex flex-col items-center gap-1.5 text-center">
          <p className="flex items-center gap-2 text-[15px] font-medium text-[var(--color-fg)]">
            <Loader2 size={15} aria-hidden className="animate-spin text-[var(--color-accent)]" />
            {t('ppt:render.title', { subject: subject || t('ppt:result.untitled') })}
          </p>
          <p className="text-xs text-[var(--color-fg-muted)]">{t('ppt:render.lead')}</p>
        </div>

        {/* Progress + staged checklist. */}
        <div className="w-full max-w-[420px]">
          <div className="h-1.5 w-full overflow-hidden rounded-full bg-[var(--color-bg-muted)]">
            <div
              className={cn(
                'h-full rounded-full bg-[var(--color-accent)]',
                !reduceMotion && 'transition-[width] duration-500 ease-[var(--ease-out)]',
              )}
              style={{ width: `${Math.max(4, Math.min(100, progress))}%` }}
            />
          </div>
          <ol className="mt-3 flex items-center justify-between gap-1 text-[11px]">
            {RENDER_STAGES.map((stage, index) => (
              <li
                key={stage}
                className={cn(
                  'inline-flex items-center gap-1',
                  index < activeStage
                    ? 'text-[var(--color-fg-muted)]'
                    : index === activeStage
                      ? 'font-medium text-[var(--color-fg)]'
                      : 'text-[var(--color-fg-muted)]/60',
                )}
              >
                {index < activeStage ? <Check size={11} aria-hidden className="text-[var(--color-accent)]" /> : null}
                {t(`ppt:render.stages.${stage}`)}
              </li>
            ))}
          </ol>
        </div>
      </div>
    </div>
  )
}

