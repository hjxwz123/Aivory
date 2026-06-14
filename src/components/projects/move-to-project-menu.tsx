import { useTranslation } from 'react-i18next'
import { Check, FolderKanban, FolderMinus } from 'lucide-react'
import { useProjects } from '@/store/projects'
import { useConversations } from '@/store/conversations'
import { accentClasses } from '@/lib/project-helpers'
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
} from '@/components/ui/dropdown-menu'
import { toast } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'

interface MoveToProjectSubProps {
  conversationId: string
  /** The conversation's current project, if any. */
  currentProjectId?: string
}

/**
 * A "Move to project ▸" submenu for a conversation's action menu. Lists every
 * project (current one ticked) and, when the conversation already belongs to a
 * project, a "Remove from project" entry that turns it back into a loose chat.
 * Drop it inside any <DropdownMenuContent>.
 */
export function MoveToProjectSub({ conversationId, currentProjectId }: MoveToProjectSubProps) {
  const { t } = useTranslation(['chat', 'projects'])
  const projects = useProjects((s) => s.projects)
  const setProject = useConversations((s) => s.setProject)

  async function move(projectId: string | undefined, name?: string) {
    if (projectId === currentProjectId) return
    try {
      await setProject(conversationId, projectId)
      toast.success(
        projectId
          ? t('projects:moveTo.moved', { name })
          : t('projects:moveTo.removed'),
      )
    } catch {
      toast.error(t('projects:moveTo.failed'))
    }
  }

  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger>
        <FolderKanban size={13} aria-hidden />
        {t('chat:sidebar.moveToProject')}
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent>
        {currentProjectId ? (
          <>
            <DropdownMenuItem onClick={() => void move(undefined)}>
              <FolderMinus size={13} aria-hidden />
              {t('chat:sidebar.removeFromProject')}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
          </>
        ) : null}
        {projects.length === 0 ? (
          <DropdownMenuItem disabled>{t('projects:moveTo.none')}</DropdownMenuItem>
        ) : (
          projects.map((p) => (
            <DropdownMenuItem key={p.id} onClick={() => void move(p.id, p.name)}>
              <span className={cn('size-2 shrink-0 rounded-full', accentClasses(p.accent).bar)} aria-hidden />
              <span className="truncate">{p.name}</span>
              {p.id === currentProjectId ? (
                <Check size={13} aria-hidden className="ml-auto text-[var(--color-fg-muted)]" />
              ) : null}
            </DropdownMenuItem>
          ))
        )}
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  )
}
