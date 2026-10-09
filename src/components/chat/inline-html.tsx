import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { RotateCcw } from 'lucide-react'
import { buildInlineHTML, inlineHTMLSnapshot } from '@/lib/inline-html-document'
import { uid } from '@/lib/utils'
import { MAX_GENERATIVE_SOURCE } from '@/lib/generative-ui'

export function InlineHTML({ code, live }: { code: string; live: boolean }) {
  const { t } = useTranslation('chat')
  const frame = useRef<HTMLIFrameElement>(null)
  const [snapshot, setSnapshot] = useState(() => inlineHTMLSnapshot(code) ?? '')
  const [reload, setReload] = useState(0)
  const [renderState, setRenderState] = useState<{ channel: string; ready: boolean; failed: boolean }>({ channel: '', ready: false, failed: false })
  const [height, setHeight] = useState(120)
  const [theme, setTheme] = useState<Record<string, string>>({})
  const latest = useRef(code)
  latest.current = code
  useEffect(() => {
    // A fixed cadence (rather than debounce) renders even during a fast stream.
    if (!live) return
    const timer = setInterval(() => {
      const next = inlineHTMLSnapshot(latest.current)
      if (next !== null) setSnapshot(next)
    }, 800)
    return () => clearInterval(timer)
  }, [live])
  useEffect(() => {
    if (live) return
    // Markdown may briefly keep deferred streaming text after live turns off.
    // Never execute that incomplete document, even on the completion flush.
    const next = inlineHTMLSnapshot(code)
    if (next !== null) setSnapshot(next)
  }, [code, live])
  useEffect(() => {
    const update = () => {
      const style = getComputedStyle(document.documentElement)
      const tokens: Record<string, string> = {}
      for (const key of ['--color-surface', '--color-bg', '--color-bg-muted', '--color-fg', '--color-fg-muted', '--color-accent', '--color-ring']) tokens[key] = style.getPropertyValue(key).trim()
      tokens['--scheme'] = document.documentElement.dataset.theme === 'dark' || style.colorScheme === 'dark' ? 'dark' : 'light'
      setTheme(current => Object.keys(tokens).every(key => current[key] === tokens[key]) ? current : tokens)
    }
    update()
    const observer = new MutationObserver(update)
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'style', 'data-theme', 'data-color-scheme', 'data-accent'] })
    return () => observer.disconnect()
  }, [])
  const frameDocument = useMemo(() => {
    if (!snapshot.trim() || snapshot.length > MAX_GENERATIVE_SOURCE) return null
    const channel = uid(`inline-ui-${reload}`)
    return { channel, html: buildInlineHTML(snapshot, channel, theme) }
  }, [snapshot, theme, reload])
  const ready = renderState.channel === frameDocument?.channel && renderState.ready
  const failed = renderState.channel === frameDocument?.channel && renderState.failed && !ready
  useEffect(() => {
    if (!frameDocument || ready) return
    // A blank or broken generated document must never look like endless loading.
    const timer = setTimeout(() => setRenderState(current => current.channel === frameDocument.channel && current.ready ? current : { channel: frameDocument.channel, ready: false, failed: true }), 8000)
    return () => clearTimeout(timer)
  }, [frameDocument, ready])
  useEffect(() => {
    const resize = (event: MessageEvent) => {
      if (!frameDocument || event.source !== frame.current?.contentWindow || event.data?.channel !== frameDocument.channel) return
      if (Number.isFinite(event.data.height)) setHeight(Math.max(120, Math.min(900, event.data.height)))
      setRenderState(current => ({ channel: frameDocument.channel, ready: event.data.ready === true || (current.channel === frameDocument.channel && current.ready), failed: event.data.error === true || (current.channel === frameDocument.channel && current.failed) }))
    }
    window.addEventListener('message', resize)
    return () => window.removeEventListener('message', resize)
  }, [frameDocument])
  const invalid = code.length > MAX_GENERATIVE_SOURCE || (!live && (!code.trim() || inlineHTMLSnapshot(code) === null))
  return <div className="relative min-w-0" aria-busy={!ready && !failed && !invalid}>
    {(!ready || invalid) && <div role="status" className="flex min-h-24 items-center gap-3 text-sm text-[var(--color-fg-muted)]">
      <div className="min-w-0 flex-1 space-y-2">
        <p className="m-0">{t(invalid || failed ? 'generative.failed' : live ? 'generative.generating' : 'generative.loading')}</p>
        {!failed && !invalid && <div aria-hidden className="flex gap-2 motion-safe:animate-pulse"><span className="h-2 w-24 rounded bg-[var(--color-bg-muted)]" /><span className="h-2 w-12 rounded bg-[var(--color-bg-muted)]" /></div>}
      </div>
      {failed && !invalid && <button type="button" onClick={() => setReload(value => value + 1)} className="inline-flex shrink-0 items-center gap-1.5 rounded-lg bg-[var(--color-bg-muted)] px-3 py-1.5 text-sm text-[var(--color-fg)] hover:bg-[var(--color-surface-sunken)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"><RotateCcw size={14} />{t('generative.retry')}</button>}
    </div>}
    {frameDocument && !invalid && <iframe key={frameDocument.channel} ref={frame} title={t('generative.interactive')} srcDoc={frameDocument.html} sandbox="allow-scripts" referrerPolicy="no-referrer" className={`block w-full border-0 bg-[var(--color-surface)] ${ready ? '' : 'pointer-events-none absolute left-0 top-0 opacity-0'}`} style={{ height }} aria-hidden={!ready} tabIndex={ready ? undefined : -1} />}
  </div>
}
