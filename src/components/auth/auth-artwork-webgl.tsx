import { useEffect, useRef } from 'react'
import { Mesh, Program, Renderer, Triangle } from 'ogl'
import { artworkFragment, artworkVertex, type ArtworkVariant } from './auth-artwork-shaders'

type Rgb = [number, number, number]

/** Browsers resolve OKLCH / color-mix; a swatch converts the result to sRGB. */
function readColors(element: HTMLElement) {
  const probe = document.createElement('span')
  probe.hidden = true
  element.appendChild(probe)
  const swatch = document.createElement('canvas')
  swatch.width = swatch.height = 1
  const context = swatch.getContext('2d', { willReadFrequently: true })
  const sample = (expression: string): Rgb => {
    if (!context) return [.55, .55, .55]
    probe.style.color = expression
    context.clearRect(0, 0, 1, 1)
    context.fillStyle = getComputedStyle(probe).color
    context.fillRect(0, 0, 1, 1)
    const bytes = context.getImageData(0, 0, 1, 1).data
    return [bytes[0] / 255, bytes[1] / 255, bytes[2] / 255]
  }
  try {
    return {
      accent: sample('var(--color-accent)'),
      tint: sample('var(--login-art-tint)'),
      pearl: sample('var(--login-art-pearl)'),
      dark: document.documentElement.classList.contains('dark') ? 1 : 0,
    }
  } finally {
    probe.remove()
  }
}

function createScene(variant: ArtworkVariant) {
  let renderer: Renderer | undefined
  let geometry: Triangle | undefined
  let program: Program | undefined
  try {
    renderer = new Renderer({
      alpha: true,
      depth: false,
      stencil: false,
      antialias: false,
      premultipliedAlpha: true,
      powerPreference: 'low-power',
      dpr: 1,
      webgl: 2,
    })
    const gl = renderer.gl
    if (!renderer.isWebgl2) throw new Error('WebGL2 is unavailable')
    geometry = new Triangle(gl)
    // The scene is a single screen triangle; all geometry is procedural.
    program = new Program(gl, {
      vertex: artworkVertex,
      fragment: artworkFragment(variant),
      depthTest: false,
      depthWrite: false,
      transparent: true,
      uniforms: {
        uResolution: { value: [1, 1] },
        uViewport: { value: [1, 1] },
        uFocus: { value: [0, 0] },
        uScale: { value: 1 },
        uTime: { value: 0 },
        uPointer: { value: [0, 0] },
        uCompact: { value: 0 },
        uDark: { value: 0 },
        uAccent: { value: [.5, .5, .5] },
        uTint: { value: [.7, .7, .7] },
        uPearl: { value: [.95, .95, .95] },
        uQuiet: { value: new Float32Array(16) },
      },
    })
    // OGL logs shader errors without throwing. Preserve the CSS artwork if
    // this GPU rejects the program instead of replacing it with a blank scene.
    if (!gl.getProgramParameter(program.program, gl.LINK_STATUS)) throw new Error('Artwork shader unavailable')
    gl.clearColor(0, 0, 0, 0)
    const mesh = new Mesh(gl, { geometry, program })
    return { renderer, geometry, program, mesh }
  } catch {
    geometry?.remove()
    program?.remove()
    renderer?.gl.getExtension('WEBGL_lose_context')?.loseContext()
    return null
  }
}

export default function AuthArtworkWebGL({ variant, seed, onReady }: { variant: ArtworkVariant; seed: number; onReady: () => void }) {
  const hostRef = useRef<HTMLDivElement>(null)
  const onReadyRef = useRef(onReady)
  onReadyRef.current = onReady

  useEffect(() => {
    const host = hostRef.current
    const anchor = host?.closest<HTMLElement>('.login-art-stage')
    const page = host?.closest<HTMLElement>('.login-page')
    if (!host || !anchor || !page) return
    const resources = createScene(variant)
    if (!resources) {
      onReadyRef.current()
      return
    }
    const { renderer, geometry, program, mesh } = resources
    const gl = renderer.gl
    const canvas = gl.canvas
    host.appendChild(canvas)

    const quietElements = ['.login-hero-copy', '.login-panel', '.login-header', '.login-footer']
      .map((selector) => page.querySelector<HTMLElement>(selector))
    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)')
    const finePointer = window.matchMedia('(hover: hover) and (pointer: fine)')
    let disposed = false
    let contextLost = false
    let announcedReady = false
    let inView = true
    let compact = false
    let frame = 0
    let previous = 0
    let elapsed = (seed % 10000) / 1000
    let layoutDirty = true
    let paletteDirty = true
    let quality = 1
    let slowFrames = 0
    let warmupFrames = 0
    let width = 1
    let height = 1
    const pointer = [0, 0]
    const target = [0, 0]

    function updateLayout() {
      if (!host || !anchor) return
      const viewport = host.getBoundingClientRect()
      width = Math.max(1, viewport.width)
      height = Math.max(1, viewport.height)
      compact = width <= 800
      // Bound fill cost on high-DPR/large displays, then adapt to slow devices.
      const budget = compact ? 900_000 : 1_700_000
      const dpr = Math.min(window.devicePixelRatio || 1, compact ? 1.5 : 1.65, Math.sqrt(budget / (width * height))) * quality
      const pixelWidth = Math.max(1, Math.floor(width * dpr))
      const pixelHeight = Math.max(1, Math.floor(height * dpr))
      if (canvas.width !== pixelWidth || canvas.height !== pixelHeight) {
        renderer.dpr = dpr
        renderer.setSize(width, height)
      }
      program.uniforms.uResolution.value = [canvas.width, canvas.height]
      program.uniforms.uViewport.value = [width, height]
      program.uniforms.uCompact.value = compact ? 1 : 0
      const rect = anchor.getBoundingClientRect()
      program.uniforms.uFocus.value = [rect.left + rect.width / 2 - viewport.left, rect.top + rect.height / 2 - viewport.top]
      // A wider scene can spill past the anchor. Its solid core still fits
      // above the copy, and the distant light dissipates before the form.
      program.uniforms.uScale.value = Math.max(1, Math.min(rect.width, rect.height) * (compact ? .41 : .49))
      const boxes = program.uniforms.uQuiet.value as Float32Array
      quietElements.forEach((element, index) => {
        if (!element) {
          boxes.set([-10000, -10000, 0, 0], index * 4)
          return
        }
        const bounds = element.getBoundingClientRect()
        const padding = compact ? 3 : 9
        boxes.set([
          bounds.left + bounds.width / 2 - viewport.left,
          bounds.top + bounds.height / 2 - viewport.top,
          bounds.width / 2 + padding,
          bounds.height / 2 + padding,
        ], index * 4)
      })
      layoutDirty = false
    }

    function draw() {
      if (disposed || contextLost) return
      if (layoutDirty) updateLayout()
      if (paletteDirty && page) {
        const colors = readColors(page)
        program.uniforms.uAccent.value = colors.accent
        program.uniforms.uTint.value = colors.tint
        program.uniforms.uPearl.value = colors.pearl
        program.uniforms.uDark.value = colors.dark
        paletteDirty = false
      }
      program.uniforms.uTime.value = elapsed
      program.uniforms.uPointer.value = pointer
      renderer.render({ scene: mesh })
      if (!gl.isContextLost()) anchor?.setAttribute('data-art-ready', 'true')
      if (!announcedReady) {
        announcedReady = true
        onReadyRef.current()
      }
    }

    function tick(now: number) {
      frame = 0
      if (disposed || contextLost || document.hidden || !inView) return
      const delta = previous ? now - previous : 16.67
      const interval = compact ? 1000 / 30 : 1000 / 60
      if (!previous || delta >= interval - 1) {
        previous = now
        elapsed += Math.min(delta, 80) / 1000
        const ease = 1 - Math.exp(-Math.min(delta, 80) / 180)
        pointer[0] += (target[0] - pointer[0]) * ease
        pointer[1] += (target[1] - pointer[1]) * ease
        // Reduce resolution after sustained slow frames, never oscillate it.
        warmupFrames += 1
        slowFrames = delta > (compact ? 54 : 28) ? slowFrames + 1 : Math.max(0, slowFrames - 1)
        if (warmupFrames > 90 && slowFrames > 35 && quality > .65) {
          quality = Math.max(.65, quality - .15)
          slowFrames = 0
          layoutDirty = true
        }
        draw()
      }
      frame = requestAnimationFrame(tick)
    }

    function stop() {
      cancelAnimationFrame(frame)
      frame = 0
      previous = 0
    }

    function syncPlayback() {
      stop()
      if (disposed || contextLost || document.hidden || !inView) return
      if (reducedMotion.matches) {
        pointer[0] = pointer[1] = target[0] = target[1] = 0
        draw()
      } else {
        frame = requestAnimationFrame(tick)
      }
    }

    function invalidateLayout() {
      layoutDirty = true
      if (reducedMotion.matches && inView && !document.hidden) draw()
    }

    function onPointer(event: PointerEvent) {
      if (!finePointer.matches || reducedMotion.matches || event.pointerType === 'touch') return
      target[0] = Math.max(-1, Math.min(1, event.clientX / width * 2 - 1))
      target[1] = Math.max(-1, Math.min(1, 1 - event.clientY / height * 2))
    }

    function resetPointer() { target[0] = target[1] = 0 }

    function onContextLost(event: Event) {
      event.preventDefault()
      contextLost = true
      stop()
      anchor?.removeAttribute('data-art-ready')
      // Retain the static artwork for this visit instead of repeatedly asking
      // a stressed GPU for another context. The next mount may try again.
      canvas.style.visibility = 'hidden'
      if (!announcedReady) {
        announcedReady = true
        onReadyRef.current()
      }
    }

    const resizeObserver = new ResizeObserver(invalidateLayout)
    for (const element of [host, anchor, page, ...quietElements]) {
      if (element) resizeObserver.observe(element)
    }
    const themeObserver = new MutationObserver(() => {
      // This collection is used in dark mode. Keep the outgoing palette while
      // fading into the botanical scene instead of flashing a light material.
      if (!document.documentElement.classList.contains('dark')) return
      paletteDirty = true
      if (reducedMotion.matches && inView && !document.hidden) draw()
    })
    themeObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class', 'data-theme', 'data-accent', 'style'],
    })
    const intersectionObserver = new IntersectionObserver(([entry]) => {
      inView = entry?.isIntersecting ?? true
      syncPlayback()
    }, { rootMargin: '120px' })
    intersectionObserver.observe(anchor)
    window.addEventListener('resize', invalidateLayout, { passive: true })
    window.addEventListener('scroll', invalidateLayout, { passive: true, capture: true })
    window.addEventListener('pointermove', onPointer, { passive: true })
    window.addEventListener('blur', resetPointer)
    document.documentElement.addEventListener('pointerleave', resetPointer)
    document.addEventListener('visibilitychange', syncPlayback)
    reducedMotion.addEventListener('change', syncPlayback)
    canvas.addEventListener('webglcontextlost', onContextLost)
    document.fonts.ready.then(() => { if (!disposed) invalidateLayout() })
    syncPlayback()

    return () => {
      disposed = true
      stop()
      resizeObserver.disconnect()
      themeObserver.disconnect()
      intersectionObserver.disconnect()
      window.removeEventListener('resize', invalidateLayout)
      window.removeEventListener('scroll', invalidateLayout, true)
      window.removeEventListener('pointermove', onPointer)
      window.removeEventListener('blur', resetPointer)
      document.documentElement.removeEventListener('pointerleave', resetPointer)
      document.removeEventListener('visibilitychange', syncPlayback)
      reducedMotion.removeEventListener('change', syncPlayback)
      canvas.removeEventListener('webglcontextlost', onContextLost)
      anchor.removeAttribute('data-art-ready')
      geometry.remove()
      program.remove()
      canvas.remove()
      gl.getExtension('WEBGL_lose_context')?.loseContext()
    }
  }, [variant, seed])

  return <div ref={hostRef} className="login-art-world" />
}
