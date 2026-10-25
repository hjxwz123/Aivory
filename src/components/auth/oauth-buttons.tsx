/**
 * OAuthButtons keeps provider names on one line when they fit, then switches
 * to icons before wrapping. Every provider stays directly accessible.
 * Clicking does a full-page navigation to the backend
 * `/start` endpoint, which 302-redirects to the provider (a fetch can't follow
 * a cross-origin auth redirect, so this must be a real navigation).
 *
 * Renders nothing when no providers are configured; callers gate the
 * surrounding divider on `providers.length` so the section disappears cleanly.
 */
import { useLayoutEffect, useRef, useState, type CSSProperties } from 'react'
import { useTranslation } from 'react-i18next'
import { apiUrl } from '@/api'
import type { ApiPublicOAuthProvider } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Tooltip } from '@/components/ui/tooltip'
import { useLanguage } from '@/store/language'
import { oauthStartPath } from '@/lib/oauth'
import { PuzzleCaptchaDialog } from './puzzle-captcha-dialog'
import { OAuthBrandGlyph } from './oauth-glyph'

interface OAuthButtonsProps {
  providers: ApiPublicOAuthProvider[]
  captchaRequired?: boolean
  disabled?: boolean
}

export function OAuthButtons({ providers, captchaRequired = false, disabled = false }: OAuthButtonsProps) {
  const { t } = useTranslation('auth')
  const [pendingProvider, setPendingProvider] = useState<ApiPublicOAuthProvider | null>(null)
  const [layout, setLayout] = useState({ iconsOnly: false, rowCount: 1 })
  const rowRef = useRef<HTMLDivElement>(null)

  useLayoutEffect(() => {
    const row = rowRef.current
    if (!row) return
    function measure() {
      if (!row || row.clientWidth === 0) return
      const buttons = Array.from(row.querySelectorAll<HTMLButtonElement>('.login-provider-button'))
      const gap = parseFloat(getComputedStyle(row).columnGap) || 0
      const required = buttons.reduce((width, button) => {
        const label = button.querySelector<HTMLElement>('.login-provider-label')
        const icon = button.querySelector<HTMLElement>('.login-provider-icon')
        const style = getComputedStyle(button)
        const spacing = ['paddingLeft', 'paddingRight', 'borderLeftWidth', 'borderRightWidth', 'columnGap'] as const
        // The clipped label retains its intrinsic scrollWidth in icon mode,
        // so resizing in either direction uses the same measurement.
        const naturalWidth = (label?.scrollWidth ?? 0) + (icon?.getBoundingClientRect().width ?? 0)
          + spacing.reduce((sum, property) => sum + (parseFloat(style[property]) || 0), 0)
        return width + Math.max(44, naturalWidth)
      }, Math.max(0, buttons.length - 1) * gap)
      const iconsOnly = required > row.clientWidth
      const capacity = Math.max(1, Math.floor((row.clientWidth + gap) / (44 + gap)))
      const rowCount = iconsOnly ? Math.max(1, Math.ceil(buttons.length / capacity)) : 1
      setLayout((current) => current.iconsOnly === iconsOnly && current.rowCount === rowCount ? current : { iconsOnly, rowCount })
    }
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(row)
    let active = true
    void document.fonts.ready.then(() => { if (active) measure() })
    document.fonts.addEventListener('loadingdone', measure)
    return () => {
      active = false
      observer.disconnect()
      document.fonts.removeEventListener('loadingdone', measure)
    }
  }, [providers])

  const columns = Math.max(1, Math.ceil(providers.length / layout.rowCount))
  const positions: CSSProperties[] = []
  // Distribute the remainder across rows, then center each shorter row on a
  // grid of half-button tracks. This keeps 7 providers at 4 + 3, not 6 + 1.
  for (let row = 0; row < layout.rowCount; row++) {
    const count = Math.floor(providers.length / layout.rowCount) + (row < providers.length % layout.rowCount ? 1 : 0)
    for (let column = 0; column < count; column++) {
      positions.push({ gridRow: row + 1, gridColumn: `${columns - count + column * 2 + 1} / span 2` })
    }
  }

  function start(provider: ApiPublicOAuthProvider, captchaToken?: string) {
    window.location.href = apiUrl(oauthStartPath(provider.id, captchaToken, useLanguage.getState().lang))
  }

  function selectProvider(provider: ApiPublicOAuthProvider) {
    if (captchaRequired) setPendingProvider(provider)
    else start(provider)
  }

  if (window.aivoryDesktop || providers.length === 0) return null
  return (
    <>
      <div ref={rowRef} className="login-oauth-row" data-icons-only={layout.iconsOnly} style={{ '--login-oauth-tracks': columns * 2 } as CSSProperties} role="group" aria-label={t('login.otherProviders')}>
        {providers.map((provider, index) => (
          <Tooltip key={provider.id} content={layout.iconsOnly ? provider.name : ''}>
            <Button
              type="button"
              variant="secondary"
              className="login-provider-button"
              style={layout.iconsOnly ? positions[index] : undefined}
              aria-label={provider.name}
              disabled={disabled}
              onClick={() => selectProvider(provider)}
            >
              <span className="login-provider-icon" aria-hidden><OAuthBrandGlyph kind={provider.kind} icon={provider.icon} size={17} /></span>
              <span className="login-provider-label">{provider.name}</span>
            </Button>
          </Tooltip>
        ))}
      </div>
      <PuzzleCaptchaDialog
        open={pendingProvider !== null}
        purpose="register"
        onOpenChange={(open) => {
          if (!open) setPendingProvider(null)
        }}
        onSolved={(token) => {
          const provider = pendingProvider
          setPendingProvider(null)
          if (provider) start(provider, token)
        }}
      />
    </>
  )
}
