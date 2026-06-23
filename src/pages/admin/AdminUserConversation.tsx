/**
 * AdminUserConversation — read-only thread view of one conversation belonging
 * to a target user, for support / abuse triage (§8.1).
 *
 * Re-uses the chat surface's <Markdown>, <ReasoningTrace>, and <CitationList>
 * primitives so the rendering matches what the user actually sees — important
 * for triage, where "this looks different here than over there" wastes time.
 *
 * Read-only: no edit / regenerate / fork actions surfaced; the admin scope is
 * intentionally limited to inspection. Style follows the rest of /admin.
 */
import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, FileText, HardDrive, RefreshCw, Trash2, ExternalLink, ChevronDown } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { adminApi, ApiError } from '@/api'
import type { ApiConversation, ApiUser } from '@/api/types'
import type { Message } from '@/types/chat'
import { Badge } from '@/components/ui/badge'
import { toast } from '@/hooks/use-toast'
import { MessageRow } from '@/components/chat/message-row'
import { useModels } from '@/store/models'
import { toLocalMessage } from '@/store/conversations'
import { cn } from '@/lib/utils'

function formatStamp(unixMs: number): string {
  if (!unixMs) return ''
  try {
    return new Date(unixMs).toLocaleString()
  } catch {
    return String(unixMs)
  }
}

export default function AdminUserConversation() {
  const { t } = useTranslation('admin')
  const navigate = useNavigate()
  const { id = '', cid = '' } = useParams<{ id: string; cid: string }>()
  const [conv, setConv] = useState<ApiConversation | null>(null)
  const [messages, setMessages] = useState<Message[]>([])
  const [users, setUsers] = useState<ApiUser[]>([])
  const [loading, setLoading] = useState(true)
  // MessageRow resolves the assistant's model name/icon from the shared models
  // store (the same source the chat surface uses). Hydrate it for deep-links.
  const getModelById = useModels((s) => s.getById)
  const modelsLoaded = useModels((s) => s.loaded)
  const loadModels = useModels((s) => s.load)
  useEffect(() => {
    if (!modelsLoaded) void loadModels()
  }, [modelsLoaded, loadModels])

  useEffect(() => {
    let cancelled = false
    async function load() {
      setLoading(true)
      try {
        const [c, msgs, us] = await Promise.all([
          adminApi.conversation(cid),
          adminApi.conversationMessages(cid),
          adminApi.users('', 200, 0).then((r) => r.users).catch(() => [] as ApiUser[]),
        ])
        if (cancelled) return
        setConv(c)
        setMessages(msgs.map(toLocalMessage))
        setUsers(us)
      } catch (e) {
        if (!cancelled) toast.error(e instanceof ApiError ? e.message : t('common.failed'))
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [cid, t])

  const headerTitle = useMemo(() => conv?.title || t('users.untitledConversation'), [conv, t])
  // Resolve the conversation's model id → label for the page header (the raw
  // m_… id is useless for triage). Per-message model names come from MessageRow.
  const modelName = (mid?: string) => (mid ? getModelById(mid)?.label ?? mid : '')
  const convUser = useMemo(() => users.find((x) => x.id === (conv?.user_id || id)), [users, conv, id])
  const userLabel = convUser?.name || convUser?.email || ''

  return (
    <div>
      <button
        type="button"
        onClick={() => navigate(`/admin/users/${encodeURIComponent(id)}/conversations`)}
        className="inline-flex items-center gap-1.5 text-[12.5px] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] interactive rounded-[6px] -ml-2 px-2 py-1.5 mb-4 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
      >
        <ArrowLeft size={12} aria-hidden />
        {t('users.backToConversations')}
      </button>

      <header>
        <div className="flex items-center gap-2 flex-wrap">
          <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)] truncate">
            {headerTitle}
          </h1>
          {conv?.archived ? (
            <Badge size="xs" variant="neutral">{t('users.archived')}</Badge>
          ) : null}
        </div>
        <p className="mt-2 text-[var(--color-fg-muted)] text-sm">
          {[userLabel, modelName(conv?.model_id) || conv?.provider].filter(Boolean).join(' · ') || '—'}
          {conv?.updated_at ? ` · ${formatStamp(conv.updated_at * 1000)}` : null}
        </p>
        <Link
          to="/admin/users"
          className="mt-3 inline-block text-[12px] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] interactive rounded-[6px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        >
          {t('users.backToUsers')}
        </Link>
      </header>

      <SandboxPanel convId={cid} />

      <section className="mt-8">
        {loading ? (
          <div className="text-sm text-[var(--color-fg-subtle)]">{t('common.loading')}</div>
        ) : messages.length === 0 ? (
          <div className="text-sm text-[var(--color-fg-subtle)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-5 py-10 text-center">
            {t('users.noMessages')}
          </div>
        ) : (
          // Same MessageRow + container the chat surface uses, in read-only mode,
          // so the admin sees exactly what the user saw (§8.1).
          <div className="flex flex-col gap-8 mx-auto w-full max-w-[var(--layout-message-max-w)]">
            {messages.map((m) => (
              <MessageRow key={m.id} message={m} userName={userLabel} readOnly />
            ))}
          </div>
        )}
      </section>
    </div>
  )
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

const PREVIEWABLE = /\.(png|jpe?g|gif|webp|svg|bmp)$/i

// SandboxPanel — admin inspector for a conversation's sandbox workspace
// (§ admin tools). Lists files, opens/previews them, and clears the session.
function SandboxPanel({ convId }: { convId: string }) {
  const { t } = useTranslation('admin')
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [session, setSession] = useState('')
  const [files, setFiles] = useState<{ path: string; size: number }[]>([])
  const [unavailable, setUnavailable] = useState(false)
  const [clearing, setClearing] = useState(false)

  async function refresh() {
    setLoading(true)
    try {
      const res = await adminApi.sandboxFiles(convId)
      setSession(res.session)
      setFiles(res.files ?? [])
      setUnavailable(!!(res as { unavailable?: boolean }).unavailable)
      setLoaded(true)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common.failed'))
    } finally {
      setLoading(false)
    }
  }

  async function clear() {
    setClearing(true)
    try {
      await adminApi.clearSandbox(convId)
      setSession('')
      setFiles([])
      setUnavailable(false)
      toast.success(t('sandbox.cleared', { defaultValue: 'Sandbox cleared' }))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common.failed'))
    } finally {
      setClearing(false)
    }
  }

  function toggle() {
    const next = !open
    setOpen(next)
    if (next && !loaded) void refresh()
  }

  return (
    <section className="mt-6 rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
      <button
        type="button"
        onClick={toggle}
        aria-expanded={open}
        className="flex w-full items-center gap-2 px-5 py-3.5 text-left interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] rounded-[14px]"
      >
        <HardDrive size={15} aria-hidden className="text-[var(--color-fg-muted)]" />
        <span className="font-medium text-[var(--color-fg)] text-sm">
          {t('sandbox.title', { defaultValue: 'Sandbox files' })}
        </span>
        {loaded ? (
          <span className="text-[12px] text-[var(--color-fg-subtle)]">· {files.length}</span>
        ) : null}
        <ChevronDown
          size={15}
          aria-hidden
          className={cn('ml-auto text-[var(--color-fg-subtle)] transition-transform', open ? 'rotate-180' : '')}
        />
      </button>

      {open ? (
        <div className="border-t border-[var(--color-divider)] px-5 py-4">
          <div className="flex items-center gap-2 mb-3">
            <Button variant="ghost" size="sm" leadingIcon={<RefreshCw size={13} aria-hidden />} onClick={() => void refresh()} loading={loading}>
              {t('sandbox.refresh', { defaultValue: 'Refresh' })}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              leadingIcon={<Trash2 size={13} aria-hidden />}
              className="text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)]"
              onClick={() => void clear()}
              loading={clearing}
              disabled={!session}
            >
              {t('sandbox.clear', { defaultValue: 'Clear sandbox' })}
            </Button>
          </div>

          {loading ? (
            <div className="text-sm text-[var(--color-fg-subtle)]">{t('common.loading')}</div>
          ) : !session ? (
            <div className="text-sm text-[var(--color-fg-subtle)]">
              {t('sandbox.none', { defaultValue: 'No sandbox session for this conversation.' })}
            </div>
          ) : unavailable ? (
            <div className="text-sm text-[var(--color-fg-subtle)]">
              {t('sandbox.unavailable', { defaultValue: 'Session expired or sidecar does not support file listing.' })}
            </div>
          ) : files.length === 0 ? (
            <div className="text-sm text-[var(--color-fg-subtle)]">
              {t('sandbox.empty', { defaultValue: 'Sandbox workspace is empty.' })}
            </div>
          ) : (
            <ul className="flex flex-col divide-y divide-[var(--color-divider)]">
              {files.map((f) => {
                const url = adminApi.sandboxFileUrl(convId, f.path)
                const isImg = PREVIEWABLE.test(f.path)
                return (
                  <li key={f.path} className="flex items-center gap-3 py-2">
                    {isImg ? (
                      <img src={url} alt={f.path} className="size-9 rounded-[6px] border border-[var(--color-border-subtle)] object-cover bg-[var(--color-bg-muted)]" />
                    ) : (
                      <span className="inline-flex size-9 items-center justify-center rounded-[6px] bg-[var(--color-bg-muted)] text-[var(--color-fg-subtle)]">
                        <FileText size={14} aria-hidden />
                      </span>
                    )}
                    <span className="min-w-0 flex-1">
                      <span className="block truncate font-mono text-[12.5px] text-[var(--color-fg)]">{f.path}</span>
                      <span className="text-[11px] text-[var(--color-fg-subtle)]">{formatBytes(f.size)}</span>
                    </span>
                    <a
                      href={url}
                      target="_blank"
                      rel="noreferrer noopener"
                      className="inline-flex items-center gap-1 text-[12px] text-[var(--color-accent)] hover:underline shrink-0"
                    >
                      {t('sandbox.open', { defaultValue: 'Open' })}
                      <ExternalLink size={11} aria-hidden />
                    </a>
                  </li>
                )
              })}
            </ul>
          )}
        </div>
      ) : null}
    </section>
  )
}
