/**
 * OAuthBrandGlyph — the icon for a login method. Google / GitHub / Apple use
 * built-in brand glyphs (no upload needed); generic OAuth2/OIDC providers render
 * their admin-set icon (uploaded URL or emoji), falling back to a neutral glyph.
 *
 * Shared by the login buttons and the admin "Login methods" page so both render
 * the same mark.
 */
import { LogIn } from 'lucide-react'
import type { OAuthKind } from '@/api/types'

export function OAuthBrandGlyph({
  kind,
  icon,
  size = 14,
}: {
  kind: OAuthKind
  icon?: string
  size?: number
}) {
  if (kind === 'google') return <GoogleGlyph size={size} />
  if (kind === 'github') return <GithubGlyph size={size} />
  if (kind === 'apple') return <AppleGlyph size={size} />
  if (icon) {
    if (icon.startsWith('http') || icon.startsWith('/')) {
      return <img src={icon} alt="" width={size} height={size} className="rounded-[3px] object-cover" />
    }
    return (
      <span className="leading-none" style={{ fontSize: size }}>
        {icon}
      </span>
    )
  }
  return <LogIn size={size} aria-hidden />
}

function GoogleGlyph({ size = 14 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden>
      <path
        fill="currentColor"
        d="M21.6 12.2c0-.7-.06-1.36-.18-2H12v3.8h5.4a4.6 4.6 0 0 1-2 3v2.5h3.23c1.9-1.74 2.97-4.3 2.97-7.3Zm-9.6 9.6c2.7 0 4.96-.9 6.62-2.43l-3.23-2.5c-.9.6-2.05.96-3.4.96-2.6 0-4.8-1.76-5.6-4.12H3.07v2.58A9.99 9.99 0 0 0 12 21.8Zm-5.6-9.7a6 6 0 0 1 0-3.8V5.72H3.06a10 10 0 0 0 0 8.56l3.34-2.18Zm5.6-6.5c1.46 0 2.78.5 3.82 1.49l2.86-2.86C16.96 2.97 14.7 2 12 2A9.99 9.99 0 0 0 3.07 7.72l3.34 2.58c.8-2.36 3-4.12 5.6-4.12Z"
      />
    </svg>
  )
}
function GithubGlyph({ size = 14 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden>
      <path
        fill="currentColor"
        d="M12 2C6.5 2 2 6.6 2 12.25c0 4.5 2.87 8.33 6.84 9.68.5.1.68-.23.68-.5v-1.7c-2.78.62-3.37-1.36-3.37-1.36-.45-1.18-1.1-1.5-1.1-1.5-.9-.62.07-.6.07-.6 1 .07 1.52 1.05 1.52 1.05.88 1.55 2.32 1.1 2.88.85.09-.66.35-1.1.63-1.36-2.22-.26-4.55-1.14-4.55-5.07 0-1.12.39-2.03 1.03-2.75-.1-.26-.45-1.3.1-2.71 0 0 .84-.27 2.75 1.05a9.42 9.42 0 0 1 5 0c1.91-1.32 2.75-1.05 2.75-1.05.55 1.41.2 2.45.1 2.7.64.73 1.03 1.64 1.03 2.76 0 3.94-2.34 4.8-4.57 5.06.36.32.68.94.68 1.89v2.8c0 .27.18.6.69.5A10.04 10.04 0 0 0 22 12.25C22 6.6 17.52 2 12 2Z"
      />
    </svg>
  )
}
function AppleGlyph({ size = 14 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden>
      <path
        fill="currentColor"
        d="M16.4 12.66c0-2.06 1.7-3.05 1.77-3.1-.97-1.4-2.46-1.6-2.99-1.62-1.28-.13-2.5.75-3.14.75-.65 0-1.65-.74-2.73-.72-1.4.02-2.7.81-3.42 2.07-1.46 2.53-.37 6.27 1.05 8.32.69 1 1.51 2.13 2.59 2.1 1.05-.05 1.45-.68 2.71-.68 1.27 0 1.62.68 2.73.66 1.13-.02 1.84-1.02 2.53-2.03.8-1.15 1.12-2.27 1.14-2.32-.02-.01-2.18-.84-2.21-3.34ZM14.34 5.9c.58-.7.97-1.66.86-2.62-.83.03-1.84.55-2.43 1.24-.53.61-1 1.59-.88 2.53.93.07 1.87-.47 2.45-1.15Z"
      />
    </svg>
  )
}
