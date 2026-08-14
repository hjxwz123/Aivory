import { toast, useToastStore } from '@/hooks/use-toast'
import i18n from '@/i18n'

export type CompactionEventType =
  | 'compaction.started'
  | 'compaction.completed'
  | 'compaction.failed'

const activeToasts = new Map<string, string>()
const activeOperations = new Map<string, string | null>()
const expiryTimers = new Map<string, ReturnType<typeof setTimeout>>()
const terminalToastDurationMs = 4500

function forgetToast(conversationId: string, toastId: string): void {
  if (activeToasts.get(conversationId) === toastId) activeToasts.delete(conversationId)
  const timer = expiryTimers.get(conversationId)
  if (timer) clearTimeout(timer)
  expiryTimers.delete(conversationId)
}

function trackTerminalToast(conversationId: string, toastId: string): void {
  activeToasts.set(conversationId, toastId)
  const timer = setTimeout(() => {
    forgetToast(conversationId, toastId)
  }, terminalToastDurationMs)
  expiryTimers.set(conversationId, timer)
}

/** Apply one server-issued automatic-compaction lifecycle event. */
export function handleCompactionNotification(
  type: CompactionEventType,
  conversationId: string,
  operationId = '',
): void {
  if (!conversationId) return

  const activeToast = activeToasts.get(conversationId)
  const operation = operationId.trim() || null
  // A previous compaction can finish after a newer pass has started for the
  // same conversation. Never let that stale terminal frame close or replace
  // the newer pass's progress notification.
  if (
    type !== 'compaction.started' &&
    activeOperations.has(conversationId) &&
    activeOperations.get(conversationId) !== operation
  ) {
    return
  }
  if (activeToast) {
    useToastStore.getState().dismiss(activeToast)
    forgetToast(conversationId, activeToast)
  }
  activeOperations.set(conversationId, operation)

  switch (type) {
    case 'compaction.started': {
      const id = toast.custom({
        title: i18n.t('chat:composer.commands.autoCompacting', {
          defaultValue: 'Compacting conversation context...',
        }),
        variant: 'info',
        duration: 0,
      })
      activeToasts.set(conversationId, id)
      break
    }
    case 'compaction.completed': {
      const id = toast.custom({
        title: i18n.t('chat:composer.commands.autoCompacted', {
          defaultValue: 'Conversation context compacted',
        }),
        variant: 'success',
        duration: terminalToastDurationMs,
      })
      trackTerminalToast(conversationId, id)
      break
    }
    case 'compaction.failed': {
      const id = toast.custom({
        title: i18n.t('chat:composer.commands.autoFailed', {
          defaultValue: 'Automatic context compaction failed',
        }),
        variant: 'danger',
        duration: terminalToastDurationMs,
      })
      trackTerminalToast(conversationId, id)
      break
    }
  }
}

/** Clear every compaction-owned notice after reconnect or an account switch. */
export function dismissCompactionNotifications(): void {
  for (const id of activeToasts.values()) {
    useToastStore.getState().dismiss(id)
  }
  for (const timer of expiryTimers.values()) clearTimeout(timer)
  activeToasts.clear()
  activeOperations.clear()
  expiryTimers.clear()
}
