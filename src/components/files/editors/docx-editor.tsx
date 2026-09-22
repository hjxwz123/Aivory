import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { DocxEditor, type DocxEditorRef } from '@docx-editor.dev/react'
import { deepMerge, en, fr, zhCN, type Translations } from '@docx-editor.dev/i18n'
import { jaChrome } from '@/i18n/docx-editor/ja'
import { zhHantChrome } from '@/i18n/docx-editor/zh-Hant'
import '@docx-editor.dev/react/styles.css'
import { toast } from '@/hooks/use-toast'
import { useTheme } from '@/store/theme'
import type { DocumentEditorProps } from '@/components/files/editors/editor-types'

/**
 * Word editor backed by `@docx-editor.dev/react` (Apache-2.0 core only — the
 * `/pro` package is licensed for evaluation and must never be shipped here).
 *
 * The library edits canonical OOXML in the browser and documents a lossless
 * round-trip: content it does not model survives `save()`. That is exactly the
 * file-level fidelity this project requires, which is why it was chosen over
 * markdown/HTML round-trip pipelines that regenerate the document.
 *
 * Saving is debounced rather than per-keystroke: `save()` serializes the whole
 * package, so running it on every change would stutter on large documents. The
 * panel's own "save a copy" then persists whatever the last serialization held.
 */

const DOCX_MIME = 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'
const SAVE_DEBOUNCE_MS = 900

/**
 * The vendor ships English, French and Simplified Chinese catalogues only.
 *
 * Traditional Chinese is therefore merged OVER `zhCN`, not over English: keys
 * this project has not translated yet then stay Simplified (readable for a
 * Traditional reader) instead of dropping to English. Japanese has no vendor
 * catalogue at all, so its partial — which the prop accepts directly — layers
 * over the bundled English.
 *
 * Both are module constants on purpose: the vendor warns that an inline
 * catalogue is a new object every render and re-renders every chrome control.
 */
const TRADITIONAL_CHINESE = deepMerge(zhCN, zhHantChrome) as unknown as Translations

function catalogueFor(language: string): Translations | undefined {
  // "zh-Hant" / "zh-TW" / "zh-HK" must be matched before the plain "zh" branch.
  if (/^zh[-_]?(hant|tw|hk|mo)/i.test(language)) return TRADITIONAL_CHINESE
  if (language.startsWith('zh')) return zhCN
  if (language.startsWith('ja')) return jaChrome
  if (language.startsWith('fr')) return fr
  if (language.startsWith('en')) return en
  return undefined
}

export default function DocxEditorPane({ name, data, onChange, flushRef }: DocumentEditorProps) {
  const { t, i18n } = useTranslation('chat')
  const resolvedTheme = useTheme((state) => state.resolved)

  const ref = useRef<DocxEditorRef>(null)
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange
  const timerRef = useRef<number | null>(null)
  const [serializing, setSerializing] = useState(false)

  const cancelTimer = useCallback(() => {
    if (timerRef.current === null) return
    window.clearTimeout(timerRef.current)
    timerRef.current = null
  }, [])

  const serialize = useCallback(async () => {
    const api = ref.current
    if (!api) return
    setSerializing(true)
    try {
      const buffer = await api.save()
      if (!buffer) return
      onChangeRef.current({ bytes: new Blob([buffer], { type: DOCX_MIME }) })
    } catch (error) {
      // Never fail silently: the user would believe their edits are captured.
      toast.error(
        t('filePreview.docxSaveFailed', { defaultValue: "Couldn't read the edited document" }),
        error instanceof Error ? error.message : undefined,
      )
    } finally {
      setSerializing(false)
    }
  }, [t])

  const scheduleSerialize = useCallback(() => {
    cancelTimer()
    timerRef.current = window.setTimeout(() => {
      timerRef.current = null
      void serialize()
    }, SAVE_DEBOUNCE_MS)
  }, [cancelTimer, serialize])

  useEffect(() => cancelTimer, [cancelTimer])

  // On-demand serialization for the panel's save/download, so an edit made
  // inside the debounce window is still persisted.
  useEffect(() => {
    if (!flushRef) return
    flushRef.current = async () => {
      const api = ref.current
      if (!api) return null
      try {
        const buffer = await api.save()
        return buffer ? new Blob([buffer], { type: DOCX_MIME }) : null
      } catch {
        return null
      }
    }
    return () => {
      flushRef.current = null
    }
  })

  const catalogue = catalogueFor(i18n.language ?? 'en')

  return (
    <div className="flex h-full min-h-0 flex-col" data-serializing={serializing || undefined}>
      <DocxEditor
        ref={ref}
        document={data}
        title={name}
        // The vendor's own chrome talks about saving/opening files; both are
        // owned by the artifact panel here, so the menu bar is removed rather
        // than left as a second, conflicting path to the same actions.
        menu={false}
        // The panel is 22–36rem wide; the navigation pane would leave too little
        // room for the page itself.
        navigation={false}
        colorMode={resolvedTheme}
        i18n={catalogue}
        onChange={scheduleSerialize}
      />
    </div>
  )
}
