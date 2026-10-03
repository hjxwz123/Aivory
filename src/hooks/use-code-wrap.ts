import { useEffect, useState } from 'react'
import { useSettings } from '@/store/settings'

export function useCodeWrap() {
  const defaultWrap = useSettings((state) => state.appearance.codeBlockWrap)
  const accountId = useSettings((state) => state.codeBlockWrapAccountId)
  const [selection, setSelection] = useState<{ accountId: string | null; override: boolean | null }>(() => ({ accountId, override: null }))
  useEffect(() => {
    setSelection((current) => current.accountId === accountId ? current : { accountId, override: null })
  }, [accountId])
  const override = selection.accountId === accountId ? selection.override : null
  const enabled = override ?? defaultWrap
  return {
    enabled,
    overridden: override !== null,
    toggle: () => setSelection({ accountId, override: !enabled }),
    reset: () => setSelection({ accountId, override: null }),
  }
}
