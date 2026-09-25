import { lazy, Suspense, useEffect, useState } from 'react'
import type { ArtworkVariant } from './auth-artwork-shaders'
import type { SvgArtworkVariant } from './auth-artwork-svg'

const AuthArtworkWebGL = lazy(() => import('./auth-artwork-webgl'))
const AuthArtworkSvg = lazy(() => import('./auth-artwork-svg'))
type AuthArtworkVariant = ArtworkVariant | SvgArtworkVariant
export type ArtworkTheme = 'light' | 'dark'
const collections: Record<ArtworkTheme, AuthArtworkVariant[]> = {
  light: ['seedling', 'flourish', 'dandelion'],
  dark: ['mercury', 'eclipse', 'aurora'],
}
interface ArtworkSelection { variant: AuthArtworkVariant; seed: number }
const historyKey = 'aivory.auth.artwork'
const pageSelections: Partial<Record<ArtworkTheme, ArtworkSelection>> = {}

function developmentPreview(theme: ArtworkTheme): AuthArtworkVariant | undefined {
  if (!import.meta.env.DEV || typeof window === 'undefined') return undefined
  // Keep a local preview selection through the initial login → setup redirect.
  const entry = performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming | undefined
  const requested = new URL(window.location.href).searchParams.get('artwork')
    ?? (entry ? new URL(entry.name).searchParams.get('artwork') : null)
  // A preview never puts a botanical scene into the dark collection.
  return collections[theme].find((variant) => variant === requested)
}

/** Remember a scene for each theme through navigation and theme toggles.
 * A reload chooses another scene within that theme's own collection. */
function selectArtwork(theme: ArtworkTheme): ArtworkSelection {
  const cached = pageSelections[theme]
  if (cached) return cached
  const key = `${historyKey}.${theme}`
  let previous: string | null = null
  try { previous = sessionStorage.getItem(key) ?? sessionStorage.getItem(historyKey) } catch { /* Storage is optional. */ }
  const candidates = collections[theme].filter((variant) => variant !== previous)
  const entropy = new Uint32Array(2)
  if (typeof crypto !== 'undefined' && crypto.getRandomValues) {
    crypto.getRandomValues(entropy)
  } else {
    entropy[0] = Math.floor(Math.random() * 4294967296)
    entropy[1] = Math.floor(Math.random() * 4294967296)
  }
  const selection = { variant: developmentPreview(theme) ?? candidates[entropy[0] % candidates.length], seed: entropy[1] }
  pageSelections[theme] = selection
  try { sessionStorage.setItem(key, selection.variant) } catch { /* Vary without persistence. */ }
  return selection
}

function SvgReady({ onReady }: { onReady: () => void }) {
  // This commits only after the sibling lazy SVG and its styles have loaded.
  useEffect(onReady, [onReady])
  return null
}

/** A layout anchor, not a crop: the scene itself paints across the viewport. */
export function AuthArtwork({ theme, onReady }: { theme: ArtworkTheme; onReady: () => void }) {
  const [selection] = useState(() => selectArtwork(theme))
  const isWebGL = (variant: AuthArtworkVariant): variant is ArtworkVariant => (
    variant === 'mercury' || variant === 'eclipse' || variant === 'aurora'
  )
  return (
    <div className="login-art-stage" data-artwork={selection.variant} aria-hidden="true">
      {isWebGL(selection.variant) ? (
        <>
          <div className="login-art-fallback">
            <i className="login-art-fallback-halo" />
            <i className="login-art-fallback-body" />
            <i className="login-art-fallback-orbit" />
          </div>
          <Suspense fallback={null}>
            <AuthArtworkWebGL variant={selection.variant} seed={selection.seed} onReady={onReady} />
          </Suspense>
        </>
      ) : (
        <Suspense fallback={<span className="login-art-seed-placeholder" />}>
          <AuthArtworkSvg variant={selection.variant} seed={selection.seed} />
          <SvgReady onReady={onReady} />
        </Suspense>
      )}
    </div>
  )
}
