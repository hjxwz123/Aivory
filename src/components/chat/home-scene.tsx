import { useContext, useLayoutEffect, useRef } from 'react'
import { SceneAppearanceContext } from '@/contexts/scene-appearance'
import type { IllustratedScene } from '@/types/settings'
import { paintHomeScene, paintSceneBand, type SceneBand } from './home-scene-art'
import './home-scene.css'

export function HomeSceneCanvas({ scene, preview = false, band }: { scene: IllustratedScene; preview?: boolean; band?: SceneBand }) {
  const ref = useRef<HTMLCanvasElement>(null)
  const { dark } = useContext(SceneAppearanceContext)

  useLayoutEffect(() => {
    const canvas = ref.current
    const ctx = canvas?.getContext('2d')
    if (!canvas || !ctx) return
    const paint = () => {
      const w = canvas.clientWidth, h = canvas.clientHeight
      if (!w || !h) return
      const ratio = Math.min(window.devicePixelRatio || 1, 2)
      canvas.width = Math.round(w * ratio)
      canvas.height = Math.round(h * ratio)
      ctx.setTransform(canvas.width / w, 0, 0, canvas.height / h, 0, 0)
      if (band) paintSceneBand(ctx, scene, dark, w, h, band)
      else paintHomeScene(ctx, scene, dark, w, h, preview)
    }
    paint()
    const observer = new ResizeObserver(paint)
    observer.observe(canvas)
    return () => observer.disconnect()
  }, [preview, scene, dark, band])

  return <canvas ref={ref} aria-hidden="true" data-scene-art={band ? undefined : scene} className="pointer-events-none absolute inset-0 block size-full" />
}

export function SceneOrnament({ surface }: { surface: SceneBand }) {
  const { scene } = useContext(SceneAppearanceContext)
  if (scene === 'plain') return null
  return <span aria-hidden="true" className="scene-ornament" data-scene-band={surface}>
    <HomeSceneCanvas scene={scene} band={surface} />
  </span>
}
