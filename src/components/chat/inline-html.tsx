import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { buildInlineHTML } from '@/lib/inline-html-document'
import { uid } from '@/lib/utils'
import { MAX_GENERATIVE_SOURCE } from '@/lib/generative-ui'

export function InlineHTML({ code, live }: { code: string; live: boolean }) {
  const { t } = useTranslation('chat')
  const frame = useRef<HTMLIFrameElement>(null)
  const [channel] = useState(() => uid('inline-ui'))
  const [snapshot, setSnapshot] = useState(code)
  const [height, setHeight] = useState(120)
  const [theme, setTheme] = useState<Record<string, string>>({})
  const latest = useRef(code)
  latest.current = code
  useEffect(() => {
    // A fixed cadence (rather than debounce) renders even during a fast stream.
    if (!live) return
    const timer = setInterval(() => setSnapshot(latest.current), 500)
    return () => clearInterval(timer)
  }, [live])
  useEffect(() => { if (!live) setSnapshot(code) }, [code, live])
  useEffect(() => {
    const update = () => {
      const style = getComputedStyle(document.documentElement)
      const tokens: Record<string, string> = {}
      for (const key of ['--color-surface', '--color-bg', '--color-bg-muted', '--color-fg', '--color-fg-muted', '--color-accent', '--color-ring']) tokens[key] = style.getPropertyValue(key).trim()
      tokens['--scheme'] = document.documentElement.dataset.theme === 'dark' || style.colorScheme === 'dark' ? 'dark' : 'light'
      setTheme(tokens)
    }
    update()
    const observer = new MutationObserver(update)
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'style', 'data-theme', 'data-color-scheme', 'data-accent'] })
    return () => observer.disconnect()
  }, [])
  useEffect(() => {
    const resize = (event: MessageEvent) => {
      if (event.source !== frame.current?.contentWindow || event.data?.channel !== channel || !Number.isFinite(event.data.height)) return
      setHeight(Math.max(120, Math.min(900, event.data.height)))
    }
    window.addEventListener('message', resize)
    return () => window.removeEventListener('message', resize)
  }, [channel])
  const srcDoc = useMemo(() => snapshot.length <= MAX_GENERATIVE_SOURCE ? buildInlineHTML(snapshot, channel, theme) : '', [snapshot, channel, theme])
  if (!srcDoc) return <p role="status" className="text-sm text-[var(--color-fg-muted)]">{t('generative.invalid')}</p>
  return <iframe ref={frame} title={t('generative.interactive')} srcDoc={srcDoc} sandbox="allow-scripts" referrerPolicy="no-referrer" className="block w-full border-0" style={{ height }} />
}
