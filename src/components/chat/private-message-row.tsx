/*
 * PrivateMessageRow — the private-chat row that visually mirrors message-row.tsx
 * (bubble geometry, headers, action bar, error card) while honoring the private
 * contract from docs/private-chat.md: assistant text renders through
 * PrivateMarkdown (no remote images / HTML / Mermaid), images are in-memory
 * data: URLs, and every action (copy / regenerate / edit-resend) operates on
 * React state only — nothing here may call server APIs.
 */
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Check, Copy, MoreHorizontal, Pencil, RefreshCw, Square } from 'lucide-react'
import type { ApiModel } from '@/api/types'
import { ModelIcon } from '@/components/chat/model-icon'
import { PrivateMarkdown } from '@/components/chat/private-markdown'
import { ReasoningTrace } from '@/components/chat/reasoning-trace'
import { ImageLightbox } from '@/components/chat/image-lightbox'
import { LogoMark } from '@/components/brand/logo'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { Sheet, SheetContent } from '@/components/ui/sheet'
import { Tooltip } from '@/components/ui/tooltip'
import { useCopy } from '@/hooks/use-clipboard'
import { useMediaQuery } from '@/hooks/use-media-query'
import { mediaQuery } from '@/lib/design-tokens'
import { privateImageURL, type PrivateImage } from '@/lib/private-chat'
import { cn } from '@/lib/utils'
import type { ReasoningItem } from '@/types/chat'

export interface PrivateDisplayMessage {
  id: number
  role: 'user' | 'assistant'
  text: string
  reasoning?: string
  generatedImages?: string[]
  images?: PrivateImage[]
  createdAt: number
  streaming?: boolean
  stopped?: boolean
  error?: string
}

interface PrivateMessageRowProps {
  message: PrivateDisplayMessage
  /** Model that produced an assistant turn (icon + label in the header). */
  model?: ApiModel
  /** Only the newest assistant answer can be regenerated in-memory. */
  isLastAssistant?: boolean
  /** A stream/attachment read is in flight — row actions must wait. */
  locked?: boolean
  onRegenerate?: () => void
  onEdit?: (text: string) => void
}

function formatTurnTime(timestamp: number, locale: string): { label: string; iso: string } {
  const formatter = new Intl.DateTimeFormat(locale || undefined, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
  return { label: formatter.format(timestamp), iso: new Date(timestamp).toISOString() }
}

/** Mirrors message-row.tsx's ThinkingLogo (the pre-token brand breathing mark). */
function ThinkingLogo() {
  return (
    <div
      className="relative grid size-11 cursor-default select-none place-items-center caret-transparent"
      aria-hidden
      onMouseDown={(event) => event.preventDefault()}
    >
      <span className="absolute inset-0 rounded-full border border-[var(--color-border)] [border-top-color:var(--color-secondary)] animate-[spin_1200ms_cubic-bezier(0.6,0.1,0.4,0.9)_infinite]" />
      <LogoMark size={24} className="animate-[core-breathe_2400ms_ease-in-out_infinite]" />
    </div>
  )
}

/** Mirrors message-row.tsx's MsgActionRow (the phone Sheet action rows). */
function MsgActionRow({
  icon,
  label,
  onClick,
  active,
  disabled = false,
}: {
  icon: ReactNode
  label: string
  onClick: () => void
  active?: boolean
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      aria-pressed={active}
      className={cn(
        'flex w-full items-center gap-3 min-h-[var(--tap-min)] px-3 text-left text-[15px] rounded-[10px] interactive',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
        'disabled:pointer-events-none disabled:opacity-50',
        active
          ? 'text-[var(--color-accent)] bg-[var(--color-accent-soft)]'
          : 'text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)]',
      )}
    >
      <span className={cn('shrink-0', active ? 'text-[var(--color-accent)]' : 'text-[var(--color-fg-muted)]')}>
        {icon}
      </span>
      <span className="truncate">{label}</span>
    </button>
  )
}

const actionButtonClass =
  'inline-flex items-center justify-center size-7 max-sm:size-9 rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]'

export function PrivateMessageRow({ message, model, isLastAssistant, locked, onRegenerate, onEdit }: PrivateMessageRowProps) {
  const { t, i18n } = useTranslation('chat')
  const isUser = message.role === 'user'
  const isPhone = useMediaQuery(mediaQuery.phone)
  const { copied, copy } = useCopy()
  const [hovered, setHovered] = useState(false)
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(message.text)
  const [actionSheetOpen, setActionSheetOpen] = useState(false)
  const [lightbox, setLightbox] = useState<{ src: string; alt?: string } | null>(null)
  const turnTime = formatTurnTime(message.createdAt, i18n.language)
  const canEdit = isUser && !locked && !message.streaming && Boolean(onEdit)
  const canRegenerate = !isUser && isLastAssistant && !locked && !message.streaming && Boolean(onRegenerate)
  const hasActions = Boolean(message.text.trim()) || canEdit || canRegenerate
  const reasoningItems: ReasoningItem[] | undefined = message.reasoning
    ? [{ kind: 'thinking', id: `private-${message.id}-thinking`, text: message.reasoning }]
    : undefined
  const errorText = message.error ? t(`private.errors.${message.error}`, { defaultValue: t('private.errors.private_provider_error') }) : ''

  function startEditing() {
    setDraft(message.text)
    setEditing(true)
  }

  const copyAction = message.text.trim() ? (
    <Tooltip content={copied ? t('actions.copied') : t('actions.copy')}>
      <button
        type="button"
        onClick={() => void copy(message.text)}
        aria-label={t('actions.copy')}
        className={actionButtonClass}
      >
        {copied ? <Check size={13} aria-hidden /> : <Copy size={13} aria-hidden />}
      </button>
    </Tooltip>
  ) : null
  const editAction = canEdit ? (
    <Tooltip content={t('actions.edit')}>
      <button
        type="button"
        onClick={startEditing}
        aria-label={t('actions.edit')}
        className={actionButtonClass}
      >
        <Pencil size={13} aria-hidden />
      </button>
    </Tooltip>
  ) : null
  const regenerateAction = canRegenerate ? (
    <Tooltip content={t('actions.regenerate')}>
      <button
        type="button"
        onClick={() => onRegenerate?.()}
        aria-label={t('actions.regenerate')}
        className={actionButtonClass}
      >
        <RefreshCw size={13} aria-hidden />
      </button>
    </Tooltip>
  ) : null

  return (
    <div
      data-message-id={message.id}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      className={cn(
        'group/msg w-full flex animate-[message-in_220ms_var(--ease-out)_both]',
        isUser ? 'justify-end' : 'justify-start',
      )}
    >
      <div
        className={cn(
          'flex w-full min-w-0 flex-col [container-type:inline-size]',
          isUser && !editing ? 'items-end max-w-[88%] sm:max-w-[68%]' : 'items-start w-full',
        )}
      >
        {!isUser && (
          <div className="flex items-center gap-2 mb-2">
            {model ? <ModelIcon icon={model.icon} size={20} /> : null}
            <span className="font-medium text-[15px] text-[var(--color-fg)]">{model?.label ?? t('private.assistant')}</span>
            <time
              dateTime={turnTime.iso}
              className="max-sm:hidden text-[11px] text-[var(--color-fg-subtle)] tabular-nums opacity-0 transition-opacity duration-[140ms] ease-out group-hover/msg:opacity-100 group-focus-within/msg:opacity-100"
            >
              {turnTime.label}
            </time>
            {message.streaming ? (
              <span className="thinking-shimmer ml-1 text-[11px] font-medium tracking-[0.04em]">
                {t('thinking')}…
              </span>
            ) : null}
          </div>
        )}

        {editing ? (
          // Full-width edit well mirroring message-row.tsx's user edit surface.
          <div className="w-full rounded-[18px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-4 py-3.5 transition-colors focus-within:border-[var(--color-border-strong)]">
            {message.images?.length ? (
              <div className="mb-3 flex flex-wrap gap-2">
                {message.images.map((image, index) => (
                  <img
                    key={index}
                    src={privateImageURL(image)}
                    alt={t('private.image', { index: index + 1 })}
                    className="size-16 rounded-[10px] border border-[var(--color-border-subtle)] object-cover"
                    draggable={false}
                  />
                ))}
              </div>
            ) : null}
            <Textarea
              autoFocus
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Escape' && !locked) setEditing(false)
                if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
                  event.preventDefault()
                  if (draft.trim()) {
                    setEditing(false)
                    onEdit?.(draft.trim())
                  }
                }
              }}
              readOnly={locked}
              spellCheck={false}
              aria-label={t('actions.edit')}
              className="min-h-24 resize-y bg-[var(--color-surface-sunken)] text-[0.9375rem] leading-relaxed"
            />
            <div className="mt-2.5 flex flex-wrap justify-end gap-2 max-sm:[&>button]:min-h-11">
              <Button size="sm" variant="ghost" disabled={locked} onClick={() => setEditing(false)}>
                {t('actions.cancelEdit', { defaultValue: 'Cancel' })}
              </Button>
              <Button
                size="sm"
                variant="primary"
                disabled={locked || !draft.trim()}
                onClick={() => {
                  setEditing(false)
                  onEdit?.(draft.trim())
                }}
              >
                {t('actions.saveEdit', { defaultValue: 'Save & resend' })}
              </Button>
            </div>
          </div>
        ) : isUser ? (
          <div
            className={cn(
              'w-fit min-w-0 max-w-full overflow-hidden rounded-[18px] px-4 py-2.5',
              'bg-[var(--color-user-bubble)] border border-[var(--color-user-bubble-border)]',
              'text-[var(--color-fg)] text-[length:var(--text-chat-body)] leading-relaxed',
              'whitespace-pre-wrap break-words',
            )}
          >
            {message.images?.length ? (
              <div className="mb-2 grid min-w-0 gap-2">
                <div
                  data-image-attachment-grid={message.images.length > 1 ? 'multiple' : 'single'}
                  className={cn(
                    message.images.length === 1
                      ? 'flex min-w-0 max-w-full'
                      : 'grid min-w-0 max-w-full grid-cols-[repeat(auto-fit,minmax(min(6rem,100%),1fr))] gap-2',
                    message.images.length === 2 && 'w-[min(14.5rem,calc(100cqw-2.125rem))]',
                    message.images.length >= 3 && 'w-[min(22rem,calc(100cqw-2.125rem))]',
                    'ml-auto',
                  )}
                >
                  {message.images.map((image, index) => (
                    <button
                      key={index}
                      type="button"
                      onClick={() => setLightbox({ src: privateImageURL(image), alt: t('private.image', { index: index + 1 }) })}
                      aria-label={t('actions.viewImage', { defaultValue: 'View image' })}
                      className={cn(
                        'block min-w-0 max-w-full overflow-hidden rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] hover:opacity-90',
                        message.images!.length === 1 ? 'shrink-0' : 'aspect-square w-full',
                      )}
                    >
                      <img
                        src={privateImageURL(image)}
                        alt={t('private.image', { index: index + 1 })}
                        className={cn(
                          'object-cover',
                          message.images!.length === 1
                            ? 'h-auto max-h-56 w-auto max-w-[min(100%,18rem)] sm:max-w-[min(100%,22rem)]'
                            : 'size-full',
                        )}
                        draggable={false}
                      />
                    </button>
                  ))}
                </div>
              </div>
            ) : null}
            {message.text}
          </div>
        ) : (
          <div className="w-full text-[var(--color-fg)]">
            <ReasoningTrace
              reasoning={reasoningItems}
              streaming={message.streaming}
              settled={Boolean(message.text)}
            />
            {message.streaming && !message.text && !message.reasoning ? (
              <div className="py-1">
                <ThinkingLogo />
              </div>
            ) : message.stopped && !message.text && !message.error ? (
              <div role="status" className="my-1 inline-flex items-center gap-2 text-sm text-[var(--color-fg-muted)]">
                <Square size={11} className="shrink-0 fill-current" aria-hidden />
                <span>{t('message.stopped', { defaultValue: 'Generation stopped.' })}</span>
              </div>
            ) : message.text ? (
              <>
                <PrivateMarkdown text={message.text} />
                {message.streaming ? (
                  <span
                    aria-hidden
                    className="inline-block align-text-bottom w-[2px] h-[1.05em] bg-[var(--color-accent)] ml-0.5 animate-[fade-in_400ms_ease-in-out_infinite_alternate]"
                  />
                ) : null}
              </>
            ) : null}
            {message.generatedImages?.length ? (
              <div className="mt-3 flex flex-wrap gap-2">
                {message.generatedImages.map((image, index) => (
                  <button
                    key={index}
                    type="button"
                    onClick={() => setLightbox({ src: image, alt: t('private.image', { index: index + 1 }) })}
                    aria-label={t('actions.viewImage', { defaultValue: 'View image' })}
                    className="block overflow-hidden rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    <img
                      src={image}
                      alt={t('private.image', { index: index + 1 })}
                      className="max-h-64 rounded-lg border border-[var(--color-border)] transition-opacity hover:opacity-90"
                    />
                  </button>
                ))}
              </div>
            ) : null}
            {message.error && !message.streaming ? (
              <div
                role="alert"
                className="mt-2 rounded-xl border border-[var(--color-danger)] bg-[var(--color-danger-soft)] px-4 py-3"
              >
                <div className="flex items-center gap-2 text-[var(--color-danger)] font-medium text-sm">
                  <AlertTriangle size={16} aria-hidden />
                  {t('message.error.title')}
                </div>
                <p className="mt-1 text-[12.5px] text-[var(--color-fg-subtle)] break-words">{errorText}</p>
                {canRegenerate ? (
                  <button
                    type="button"
                    onClick={() => onRegenerate?.()}
                    className="mt-2.5 inline-flex items-center gap-1.5 h-8 px-3 rounded-[9px] text-sm font-medium bg-[var(--color-danger)] text-[var(--color-fg-inverted)] interactive hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    <RefreshCw size={13} aria-hidden />
                    {t('message.error.retry')}
                  </button>
                ) : null}
              </div>
            ) : null}
          </div>
        )}

        {/* Actions — mirrored from message-row.tsx: always laid out after
            streaming settles, revealed on hover via opacity (never reflow). */}
        {!editing && !message.streaming && hasActions ? (
          isPhone ? (
            <div className="mt-1.5 flex items-center gap-2">
              <button
                type="button"
                onClick={() => setActionSheetOpen(true)}
                aria-label={t('actions.more')}
                className="inline-flex items-center justify-center size-[var(--tap-min)] -ml-2 rounded-[10px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                <MoreHorizontal size={18} aria-hidden />
              </button>
            </div>
          ) : (
            <div
              className={cn(
                'mt-2 inline-flex items-center gap-0.5 transition-opacity duration-[140ms] ease-out focus-within:opacity-100',
                hovered ? 'opacity-100' : 'opacity-0 pointer-events-none',
              )}
            >
              {copyAction}
              {editAction}
              {regenerateAction}
            </div>
          )
        ) : null}
      </div>

      {isPhone && !editing && !message.streaming && hasActions ? (
        <Sheet open={actionSheetOpen} onOpenChange={setActionSheetOpen}>
          <SheetContent side="bottom" size="sm" label={t('actions.more')} className="h-auto max-h-[85dvh]">
            <div className="flex flex-col px-2 py-2">
              {message.text.trim() ? (
                <MsgActionRow
                  icon={copied ? <Check size={18} aria-hidden /> : <Copy size={18} aria-hidden />}
                  label={copied ? t('actions.copied') : t('actions.copy')}
                  onClick={() => void copy(message.text)}
                />
              ) : null}
              {canEdit ? (
                <MsgActionRow
                  icon={<Pencil size={18} aria-hidden />}
                  label={t('actions.edit')}
                  onClick={() => {
                    setActionSheetOpen(false)
                    startEditing()
                  }}
                />
              ) : null}
              {canRegenerate ? (
                <MsgActionRow
                  icon={<RefreshCw size={18} aria-hidden />}
                  label={t('actions.regenerate')}
                  onClick={() => {
                    setActionSheetOpen(false)
                    onRegenerate?.()
                  }}
                />
              ) : null}
            </div>
          </SheetContent>
        </Sheet>
      ) : null}

      <ImageLightbox
        open={lightbox !== null}
        onOpenChange={(open) => !open && setLightbox(null)}
        src={lightbox?.src ?? ''}
        alt={lightbox?.alt}
        downloadUrl={lightbox?.src}
        filename={lightbox?.alt}
      />
    </div>
  )
}
