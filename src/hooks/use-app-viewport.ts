import { useLayoutEffect } from 'react'

/** Safari's keyboard changes the visual viewport without resizing 100dvh. */
export function useAppViewport() {
  useLayoutEffect(() => {
    const viewport = window.visualViewport
    const root = document.documentElement
    let frame = 0
    const update = () => {
      const height = viewport && viewport.scale === 1 ? viewport.height : window.innerHeight
      root.style.setProperty('--app-height', `${Math.floor(height)}px`)
    }
    const schedule = () => {
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(update)
    }
    update()
    window.addEventListener('resize', schedule)
    viewport?.addEventListener('resize', schedule)
    return () => {
      cancelAnimationFrame(frame)
      window.removeEventListener('resize', schedule)
      viewport?.removeEventListener('resize', schedule)
      root.style.removeProperty('--app-height')
    }
  }, [])
}
