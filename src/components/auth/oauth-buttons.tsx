/**
 * OAuthButtons — renders one "Continue with …" button per admin-configured
 * social-login provider. Clicking does a full-page navigation to the backend
 * `/start` endpoint, which 302-redirects to the provider (a fetch can't follow
 * a cross-origin auth redirect, so this must be a real navigation).
 *
 * Renders nothing when no providers are configured; callers gate the
 * surrounding divider on `providers.length` so the section disappears cleanly.
 */
import { useState } from 'react'
import { apiUrl } from '@/api'
import type { ApiPublicOAuthProvider } from '@/api/types'
import { Button } from '@/components/ui/button'
import { oauthStartPath } from '@/lib/oauth'
import { PuzzleCaptchaDialog } from './puzzle-captcha-dialog'
import { OAuthBrandGlyph } from './oauth-glyph'

interface OAuthButtonsProps {
  providers: ApiPublicOAuthProvider[]
  captchaRequired?: boolean
}

export function OAuthButtons({ providers, captchaRequired = false }: OAuthButtonsProps) {
  const [pendingProvider, setPendingProvider] = useState<ApiPublicOAuthProvider | null>(null)

  function start(provider: ApiPublicOAuthProvider, captchaToken?: string) {
    window.location.href = apiUrl(oauthStartPath(provider.id, captchaToken))
  }

  if (providers.length === 0) return null
  return (
    <>
      {providers.map((p) => (
        <Button
          key={p.id}
          type="button"
          variant="secondary"
          size="lg"
          onClick={() => {
            if (captchaRequired) {
              setPendingProvider(p)
              return
            }
            start(p)
          }}
        >
          <OAuthBrandGlyph kind={p.kind} icon={p.icon} />
          {p.name}
        </Button>
      ))}
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
