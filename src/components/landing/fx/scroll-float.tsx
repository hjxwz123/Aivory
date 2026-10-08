import { useMemo, useRef } from 'react'
import { gsap } from 'gsap'
import { ScrollTrigger } from 'gsap/ScrollTrigger'
import { useGSAP } from '@gsap/react'
import { cn } from '@/lib/utils'

gsap.registerPlugin(ScrollTrigger, useGSAP)

/**
 * ScrollFloat — per-character float-in scrubbed by scroll position: each char
 * rises out of a mask while un-squashing (scaleY 2.3 → 1), progress tied
 * directly to the scroll bar (`scrub: true`), so the effect plays forward and
 * backward with the user's scrolling.
 *
 * Renders a SPAN that inherits all typography from the parent — drop it inside
 * an already-styled heading. The wrapper carries `aria-label` with the raw
 * text and the char spans are aria-hidden, so screen readers hear one
 * utterance. `prefers-reduced-motion: reduce` creates no tween — the text
 * renders statically, fully visible.
 */
export interface ScrollFloatProps {
  text: string
  /** ScrollTrigger start position (trigger edge / viewport edge). */
  scrollStart?: string
  /** ScrollTrigger end position. */
  scrollEnd?: string
  /** Per-char offset inside the scrubbed timeline. */
  stagger?: number
  ease?: string | ((t: number) => number)
  className?: string
}

export function ScrollFloat({
  text,
  scrollStart = 'center bottom+=50%',
  scrollEnd = 'bottom bottom-=40%',
  stagger = 0.03,
  ease = 'back.inOut(2)',
  className,
}: ScrollFloatProps) {
  const ref = useRef<HTMLSpanElement>(null)

  // Per-char split GROUPED BY WORD: bare inline-block chars would let Latin
  // sentences wrap mid-word, so each word is a nowrap inline-block of char
  // spans and real spaces sit between the groups (line breaks land on them).
  // CJK (no spaces) forms one group and splits per character naturally.
  // Array.from iterates code points, so surrogate pairs stay intact.
  const chars = useMemo(() => {
    let i = 0
    const words = text.split(' ')
    return words.flatMap((word, w) => [
      <span key={`w${w}`} className="inline-block whitespace-nowrap">
        {Array.from(word).map((char) => (
          <span key={i++} data-char="" className="inline-block">
            {char}
          </span>
        ))}
      </span>,
      // Plain text-node space BETWEEN groups — breaks land here; a space
      // inside the inline-block's tail would be collapsed instead.
      w < words.length - 1 ? ' ' : null,
    ])
  }, [text])

  useGSAP(
    () => {
      const el = ref.current
      if (!el || !text) return

      const mm = gsap.matchMedia()
      // No reduce branch: without the tween the chars keep their natural
      // static state, fully visible.
      mm.add('(prefers-reduced-motion: no-preference)', () => {
        const tween = gsap.fromTo(
          el.querySelectorAll<HTMLElement>('[data-char]'),
          {
            willChange: 'opacity, transform',
            opacity: 0,
            yPercent: 120,
            scaleY: 2.3,
            scaleX: 0.7,
            transformOrigin: '50% 0%',
          },
          {
            opacity: 1,
            yPercent: 0,
            scaleY: 1,
            scaleX: 1,
            duration: 1,
            ease,
            stagger,
            scrollTrigger: {
              scroller: el.closest<HTMLElement>('[data-page-scroll]') ?? undefined,
              trigger: el,
              start: scrollStart,
              end: scrollEnd,
              // Scrub binds progress to scroll position — both directions.
              scrub: true,
            },
          },
        )

        return () => {
          tween.scrollTrigger?.kill()
          tween.kill()
        }
      })
    },
    // Text change re-renders the char spans, then this re-runs: old trigger
    // is killed via the matchMedia cleanup and a fresh one is created.
    { dependencies: [text, scrollStart, scrollEnd, stagger, ease], scope: ref },
  )

  return (
    <span
      ref={ref}
      aria-label={text}
      className={cn(
        // overflow-hidden masks the rise; align-bottom keeps the box flush
        // with sibling inline content (hidden overflow moves the baseline).
        // Typography inherits entirely from the parent heading.
        'inline-block overflow-hidden align-bottom',
        className,
      )}
    >
      <span aria-hidden="true">{chars}</span>
    </span>
  )
}
