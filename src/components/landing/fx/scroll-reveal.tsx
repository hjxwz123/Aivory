import { useMemo, useRef } from 'react'
import { gsap } from 'gsap'
import { ScrollTrigger } from 'gsap/ScrollTrigger'
import { useGSAP } from '@gsap/react'
import { cn } from '@/lib/utils'

gsap.registerPlugin(ScrollTrigger, useGSAP)

/**
 * ScrollReveal — reading-pace text: every word starts faint and blurred, and
 * sharpens to full ink as the scroll bar sweeps the paragraph (scrub, both
 * directions). The eye and the scroll literally read together.
 *
 * Renders a SPAN inheriting all typography from the parent. Word-split on
 * spaces; space-less text (CJK) splits per character so the sweep still
 * travels through the sentence. The wrapper carries `aria-label`; fragments
 * are aria-hidden. `prefers-reduced-motion: reduce` renders static full-ink
 * text (no tween is created).
 */
interface ScrollRevealProps {
  text: string
  /** Resting opacity of unread words. */
  baseOpacity?: number
  /** Blur radius (px) on unread words; 0 disables the blur pass. */
  blurStrength?: number
  /** ScrollTrigger end for the word sweep. */
  wordAnimationEnd?: string
  className?: string
}

export function ScrollReveal({
  text,
  baseOpacity = 0.12,
  blurStrength = 4,
  wordAnimationEnd = 'bottom bottom-=15%',
  className,
}: ScrollRevealProps) {
  const ref = useRef<HTMLSpanElement>(null)

  const words = useMemo(() => {
    // Whitespace-delimited words; CJK (no spaces) falls back to per-character
    // so the reveal still sweeps through the line instead of popping at once.
    const parts = text.includes(' ') ? text.split(/(\s+)/) : Array.from(text)
    return parts.map((part, i) =>
      /^\s+$/.test(part) ? (
        part
      ) : (
        <span key={i} data-word="" className="inline-block">
          {part}
        </span>
      ),
    )
  }, [text])

  useGSAP(
    () => {
      const el = ref.current
      if (!el || !text) return
      const mm = gsap.matchMedia()
      // No reduce branch: without tweens the words render at full ink.
      mm.add('(prefers-reduced-motion: no-preference)', () => {
        const targets = el.querySelectorAll<HTMLElement>('[data-word]')
        const tweens = [
          gsap.fromTo(
            targets,
            { opacity: baseOpacity, willChange: 'opacity' },
            {
              opacity: 1,
              ease: 'none',
              stagger: 0.05,
              scrollTrigger: { scroller: el.closest<HTMLElement>('[data-page-scroll]') ?? undefined, trigger: el, start: 'top bottom-=20%', end: wordAnimationEnd, scrub: true },
            },
          ),
        ]
        if (blurStrength > 0) {
          tweens.push(
            gsap.fromTo(
              targets,
              { filter: `blur(${blurStrength}px)` },
              {
                filter: 'blur(0px)',
                ease: 'none',
                stagger: 0.05,
                scrollTrigger: { scroller: el.closest<HTMLElement>('[data-page-scroll]') ?? undefined, trigger: el, start: 'top bottom-=20%', end: wordAnimationEnd, scrub: true },
              },
            ),
          )
        }
        return () => {
          // Kill only OUR triggers — the original killed every trigger on the
          // page, nuking unrelated scroll animations.
          for (const tw of tweens) {
            tw.scrollTrigger?.kill()
            tw.kill()
          }
        }
      })
    },
    { dependencies: [text, baseOpacity, blurStrength, wordAnimationEnd], scope: ref },
  )

  return (
    <span ref={ref} aria-label={text} className={cn('inline', className)}>
      <span aria-hidden="true">{words}</span>
    </span>
  )
}
