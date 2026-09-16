import { useEffect, useRef, useState } from 'react'
import { htmlPreviewsApi } from '@/api/html-previews'
import { copyText } from '@/lib/utils'
import { toast } from '@/hooks/use-toast'
import { useTranslation } from 'react-i18next'

export function useHTMLPreviewShare(html: string) {
  const { t } = useTranslation('chat')
  const [sharing, setSharing] = useState(false)
  const [copied, setCopied] = useState(false)
  const copiedTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => () => {
    if (copiedTimer.current) clearTimeout(copiedTimer.current)
  }, [])

  async function copyLink() {
    if (sharing || !html.trim()) return
    setSharing(true)
    try {
      const created = await htmlPreviewsApi.create(html)
      if (await copyText(created.absoluteUrl)) {
        setCopied(true)
        if (copiedTimer.current) clearTimeout(copiedTimer.current)
        copiedTimer.current = setTimeout(() => setCopied(false), 1800)
        toast.success(t('code.previewLinkCopied'))
      } else {
        toast.error(t('code.previewLinkCreatedCopyFailed'))
      }
    } catch {
      toast.error(t('code.previewLinkFailed'))
    } finally {
      setSharing(false)
    }
  }

  return { sharing, copied, copyLink }
}
