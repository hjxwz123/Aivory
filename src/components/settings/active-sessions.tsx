import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Monitor, Smartphone, MapPin, X } from 'lucide-react'
import { authApi, ApiError } from '@/api'
import type { ApiSession } from '@/api/types'
import { useLanguage } from '@/store/language'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { SettingsSection } from '@/components/settings/settings-section'
import { toast } from '@/hooks/use-toast'
import { useAuth } from '@/store/auth'
import { createKeyedResourceCache, resolveOwnedResourceView } from '@/lib/keyed-resource-cache'
import { parseClientDevice } from '@/lib/client-device'

type SessionSnapshot = { sessions: ApiSession[]; current: string }
const EMPTY_SESSION_SNAPSHOT: SessionSnapshot = { sessions: [], current: '' }
const sessionsCache = createKeyedResourceCache<SessionSnapshot>()
useAuth.subscribe((state, previous) => {
  if (state.user?.id !== previous.user?.id) sessionsCache.clear()
})

/** Private/loopback ranges — used to label local sessions when geo is absent. */
function isLocalIp(ip: string): boolean {
  return /^(127\.|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|::1$|fc|fd)/i.test(ip)
}

function relativeTime(unixSec: number, locale: string): string {
  const diff = unixSec - Date.now() / 1000 // negative → in the past
  const abs = Math.abs(diff)
  const rtf = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' })
  if (abs < 60) return rtf.format(Math.round(diff), 'second')
  if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute')
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour')
  if (abs < 2592000) return rtf.format(Math.round(diff / 86400), 'day')
  if (abs < 31536000) return rtf.format(Math.round(diff / 2592000), 'month')
  return rtf.format(Math.round(diff / 31536000), 'year')
}

export function ActiveSessions() {
  const { t } = useTranslation(['settings', 'common'])
  const lang = useLanguage((s) => s.lang)
  const userId = useAuth((s) => s.user?.id ?? '')
  const cached = userId ? sessionsCache.peek(userId) : undefined
  const [resourceUserId, setResourceUserId] = useState(userId)
  const [sessions, setSessions] = useState<ApiSession[]>(() => cached?.sessions ?? [])
  const [current, setCurrent] = useState(() => cached?.current ?? '')
  const [loading, setLoading] = useState(cached === undefined)
  const [busy, setBusy] = useState<string | null>(null)
  const requestVersionRef = useRef(0)

  // The auth store can switch owners before effects run. Derive the visible
  // snapshot from the CURRENT owner immediately so one render can never expose
  // the previous account's sessions.
  const visible = resolveOwnedResourceView({
    resourceUserId,
    userId,
    value: { sessions, current },
    cached,
    empty: EMPTY_SESSION_SNAPSHOT,
    loading,
  })
  const visibleSessions = visible.value.sessions
  const visibleCurrent = visible.value.current
  const visibleLoading = visible.loading

  useEffect(() => {
    const nextCached = userId ? sessionsCache.peek(userId) : undefined
    const background = nextCached !== undefined
    const requestVersion = ++requestVersionRef.current

    setResourceUserId(userId)
    setSessions(nextCached?.sessions ?? [])
    setCurrent(nextCached?.current ?? '')
    setLoading(Boolean(userId && !background))

    if (!userId) return
    const stillCurrent = () =>
      requestVersionRef.current === requestVersion && useAuth.getState().user?.id === userId

    void sessionsCache
      .load(userId, () => authApi.sessions(), true)
      .then((res) => {
        if (!stillCurrent()) return
        setResourceUserId(userId)
        setSessions(res.sessions)
        setCurrent(res.current)
      })
      .catch((error: unknown) => {
        if (!stillCurrent() || background) return
        toast.error(error instanceof ApiError ? error.message : t('settings:account.sessions.loadFailed'))
      })
      .finally(() => {
        if (stillCurrent()) setLoading(false)
      })

    return () => {
      if (requestVersionRef.current === requestVersion) requestVersionRef.current += 1
    }
  }, [t, userId])

  async function revoke(id: string) {
    setBusy(id)
    try {
      await authApi.revokeSession(id)
      setSessions((s) => {
        const next = s.filter((x) => x.id !== id)
        if (userId) sessionsCache.set(userId, { sessions: next, current: visibleCurrent })
        return next
      })
      toast.success(t('settings:account.sessions.revoked'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('settings:account.sessions.revokeFailed'))
    } finally {
      setBusy(null)
    }
  }

  async function revokeOthers() {
    setBusy('others')
    try {
      await authApi.revokeOtherSessions()
      setSessions((s) => {
        const next = s.filter((x) => x.id === visibleCurrent)
        if (userId) sessionsCache.set(userId, { sessions: next, current: visibleCurrent })
        return next
      })
      toast.success(t('settings:account.sessions.othersRevoked'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('settings:account.sessions.revokeFailed'))
    } finally {
      setBusy(null)
    }
  }

  const hasOthers = visibleSessions.some((s) => s.id !== visibleCurrent)

  return (
    <SettingsSection
      title={t('settings:account.sessions.title')}
      description={t('settings:account.sessions.subtitle')}
      actions={hasOthers ? (
        <Button variant="ghost" size="sm" loading={busy === 'others'} onClick={() => void revokeOthers()}>
          {t('settings:account.sessions.signOutOthers')}
        </Button>
      ) : null}
    >
      {visibleLoading ? (
        <div className="py-5 text-sm text-[var(--color-fg-subtle)]">{t('common:common.loading')}</div>
      ) : visibleSessions.length === 0 ? (
        <div className="py-5 text-sm text-[var(--color-fg-subtle)]">
          {t('settings:account.sessions.empty')}
        </div>
      ) : (
        visibleSessions.map((s) => {
          const { browser, os, mobile } = parseClientDevice(s.user_agent, t('common:desktopApp'))
          const Icon = mobile ? Smartphone : Monitor
          const device = [browser, os].filter(Boolean).join(' · ') || t('settings:account.sessions.unknownDevice')
          const isCurrent = s.id === visibleCurrent
          const place = s.location || (isLocalIp(s.ip) ? t('settings:account.sessions.localNetwork') : '')
          return (
            <div key={s.id} className="flex items-center gap-3 py-3">
              <div className="shrink-0 size-9 inline-flex items-center justify-center rounded-[10px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                <Icon size={17} aria-hidden />
              </div>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="text-sm font-medium text-[var(--color-fg)] truncate">{device}</span>
                  {isCurrent ? (
                    <Badge size="xs" variant="accent">
                      {t('settings:account.sessions.thisDevice')}
                    </Badge>
                  ) : null}
                </div>
                <div className="mt-0.5 flex items-center gap-1.5 text-[12px] text-[var(--color-fg-subtle)]">
                  {place ? (
                    <>
                      <MapPin size={11} aria-hidden className="shrink-0" />
                      <span className="truncate">{place}</span>
                      <span aria-hidden>·</span>
                    </>
                  ) : null}
                  <span className="font-mono truncate">{s.ip || '—'}</span>
                  <span aria-hidden>·</span>
                  <span className="whitespace-nowrap">
                    {isCurrent ? t('settings:account.sessions.activeNow') : relativeTime(s.last_seen, lang)}
                  </span>
                </div>
              </div>
              {isCurrent ? null : (
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t('settings:account.sessions.revoke')}
                  loading={busy === s.id}
                  onClick={() => void revoke(s.id)}
                >
                  <X size={15} aria-hidden />
                </Button>
              )}
            </div>
          )
        })
      )}
    </SettingsSection>
  )
}
