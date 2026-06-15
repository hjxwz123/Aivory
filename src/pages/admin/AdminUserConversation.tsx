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
import { ArrowLeft, Bot, User as UserIcon } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiConversation, ApiModel, ApiUser } from '@/api/types'
import type { Message } from '@/types/chat'
import { Badge } from '@/components/ui/badge'
import { toast } from '@/hooks/use-toast'
import { Markdown } from '@/components/chat/markdown'
import { ReasoningTrace } from '@/components/chat/reasoning-trace'
import { CitationList } from '@/components/chat/citation'
import { toLocalMessage } from '@/store/conversations'

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
  const [models, setModels] = useState<ApiModel[]>([])
  const [users, setUsers] = useState<ApiUser[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false
    async function load() {
      setLoading(true)
      try {
        const [c, msgs, ms, us] = await Promise.all([
          adminApi.conversation(cid),
          adminApi.conversationMessages(cid),
          adminApi.models().catch(() => [] as ApiModel[]),
          adminApi.users().catch(() => [] as ApiUser[]),
        ])
        if (cancelled) return
        setConv(c)
        setMessages(msgs.map(toLocalMessage))
        setModels(ms)
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
  // Resolve ids → human-readable names (the raw m_…/u_… ids are useless for triage).
  const modelName = useMemo(() => {
    const byId = new Map(models.map((m) => [m.id, m.label]))
    return (mid?: string) => (mid ? byId.get(mid) ?? mid : '')
  }, [models])
  const userLabel = useMemo(() => {
    const u = users.find((x) => x.id === (conv?.user_id || id))
    return u?.name || u?.email || ''
  }, [users, conv, id])

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

      <section className="mt-8">
        {loading ? (
          <div className="text-sm text-[var(--color-fg-subtle)]">{t('common.loading')}</div>
        ) : messages.length === 0 ? (
          <div className="text-sm text-[var(--color-fg-subtle)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-5 py-10 text-center">
            {t('users.noMessages')}
          </div>
        ) : (
          <ol className="flex flex-col gap-6">
            {messages.map((m) => (
              <AdminMessageRow key={m.id} message={m} modelName={modelName} />
            ))}
          </ol>
        )}
      </section>
    </div>
  )
}

/**
 * AdminMessageRow — stripped-down message renderer with no action affordances.
 * Avatar + role badge + timestamp; thinking is collapsed by default; tool
 * calls and citations render with their full chat-side components so admins
 * see exactly what the user saw.
 */
function AdminMessageRow({ message, modelName }: { message: Message; modelName: (id?: string) => string }) {
  const { t } = useTranslation('admin')
  const isAssistant = message.role === 'assistant'
  const Icon = isAssistant ? Bot : UserIcon
  return (
    <li className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-5 py-4">
      <header className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <span
            className="inline-flex items-center justify-center w-7 h-7 rounded-full bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]"
            aria-hidden
          >
            <Icon size={13} />
          </span>
          <span className="text-[13px] font-medium text-[var(--color-fg)] capitalize">
            {message.role}
          </span>
          {message.modelId ? (
            <Badge size="xs" variant="neutral">{modelName(message.modelId)}</Badge>
          ) : null}
          {message.refused ? <Badge size="xs" variant="neutral">{t('users.refused')}</Badge> : null}
        </div>
        <div className="text-[11.5px] text-[var(--color-fg-subtle)] font-mono">
          {formatStamp(message.createdAt)}
        </div>
      </header>

      {/* Same unified reasoning trace the chat surface uses (read-only here). */}
      <ReasoningTrace reasoning={message.reasoning} />

      {message.content ? (
        <div className="mt-2">
          {isAssistant ? (
            <Markdown content={message.content} />
          ) : (
            <p className="whitespace-pre-wrap text-[14px] text-[var(--color-fg)]">{message.content}</p>
          )}
        </div>
      ) : null}

      {message.citations && message.citations.length > 0 ? (
        <div className="mt-3">
          <CitationList citations={message.citations} />
        </div>
      ) : null}

      {message.artifacts && message.artifacts.length > 0 ? (
        <ul className="mt-3 flex flex-wrap gap-2">
          {message.artifacts.map((a) => (
            <li
              key={a.id}
              className="text-[12px] rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-2.5 py-1 text-[var(--color-fg-muted)]"
            >
              {a.filename}
            </li>
          ))}
        </ul>
      ) : null}
    </li>
  )
}
