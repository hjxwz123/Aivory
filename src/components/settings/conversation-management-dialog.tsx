import { useCallback, useEffect, useMemo, useRef, useState, type RefObject } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ArchiveRestore, ChevronLeft, ChevronRight, Copy, ExternalLink, RefreshCw, Trash2, Unlink } from 'lucide-react'
import { apiUrl, conversationsApi } from '@/api'
import { userLinksApi } from '@/api/user-links'
import { useAuth } from '@/store/auth'
import { useConversations } from '@/store/conversations'
import { useSettingsModal } from '@/store/settings-modal'
import { useLanguage } from '@/store/language'
import { userCan } from '@/lib/user-permissions'
import { copyText } from '@/lib/utils'
import { publicServerUrl } from '@/lib/server-url'
import { toast } from '@/hooks/use-toast'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import { SettingsBlock, SettingsRow, SettingsSection } from '@/components/settings/settings-section'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

type View = 'archived' | 'shared' | 'html'
type Entry = { id: string; title: string; createdAt: number; url?: string }
type Confirmation = { row: Entry; action: 'delete' | 'revoke' }
const PAGE_SIZE = 20

export function ConversationManagementDialog({ open, mode, onOpenChange, returnFocus }: {
  open: boolean
  mode: 'archived' | 'links'
  onOpenChange: (open: boolean) => void
  returnFocus?: RefObject<HTMLButtonElement | null>
}) {
  const { t } = useTranslation('settings')
  const userId = useAuth((state) => state.user?.id)
  return (
    <Dialog open={open && Boolean(userId)} onOpenChange={onOpenChange}>
      <DialogContent size="lg" className="min-h-[min(18rem,calc(100dvh-2rem))] max-h-[min(34rem,calc(100dvh-2rem))]" onCloseAutoFocus={(event) => {
        event.preventDefault()
        const settings = useSettingsModal.getState()
        if (settings.open && settings.tab === 'privacy') returnFocus?.current?.focus()
      }}>
        <DialogHeader className="pr-14">
          <DialogTitle>{t(`privacy.management.${mode === 'archived' ? 'archivedTitle' : 'linksTitle'}`)}</DialogTitle>
          <DialogDescription>{t(`privacy.management.${mode === 'archived' ? 'archivedDescription' : 'linksDescription'}`)}</DialogDescription>
        </DialogHeader>
        <DialogBody>
          {open && userId ? (
            <ConversationManager key={`${userId}:${mode}`} userId={userId} mode={mode} onClose={() => onOpenChange(false)} />
          ) : null}
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}

function ConversationManager({ userId, mode, onClose }: { userId: string; mode: 'archived' | 'links'; onClose: () => void }) {
  const { t } = useTranslation('settings')
  const [revision, setRevision] = useState(0)
  const onConversationDeleted = useCallback(() => setRevision((value) => value + 1), [])

  return (
    <div className="min-w-0">
      {mode === 'archived' ? (
        <ConversationSection userId={userId} view="archived" revision={revision} onConversationDeleted={onConversationDeleted} onClose={onClose} />
      ) : (
        <Tabs defaultValue="shared">
          <TabsList variant="segmented" className="w-full sm:w-auto" aria-label={t('privacy.management.linksTitle')}>
            <TabsTrigger variant="segmented" value="shared" className="h-auto min-h-8 flex-1 justify-center whitespace-normal py-1.5 sm:flex-none">{t('privacy.management.conversationLinks')}</TabsTrigger>
            <TabsTrigger variant="segmented" value="html" className="h-auto min-h-8 flex-1 justify-center whitespace-normal py-1.5 sm:flex-none">{t('privacy.management.htmlLinks')}</TabsTrigger>
          </TabsList>
          {(['shared', 'html'] as const).map((view) => (
            <TabsContent key={view} value={view}>
              <ConversationSection userId={userId} view={view} revision={revision} onConversationDeleted={onConversationDeleted} onClose={onClose} />
            </TabsContent>
          ))}
        </Tabs>
      )}
    </div>
  )
}

function ConversationSection({ userId, view, revision, onConversationDeleted, onClose }: {
  userId: string
  view: View
  revision: number
  onConversationDeleted: () => void
  onClose: () => void
}) {
  const { t } = useTranslation(['settings', 'chat', 'common'])
  const navigate = useNavigate()
  const user = useAuth((state) => state.user)
  const lang = useLanguage((state) => state.lang)
  const dateFormat = useMemo(() => new Intl.DateTimeFormat(lang, { dateStyle: 'medium' }), [lang])
  const [page, setPage] = useState(0)
  const [version, setVersion] = useState(0)
  const [resource, setResource] = useState<{ key: string; rows: Entry[]; hasMore: boolean }>({ key: '', rows: [], hasMore: false })
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading')
  const [busy, setBusy] = useState<string | null>(null)
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null)
  const requestRef = useRef(0)
  const busyRef = useRef(false)
  const mountedRef = useRef(true)
  const key = `${view}:${page}`
  const rows = resource.key === key ? resource.rows : []
  const loading = status === 'loading' || resource.key !== key

  useEffect(() => {
    mountedRef.current = true
    return () => { mountedRef.current = false }
  }, [])

  useEffect(() => {
    const request = ++requestRef.current
    setStatus('loading')
    const stillCurrent = () => requestRef.current === request && useAuth.getState().user?.id === userId
    const load = async () => {
      try {
        let entries: Entry[]
        let hasMore: boolean
        if (view === 'archived') {
          const result = await conversationsApi.listArchived(PAGE_SIZE + 1, page * PAGE_SIZE)
          entries = result.conversations.slice(0, PAGE_SIZE).map((row) => ({ id: row.id, title: row.title, createdAt: row.updated_at }))
          hasMore = result.conversations.length > PAGE_SIZE
        } else {
          const result = view === 'shared'
            ? await userLinksApi.conversations(PAGE_SIZE, page * PAGE_SIZE)
            : await userLinksApi.htmlPreviews(PAGE_SIZE, page * PAGE_SIZE)
          entries = result.items.map((row) => ({
            id: row.id, title: row.title || row.id, createdAt: row.created_at,
            url: publicServerUrl(view === 'shared' ? `/share/${encodeURIComponent(row.id)}` : apiUrl(`/public/html-previews/${encodeURIComponent(row.id)}`)),
          }))
          hasMore = result.has_more
        }
        if (!stillCurrent()) return
        if (entries.length === 0 && page > 0) {
          setPage(page - 1)
          return
        }
        setResource({ key, rows: entries, hasMore })
        setStatus('ready')
      } catch {
        if (!stillCurrent()) return
        setResource({ key, rows: [], hasMore: false })
        setStatus('error')
      }
    }
    void load()
    return () => { requestRef.current += 1 }
  }, [key, page, revision, userId, version, view])

  async function mutate(row: Entry, action: 'restore' | 'delete' | 'revoke') {
    if (busyRef.current) return
    busyRef.current = true
    setBusy(row.id)
    try {
      if (action === 'restore') {
        await conversationsApi.update(row.id, { archived: false })
        await useConversations.getState().load()
      } else if (action === 'delete') {
        if (!await useConversations.getState().deleteConversation(row.id)) return
      } else if (view === 'shared') {
        await userLinksApi.revokeConversation(row.id)
      } else {
        await userLinksApi.revokeHTMLPreview(row.id)
      }
      if (!mountedRef.current || useAuth.getState().user?.id !== userId) return
      setConfirmation(null)
      if (action === 'delete') onConversationDeleted()
      else setVersion((value) => value + 1)
      toast.success(t(`settings:conversations.${action === 'restore' ? 'restored' : action === 'delete' ? 'deleted' : 'revoked'}`))
    } catch (error) {
      if (mountedRef.current && useAuth.getState().user?.id === userId) toast.error(t('settings:conversations.actionFailed'), error instanceof Error ? error.message : undefined)
    } finally {
      busyRef.current = false
      if (mountedRef.current) setBusy(null)
    }
  }

  async function copyLink(row: Entry) {
    if (!row.url) return
    const copied = await copyText(row.url)
    toast[copied ? 'success' : 'error'](t(`settings:conversations.${copied ? 'copied' : 'copyFailed'}`))
  }

  const canDelete = userCan(user, 'allow_conversation_deletion')
  const canShare = userCan(user, 'allow_sharing')

  return (
    <SettingsSection
      id={`settings-conversations-${view}`}
      actions={
        <Button
          variant="secondary"
          leadingIcon={<RefreshCw size={13} aria-hidden />}
          disabled={loading || Boolean(busy)}
          aria-label={t('settings:conversations.refresh')}
          onClick={() => setVersion((value) => value + 1)}
        >
          {t('settings:conversations.refreshLabel')}
        </Button>
      }
    >
      <div data-conversation-view={view} aria-busy={loading || Boolean(busy) || undefined}>
        {confirmation && (
          <div className="mb-3 rounded-[10px] bg-[var(--color-bg-muted)] px-4 py-4" role="alert">
            <p className="break-words text-sm leading-relaxed">
              {t(`settings:conversations.${confirmation.action === 'delete' ? 'deleteConfirm' : 'revokeConfirm'}`, { title: confirmation.row.title })}
            </p>
            <div className="mt-3 flex flex-wrap justify-end gap-2">
              <Button size="sm" variant="ghost" disabled={Boolean(busy)} onClick={() => setConfirmation(null)}>
                {t('common:actions.cancel')}
              </Button>
              <Button size="sm" variant="destructive" loading={busy === confirmation.row.id} onClick={() => void mutate(confirmation.row, confirmation.action)}>
                {t(`settings:conversations.${confirmation.action}`)}
              </Button>
            </div>
          </div>
        )}
        {view !== 'archived' && !canShare && (
          <SettingsBlock>
            <p className="text-sm text-[var(--color-fg-muted)]">{t('settings:conversations.sharingDisabled')}</p>
          </SettingsBlock>
        )}
        {loading ? (
          <SettingsBlock>
            <div role="status" aria-label={t('common:aria.loading')} className="space-y-5">
              {[0, 1, 2].map((index) => (
                <div key={index}>
                  <Skeleton className="h-4 w-3/4" />
                  <Skeleton className="mt-2 h-3 w-1/2" />
                </div>
              ))}
            </div>
          </SettingsBlock>
        ) : status === 'error' ? (
          <SettingsBlock>
            <div className="flex flex-wrap items-center justify-between gap-3 text-sm text-[var(--color-fg-muted)]" role="alert">
              <p>{t('settings:conversations.loadFailed')}</p>
              <Button size="sm" variant="secondary" onClick={() => setVersion((value) => value + 1)}>
                {t('common:actions.tryAgain')}
              </Button>
            </div>
          </SettingsBlock>
        ) : rows.length === 0 ? (
          <SettingsBlock>
            <p className="text-sm text-[var(--color-fg-muted)]">{t(`settings:conversations.empty.${view}`)}</p>
          </SettingsBlock>
        ) : (
          <ul>
            {rows.map((row) => (
              <li key={row.id}>
                <SettingsRow
                  label={view === 'archived' ? (
                    <button
                      type="button"
                      className="block max-w-full truncate rounded-[4px] text-left hover:text-[var(--color-accent)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                      title={row.title}
                      onClick={() => {
                        onClose()
                        useSettingsModal.getState().close()
                        navigate(`/chat/${encodeURIComponent(row.id)}`)
                      }}
                    >
                      {row.title || t('chat:share.untitled')}
                    </button>
                  ) : <span className="block truncate" title={row.title}>{row.title}</span>}
                  description={
                    <>
                      <span className="block">{dateFormat.format(new Date(row.createdAt * 1000))}</span>
                      {row.url && <span className="mt-0.5 block truncate" title={row.url}>{row.url}</span>}
                    </>
                  }
                >
                  <div className="flex items-center justify-end gap-1">
                    {view === 'archived' ? (
                      <>
                        <Button
                          variant="secondary"
                          leadingIcon={<ArchiveRestore size={13} aria-hidden />}
                          disabled={Boolean(busy)}
                          aria-label={t('settings:conversations.restore')}
                          onClick={() => void mutate(row, 'restore')}
                        >
                          {t('settings:conversations.restore')}
                        </Button>
                        {canDelete && (
                          <Tooltip content={t('common:actions.delete')}>
                            <Button
                              size="icon-sm"
                              variant="ghost"
                              className="text-[var(--color-fg-muted)] hover:text-[var(--color-danger)] max-sm:size-11"
                              disabled={Boolean(busy)}
                              aria-label={t('common:actions.delete')}
                              onClick={() => setConfirmation({ row, action: 'delete' })}
                            >
                              <Trash2 size={15} aria-hidden />
                            </Button>
                          </Tooltip>
                        )}
                      </>
                    ) : (
                      <>
                        <Tooltip content={t('settings:conversations.openLink')}><Button asChild size="icon-sm" variant="ghost" className="max-sm:size-11"><a href={row.url} target="_blank" rel="noopener noreferrer" aria-label={t('settings:conversations.openLink')}><ExternalLink size={15} aria-hidden /></a></Button></Tooltip>
                        <Tooltip content={t('settings:conversations.copyLink')}><Button size="icon-sm" variant="ghost" className="max-sm:size-11" aria-label={t('settings:conversations.copyLink')} onClick={() => void copyLink(row)}><Copy size={15} aria-hidden /></Button></Tooltip>
                        <Tooltip content={t('settings:conversations.revoke')}><Button size="icon-sm" variant="ghost" className="text-[var(--color-fg-muted)] hover:text-[var(--color-danger)] max-sm:size-11" disabled={Boolean(busy)} aria-label={t('settings:conversations.revoke')} onClick={() => setConfirmation({ row, action: 'revoke' })}><Unlink size={15} aria-hidden /></Button></Tooltip>
                      </>
                    )}
                  </div>
                </SettingsRow>
              </li>
            ))}
          </ul>
        )}
        {(page > 0 || resource.hasMore) && !loading && status === 'ready' && (
          <div className="flex items-center justify-center gap-3 px-4 py-3">
            <Button size="icon-sm" variant="ghost" className="max-sm:size-11" disabled={page === 0 || Boolean(busy)} aria-label={t('common:pagination.prev')} onClick={() => setPage((value) => value - 1)}>
              <ChevronLeft size={15} aria-hidden />
            </Button>
            <span className="text-xs tabular-nums text-[var(--color-fg-muted)]">{page + 1}</span>
            <Button size="icon-sm" variant="ghost" className="max-sm:size-11" disabled={!resource.hasMore || Boolean(busy)} aria-label={t('common:pagination.next')} onClick={() => setPage((value) => value + 1)}>
              <ChevronRight size={15} aria-hidden />
            </Button>
          </div>
        )}
      </div>
    </SettingsSection>
  )
}
