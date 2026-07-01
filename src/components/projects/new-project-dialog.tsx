import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import type { ProjectAccent } from '@/types/project'
import { useProjects } from '@/store/projects'
import { PROJECT_ACCENT_OPTIONS, accentClasses } from '@/lib/project-helpers'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Button } from '@/components/ui/button'
import { toast } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'

interface NewProjectDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /**
   * Called after the project is created with its id. Defaults to
   * navigating to the project's detail page.
   */
  onCreated?: (projectId: string) => void
}

const DEFAULT_ACCENT: ProjectAccent = 'violet'

export function NewProjectDialog({ open, onOpenChange, onCreated }: NewProjectDialogProps) {
  const { t } = useTranslation(['projects', 'common'])
  const create = useProjects((s) => s.createProject)
  const navigate = useNavigate()

  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [instructions, setInstructions] = useState('')
  const [accent, setAccent] = useState<ProjectAccent>(DEFAULT_ACCENT)
  const [error, setError] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const creatingRef = useRef(false)

  // Reset every time the dialog re-opens so a stale draft never reappears.
  useEffect(() => {
    if (open) {
      setName('')
      setDescription('')
      setInstructions('')
      setAccent(DEFAULT_ACCENT)
      setError(null)
    }
  }, [open])

  async function submit() {
    if (creatingRef.current) return
    const trimmed = name.trim()
    if (!trimmed) {
      setError(t('projects:create.nameLabel'))
      return
    }
    creatingRef.current = true
    setCreating(true)
    try {
      const project = await create({
        name: trimmed,
        description: description.trim() || undefined,
        instructions: instructions.trim(),
        accent,
      })
      if (!project) {
        const err = useProjects.getState().error
        setError(
          err === 'project_limit_reached'
            ? t('projects:create.limitReached', { defaultValue: 'You’ve reached your plan’s project limit.' })
            : err === 'name_exists'
              ? t('projects:create.nameExists', { defaultValue: 'A project with this name already exists.' })
              : t('common:somethingWentWrong', { defaultValue: 'Something went wrong' }),
        )
        return
      }
      onOpenChange(false)
      toast.success(t('projects:create.created'))
      if (onCreated) onCreated(project.id)
      else navigate(`/projects/${project.id}`)
    } finally {
      creatingRef.current = false
      setCreating(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(next) => !creatingRef.current && onOpenChange(next)}>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>{t('projects:create.title')}</DialogTitle>
          <DialogDescription>{t('projects:create.description')}</DialogDescription>
        </DialogHeader>
        <DialogBody className="flex flex-col gap-4">
          <Field
            label={t('projects:create.nameLabel')}
            htmlFor="np-name"
            error={error ?? undefined}
          >
            <Input
              id="np-name"
              autoFocus
              value={name}
              onChange={(e) => {
                setName(e.target.value)
                if (error) setError(null)
              }}
              placeholder={t('projects:create.namePlaceholder')}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
                  e.preventDefault()
                  void submit()
                }
              }}
            />
          </Field>
          <Field label={t('projects:create.descLabel')} htmlFor="np-desc">
            <Input
              id="np-desc"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder={t('projects:create.descPlaceholder')}
            />
          </Field>
          <Field
            label={t('projects:create.instructionsLabel')}
            hint={t('projects:create.instructionsHelper')}
            htmlFor="np-instr"
          >
            <Textarea
              id="np-instr"
              value={instructions}
              onChange={(e) => setInstructions(e.target.value)}
              placeholder={t('projects:create.instructionsPlaceholder')}
              rows={4}
            />
          </Field>
          <Field label={t('projects:create.accentLabel')}>
            <div className="flex flex-wrap gap-2">
              {PROJECT_ACCENT_OPTIONS.map((a) => {
                const cls = accentClasses(a)
                const selected = accent === a
                return (
                  <button
                    type="button"
                    key={a}
                    onClick={() => setAccent(a)}
                    aria-pressed={selected}
                    aria-label={t(`projects:accent.${a}`)}
                    className={cn(
                      'inline-flex items-center gap-2 rounded-[10px] px-2.5 py-1.5 text-xs',
                      'border interactive',
                      'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                      selected
                        ? 'border-[var(--color-border-strong)] bg-[var(--color-bg-muted)] text-[var(--color-fg)]'
                        : 'border-[var(--color-border)] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                    )}
                  >
                    <span className={cn('inline-block size-3 rounded-full', cls.bar)} aria-hidden />
                    {t(`projects:accent.${a}`)}
                  </button>
                )
              })}
            </div>
          </Field>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={creating}>
            {t('projects:create.cancel')}
          </Button>
          <Button onClick={() => void submit()} loading={creating}>{t('projects:create.submit')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
