import { useRef } from 'react'
import { gsap } from 'gsap'
import { useGSAP } from '@gsap/react'
import { FlourishScene, SeedlingScene } from './auth-artwork-svg-scenes'
import DandelionArtwork from './auth-artwork-dandelion'
import { animateSvgStory, svgStoryDuration } from './auth-artwork-svg-motion'
import '@/styles/auth-artwork-svg.css'

gsap.registerPlugin(useGSAP)

export type SvgArtworkVariant = 'seedling' | 'flourish' | 'dandelion'

/** Long-form SVG studies: a growing canopy, a transforming flower, and a
 * generational seed journey. Shapes, timing, and lifecycle stay independent. */
function BotanicalArtwork({ variant, seed }: { variant: Exclude<SvgArtworkVariant, 'dandelion'>; seed: number }) {
  const rootRef = useRef<SVGSVGElement>(null)

  useGSAP(() => {
    const root = rootRef.current
    if (!root) return
    const media = gsap.matchMedia()
    media.add('(prefers-reduced-motion: no-preference)', () => {
      const timeline = gsap.timeline({ paused: true, repeat: -1, defaults: { ease: 'sine.inOut' } })
      const scene = root.querySelector('[data-growth-scene]')
      timeline.fromTo(scene, { opacity: 0 }, { opacity: 1, duration: 1.2 }, 0)
      animateSvgStory[variant](root, timeline)
      const duration = svgStoryDuration[variant]
      // The small central seed remains visible through the handoff; only the
      // composition resets, so no full-frame flash separates the chapters.
      timeline.set(scene, { opacity: 0 }, duration)
      timeline.timeScale(.98 + (seed % 5) * .01)
      // Land inside the first chapter with something already taking shape.
      timeline.seek(5.5)

      let inView = true
      const sync = () => { timeline.paused(document.hidden || !inView) }
      const observer = new IntersectionObserver(([entry]) => {
        inView = entry?.isIntersecting ?? true
        sync()
      })
      observer.observe(root)
      document.addEventListener('visibilitychange', sync)
      sync()
      return () => {
        observer.disconnect()
        document.removeEventListener('visibilitychange', sync)
        timeline.kill()
      }
    }, root)
    // Restore the mature, static drawing if reduced motion is enabled, and
    // dispose all timelines/listeners when leaving the authentication layout.
    return () => media.revert()
  }, { scope: rootRef, dependencies: [variant, seed], revertOnUpdate: true })

  return (
    <svg ref={rootRef} className="login-growth-art" viewBox="0 0 640 480" fill="none" aria-hidden="true" focusable="false">
      {variant === 'seedling' ? <SeedlingScene /> : <FlourishScene />}
    </svg>
  )
}

export default function AuthArtworkSvg({ variant, seed }: { variant: SvgArtworkVariant; seed: number }) {
  return variant === 'dandelion' ? <DandelionArtwork seed={seed} /> : <BotanicalArtwork variant={variant} seed={seed} />
}
