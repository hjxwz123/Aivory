import { useMemo, useState } from 'react'
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
import { useConversations } from '@/store/conversations'
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
  const { t } = useTranslation(['chat', 'projects'])
  const allConversations = useConversations((s) => s.conversations)
  const conversations = useMemo(
    () => allConversations.filter((c) => !c.archived),
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
            'fixed left-1/2 top-[18%] z-[70] -translate-x-1/2 w-[min(92vw,560px)]',
            'rounded-[18px] bg-[var(--color-surface-raised)] border border-[var(--color-border)]',
            'shadow-[var(--shadow-xl)] overflow-hidden',
            'data-[state=open]:animate-[pop-in_220ms_var(--ease-out)]',
            'data-[state=closed]:animate-[fade-out_140ms_var(--ease-in)]',
            'focus:outline-none',
          )}
        >
          <DialogPrimitive.Title className="sr-only">{t('chat:commandMenu.title')}</DialogPrimitive.Title>
          <Command>
            <CommandInput placeholder={t('chat:commandMenu.placeholder')} autoFocus />
            <CommandList>
              <CommandEmpty>{t('chat:commandMenu.noMatch')}</CommandEmpty>
              <CommandGroup heading={t('chat:commandMenu.groups.actions')}>
                <CommandItem onSelect={() => run(() => navigate('/'))}>
                  <Plus size={14} aria-hidden />
                  {t('chat:commandMenu.actions.newChat')}
                  <CommandShortcut>{modKey()} Shift O</CommandShortcut>
                </CommandItem>
                <CommandItem onSelect={() => run(() => navigate('/chat'))}>
                  <MessageSquare size={14} aria-hidden />
                  {t('chat:commandMenu.actions.goToChat')}
                </CommandItem>
                <CommandItem onSelect={() => run(() => navigate('/settings/account'))}>
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

              <CommandSeparator />
              <CommandGroup heading={t('chat:commandMenu.groups.help')}>
                <CommandItem onSelect={() => run(() => navigate('/settings/shortcuts'))}>
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
