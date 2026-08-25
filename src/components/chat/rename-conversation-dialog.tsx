import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Pencil } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { toast } from '@/hooks/use-toast'
import { useConversations } from '@/store/conversations'

interface RenameConversationDialogProps {
  conversationId: string
  currentTitle: string
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function RenameConversationDialog({
  conversationId,
  currentTitle,
  open,
  onOpenChange,
}: RenameConversationDialogProps) {
  const { t } = useTranslation(['chat', 'common'])
  const rename = useConversations((state) => state.renameConversation)
  const [draft, setDraft] = useState(currentTitle)
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!open) return
    setDraft(currentTitle)
    setError('')
  }, [currentTitle, open])

  function handleOpenChange(next: boolean) {
    if (!next && saving) return
    if (!next) setError('')
    onOpenChange(next)
  }

  async function submit() {
    const title = draft.trim()
    if (!title) {
      setError(t('chat:sidebar.renameEmpty'))
      return
    }

    if (title === currentTitle.trim()) {
      handleOpenChange(false)
      return
    }

    setSaving(true)
    const renamed = await rename(conversationId, title)
    setSaving(false)
    if (!renamed) return

    toast.success(t('chat:sidebar.renamed'))
    handleOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent size="sm" showClose={!saving} aria-busy={saving || undefined}>
        <DialogHeader>
          <DialogTitle>{t('chat:sidebar.renameTitle')}</DialogTitle>
          <DialogDescription>{t('chat:sidebar.renameHint')}</DialogDescription>
        </DialogHeader>
        <DialogBody>
          <form
            id={`rename-conversation-${conversationId}`}
            onSubmit={(event) => {
              event.preventDefault()
              void submit()
            }}
          >
            <Input
              value={draft}
              onChange={(event) => {
                setDraft(event.target.value)
                if (event.target.value.trim()) setError('')
              }}
              autoFocus
              invalid={Boolean(error)}
              aria-describedby={error ? `rename-conversation-error-${conversationId}` : undefined}
              disabled={saving}
            />
            {error ? (
              <p
                id={`rename-conversation-error-${conversationId}`}
                role="alert"
                className="mt-1.5 text-[12px] leading-relaxed text-[var(--color-danger)]"
              >
                {error}
              </p>
            ) : null}
          </form>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" disabled={saving} onClick={() => handleOpenChange(false)}>
            {t('common:actions.cancel')}
          </Button>
          <Button
            type="submit"
            form={`rename-conversation-${conversationId}`}
            loading={saving}
            leadingIcon={<Pencil size={15} aria-hidden />}
          >
            {t('common:actions.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
