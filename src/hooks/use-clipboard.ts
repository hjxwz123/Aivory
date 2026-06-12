import { useEffect, useRef, useState } from 'react'
import { copyText } from '@/lib/utils'

export function useCopy(timeoutMs = 1500): {
  copied: boolean
  copy: (text: string) => Promise<boolean>
} {
  const [copied, setCopied] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  async function copy(text: string) {
    const ok = await copyText(text)
    if (ok) {
      setCopied(true)
      if (timer.current) clearTimeout(timer.current)
      timer.current = setTimeout(() => setCopied(false), timeoutMs)
    }
    return ok
  }

  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current)
  }, [])

  return { copied, copy }
}
