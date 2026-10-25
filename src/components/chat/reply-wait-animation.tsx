import { cn } from '@/lib/utils'
import type { ReplyAnimationPref } from '@/types/settings'
import styles from './reply-wait-animation.module.css'

/** The same artwork and motion are used in chat, settings and onboarding. */
export function ReplyWaitAnimation({ variant, className }: { variant: ReplyAnimationPref; className?: string }) {
  return (
    <span className={cn(styles.art, className)} aria-hidden="true" data-reply-animation={variant}>
      {variant === 'paper-plane' && (
        <svg viewBox="0 0 64 56" fill="none">
          <path className={styles.flightTrail} d="M5 40C13 48 27 46 31 37" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" strokeDasharray="2 4" />
          <g className={styles.plane} data-motion-part>
            <path d="M12 23 51 10 38 43 29 30Z" fill="var(--paper-light)" stroke="currentColor" strokeWidth="1" strokeLinejoin="round" />
            <path d="m12 23 17 7 22-20Z" fill="var(--paper-mid)" />
            <path d="m29 30 2 12 5-7 15-25Z" fill="var(--paper-dark)" />
            <path d="m29 30 22-20M31 42l-2-12" stroke="currentColor" strokeWidth=".85" strokeLinejoin="round" />
          </g>
        </svg>
      )}
      {variant === 'origami' && (
        <span className={styles.paperStage}>
          <span className={styles.paperObject}>
            <svg className={styles.paperBase} viewBox="0 0 56 56">
              <path d="M8 8H48L8 48Z" fill="var(--paper-mid)" stroke="currentColor" strokeWidth=".8" strokeLinejoin="round" />
              <path d="M8 8 28 28" stroke="currentColor" strokeWidth=".55" opacity=".3" />
            </svg>
            <span className={styles.foldingHalf} data-motion-part>
              <svg className={styles.paperFront} viewBox="0 0 56 56">
                <path d="M48 8V48H8Z" fill="var(--paper-light)" stroke="currentColor" strokeWidth=".8" strokeLinejoin="round" />
                <path d="M48 48 28 28" stroke="currentColor" strokeWidth=".55" opacity=".3" />
              </svg>
              <svg className={styles.paperBack} viewBox="0 0 56 56">
                <path d="M8 8H48L8 48Z" fill="var(--paper-dark)" stroke="currentColor" strokeWidth=".8" strokeLinejoin="round" />
                <path d="M8 8 28 28" stroke="currentColor" strokeWidth=".55" opacity=".3" />
              </svg>
            </span>
            <svg className={styles.paperCrease} viewBox="0 0 56 56">
              <path d="M8 48 48 8" stroke="currentColor" strokeWidth=".8" opacity=".55" />
            </svg>
          </span>
        </span>
      )}
      {variant === 'ink' && (
        <svg viewBox="0 0 64 56" fill="none">
          <path d="M9 35C9 18 25 13 31 25S42 44 53 21" stroke="currentColor" strokeOpacity=".13" strokeWidth="3" strokeLinecap="round" />
          <path className={styles.inkStroke} data-motion-part d="M9 35C9 18 25 13 31 25S42 44 53 21" pathLength="100" stroke="currentColor" strokeWidth="3" strokeLinecap="round" />
        </svg>
      )}
      {variant === 'sprout' && (
        <svg viewBox="0 0 64 56" fill="none">
          <ellipse className={styles.ground} cx="32" cy="46" rx="13" ry="2" fill="currentColor" opacity=".1" />
          <g className={styles.stem} data-motion-part>
            <path d="M32 44V28" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
            <g className={styles.leafLeft}>
              <path d="M32 33C18 35 15 25 16 17c10 0 18 5 16 16Z" fill="var(--paper-mid)" stroke="currentColor" strokeWidth=".9" />
              <path d="m19 21 13 12" stroke="currentColor" strokeWidth=".7" opacity=".6" />
            </g>
            <g className={styles.leafRight}>
              <path d="M32 28c-2-14 6-19 16-18 0 10-5 19-16 18Z" fill="var(--paper-light)" stroke="currentColor" strokeWidth=".9" />
              <path d="m32 28 12-14" stroke="currentColor" strokeWidth=".7" opacity=".6" />
            </g>
          </g>
        </svg>
      )}
      {variant === 'orbit' && (
        <svg viewBox="0 0 64 56" fill="none">
          <ellipse cx="32" cy="28" rx="25" ry="10" transform="rotate(-27 32 28)" stroke="currentColor" strokeOpacity=".18" strokeWidth="1" />
          <ellipse cx="32" cy="28" rx="25" ry="10" transform="rotate(45 32 28)" stroke="currentColor" strokeOpacity=".28" strokeWidth="1" />
          <path className={styles.orbitStar} d="m32 20 2.2 5.8L40 28l-5.8 2.2L32 36l-2.2-5.8L24 28l5.8-2.2Z" fill="var(--paper-mid)" stroke="currentColor" strokeWidth=".9" />
          <g className={styles.comet} data-motion-part>
            <path d="M11 35c4 2 9 3 14 2" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
            <path d="m10 29 1.5 3.5L15 34l-3.5 1.5L10 39l-1.5-3.5L5 34l3.5-1.5Z" fill="currentColor" />
          </g>
        </svg>
      )}
      {variant === 'prism' && (
        <svg viewBox="0 0 64 56" fill="none">
          <g className={styles.prismShape} data-motion-part>
            <path d="m32 7 19 21-19 21-19-21Z" fill="var(--paper-light)" stroke="currentColor" strokeWidth="1" strokeLinejoin="round" />
            <path d="M32 7v42L13 28Z" fill="var(--paper-mid)" />
            <path d="m32 22 19 6-19 21Z" fill="var(--paper-dark)" />
            <path d="M32 7v42M13 28l19-6 19 6" stroke="currentColor" strokeWidth=".8" strokeLinejoin="round" />
          </g>
          <path className={styles.prismGlint} d="m53 8 1.2 3.8L58 13l-3.8 1.2L53 18l-1.2-3.8L48 13l3.8-1.2Z" fill="currentColor" />
        </svg>
      )}
    </span>
  )
}
