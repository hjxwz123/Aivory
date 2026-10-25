import { useAuth } from '@/store/auth'
import { useSettings } from '@/store/settings'
import type { ReplyAnimationPref } from '@/types/settings'
import { persistUserSettings } from './user-settings'

export async function saveReplyAnimationPreference(value: ReplyAnimationPref): Promise<void> {
  const auth = useAuth.getState()
  const userId = auth.status === 'authenticated' ? auth.user?.id ?? null : null
  const previousValue = useSettings.getState().appearance.replyAnimation
  const requestId = useSettings.getState().beginReplyAnimationSave(value, userId)
  const stillCurrent = () => {
    const current = useAuth.getState()
    const currentUserId = current.status === 'authenticated' ? current.user?.id ?? null : null
    return currentUserId === userId && useSettings.getState().replyAnimationPending?.requestId === requestId
  }
  try {
    const updated = await persistUserSettings({ reply_animation: value })
    if (!stillCurrent()) return
    if (userId && updated?.reply_animation !== value) throw new Error('The server did not save the reply animation preference')
    useSettings.getState().finishReplyAnimationSave(requestId, value)
  } catch (error) {
    if (!stillCurrent()) return
    useSettings.getState().finishReplyAnimationSave(requestId, previousValue)
    throw error
  }
}
