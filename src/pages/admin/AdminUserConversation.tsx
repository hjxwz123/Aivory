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
import { ArrowLeft, Bot, FileText } from 'lucide-react'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { initials } from '@/components/ui/avatar.utils'
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
  const modelMeta = useMemo(() => {
    const byId = new Map(models.map((m) => [m.id, m]))
    return (mid?: string) => {
      const m = mid ? byId.get(mid) : undefined
      return { label: m?.label ?? mid ?? '', icon: m?.icon ?? '' }
    }
  }, [models])
  const modelName = (mid?: string) => modelMeta(mid).label
  const convUser = useMemo(() => users.find((x) => x.id === (conv?.user_id || id)), [users, conv, id])
  const userLabel = convUser?.name || convUser?.email || ''
  const userAvatar = (convUser?.settings as Record<string, unknown> | undefined)?.avatar_url as string | undefined

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
              <AdminMessageRow
                key={m.id}
                message={m}
                modelMeta={modelMeta}
                userLabel={userLabel}
                userAvatar={userAvatar}
              />
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
function AdminMessageRow({
  message,
  modelMeta,
  userLabel,
  userAvatar,
}: {
  message: Message
  modelMeta: (id?: string) => { label: string; icon: string }
  userLabel: string
  userAvatar?: string
}) {
  const { t } = useTranslation('admin')
  const isAssistant = message.role === 'assistant'
  // Assistant → model name + icon; user → their nickname + avatar. No "user/assistant" text.
  const m = isAssistant ? modelMeta(message.modelId) : null
  const title = isAssistant ? m?.label || t('users.untitledConversation', { defaultValue: 'Assistant' }) : userLabel || 'User'
  const iconUrl = isAssistant ? m?.icon : userAvatar
  return (
    <li className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-5 py-4">
      <header className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <Avatar size="sm" tone={isAssistant ? 'sage' : 'clay'}>
            {iconUrl ? <AvatarImage src={iconUrl} alt={title} /> : null}
            <AvatarFallback>
              {isAssistant ? <Bot size={13} aria-hidden /> : initials(title)}
            </AvatarFallback>
          </Avatar>
          <span className="text-[13px] font-medium text-[var(--color-fg)]">{title}</span>
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

      {/* User uploads (images shown inline, other files as openable chips). */}
      {message.attachments && message.attachments.length > 0 ? (
        <div className="mt-3 flex flex-wrap gap-2">
          {message.attachments.map((a) =>
            a.kind === 'image' && a.previewUrl ? (
              <a key={a.id} href={a.previewUrl} target="_blank" rel="noreferrer" className="block">
                <img
                  src={a.previewUrl}
                  alt={a.name}
                  className="size-24 rounded-[10px] border border-[var(--color-border-subtle)] object-cover"
                />
              </a>
            ) : (
              <a
                key={a.id}
                href={a.previewUrl ?? '#'}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1.5 text-[12px] rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-2.5 py-1.5 text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] interactive"
              >
                <FileText size={13} aria-hidden /> {a.name}
              </a>
            ),
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
            <li key={a.id}>
              <a
                href={a.url ?? `/api/artifacts/${a.id}`}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1.5 text-[12px] rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-2.5 py-1.5 text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] interactive"
              >
                <FileText size={13} aria-hidden /> {a.filename}
              </a>
            </li>
          ))}
        </ul>
      ) : null}
    </li>
  )
}
