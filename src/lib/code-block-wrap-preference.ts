import { useAuth } from '@/store/auth'
import { useSettings } from '@/store/settings'
import { persistUserSettings } from './user-settings'

export async function saveCodeBlockWrapPreference(value: boolean): Promise<void> {
  const auth = useAuth.getState()
  const userId = auth.status === 'authenticated' ? auth.user?.id ?? null : null
  const previousValue = useSettings.getState().appearance.codeBlockWrap
  const requestId = useSettings.getState().beginCodeBlockWrapSave(value, userId)
  const stillCurrent = () => {
    const current = useAuth.getState()
    const currentUserId = current.status === 'authenticated' ? current.user?.id ?? null : null
    return currentUserId === userId && useSettings.getState().codeBlockWrapPending?.requestId === requestId
  }
  try {
    const updated = await persistUserSettings({ code_block_wrap: value })
    if (!stillCurrent()) return
    if (userId && updated?.code_block_wrap !== value) throw new Error('The server did not save the code wrapping preference')
    useSettings.getState().finishCodeBlockWrapSave(requestId, value)
  } catch (error) {
    if (!stillCurrent()) return
    useSettings.getState().finishCodeBlockWrapSave(requestId, previousValue)
    throw error
  }
}
