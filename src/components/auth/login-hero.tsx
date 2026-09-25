import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AuthArtwork, type ArtworkTheme } from './auth-artwork'
import { useTheme } from '@/store/theme'

interface HeroState {
  mounted: ArtworkTheme[]
  visible: ArtworkTheme
  ready: Partial<Record<ArtworkTheme, boolean>>
}

function ArtworkFrame({ theme, visible, onReady }: {
  theme: ArtworkTheme
  visible: boolean
  onReady: (theme: ArtworkTheme) => void
}) {
  const frameRef = useRef<HTMLDivElement>(null)
  const signalReady = useCallback(() => {
    // Commit the incoming layer's opacity:0 before changing it to opacity:1.
    // This also covers a cached lazy module that resolves before the first paint.
    if (frameRef.current) void getComputedStyle(frameRef.current).opacity
    onReady(theme)
  }, [theme, onReady])

  return (
    <div ref={frameRef} className="login-art-frame" data-active={visible}>
      <AuthArtwork theme={theme} onReady={signalReady} />
    </div>
  )
}

export function LoginHero() {
  const { t } = useTranslation('auth')
  const resolved = useTheme((state) => state.resolved)
  const [state, setState] = useState<HeroState>(() => ({ mounted: [resolved], visible: resolved, ready: {} }))

  useLayoutEffect(() => {
    setState((current) => {
      if (!current.mounted.includes(resolved)) {
        return { ...current, mounted: [...current.mounted, resolved] }
      }
      if (current.ready[resolved] && current.visible !== resolved) return { ...current, visible: resolved }
      return current
    })
  }, [resolved])

  const onReady = useCallback((theme: ArtworkTheme) => {
    setState((current) => {
      if (!current.mounted.includes(theme)) return current
      const target = useTheme.getState().resolved
      const visible = target === theme ? theme : current.visible
      if (current.ready[theme] && visible === current.visible) return current
      return { ...current, visible, ready: { ...current.ready, [theme]: true } }
    })
  }, [])

  useEffect(() => {
    if (state.mounted.length < 2 || state.visible !== resolved || !state.ready[resolved]) return
    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    // Retire the outgoing animation after the fade, releasing its GPU/GSAP
    // resources. Rapid toggles cancel this timer and reverse the current fade.
    const timeout = window.setTimeout(() => {
      setState((current) => {
        if (current.visible !== resolved || useTheme.getState().resolved !== resolved) return current
        return { mounted: [resolved], visible: resolved, ready: { [resolved]: true } }
      })
    }, reducedMotion ? 0 : 780)
    return () => window.clearTimeout(timeout)
  }, [resolved, state.visible, state.mounted, state.ready])

  return (
    <aside className="login-hero" aria-labelledby="login-brand-message">
      <div className="login-art-stack" aria-hidden="true">
        {state.mounted.map((theme) => (
          <ArtworkFrame key={theme} theme={theme} visible={state.visible === theme} onReady={onReady} />
        ))}
      </div>
      <div className="login-hero-copy">
        {(['light', 'dark'] as const).map((theme) => {
          const active = state.visible === theme
          return (
            <div key={theme} className="login-hero-copy-layer" data-active={active} aria-hidden={!active}>
              <h2 id={active ? 'login-brand-message' : undefined}>{t(theme === 'dark' ? 'login.heroDarkTitle' : 'login.heroTitle')}</h2>
              <p>{t(theme === 'dark' ? 'login.heroDarkSubtitle' : 'login.heroSubtitle')}</p>
            </div>
          )
        })}
      </div>
    </aside>
  )
}
