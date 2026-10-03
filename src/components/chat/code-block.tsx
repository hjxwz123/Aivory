import { useEffect, useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Copy, Check, Play, Square, AppWindow, Link2, LoaderCircle } from 'lucide-react'
import { useCopy } from '@/hooks/use-clipboard'
import { useHTMLPreviewShare } from '@/hooks/use-html-preview-share'
import { useCodeHighlight } from '@/lib/syntax/use-code-highlight'
import {
  runPython,
  type PythonRunHandle,
  type PythonRunPhase,
  type PythonRunResult,
  type PythonStreamChunk,
} from '@/lib/pyodide-runner'
import { autoOpenPreview, useArtifactPanel } from '@/store/artifact-panel'
import { useTheme } from '@/store/theme'
import { CodeRunOutput } from './code-run-output'
import { CodeBlockFrame } from './code-block-frame'
import { CodeAction as IconAction } from './code-action'

interface CodeBlockProps {
  code: string
  lang?: string
  className?: string
  /** True while the owning assistant message is still streaming. */
  live?: boolean
  /**
   * Stable identity for this block (message id + block index). Drives the
   * HTML preview panel ownership; falls back to useId when absent.
   */
  previewKey?: string
  /** Public preview links are only offered for completed assistant output. */
  allowPublicShare?: boolean
}

const PYTHON_LANGS = new Set(['python', 'py', 'python3'])
const HTML_LANGS = new Set(['html', 'htm', 'xhtml'])

/** Fenced block is HTML when tagged so, or (untagged) when it *reads* like a document. */
function isHtmlSnippet(code: string, lang?: string): boolean {
  if (lang) return HTML_LANGS.has(lang.toLowerCase())
  return /^\s*(?:<!doctype\s+html|<html[\s>])/i.test(code)
}

/**
 * Calm, sunken code block with a sticky header (language + actions).
 * Python blocks gain a Run button (Pyodide, in-browser, off-thread) with a
 * result well underneath; HTML blocks gain a Preview button and, while the
 * message streams, drive the live preview drawer automatically.
 */
export function CodeBlock({ code, lang, className, live = false, previewKey, allowPublicShare = false }: CodeBlockProps) {
  const { t } = useTranslation('chat')
  const { copied, copy } = useCopy()
  const theme = useTheme((s) => s.resolved)
  const { html } = useCodeHighlight({ code, lang, live, theme })

  const fallbackKey = useId()
  const blockKey = previewKey ?? fallbackKey
  const isPython = PYTHON_LANGS.has((lang ?? '').toLowerCase())
  const isHtml = isHtmlSnippet(code, lang)
  const previewShare = useHTMLPreviewShare(code)

  // ---- Python execution -------------------------------------------------
  const [running, setRunning] = useState(false)
  const [phase, setPhase] = useState<PythonRunPhase>('queued')
  const [chunks, setChunks] = useState<PythonStreamChunk[]>([])
  const [outcome, setOutcome] = useState<PythonRunResult | null>(null)
  const handleRef = useRef<PythonRunHandle | null>(null)

  function startRun() {
    if (running) return
    setRunning(true)
    setPhase('queued')
    setChunks([])
    setOutcome(null)
    const handle = runPython(code, {
      onPhase: setPhase,
      onStream: (chunk) => setChunks((prev) => [...prev, chunk]),
    })
    handleRef.current = handle
    void handle.promise.then((result) => {
      handleRef.current = null
      setOutcome(result)
      setRunning(false)
    })
  }

  // Don't leave an orphaned run burning CPU after the block unmounts.
  useEffect(() => () => handleRef.current?.cancel(), [])

  // ---- HTML live preview --------------------------------------------------
  const ownsPreview = useArtifactPanel((s) => s.source?.type === 'html' && s.source.sourceKey === blockKey)
  useEffect(() => {
    if (!isHtml) return
    if (live && code.trim().length > 16) {
      // Streaming HTML pops the panel once, then keeps it in sync.
      autoOpenPreview(blockKey, code)
    } else if (ownsPreview) {
      useArtifactPanel.getState().syncHtml(blockKey, code, allowPublicShare && !live)
    }
  }, [isHtml, live, code, blockKey, ownsPreview, allowPublicShare])

  return (
    <CodeBlockFrame
      code={code}
      lang={lang}
      html={html}
      className={className}
      actions={
        <>
          {isPython && !live ? (
            running ? (
              <IconAction onClick={() => handleRef.current?.cancel()} label={t('code.stop')}>
                <Square size={13} aria-hidden />
              </IconAction>
            ) : (
              <IconAction onClick={startRun} label={outcome ? t('code.rerun') : t('code.run')}>
                <Play size={13} aria-hidden />
              </IconAction>
            )
          ) : null}
          {isHtml ? (
            <IconAction
              onClick={() => useArtifactPanel.getState().openArtifact({ type: 'html', sourceKey: blockKey, html: code, shareable: allowPublicShare && !live })}
              label={t('code.preview')}
            >
              <AppWindow size={13} aria-hidden />
            </IconAction>
          ) : null}
          {isHtml && allowPublicShare && !live ? (
            <IconAction
              onClick={() => void previewShare.copyLink()}
              label={previewShare.copied ? t('code.previewLinkCopied') : t('code.copyPreviewLink')}
              disabled={previewShare.sharing}
            >
              {previewShare.sharing ? (
                <LoaderCircle className="animate-spin" size={13} aria-hidden />
              ) : previewShare.copied ? (
                <Check size={13} aria-hidden />
              ) : (
                <Link2 size={13} aria-hidden />
              )}
            </IconAction>
          ) : null}
        </>
      }
      copyAction={
        <IconAction onClick={() => void copy(code)} label={copied ? t('actions.copied') : t('actions.copy')}>
          {copied ? <Check size={13} aria-hidden /> : <Copy size={13} aria-hidden />}
        </IconAction>
      }
      footer={isPython ? (
        <CodeRunOutput
          running={running}
          phase={phase}
          chunks={chunks}
          outcome={outcome}
          onClear={() => {
            setChunks([])
            setOutcome(null)
          }}
        />
      ) : null}
    />
  )
}
