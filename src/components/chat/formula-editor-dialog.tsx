import 'mathlive/fonts.css'
import katex from 'katex'
import { Sigma } from 'lucide-react'
import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { MathfieldElement } from 'mathlive'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { cn } from '@/lib/utils'
import { useTheme } from '@/store/theme'
import { installFormulaMathfieldAccessibility } from './formula-editor-accessibility'

type FormulaCategory = 'basic' | 'greek' | 'relations' | 'calculus' | 'sets' | 'matrices'
type FormulaEditorError = 'loadFailed' | 'empty' | 'incomplete'

interface FormulaTemplate {
  preview: string
  insert: string
}

const FORMULA_TEMPLATES: Record<FormulaCategory, FormulaTemplate[]> = {
  basic: [
    { preview: String.raw`\frac{a}{b}`, insert: String.raw`\frac{\placeholder{}}{\placeholder{}}` },
    { preview: String.raw`x^n`, insert: String.raw`{\placeholder{}}^{\placeholder{}}` },
    { preview: String.raw`x_n`, insert: String.raw`{\placeholder{}}_{\placeholder{}}` },
    { preview: String.raw`\sqrt{x}`, insert: String.raw`\sqrt{\placeholder{}}` },
    { preview: String.raw`\sqrt[n]{x}`, insert: String.raw`\sqrt[\placeholder{}]{\placeholder{}}` },
    { preview: String.raw`\left(x\right)`, insert: String.raw`\left(\placeholder{}\right)` },
    { preview: String.raw`\left|x\right|`, insert: String.raw`\left|\placeholder{}\right|` },
    { preview: String.raw`\binom{n}{k}`, insert: String.raw`\binom{\placeholder{}}{\placeholder{}}` },
    { preview: String.raw`\overline{x}`, insert: String.raw`\overline{\placeholder{}}` },
    { preview: String.raw`\widehat{x}`, insert: String.raw`\widehat{\placeholder{}}` },
    { preview: String.raw`x\cdot y`, insert: String.raw`\cdot` },
    { preview: String.raw`x\div y`, insert: String.raw`\div` },
  ],
  greek: [
    '\\alpha', '\\beta', '\\gamma', '\\delta', '\\epsilon', '\\varepsilon', '\\zeta', '\\eta',
    '\\theta', '\\vartheta', '\\iota', '\\kappa', '\\lambda', '\\mu', '\\nu', '\\xi',
    '\\pi', '\\rho', '\\sigma', '\\tau', '\\phi', '\\varphi', '\\chi', '\\psi', '\\omega',
    '\\Gamma', '\\Delta', '\\Theta', '\\Lambda', '\\Xi', '\\Pi', '\\Sigma', '\\Phi', '\\Psi', '\\Omega',
  ].map((symbol) => ({ preview: symbol, insert: symbol })),
  relations: [
    '=', '\\ne', '<', '>', '\\le', '\\ge', '\\approx', '\\equiv', '\\sim', '\\simeq',
    '\\cong', '\\propto', '\\parallel', '\\perp', '\\mid', '\\nmid', '\\ll', '\\gg',
  ].map((symbol) => ({ preview: symbol, insert: symbol })),
  calculus: [
    { preview: String.raw`\sum_{i=1}^{n}`, insert: String.raw`\sum_{\placeholder{}}^{\placeholder{}}` },
    { preview: String.raw`\prod_{i=1}^{n}`, insert: String.raw`\prod_{\placeholder{}}^{\placeholder{}}` },
    { preview: String.raw`\int f(x)\,dx`, insert: String.raw`\int \placeholder{}\,d\placeholder{}` },
    { preview: String.raw`\int_a^b`, insert: String.raw`\int_{\placeholder{}}^{\placeholder{}}` },
    { preview: String.raw`\iint`, insert: String.raw`\iint` },
    { preview: String.raw`\oint`, insert: String.raw`\oint` },
    { preview: String.raw`\lim_{x\to a}`, insert: String.raw`\lim_{\placeholder{}\to\placeholder{}}` },
    { preview: String.raw`\frac{d}{dx}`, insert: String.raw`\frac{d}{d\placeholder{}}` },
    { preview: String.raw`\frac{\partial}{\partial x}`, insert: String.raw`\frac{\partial}{\partial\placeholder{}}` },
    { preview: String.raw`\nabla`, insert: String.raw`\nabla` },
    { preview: String.raw`\infty`, insert: String.raw`\infty` },
    { preview: String.raw`\Delta x`, insert: String.raw`\Delta` },
  ],
  sets: [
    '\\in', '\\notin', '\\ni', '\\subset', '\\subseteq', '\\supset', '\\supseteq', '\\cup',
    '\\cap', '\\setminus', '\\emptyset', '\\mathbb{N}', '\\mathbb{Z}', '\\mathbb{Q}', '\\mathbb{R}',
    '\\mathbb{C}', '\\forall', '\\exists', '\\nexists', '\\land', '\\lor', '\\neg', '\\Rightarrow',
    '\\Leftrightarrow', '\\to', '\\leftarrow', '\\leftrightarrow', '\\mapsto',
  ].map((symbol) => ({ preview: symbol, insert: symbol })),
  matrices: [
    {
      preview: String.raw`\begin{pmatrix}a&b\\c&d\end{pmatrix}`,
      insert: String.raw`\begin{pmatrix}\placeholder{}&\placeholder{}\\\placeholder{}&\placeholder{}\end{pmatrix}`,
    },
    {
      preview: String.raw`\begin{bmatrix}a&b\\c&d\end{bmatrix}`,
      insert: String.raw`\begin{bmatrix}\placeholder{}&\placeholder{}\\\placeholder{}&\placeholder{}\end{bmatrix}`,
    },
    {
      preview: String.raw`\begin{vmatrix}a&b\\c&d\end{vmatrix}`,
      insert: String.raw`\begin{vmatrix}\placeholder{}&\placeholder{}\\\placeholder{}&\placeholder{}\end{vmatrix}`,
    },
    {
      preview: String.raw`\begin{pmatrix}x\\y\\z\end{pmatrix}`,
      insert: String.raw`\begin{pmatrix}\placeholder{}\\\placeholder{}\\\placeholder{}\end{pmatrix}`,
    },
    {
      preview: String.raw`\begin{cases}a&x>0\\b&x\le0\end{cases}`,
      insert: String.raw`\begin{cases}\placeholder{}&\placeholder{}\\\placeholder{}&\placeholder{}\end{cases}`,
    },
    {
      preview: String.raw`\begin{aligned}a&=b\\c&=d\end{aligned}`,
      insert: String.raw`\begin{aligned}\placeholder{}&=\placeholder{}\\\placeholder{}&=\placeholder{}\end{aligned}`,
    },
  ],
}

const CATEGORIES: FormulaCategory[] = ['basic', 'greek', 'relations', 'calculus', 'sets', 'matrices']

function mathLiveLocale(language: string | undefined): string {
  const normalized = language?.toLowerCase() ?? 'en'
  if (normalized.startsWith('zh-hant')) return 'zh-TW'
  if (normalized.startsWith('zh')) return 'zh-CN'
  if (normalized.startsWith('ja')) return 'ja-JP'
  if (normalized.startsWith('fr')) return 'fr-FR'
  return 'en-US'
}

function isMathLiveOverlay(target: EventTarget | null): boolean {
  return target instanceof Element && Boolean(
    target.closest('.ML__keyboard, .ui-menu-container, [id^="mathlive-"]'),
  )
}

function FormulaSymbolButton({
  template,
  onInsert,
  label,
}: {
  template: FormulaTemplate
  onInsert: (latex: string) => void
  label: string
}) {
  const html = useMemo(
    () => katex.renderToString(template.preview, { throwOnError: false, strict: false }),
    [template.preview],
  )
  return (
    <button
      type="button"
      onClick={() => onInsert(template.insert)}
      aria-label={`${label}: ${template.preview}`}
      className={cn(
        'formula-symbol-button grid min-h-10 min-w-0 w-full place-items-center overflow-hidden rounded-[8px] px-2 text-[var(--color-fg)]',
        'hover:bg-[var(--color-bg-muted)] active:bg-[var(--color-surface-sunken)] interactive',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
        'max-sm:min-h-11 max-sm:min-w-11',
      )}
    >
      <span aria-hidden dangerouslySetInnerHTML={{ __html: html }} />
    </button>
  )
}

interface FormulaEditorDialogProps {
  open: boolean
  initialLatex?: string
  editing?: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: (latex: string) => void
}

export function FormulaEditorDialog({
  open,
  initialLatex = '',
  editing = false,
  onOpenChange,
  onConfirm,
}: FormulaEditorDialogProps) {
  const { t, i18n } = useTranslation('chat')
  const resolvedTheme = useTheme((state) => state.resolved)
  const errorId = useId()
  const [hostElement, setHostElement] = useState<HTMLDivElement | null>(null)
  const fallbackFocusRef = useRef<HTMLDivElement>(null)
  const mathfieldRef = useRef<MathfieldElement | null>(null)
  const [latex, setLatex] = useState(initialLatex)
  const [loading, setLoading] = useState(false)
  const [loadFailed, setLoadFailed] = useState(false)
  const [loadAttempt, setLoadAttempt] = useState(0)
  const [keyboardInset, setKeyboardInset] = useState(0)
  const [errorKey, setErrorKey] = useState<FormulaEditorError | null>(null)
  const error = errorKey ? t(`composer.formula.${errorKey}`) : ''
  const localeRef = useRef(mathLiveLocale(i18n.resolvedLanguage))
  const themeRef = useRef(resolvedTheme)
  const errorRef = useRef(error)
  const mathfieldAccessibilityRef = useRef<ReturnType<typeof installFormulaMathfieldAccessibility> | null>(null)
  const labelsRef = useRef({
    input: t('composer.formula.inputLabel'),
    menu: t('composer.formula.menuLabel'),
    keyboard: t('composer.formula.keyboardLabel'),
  })
  localeRef.current = mathLiveLocale(i18n.resolvedLanguage)
  themeRef.current = resolvedTheme
  errorRef.current = error
  labelsRef.current = {
    input: t('composer.formula.inputLabel'),
    menu: t('composer.formula.menuLabel'),
    keyboard: t('composer.formula.keyboardLabel'),
  }

  useEffect(() => {
    if (!open) return
    const root = document.documentElement
    const previousMathLiveTheme = root.getAttribute('theme')
    root.setAttribute('theme', resolvedTheme)
    mathfieldRef.current?.setAttribute('theme', resolvedTheme)
    return () => {
      if (previousMathLiveTheme === null) root.removeAttribute('theme')
      else root.setAttribute('theme', previousMathLiveTheme)
    }
  }, [open, resolvedTheme])

  useEffect(() => {
    if (!open) return
    const locale = mathLiveLocale(i18n.resolvedLanguage)
    const inputLabel = t('composer.formula.inputLabel')
    const field = mathfieldRef.current
    if (field) {
      field.setAttribute('lang', locale)
      field.setAttribute('aria-label', inputLabel)
      mathfieldAccessibilityRef.current?.sync()
    }
    void import('mathlive').then(({ MathfieldElement }) => {
      MathfieldElement.locale = locale
      const currentField = mathfieldRef.current
      if (!currentField) return
      currentField.setAttribute('lang', locale)
      currentField.setAttribute('aria-label', inputLabel)
      mathfieldAccessibilityRef.current?.sync()
    })
  }, [i18n.resolvedLanguage, open, t])

  useEffect(() => {
    if (!open) return
    const host = hostElement
    if (!host) return
    let cancelled = false
    let mountedMathfield: MathfieldElement | null = null
    let accessibilityController: ReturnType<typeof installFormulaMathfieldAccessibility> | null = null
    let virtualKeyboard: typeof window.mathVirtualKeyboard | undefined
    let syncKeyboardInset: (() => void) | undefined
    let scheduleKeyboardInset: (() => void) | undefined
    const keyboardSyncTimeouts: number[] = []
    setLatex(initialLatex)
    errorRef.current = ''
    setErrorKey(null)
    setLoadFailed(false)
    setKeyboardInset(0)
    setLoading(true)

    void import('mathlive')
      .then(({ MathfieldElement }) => {
        if (cancelled) return
        MathfieldElement.soundsDirectory = null
        MathfieldElement.fontsDirectory = null
        const locale = localeRef.current
        MathfieldElement.locale = locale
        const mathfield = new MathfieldElement()
        mountedMathfield = mathfield
        mathfield.className = 'aivory-formula-mathfield'
        mathfield.value = initialLatex
        mathfield.smartFence = true
        mathfield.smartMode = false
        mathfield.mathVirtualKeyboardPolicy = 'manual'
        mathfield.setAttribute('lang', locale)
        mathfield.setAttribute('theme', themeRef.current)
        mathfield.setAttribute('aria-label', labelsRef.current.input)
        mathfield.setAttribute('aria-describedby', errorId)
        const onInput = () => {
          setLatex(mathfield.getValue('latex'))
          errorRef.current = ''
          setErrorKey(null)
          accessibilityController?.sync()
        }
        mathfield.addEventListener('input', onInput)
        host.replaceChildren(mathfield)
        mathfieldRef.current = mathfield
        accessibilityController = installFormulaMathfieldAccessibility(mathfield, () => ({
          locale: localeRef.current,
          inputLabel: labelsRef.current.input,
          menuLabel: labelsRef.current.menu,
          keyboardLabel: labelsRef.current.keyboard,
          error: errorRef.current,
          errorId,
        }))
        mathfieldAccessibilityRef.current = accessibilityController
        if (window.mathVirtualKeyboard) {
          virtualKeyboard = window.mathVirtualKeyboard
          virtualKeyboard.layouts = ['numeric', 'symbols', 'alphabetic', 'greek']
          syncKeyboardInset = () => {
            const height = virtualKeyboard?.visible
              ? Math.ceil(virtualKeyboard.boundingRect.height)
              : 0
            setKeyboardInset(Math.min(height, window.innerHeight))
          }
          scheduleKeyboardInset = () => {
            keyboardSyncTimeouts.splice(0).forEach((timeout) => window.clearTimeout(timeout))
            syncKeyboardInset?.()
            keyboardSyncTimeouts.push(
              window.setTimeout(() => syncKeyboardInset?.(), 80),
              window.setTimeout(() => syncKeyboardInset?.(), 180),
              window.setTimeout(() => syncKeyboardInset?.(), 360),
            )
          }
          virtualKeyboard.addEventListener('geometrychange', scheduleKeyboardInset)
          virtualKeyboard.addEventListener('virtual-keyboard-toggle', scheduleKeyboardInset)
          mathfield.addEventListener('virtual-keyboard-toggle', scheduleKeyboardInset)
          window.addEventListener('resize', scheduleKeyboardInset)
          // MathLive announces the toggle before its animated plate always has
          // final geometry. Calibrate briefly while the dialog is open so the
          // footer remains above both the first frame and later layout changes.
          scheduleKeyboardInset()
        }
        setLoading(false)
        requestAnimationFrame(() => mathfield.focus())
      })
      .catch(() => {
        if (cancelled) return
        setLoading(false)
        setLoadFailed(true)
        setErrorKey('loadFailed')
        requestAnimationFrame(() => fallbackFocusRef.current?.focus())
      })

    return () => {
      cancelled = true
      accessibilityController?.dispose()
      if (mathfieldAccessibilityRef.current === accessibilityController) {
        mathfieldAccessibilityRef.current = null
      }
      if (virtualKeyboard && scheduleKeyboardInset) {
        virtualKeyboard.removeEventListener('geometrychange', scheduleKeyboardInset)
        virtualKeyboard.removeEventListener('virtual-keyboard-toggle', scheduleKeyboardInset)
        mountedMathfield?.removeEventListener('virtual-keyboard-toggle', scheduleKeyboardInset)
        window.removeEventListener('resize', scheduleKeyboardInset)
      }
      keyboardSyncTimeouts.forEach((timeout) => window.clearTimeout(timeout))
      virtualKeyboard?.hide({ animate: false })
      if (mountedMathfield) mountedMathfield.replaceChildren()
      mathfieldRef.current = null
      host.replaceChildren()
      setKeyboardInset(0)
    }
  }, [errorId, hostElement, initialLatex, loadAttempt, open])

  useEffect(() => {
    const mathfield = mathfieldRef.current
    if (!mathfield) return
    if (error && !loadFailed) {
      mathfield.setAttribute('aria-invalid', 'true')
      mathfield.setAttribute('aria-errormessage', errorId)
    } else {
      mathfield.removeAttribute('aria-invalid')
      mathfield.removeAttribute('aria-errormessage')
    }
    mathfieldAccessibilityRef.current?.sync()
  }, [error, errorId, loadFailed])

  const insertTemplate = (template: string) => {
    const mathfield = mathfieldRef.current
    if (!mathfield) return
    mathfield.insert(template, {
      insertionMode: 'replaceSelection',
      selectionMode: 'placeholder',
      focus: true,
      feedback: false,
    })
    setLatex(mathfield.getValue('latex'))
    errorRef.current = ''
    setErrorKey(null)
    mathfieldAccessibilityRef.current?.sync()
  }

  const confirm = () => {
    const value = (mathfieldRef.current?.getValue('latex') ?? latex).trim()
    if (!value) {
      window.mathVirtualKeyboard?.hide({ animate: false })
      const message = t('composer.formula.empty')
      errorRef.current = message
      setErrorKey('empty')
      mathfieldAccessibilityRef.current?.sync()
      mathfieldRef.current?.focus()
      return
    }
    if (/\\placeholder(?:\[[^\]]*\])?\{[^}]*\}/.test(value)) {
      window.mathVirtualKeyboard?.hide({ animate: false })
      const message = t('composer.formula.incomplete')
      errorRef.current = message
      setErrorKey('incomplete')
      mathfieldAccessibilityRef.current?.sync()
      mathfieldRef.current?.focus()
      return
    }
    onConfirm(value)
    onOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        size="lg"
        className="formula-editor-dialog max-sm:h-[calc(100dvh-var(--safe-top)-var(--safe-bottom))] max-sm:max-h-none max-sm:w-full max-sm:rounded-none"
        data-keyboard-open={keyboardInset > 0 ? 'true' : undefined}
        style={{ paddingBottom: keyboardInset > 0 ? `${keyboardInset}px` : undefined }}
        onOpenAutoFocus={(event) => {
          event.preventDefault()
          requestAnimationFrame(() => fallbackFocusRef.current?.focus())
        }}
        onPointerDownOutside={(event) => {
          if (isMathLiveOverlay(event.target)) event.preventDefault()
        }}
        onInteractOutside={(event) => {
          if (isMathLiveOverlay(event.target)) event.preventDefault()
        }}
      >
        <DialogHeader className="formula-editor-header px-4 pb-3 pt-4 sm:px-5 sm:pt-5">
          <DialogTitle className="flex items-center gap-2 pr-10 text-[1.125rem] font-semibold tracking-normal">
            <Sigma size={18} className="text-[var(--color-accent)]" aria-hidden />
            {editing ? t('composer.formula.editTitle') : t('composer.formula.title')}
          </DialogTitle>
          <DialogDescription className="sr-only">{t('composer.formula.description')}</DialogDescription>
        </DialogHeader>

        <DialogBody className="formula-editor-body flex min-h-0 flex-col gap-3 px-4 pb-3 sm:px-5">
          <div
            ref={fallbackFocusRef}
            tabIndex={-1}
            className={cn(
              'formula-mathfield-host relative min-h-[6rem] rounded-[10px] bg-[var(--color-surface-sunken)] p-3',
              'ring-1 ring-inset ring-[var(--color-border)] focus-within:ring-2 focus-within:ring-[var(--color-ring)]',
              error && 'ring-[var(--color-danger)] focus-within:ring-[var(--color-danger)]',
            )}
            aria-busy={loading || undefined}
            aria-label={t('composer.formula.inputLabel')}
            aria-describedby={errorId}
          >
            <div ref={setHostElement} className="min-h-[4.5rem]" />
            {loading ? (
              <div
                className="pointer-events-none absolute inset-3 h-[4.5rem] animate-pulse rounded-[8px] bg-[var(--color-bg-muted)]"
                aria-hidden
              />
            ) : null}
          </div>

          <div id={errorId} className="formula-editor-error min-h-5" aria-live="polite" role={loadFailed ? 'alert' : undefined}>
            {error ? (
              <div className="flex items-center justify-between gap-3 text-[0.8125rem] text-[var(--color-danger)]">
                <p>{error}</p>
                {loadFailed ? (
                  <button
                    type="button"
                    onClick={() => setLoadAttempt((attempt) => attempt + 1)}
                    className="formula-editor-retry min-h-9 shrink-0 rounded-[6px] px-2 py-1 font-medium hover:bg-[var(--color-danger-soft)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-sm:min-h-11"
                  >
                    {t('composer.retry')}
                  </button>
                ) : null}
              </div>
            ) : null}
          </div>

          <Tabs defaultValue="basic" className="formula-template-library flex min-h-0 flex-1 flex-col">
            <div className="overflow-x-auto overscroll-x-contain scrollbar-thin pb-1">
              <TabsList
                variant="segmented"
                aria-label={t('composer.formula.description')}
                className="w-max min-w-full justify-start"
              >
                {CATEGORIES.map((category) => (
                  <TabsTrigger key={category} value={category} variant="segmented" className="shrink-0 px-2.5 max-sm:min-h-11">
                    {t(`composer.formula.categories.${category}`)}
                  </TabsTrigger>
                ))}
              </TabsList>
            </div>
            {CATEGORIES.map((category) => (
              <TabsContent
                key={category}
                value={category}
                className="mt-2 min-h-0 flex-1 overflow-y-auto rounded-[10px] bg-[var(--color-bg-muted)] p-1.5"
              >
                <div
                  className={cn(
                    'grid gap-1',
                    category === 'matrices'
                      ? 'grid-cols-[repeat(auto-fill,minmax(8.5rem,1fr))]'
                      : category === 'calculus'
                        ? 'grid-cols-[repeat(auto-fill,minmax(6rem,1fr))]'
                        : 'grid-cols-[repeat(auto-fill,minmax(2.75rem,1fr))]',
                  )}
                >
                  {FORMULA_TEMPLATES[category].map((template, index) => (
                    <FormulaSymbolButton
                      key={`${category}-${template.preview}-${index}`}
                      template={template}
                      onInsert={insertTemplate}
                      label={t('composer.formula.insertSymbol')}
                    />
                  ))}
                </div>
              </TabsContent>
            ))}
          </Tabs>
        </DialogBody>

        <DialogFooter className="formula-editor-footer px-4 py-3 sm:px-5 [&>button]:max-sm:min-h-11">
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t('composer.formula.cancel')}
          </Button>
          <Button onClick={confirm} disabled={loading || loadFailed}>
            {editing ? t('composer.formula.update') : t('composer.formula.insert')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
