import { useEffect, useRef, useState, type ElementType } from 'react'
import { gsap } from 'gsap'
import { ScrollTrigger } from 'gsap/ScrollTrigger'
import { SplitText as GSAPSplitText } from 'gsap/SplitText'
import { useGSAP } from '@gsap/react'
import { cn } from '@/lib/utils'

gsap.registerPlugin(ScrollTrigger, GSAPSplitText, useGSAP)

/**
 * SplitText — a masked, staggered entrance for headlines: GSAP SplitText
 * fragments the text into chars/words/lines and each fragment rises into
 * place once the element scrolls into view. Works for CJK text (GSAP splits
 * per grapheme), re-splits when `text` changes (locale switch), and inherits
 * all typography from the parent — it styles nothing but the motion.
 *
 * Accessibility: the wrapper carries `aria-label` with the raw text and the
 * split fragments are aria-hidden, so screen readers hear one utterance, not
 * a letter salad. `prefers-reduced-motion: reduce` skips the split entirely —
 * the text renders statically, fully visible.
 */
export interface SplitTextProps {
  text: string
  as?: 'h1' | 'h2' | 'h3' | 'h4' | 'h5' | 'h6' | 'p' | 'span' | 'div'
  splitType?: 'chars' | 'words' | 'lines' | 'words, chars'
  /** Per-fragment stagger, in milliseconds. */
  delay?: number
  /** Per-fragment tween duration, in seconds. */
  duration?: number
  ease?: string | ((t: number) => number)
  from?: gsap.TweenVars
  to?: gsap.TweenVars
  /** IntersectionObserver-style trigger tuning, mapped onto ScrollTrigger. */
  threshold?: number
  rootMargin?: string
  onLetterAnimationComplete?: () => void
  className?: string
}

export function SplitText({
  text,
  as = 'span',
  splitType = 'chars',
  delay = 50,
  duration = 1.25,
  ease = 'power3.out',
  from = { opacity: 0, y: 40 },
  to = { opacity: 1, y: 0 },
  threshold = 0.1,
  rootMargin = '-100px',
  onLetterAnimationComplete,
  className,
}: SplitTextProps) {
  const ref = useRef<HTMLElement>(null)
  // Which text the entrance already played for — replay only on a real text
  // change (locale switch), not when unrelated props re-run the effect.
  const completedForRef = useRef<string | null>(null)
  const onCompleteRef = useRef(onLetterAnimationComplete)
  const [fontsReady, setFontsReady] = useState(false)

  useEffect(() => {
    onCompleteRef.current = onLetterAnimationComplete
  }, [onLetterAnimationComplete])

  // Splitting before webfonts settle bakes in wrong glyph metrics/line breaks.
  useEffect(() => {
    if (document.fonts.status === 'loaded') setFontsReady(true)
    else document.fonts.ready.then(() => setFontsReady(true))
  }, [])

  useGSAP(
    () => {
      const el = ref.current
      if (!el || !text || !fontsReady) return
      if (completedForRef.current === text) return

      const mm = gsap.matchMedia()
      // No reduce branch: under reduced motion we never split, so the raw
      // text node stays in the DOM, fully visible.
      mm.add('(prefers-reduced-motion: no-preference)', () => {
        const startPct = (1 - threshold) * 100
        const marginMatch = /^(-?\d+(?:\.\d+)?)(px|em|rem|%)?$/.exec(rootMargin)
        const marginValue = marginMatch ? parseFloat(marginMatch[1]) : 0
        const marginUnit = marginMatch ? marginMatch[2] || 'px' : 'px'
        const offset =
          marginValue === 0
            ? ''
            : marginValue < 0
              ? `-=${Math.abs(marginValue)}${marginUnit}`
              : `+=${marginValue}${marginUnit}`
        const start = `top ${startPct}%${offset}`

        const pickTargets = (self: GSAPSplitText): Element[] => {
          if (splitType.includes('chars') && self.chars.length) return self.chars
          if (splitType.includes('words') && self.words.length) return self.words
          if (splitType.includes('lines') && self.lines.length) return self.lines
          return self.chars
        }

        const split = new GSAPSplitText(el, {
          type: splitType,
          // smartWrap groups chars into nowrap word spans so Latin words don't
          // break mid-word. But it tokenizes on SPACES, so a CJK run (even in
          // mixed text like "…细腻的 AI 伙伴") becomes one unbreakable "word"
          // and the line overflows its mask (the clipped-的 bug). CJK breaks
          // legally between any characters, so any CJK presence disables it.
          smartWrap: !/[⺀-鿿぀-ヿ가-힯豈-﫿]/.test(text),
          autoSplit: splitType === 'lines',
          linesClass: 'split-line',
          wordsClass: 'split-word',
          charsClass: 'split-char',
          reduceWhiteSpace: false,
          // The wrapper carries aria-label; hide fragments from screen
          // readers so chars are not announced one by one.
          aria: 'hidden',
          onSplit: (self) =>
            gsap.fromTo(
              pickTargets(self),
              { ...from },
              {
                ...to,
                duration,
                ease,
                stagger: delay / 1000,
                scrollTrigger: {
                  scroller: el.closest<HTMLElement>('[data-page-scroll]') ?? undefined,
                  trigger: el,
                  start,
                  once: true,
                  fastScrollEnd: true,
                  anticipatePin: 0.4,
                },
                onComplete: () => {
                  completedForRef.current = text
                  onCompleteRef.current?.()
                },
                willChange: 'transform, opacity',
                force3D: true,
              },
            ),
        })

        return () => {
          ScrollTrigger.getAll().forEach((st) => {
            if (st.trigger === el) st.kill()
          })
          // Revert can throw if React already swapped the text node underneath.
          try {
            split.revert()
          } catch {
            /* DOM already replaced */
          }
        }
      })
    },
    {
      dependencies: [
        text,
        delay,
        duration,
        ease,
        splitType,
        JSON.stringify(from),
        JSON.stringify(to),
        threshold,
        rootMargin,
        fontsReady,
      ],
      scope: ref,
    },
  )

  const Tag = as as ElementType
  return (
    <Tag
      ref={ref}
      aria-label={text}
      className={cn(
        // overflow-hidden gives the masked rise; typography inherits from the parent.
        'inline-block overflow-hidden whitespace-normal break-words [will-change:transform,opacity]',
        className,
      )}
    >
      {text}
    </Tag>
  )
}
