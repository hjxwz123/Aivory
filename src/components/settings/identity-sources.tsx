/**
 * IdentitySources — the account page's "identity sources" section (§ identity
 * linking). Lists the third-party accounts bound to the current user, lets them
 * bind an admin-configured provider, and unbind existing links.
 *
 * Binding is a full-page OAuth round-trip: linkIdentityStart returns the
 * provider authorize URL (with the caller stashed server-side in the OAuth
 * state) and we navigate to it; the callback links and redirects back to
 * /settings/account?linked=… | ?link_error=…, which we turn into a toast.
 *
 * The whole section hides when there's nothing to show — no providers
 * configured AND nothing already bound.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Link2, X } from 'lucide-react'
import { authApi, ApiError } from '@/api'
import type { ApiOAuthIdentity, ApiPublicOAuthProvider } from '@/api/types'
import { useOAuthProviders } from '@/hooks/use-oauth-providers'
import { useAuth } from '@/store/auth'
import { useLanguage } from '@/store/language'
import { OAuthBrandGlyph } from '@/components/auth/oauth-glyph'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { toast } from '@/hooks/use-toast'
import { createKeyedResourceCache, resolveOwnedResourceView } from '@/lib/keyed-resource-cache'

const EMPTY_IDENTITIES: ApiOAuthIdentity[] = []
const identitiesCache = createKeyedResourceCache<ApiOAuthIdentity[]>()
useAuth.subscribe((state, previous) => {
  if (state.user?.id !== previous.user?.id) identitiesCache.clear()
})

function boundDate(unixSec: number, locale: string): string {
  try {
    return new Intl.DateTimeFormat(locale, { year: 'numeric', month: 'short', day: 'numeric' }).format(
      new Date(unixSec * 1000),
    )
  } catch {
    return ''
  }
}

export function IdentitySources() {
  const { t } = useTranslation(['settings', 'common'])
  const lang = useLanguage((s) => s.lang)
  const user = useAuth((s) => s.user)
  const userId = user?.id ?? ''
  const { providers } = useOAuthProviders()
  const cached = userId ? identitiesCache.peek(userId) : undefined
  const [resourceUserId, setResourceUserId] = useState(userId)
  const [identities, setIdentities] = useState<ApiOAuthIdentity[]>(() => cached ?? [])
  const [loading, setLoading] = useState(cached === undefined)
  const [busy, setBusy] = useState<string | null>(null)
  const [searchParams, setSearchParams] = useSearchParams()
  const handledCallback = useRef(false)
  const requestVersionRef = useRef(0)

  const visible = resolveOwnedResourceView({
    resourceUserId,
    userId,
    value: identities,
    cached,
    empty: EMPTY_IDENTITIES,
    loading,
  })
  const visibleIdentities = visible.value
  const visibleLoading = visible.loading

  const load = useCallback(async (background = false) => {
    if (!userId) return
    const requestVersion = ++requestVersionRef.current
    if (!background) setLoading(true)
    try {
      const next = await identitiesCache.load(userId, () => authApi.identities(), true)
      if (
        requestVersionRef.current !== requestVersion ||
        useAuth.getState().user?.id !== userId
      ) return
      setResourceUserId(userId)
      setIdentities(next)
    } catch {
      /* leave the list empty; the section stays hidden if nothing else shows */
    } finally {
      if (
        requestVersionRef.current === requestVersion &&
        useAuth.getState().user?.id === userId
      ) setLoading(false)
    }
  }, [userId])

  useEffect(() => {
    const nextCached = userId ? identitiesCache.peek(userId) : undefined
    requestVersionRef.current += 1
    setResourceUserId(userId)
    setIdentities(nextCached ?? [])
    setLoading(Boolean(userId && nextCached === undefined))
    if (userId) void load(nextCached !== undefined)
    return () => {
      requestVersionRef.current += 1
    }
  }, [load, userId])

  // Turn the OAuth callback's ?linked / ?link_error into a toast, then strip the
  // params so a refresh doesn't re-fire it. Runs once (StrictMode double-invoke
  // guarded by the ref).
  useEffect(() => {
    if (handledCallback.current) return
    const linked = searchParams.get('linked')
    const err = searchParams.get('link_error')
    if (!linked && !err) return
    handledCallback.current = true
    if (linked) {
      toast.success(t('settings:account.identities.linked', { provider: linked }))
      void load(false)
    } else if (err === 'conflict') {
      toast.error(t('settings:account.identities.conflict'))
    } else {
      toast.error(t('settings:account.identities.linkFailed'))
    }
    const next = new URLSearchParams(searchParams)
    next.delete('linked')
    next.delete('link_error')
    setSearchParams(next, { replace: true })
  }, [searchParams, setSearchParams, t, load])

  async function bind(p: ApiPublicOAuthProvider) {
    setBusy('link:' + p.id)
    try {
      const { authorize_url } = await authApi.linkIdentityStart(p.id)
      window.location.href = authorize_url // leaves the page; no need to reset busy
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('settings:account.identities.linkFailed'))
      setBusy(null)
    }
  }

  async function unbind(it: ApiOAuthIdentity) {
    const key = it.provider_id + ':' + it.subject
    setBusy('unlink:' + key)
    try {
      await authApi.unlinkIdentity(it.provider_id, it.subject)
      setIdentities((list) => {
        const next = list.filter((x) => x.provider_id !== it.provider_id || x.subject !== it.subject)
        if (userId) identitiesCache.set(userId, next)
        return next
      })
      toast.success(t('settings:account.identities.unlinked'))
    } catch (e) {
      const msg =
        e instanceof ApiError && e.message === 'oauth_last_login_method'
          ? t('settings:account.identities.lastMethod')
          : e instanceof ApiError
            ? e.message
            : t('settings:account.identities.unlinkFailed')
      toast.error(msg)
    } finally {
      setBusy(null)
    }
  }

  // Providers not yet bound → offered as "bind" options (one row per provider).
  const boundIDs = new Set(visibleIdentities.map((i) => i.provider_id))
  const available = providers.filter((p) => !boundIDs.has(p.id))

  // Removing the only sign-in method would lock out a password-less account.
  const hasPassword = user?.has_password ?? true
  const lockoutOnLast = !hasPassword && visibleIdentities.length <= 1

  // Nothing configured and nothing bound → render nothing at all.
  if (!visibleLoading && visibleIdentities.length === 0 && providers.length === 0) return null

  return (
    <section className="mb-12">
      <div className="mb-5">
        <h2 className="tracking-tight text-xl text-[var(--color-fg)]">
          {t('settings:account.identities.title')}
        </h2>
        <p className="mt-1.5 text-sm text-[var(--color-fg-muted)]">{t('settings:account.identities.subtitle')}</p>
      </div>

      <div className="rounded-2xl border border-[var(--color-border)] bg-[var(--color-surface)] divide-y divide-[var(--color-divider)]">
        {visibleLoading ? (
          <div className="px-5 sm:px-6 py-8 text-sm text-[var(--color-fg-subtle)]">{t('common:common.loading')}</div>
        ) : (
          <>
            {visibleIdentities.map((it) => {
              const key = it.provider_id + ':' + it.subject
              const disableUnbind = lockoutOnLast
              return (
                <div key={key} className="px-5 sm:px-6 py-4 flex items-center gap-4">
                  <div className="shrink-0 size-9 inline-flex items-center justify-center rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                    <OAuthBrandGlyph kind={it.provider_kind} icon={it.provider_icon} size={17} />
                  </div>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className="text-sm font-medium text-[var(--color-fg)] truncate">{it.provider_name}</span>
                      {!it.provider_enabled ? (
                        <Badge size="xs" variant="neutral">
                          {t('settings:account.identities.disabled')}
                        </Badge>
                      ) : null}
                    </div>
                    <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)] truncate">
                      {it.email || t('settings:account.identities.noEmail')}
                      {it.created_at ? (
                        <>
                          {' · '}
                          {t('settings:account.identities.boundOn', { date: boundDate(it.created_at, lang) })}
                        </>
                      ) : null}
                    </div>
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={disableUnbind}
                    title={disableUnbind ? t('settings:account.identities.lastMethod') : undefined}
                    loading={busy === 'unlink:' + key}
                    onClick={() => void unbind(it)}
                    leadingIcon={<X size={14} aria-hidden />}
                  >
                    {t('settings:account.identities.unlink')}
                  </Button>
                </div>
              )
            })}

            {available.map((p) => (
              <div key={p.id} className="px-5 sm:px-6 py-4 flex items-center gap-4">
                <div className="shrink-0 size-9 inline-flex items-center justify-center rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                  <OAuthBrandGlyph kind={p.kind} icon={p.icon} size={17} />
                </div>
                <div className="min-w-0 flex-1">
                  <span className="text-sm font-medium text-[var(--color-fg)] truncate">{p.name}</span>
                  <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)]">
                    {t('settings:account.identities.notLinked')}
                  </div>
                </div>
                <Button
                  variant="secondary"
                  size="sm"
                  loading={busy === 'link:' + p.id}
                  onClick={() => void bind(p)}
                  leadingIcon={<Link2 size={14} aria-hidden />}
                >
                  {t('settings:account.identities.link')}
                </Button>
              </div>
            ))}

            {visibleIdentities.length === 0 && available.length === 0 ? (
              <div className="px-5 sm:px-6 py-8 text-sm text-[var(--color-fg-subtle)]">
                {t('settings:account.identities.empty')}
              </div>
            ) : null}
          </>
        )}
      </div>
    </section>
  )
}
