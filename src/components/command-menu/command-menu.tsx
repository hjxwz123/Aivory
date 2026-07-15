import { activeWorkspaceId } from '@/store/workspaces'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  MessageSquare,
  Plus,
  Settings,
  Search,
  Sun,
  Moon,
  Monitor,
  Sparkles,
  ArrowRight,
  HelpCircle,
  Languages,
  FolderKanban,
  TextSearch,
} from 'lucide-react'
import * as DialogPrimitive from '@radix-ui/react-dialog'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
  CommandShortcut,
} from '@/components/ui/command'
import { NewProjectDialog } from '@/components/projects/new-project-dialog'
import { useCommandMenu } from '@/hooks/use-command-menu'
import { useOpenSettings } from '@/hooks/use-open-settings'
import { searchApi, type SearchHit } from '@/api'
import { useConversations, sameConvListShape } from '@/store/conversations'
import { resetComposerToolModeToDefault } from '@/store/composer-prefs'
import { useProjects } from '@/store/projects'
import { useTheme } from '@/store/theme'
import { useSettings } from '@/store/settings'
import { useLanguage } from '@/store/language'
import { SUPPORTED_LANGUAGES } from '@/i18n'
import { truncate, modKey } from '@/lib/utils'
import { cn } from '@/lib/utils'

export function CommandMenu() {
  const open = useCommandMenu((s) => s.open)
  const setOpen = useCommandMenu((s) => s.setOpen)
  const navigate = useNavigate()
  const openSettings = useOpenSettings()
  const { t } = useTranslation(['chat', 'projects'])
  // Summary-only subscription (see sidebar): don't re-render per streamed token.
  const allConversations = useConversations((s) => s.conversations, sameConvListShape)
  const conversations = useMemo(
    () => allConversations.filter((c) => !c.archived && !c.inline),
    [allConversations],
  )
  const projects = useProjects((s) => s.projects)
  const recentProjects = useMemo(
    () =>
      projects
        .slice()
        .sort((a, b) => {
          if ((a.pinned ? 1 : 0) !== (b.pinned ? 1 : 0)) return a.pinned ? -1 : 1
          return b.updatedAt - a.updatedAt
        })
        .slice(0, 5),
    [projects],
  )
  const setTheme = useTheme((s) => s.setPref)
  const toggleSidebar = useSettings((s) => s.toggleSidebar)
  const currentLang = useLanguage((s) => s.lang)
  const setLang = useLanguage((s) => s.setLang)
  const [newProjectOpen, setNewProjectOpen] = useState(false)

  // ── Content search ────────────────────────────────────────────────────────
  // The typed query, debounced, drives a backend search over message CONTENT
  // (not just titles) so a word that only appears deep inside a conversation is
  // still findable. Title matches still come from cmdk's local filter above.
  const [query, setQuery] = useState('')
  const [msgHits, setMsgHits] = useState<SearchHit[]>([])
  // Backend title hits cover conversations not in the local cache (a heavy user
  // has only the first page loaded), which cmdk's local title filter can't see.
  const [titleHits, setTitleHits] = useState<SearchHit[]>([])
  const [searching, setSearching] = useState(false)
  const seq = useRef(0)
  useEffect(() => {
    const q = query.trim()
    if (q.length < 2) {
      setMsgHits([])
      setTitleHits([])
      setSearching(false)
      return
    }
    const mine = ++seq.current
    setSearching(true)
    const tmo = setTimeout(() => {
      void searchApi
        .query(q, activeWorkspaceId())
        .then((res) => {
          // Ignore out-of-order responses (a newer keystroke already fired).
          if (mine !== seq.current) return
          setMsgHits(res.messages)
          setTitleHits(res.titles)
          setSearching(false)
        })
        .catch(() => {
          if (mine !== seq.current) return
          setMsgHits([])
          setTitleHits([])
          setSearching(false)
        })
    }, 200)
    return () => clearTimeout(tmo)
  }, [query])
  // Reset the query whenever the menu closes so it reopens clean.
  useEffect(() => {
    if (!open) {
      setQuery('')
      setMsgHits([])
      setTitleHits([])
      setSearching(false)
    }
  }, [open])

  // Title hits for conversations NOT already shown in the local list (dedupe),
  // so >200-conversation users can still find chats by title.
  const extraTitleHits = useMemo(() => {
    if (titleHits.length === 0) return []
    const localIds = new Set(conversations.map((c) => c.id))
    return titleHits.filter((h) => !localIds.has(h.conversation_id))
  }, [titleHits, conversations])

  function run(fn: () => void | Promise<void>) {
    setOpen(false)
    setTimeout(() => {
      void fn()
    }, 60)
  }

  return (
    <>
    <DialogPrimitive.Root open={open} onOpenChange={setOpen}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay
          className={cn(
            'fixed inset-0 z-[70] bg-[var(--color-overlay)] backdrop-blur-[2px]',
            'data-[state=open]:animate-[fade-in_200ms_var(--ease-out)]',
            'data-[state=closed]:animate-[fade-out_140ms_var(--ease-in)]',
          )}
        />
        <DialogPrimitive.Content
          aria-describedby={undefined}
          className={cn(
            'fixed z-[70] bg-[var(--color-surface-raised)] shadow-[var(--shadow-xl)] overflow-hidden focus:outline-none',
            // Desktop: a centered floating palette.
            'left-1/2 top-[18%] -translate-x-1/2 w-[min(92vw,560px)] rounded-[18px] border border-[var(--color-border)]',
            'data-[state=open]:animate-[pop-in_220ms_var(--ease-out)] data-[state=closed]:animate-[fade-out_140ms_var(--ease-in)]',
            // Phone: a full-screen search surface (with safe-area insets).
            'max-sm:inset-0 max-sm:left-0 max-sm:top-0 max-sm:w-full max-sm:translate-x-0 max-sm:rounded-none max-sm:border-0',
            'max-sm:pt-[var(--safe-top)] max-sm:pb-[var(--safe-bottom)] max-sm:data-[state=open]:animate-[fade-in_160ms_var(--ease-out)]',
          )}
        >
          <DialogPrimitive.Title className="sr-only">{t('chat:commandMenu.title')}</DialogPrimitive.Title>
          <Command className="max-sm:h-full">
            <CommandInput
              placeholder={t('chat:commandMenu.placeholder')}
              autoFocus
              value={query}
              onValueChange={setQuery}
              onClose={() => setOpen(false)}
            />
            <CommandList>
              {/* While a content search is in flight, don't flash "No results" —
                  cmdk would otherwise show it during the debounce + request. */}
              {searching ? (
                <div className="px-3 py-10 text-center text-sm text-[var(--color-fg-muted)]">
                  {t('chat:commandMenu.searching', { defaultValue: 'Searching…' })}
                </div>
              ) : (
                <CommandEmpty>{t('chat:commandMenu.noMatch')}</CommandEmpty>
              )}
              <CommandGroup heading={t('chat:commandMenu.groups.actions')}>
                <CommandItem
                  onSelect={() =>
                    run(() => {
                      resetComposerToolModeToDefault()
                      navigate('/')
                    })
                  }
                >
                  <Plus size={14} aria-hidden />
                  {t('chat:commandMenu.actions.newChat')}
                  <CommandShortcut>{modKey()} Shift O</CommandShortcut>
                </CommandItem>
                <CommandItem onSelect={() => run(() => navigate('/chat'))}>
                  <MessageSquare size={14} aria-hidden />
                  {t('chat:commandMenu.actions.goToChat')}
                </CommandItem>
                <CommandItem onSelect={() => run(() => openSettings('account'))}>
                  <Settings size={14} aria-hidden />
                  {t('chat:commandMenu.actions.openSettings')}
                  <CommandShortcut>{modKey()} ,</CommandShortcut>
                </CommandItem>
                <CommandItem onSelect={() => run(() => toggleSidebar())}>
                  <Search size={14} aria-hidden />
                  {t('chat:commandMenu.actions.toggleSidebar')}
                  <CommandShortcut>{modKey()} B</CommandShortcut>
                </CommandItem>
              </CommandGroup>

              <CommandSeparator />

              <CommandGroup heading={t('projects:commandMenu.group')}>
                <CommandItem
                  value="new project"
                  onSelect={() => run(() => setNewProjectOpen(true))}
                >
                  <Plus size={14} aria-hidden />
                  {t('projects:commandMenu.newProject')}
                </CommandItem>
                <CommandItem
                  value="all projects"
                  onSelect={() => run(() => navigate('/projects'))}
                >
                  <FolderKanban size={14} aria-hidden />
                  {t('projects:commandMenu.viewAll')}
                </CommandItem>
                {recentProjects.map((p) => (
                  <CommandItem
                    key={p.id}
                    value={`project ${p.name} ${p.id}`}
                    onSelect={() => run(() => navigate(`/projects/${p.id}`))}
                  >
                    <FolderKanban size={14} className="text-[var(--color-secondary)]" aria-hidden />
                    {t('projects:commandMenu.open', { name: truncate(p.name, 40) })}
                    <ArrowRight size={12} className="ml-auto text-[var(--color-fg-subtle)]" aria-hidden />
                  </CommandItem>
                ))}
              </CommandGroup>

              <CommandSeparator />

              <CommandGroup heading={t('chat:commandMenu.groups.theme')}>
                <CommandItem onSelect={() => run(() => setTheme('light'))}>
                  <Sun size={14} aria-hidden />
                  {t('chat:commandMenu.actions.light')}
                </CommandItem>
                <CommandItem onSelect={() => run(() => setTheme('dark'))}>
                  <Moon size={14} aria-hidden />
                  {t('chat:commandMenu.actions.dark')}
                </CommandItem>
                <CommandItem onSelect={() => run(() => setTheme('system'))}>
                  <Monitor size={14} aria-hidden />
                  {t('chat:commandMenu.actions.system')}
                </CommandItem>
              </CommandGroup>

              <CommandSeparator />

              <CommandGroup heading={t('chat:commandMenu.groups.language')}>
                {SUPPORTED_LANGUAGES.filter((l) => l.code !== currentLang).map((l) => (
                  <CommandItem key={l.code} onSelect={() => run(() => setLang(l.code))}>
                    <Languages size={14} aria-hidden />
                    {t('chat:commandMenu.actions.switchLanguage', { language: l.label })}
                  </CommandItem>
                ))}
              </CommandGroup>

              {conversations.length > 0 && (
                <>
                  <CommandSeparator />
                  <CommandGroup heading={t('chat:commandMenu.groups.conversations')}>
                    {conversations.slice(0, 8).map((c) => (
                      <CommandItem
                        key={c.id}
                        value={`${c.title} ${c.id}`}
                        onSelect={() => run(() => navigate(`/chat/${c.id}`))}
                      >
                        <Sparkles size={14} className="text-[var(--color-secondary)]" aria-hidden />
                        {truncate(c.title, 60)}
                        <ArrowRight size={12} className="ml-auto text-[var(--color-fg-subtle)]" aria-hidden />
                      </CommandItem>
                    ))}
                  </CommandGroup>
                </>
              )}

              {extraTitleHits.length > 0 && (
                <>
                  <CommandSeparator />
                  <CommandGroup heading={t('chat:commandMenu.groups.conversations')}>
                    {extraTitleHits.slice(0, 8).map((h) => (
                      <CommandItem
                        // Query embedded in value so cmdk's local filter keeps it.
                        key={`title-${h.conversation_id}`}
                        value={`title ${query} ${h.title} ${h.conversation_id}`}
                        onSelect={() => run(() => navigate(`/chat/${h.conversation_id}`))}
                      >
                        <Sparkles size={14} className="text-[var(--color-secondary)]" aria-hidden />
                        {truncate(h.title, 60)}
                        <ArrowRight size={12} className="ml-auto text-[var(--color-fg-subtle)]" aria-hidden />
                      </CommandItem>
                    ))}
                  </CommandGroup>
                </>
              )}

              {msgHits.length > 0 && (
                <>
                  <CommandSeparator />
                  <CommandGroup heading={t('chat:commandMenu.groups.messages')}>
                    {msgHits.slice(0, 8).map((h) => (
                      <CommandItem
                        // The query is embedded in `value` so cmdk's local filter
                        // never hides a content hit whose title doesn't match the
                        // typed text.
                        key={`msg-${h.message_id}`}
                        value={`msg ${query} ${h.title} ${h.snippet ?? ''} ${h.message_id}`}
                        onSelect={() =>
                          run(() =>
                            // `j` nonce makes re-selecting the SAME result re-jump
                            // (otherwise the unchanged ?m= URL is a no-op).
                            navigate(
                              `/chat/${h.conversation_id}?m=${encodeURIComponent(h.message_id ?? '')}&j=${Date.now()}`,
                            ),
                          )
                        }
                      >
                        <TextSearch size={14} className="shrink-0 text-[var(--color-secondary)]" aria-hidden />
                        <span className="flex min-w-0 flex-col">
                          <span className="truncate text-[var(--color-fg)]">{truncate(h.title, 50)}</span>
                          {h.snippet ? (
                            <span className="truncate text-[12px] text-[var(--color-fg-muted)]">{h.snippet}</span>
                          ) : null}
                        </span>
                        <ArrowRight size={12} className="ml-auto shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
                      </CommandItem>
                    ))}
                  </CommandGroup>
                </>
              )}

              <CommandSeparator />
              <CommandGroup heading={t('chat:commandMenu.groups.help')}>
                <CommandItem onSelect={() => run(() => openSettings('shortcuts'))}>
                  <HelpCircle size={14} aria-hidden />
                  {t('chat:commandMenu.actions.shortcuts')}
                  <CommandShortcut>{modKey()} /</CommandShortcut>
                </CommandItem>
              </CommandGroup>
            </CommandList>
          </Command>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
    <NewProjectDialog open={newProjectOpen} onOpenChange={setNewProjectOpen} />
    </>
  )
}
