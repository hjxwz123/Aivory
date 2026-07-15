/**
 * auth-errors — translates the stable snake_case error codes the auth
 * endpoints (login/register/forgot-reset/2FA-login/first-run setup) return
 * into localized text.
 *
 * The server intentionally returns machine codes ("invalid_credentials",
 * "rate_limited", …) rather than English prose specifically so every locale
 * can show a real translation instead of raw English (§ auth.json
 * `errorCodes`). An unrecognized code — should not happen once every code the
 * backend can emit has a matching key — falls back to the code itself rather
 * than silently swallowing it.
 */
import type { TFunction } from 'i18next'

export function authErrorText(t: TFunction<'auth'>, code: string | null | undefined, fallback: string): string {
  if (!code) return fallback
  return t(`errorCodes.${code}`, { defaultValue: code })
}
