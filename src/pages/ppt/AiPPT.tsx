/**
 * AI PPT — the self-built UI (§ AI PPT API mode).
 *
 * Replaces the vendor iframe with our own four-step flow, backed entirely by our
 * server (which proxies Docmee, keeps the Api-Key and the vendor token, bills the
 * credit ledger and mirrors the rendered .pptx into the user's files):
 *
 *   1. 输入 (topic / pasted text / URL / upload / Markdown)
 *   2. 大纲 (streamed live over our own SSE, then editable)
 *   3. 模板 (our gallery; covers proxied through our origin)
 *   4. 成品 (preview, download, rename, change template, AI rewrite)
 *
 * Plus "我的 PPT", which lists our own deck records rather than the vendor's.
 */
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import {
  ArrowLeft,
  ArrowRight,
  Coins,
  Check,
  Download,
  FileUp,
  Loader2,
  Pencil,
  Presentation,
  RefreshCw,
  Sparkles,
  SlidersHorizontal,
  Square,
  Wand2,
  X,
} from 'lucide-react'

import { ApiError, aipptApi, authApi, streamSSE } from '@/api'
import type { ApiAiPPTDeck, ApiAiPPTStreamEvent, ApiAiPPTTemplate } from '@/api/types'
import { DocumentPreview } from '@/components/files/document-preview'
import { ContentHeader } from '@/components/layout/content-header'
import { DeckList } from '@/components/ppt/deck-list'
import { DocmeeEditorDialog } from '@/components/ppt/docmee-editor-dialog'
import { RenderStage } from '@/components/ppt/render-stage'
import { TemplatePicker } from '@/components/ppt/template-picker'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'
import { usePrefersReducedMotion } from '@/hooks/use-prefers-reduced-motion'
import { toast } from '@/hooks/use-toast'
import { normalizeLanguage } from '@/i18n'
import {
  AI_PPT_INPUT_TYPES,
  AI_PPT_LENGTHS,
  aiPPTOutlineStats,
  aiPPTOutlineWarnings,
  aiPPTTypeKey,
  parseAiPPTHeadings,
} from '@/lib/aippt-outline'
import { cn } from '@/lib/utils'
import { useAiPPT } from '@/store/aippt'

type View = 'create' | 'decks'
type Step = 'input' | 'outline' | 'template' | 'result'

interface CreateForm {
  length: string
  scene: string
  audience: string
  lang: string
  prompt: string
}

const PPTX_MIME = 'application/vnd.openxmlformats-officedocument.presentationml.presentation'

export default function AiPPT() {
  const { t, i18n } = useTranslation(['ppt', 'common'])
  const config = useAiPPT((s) => s.config)
  const configStatus = useAiPPT((s) => s.status)
  const configError = useAiPPT((s) => s.error)
  const available = useAiPPT((s) => s.available)
  const loadConfig = useAiPPT((s) => s.load)
  const setAvailable = useAiPPT((s) => s.setAvailable)

  const [view, setView] = useState<View>('create')
  const [step, setStep] = useState<Step>('input')
  const [deck, setDeck] = useState<ApiAiPPTDeck | null>(null)
  const [outline, setOutline] = useState('')
  const [streaming, setStreaming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [inputType, setInputType] = useState<number>(1)
  const [content, setContent] = useState('')
  const [upload, setUpload] = useState<File | null>(null)
  const [template, setTemplate] = useState<ApiAiPPTTemplate | null>(null)
  const [rewriteOpen, setRewriteOpen] = useState(false)
  const [rewriteQuestion, setRewriteQuestion] = useState('')
  const [insufficient, setInsufficient] = useState(false)
  const [refreshToken, setRefreshToken] = useState(0)
  /** Rendering animation state (see renderDeck): the official UI shows a beat here. */
  const [render, setRender] = useState({ active: false, progress: 0 })
  const reduceMotion = usePrefersReducedMotion()
  const [options, setOptions] = useState<Record<string, { name: string; value: string }[]>>({})
  /** A few system covers shown as inspiration on the input step. */
  const [previewTemplates, setPreviewTemplates] = useState<ApiAiPPTTemplate[]>([])
  const [form, setForm] = useState<CreateForm>(() => ({
    length: 'medium',
    scene: '',
    audience: '',
    lang: normalizeLanguage(i18n.language) ?? 'zh',
    prompt: '',
  }))

  const abortRef = useRef<AbortController | null>(null)
  const outlineRef = useRef('')
  const flushTimer = useRef<number | null>(null)

  useEffect(() => {
    void loadConfig()
  }, [loadConfig])

  // Vendor enumerations fill the form; a failure is non-fatal (free text stays).
  useEffect(() => {
    if (!config?.enabled) return
    let cancelled = false
    void aipptApi
      .options(form.lang)
      .then((result) => {
        if (!cancelled) setOptions(result.options ?? {})
      })
      .catch(() => undefined)
    return () => {
      cancelled = true
    }
  }, [config?.enabled, form.lang])

  useEffect(
    () => () => {
      abortRef.current?.abort()
      if (flushTimer.current !== null) window.clearTimeout(flushTimer.current)
    },
    [],
  )

  // The price the admin configured for one generation, and whether this
  // deployment actually charges it. `priced` keeps the setting visible to users
  // even while billing is off platform-wide (the charge switch is the global
  // credits_per_usd rate, shared with chat), so nobody has to guess the price.
  const price = config?.credits_per_ppt ?? 0
  const priced = price > 0
  const billed = Boolean(config?.credits_enabled && priced)
  const editPrice = config?.edit_credits ?? 0
  const editBilled = Boolean(config?.edit_credits_enabled && editPrice > 0)
  const maxUploadMB = config?.max_upload_mb ?? 50
  const langOptions = options.lang ?? []
  const sceneOptions = options.scene ?? []
  const audienceOptions = options.audience ?? []

  // One cached read of the catalogue: the input step shows a taste of the looks.
  useEffect(() => {
    if (!config?.enabled) return
    let cancelled = false
    void aipptApi
      .templates({ type: 1, size: 3 })
      .then((page) => {
        if (!cancelled) setPreviewTemplates(page.templates ?? [])
      })
      .catch(() => undefined)
    return () => {
      cancelled = true
    }
  }, [config?.enabled])

  const headings = useMemo(() => parseAiPPTHeadings(outline), [outline])
  const stats = useMemo(() => aiPPTOutlineStats(outline), [outline])
  const warnings = useMemo(() => aiPPTOutlineWarnings(outline), [outline])

  /**
   * Deltas arrive per token; flush them to state on a timer so a long outline
   * does not trigger thousands of React renders.
   */
  const pushDelta = useCallback((text: string) => {
    outlineRef.current += text
    if (flushTimer.current !== null) return
    flushTimer.current = window.setTimeout(() => {
      flushTimer.current = null
      setOutline(outlineRef.current)
    }, 80)
  }, [])

  const stopStreaming = useCallback(() => {
    abortRef.current?.abort()
    abortRef.current = null
    setStreaming(false)
  }, [])

  const runOutline = useCallback(
    async (deckID: string, payload: Record<string, unknown>) => {
      stopStreaming()
      const controller = new AbortController()
      abortRef.current = controller
      outlineRef.current = ''
      setOutline('')
      setStreaming(true)
      try {
        for await (const frame of streamSSE(
          `/me/ppt/decks/${encodeURIComponent(deckID)}/outline`,
          payload,
          controller.signal,
        )) {
          const event = frame.data as ApiAiPPTStreamEvent
          if (event?.type === 'delta' && event.text) {
            pushDelta(event.text)
          } else if (event?.type === 'done') {
            outlineRef.current = event.markdown ?? outlineRef.current
            setOutline(outlineRef.current)
            if (event.deck) setDeck(event.deck)
            if (typeof event.credits_available === 'number') setAvailable(event.credits_available)
            setStep('outline')
          } else if (event?.type === 'error') {
            if (event.code === 'insufficient_credits') setInsufficient(true)
            else toast.error(t('ppt:errors.generic'), event.message ?? undefined)
          }
        }
      } catch (err) {
        if (!controller.signal.aborted) {
          if (err instanceof ApiError && err.status === 402) setInsufficient(true)
          else toast.error(t('ppt:errors.upstream'), err instanceof Error ? err.message : undefined)
        }
      } finally {
        if (flushTimer.current !== null) {
          window.clearTimeout(flushTimer.current)
          flushTimer.current = null
        }
        setOutline(outlineRef.current)
        setStreaming(false)
        abortRef.current = null
      }
    },
    [pushDelta, setAvailable, stopStreaming, t],
  )

  const startGeneration = useCallback(async () => {
    if (inputType === 2 && !upload) {
      toast.warning(t('ppt:input.uploadMissing'))
      return
    }
    if (inputType !== 2 && !content.trim()) {
      toast.warning(t('ppt:input.contentMissing'))
      return
    }
    setBusy(true)
    try {
      const created =
        inputType === 2 && upload
          ? await aipptApi.createTaskFromFile(upload, { type: 2 })
          : await aipptApi.createTask({ type: inputType, content: content.trim() })
      setDeck(created.deck)
      setRefreshToken((value) => value + 1)
      // Hand off to the outline step as soon as the task exists: the streamed
      // text is the progress indicator, so the button must stop spinning now.
      setStep('outline')
      setBusy(false)
      await runOutline(created.deck.id, {
        length: form.length,
        scene: form.scene,
        audience: form.audience,
        lang: form.lang,
        prompt: form.prompt,
      })
    } catch (err) {
      toast.error(t('ppt:errors.generic'), err instanceof ApiError ? err.message : undefined)
    } finally {
      setBusy(false)
    }
  }, [content, form, inputType, runOutline, t, upload])

  const rewriteOutline = useCallback(async () => {
    if (!deck) return
    const question = rewriteQuestion.trim()
    if (!question) return
    setRewriteOpen(false)
    setRewriteQuestion('')
    // The outline panel shows the streaming rewrite; no modal spinner needed.
    setStep('outline')
    await runOutline(deck.id, { question, markdown: outlineRef.current || outline })
  }, [deck, outline, rewriteQuestion, runOutline])

  const renderDeck = useCallback(async () => {
    if (!deck) return
    setBusy(true)
    // The vendor exposes no per-page progress for generatePptx, so the stage is
    // staged and time-boxed: creep towards ~92% while the request is in flight,
    // snap to 100% when it returns, and keep a minimum visible beat so a fast
    // render does not flash the animation for 200ms.
    const startedAt = Date.now()
    const minimumBeat = reduceMotion ? 300 : 1400
    setRender({ active: true, progress: 6 })
    const timer = window.setInterval(() => {
      setRender((current) => (current.active ? { ...current, progress: Math.min(92, current.progress + 4) } : current))
    }, 120)
    try {
      const result = await aipptApi.generate(deck.id, {
        template_id: template?.id ?? deck.template_id,
        markdown: outlineRef.current || outline,
      })
      const elapsed = Date.now() - startedAt
      if (elapsed < minimumBeat) await new Promise((resolve) => setTimeout(resolve, minimumBeat - elapsed))
      setRender({ active: true, progress: 100 })
      setDeck(result.deck)
      setAvailable(result.credits_available)
      setRefreshToken((value) => value + 1)
      await new Promise((resolve) => setTimeout(resolve, reduceMotion ? 0 : 260))
      setStep('result')
      if (result.credits > 0) {
        toast.success(
          t('ppt:charged.title', { price: result.credits }),
          t('ppt:charged.description', { available: result.credits_available }),
        )
      }
    } catch (err) {
      if (err instanceof ApiError && err.status === 402) setInsufficient(true)
      else toast.error(t('ppt:errors.charge'), err instanceof ApiError ? err.message : undefined)
    } finally {
      window.clearInterval(timer)
      setRender({ active: false, progress: 0 })
      setBusy(false)
    }
  }, [deck, outline, reduceMotion, setAvailable, t, template])

  const openDeck = useCallback((next: ApiAiPPTDeck) => {
    setDeck(next)
    outlineRef.current = next.outline ?? ''
    setOutline(next.outline ?? '')
    setTemplate(next.template_id ? { id: next.template_id, name: next.template_name } : null)
    setView('create')
    setStep(next.status === 'ready' ? 'result' : next.outline ? 'outline' : 'input')
  }, [])

  const resetFlow = useCallback(() => {
    stopStreaming()
    setDeck(null)
    setOutline('')
    outlineRef.current = ''
    setTemplate(null)
    setContent('')
    setUpload(null)
    setStep('input')
  }, [stopStreaming])

  const creditChip = priced ? (
    <span className="inline-flex items-center gap-1.5 rounded-full bg-[var(--color-bg-muted)] px-2.5 py-1 text-xs text-[var(--color-fg-muted)]">
      <Coins size={13} aria-hidden />
      {billed
        ? t('ppt:price', { price })
        : t('ppt:priceInactive', { price, defaultValue: '{{price}} credits per deck · free right now' })}
      {billed ? (
        <>
          <span className="text-[var(--color-divider)]" aria-hidden>
            ·
          </span>
          {t('ppt:balance', { available })}
        </>
      ) : null}
    </span>
  ) : null

  let body: ReactNode
  if (configStatus === 'error') {
    body = (
      <EmptyState
        icon={<Presentation size={22} aria-hidden />}
        title={t('ppt:loadFailed.title')}
        description={configError ?? t('ppt:loadFailed.description')}
        action={
          <Button variant="outline" onClick={() => void loadConfig(true)}>
            {t('common:actions.tryAgain')}
          </Button>
        }
      />
    )
  } else if (!config) {
    body = <Skeleton className="h-full w-full rounded-[12px]" />
  } else if (!config.configured) {
    body = (
      <EmptyState
        icon={<Presentation size={22} aria-hidden />}
        title={t('ppt:unconfigured.title')}
        description={t('ppt:unconfigured.description')}
      />
    )
  } else if (!config.enabled) {
    body = (
      <EmptyState
        icon={<Presentation size={22} aria-hidden />}
        title={t('ppt:disabled.title')}
        description={t('ppt:disabled.description')}
      />
    )
  } else if (view === 'decks') {
    body = <DeckList onOpen={openDeck} refreshToken={refreshToken} />
  } else {
    body = (
      <div className="flex h-full min-h-0 flex-col gap-4">
        <StepCrumbs
          step={step}
          hasDeck={Boolean(deck)}
          onJump={(next) => (deck || next === 'input' ? setStep(next) : undefined)}
        />
        {step === 'input' ? (
          <StepInput
            previewTemplates={previewTemplates}
            price={price}
            billed={billed}
            inputType={inputType}
            setInputType={setInputType}
            content={content}
            setContent={setContent}
            upload={upload}
            setUpload={setUpload}
            form={form}
            setForm={setForm}
            langOptions={langOptions}
            sceneOptions={sceneOptions}
            audienceOptions={audienceOptions}
            maxUploadMB={maxUploadMB}
            busy={busy}
            onSubmit={() => void startGeneration()}
          />
        ) : null}
        {step === 'outline' ? (
          <StepOutline
            outline={outline}
            setOutline={(value) => {
              outlineRef.current = value
              setOutline(value)
            }}
            headings={headings}
            stats={stats}
            warnings={warnings}
            streaming={streaming}
            busy={busy}
            onStop={stopStreaming}
            onRewrite={() => setRewriteOpen(true)}
            onRegenerate={() => void startGeneration()}
            onNext={() => setStep('template')}
          />
        ) : null}
        {step === 'template' ? (
          render.active ? (
            <RenderStage
              template={template}
              subject={deck?.subject ?? ''}
              progress={render.progress}
              reduceMotion={reduceMotion}
            />
          ) : (
          <StepTemplate
            template={template}
            onSelect={setTemplate}
            onClearSelection={() => setTemplate(null)}
            busy={busy}
            price={price}
            billed={billed}
            onBack={() => setStep('outline')}
            onGenerate={() => void renderDeck()}
          />
          )
        ) : null}
        {step === 'result' && deck ? (
          <StepResult
            deck={deck}
            onDeckChange={setDeck}
            onDone={() => setRefreshToken((value) => value + 1)}
            onNew={resetFlow}
            onRewrite={() => {
              setStep('outline')
              setRewriteOpen(true)
            }}
            price={price}
            editPrice={editPrice}
            editBilled={editBilled}
          />
        ) : null}
      </div>
    )
  }

  return (
    <>
      <ContentHeader
        title={t('ppt:title')}
        fluid
        actions={
          <div className="flex items-center gap-2">
            {creditChip}
            <div className="hidden items-center rounded-full bg-[var(--color-bg-muted)] p-0.5 sm:flex">
              {(['create', 'decks'] as const).map((value) => (
                <button
                  key={value}
                  type="button"
                  onClick={() => setView(value)}
                  aria-pressed={view === value}
                  className={cn(
                    'rounded-full px-2.5 py-1 text-xs interactive',
                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                    view === value
                      ? 'bg-[var(--color-surface)] font-medium text-[var(--color-fg)]'
                      : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
                  )}
                >
                  {t(value === 'create' ? 'ppt:view.create' : 'ppt:view.decks')}
                </button>
              ))}
            </div>
          </div>
        }
      />

      <main className="min-h-0 flex-1 overflow-hidden px-3 pb-3 sm:px-6 sm:pb-6">
        <div className="mx-auto flex h-full min-h-0 w-full max-w-[var(--layout-content-max-w)] flex-col">{body}</div>
      </main>

      <Dialog open={insufficient} onOpenChange={setInsufficient}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('ppt:insufficient.title')}</DialogTitle>
            <DialogDescription>
              {t('ppt:insufficient.description', { price: price || editPrice, available })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setInsufficient(false)}>
              {t('common:actions.cancel')}
            </Button>
            <Button asChild>
              <a href="/subscription">{t('ppt:insufficient.action')}</a>
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={rewriteOpen} onOpenChange={setRewriteOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('ppt:outline.rewriteTitle')}</DialogTitle>
            <DialogDescription>
              {editBilled ? t('ppt:outline.rewriteBilled', { price: editPrice }) : t('ppt:outline.rewriteLead')}
            </DialogDescription>
          </DialogHeader>
          <Textarea
            value={rewriteQuestion}
            onChange={(event) => setRewriteQuestion(event.target.value)}
            placeholder={t('ppt:outline.rewritePlaceholder')}
            rows={3}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setRewriteOpen(false)}>
              {t('common:actions.cancel')}
            </Button>
            <Button disabled={!rewriteQuestion.trim() || busy} onClick={() => void rewriteOutline()}>
              {t('ppt:outline.rewrite')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

// ----- step crumbs ----------------------------------------------------------

function StepCrumbs({
  step,
  hasDeck,
  onJump,
}: {
  step: Step
  hasDeck: boolean
  onJump: (step: Step) => void
}) {
  const { t } = useTranslation('ppt')
  const steps: Step[] = ['input', 'outline', 'template', 'result']
  const currentIndex = steps.indexOf(step)
  return (
    <ol className="flex flex-wrap items-center gap-1.5 text-xs sm:gap-2">
      {steps.map((value, index) => {
        const disabled = value !== 'input' && !hasDeck
        const done = index < currentIndex
        const current = index === currentIndex
        return (
          <li key={value} className="flex items-center gap-1.5 sm:gap-2">
            <button
              type="button"
              disabled={disabled}
              onClick={() => onJump(value)}
              aria-current={current ? 'step' : undefined}
              className={cn(
                'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 interactive',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                current
                  ? 'border-[var(--color-accent)] bg-[var(--color-bg-muted)] font-medium text-[var(--color-fg)]'
                  : done
                    ? 'border-transparent text-[var(--color-fg)]'
                    : 'border-[var(--color-border)] text-[var(--color-fg-muted)]',
                'disabled:cursor-not-allowed disabled:opacity-50',
              )}
            >
              <span
                className={cn(
                  'inline-flex size-4 items-center justify-center rounded-full text-[10px]',
                  current
                    ? 'bg-[var(--color-accent)] text-[var(--color-accent-fg)]'
                    : done
                      ? 'bg-[var(--color-accent)]/15 text-[var(--color-accent)]'
                      : 'bg-[var(--color-bg-muted)]',
                )}
              >
                {done ? <Check size={10} aria-hidden /> : index + 1}
              </span>
              {t(`ppt:steps.${value}`)}
            </button>
            {index < steps.length - 1 ? (
              <span
                aria-hidden
                className={cn('h-px w-4 sm:w-6', done ? 'bg-[var(--color-accent)]/40' : 'bg-[var(--color-divider)]')}
              />
            ) : null}
          </li>
        )
      })}
    </ol>
  )
}

// ----- step 1: input --------------------------------------------------------

interface StepInputProps {
  previewTemplates: ApiAiPPTTemplate[]
  price: number
  billed: boolean
  inputType: number
  setInputType: (value: number) => void
  content: string
  setContent: (value: string) => void
  upload: File | null
  setUpload: (file: File | null) => void
  form: CreateForm
  setForm: (value: CreateForm) => void
  langOptions: { name: string; value: string }[]
  sceneOptions: { name: string; value: string }[]
  audienceOptions: { name: string; value: string }[]
  maxUploadMB: number
  busy: boolean
  onSubmit: () => void
}

function StepInput(props: StepInputProps) {
  const { t } = useTranslation('ppt')
  const {
    previewTemplates, price, billed, inputType, setInputType, content, setContent, upload, setUpload,
    form, setForm, langOptions, sceneOptions, audienceOptions, maxUploadMB, busy, onSubmit,
  } = props
  const fileInput = useRef<HTMLInputElement | null>(null)
  const typeKey = aiPPTTypeKey(inputType)

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="grid items-start gap-5 lg:grid-cols-[1.2fr_0.8fr]">
        <section className="rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] p-5 shadow-[var(--shadow-xs)]">
          <h2 className="flex items-center gap-2 text-[15px] font-medium text-[var(--color-fg)]">
            <Sparkles size={15} aria-hidden className="text-[var(--color-accent)]" />
            {t('ppt:input.title')}
          </h2>
          <p className="mt-1.5 text-xs leading-relaxed text-[var(--color-fg-muted)]">{t('ppt:input.lead')}</p>

          <div className="mt-4 flex flex-wrap gap-1.5">
            {AI_PPT_INPUT_TYPES.map((value) => {
              const key = aiPPTTypeKey(value)
              return (
                <button
                  key={value}
                  type="button"
                  disabled={busy}
                  onClick={() => setInputType(value)}
                  aria-pressed={inputType === value}
                  className={cn(
                    'rounded-full border px-3 py-1 text-xs interactive',
                    inputType === value
                      ? 'border-[var(--color-accent)] bg-[var(--color-bg-muted)] text-[var(--color-fg)]'
                      : 'border-[var(--color-border)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
                  )}
                >
                  {t(`ppt:input.types.${key}`)}
                </button>
              )
            })}
          </div>

          <div className="mt-4">
            {inputType === 2 ? (
              <div
                onDragOver={(event) => event.preventDefault()}
                onDrop={(event) => {
                  event.preventDefault()
                  const file = event.dataTransfer.files?.[0]
                  if (file) setUpload(file)
                }}
                className="flex flex-col items-center justify-center gap-2 rounded-[10px] border border-dashed border-[var(--color-border-strong)] px-4 py-10 text-center"
              >
                <FileUp size={20} aria-hidden className="text-[var(--color-fg-muted)]" />
                <p className="text-sm text-[var(--color-fg)]">
                  {upload ? upload.name : t('ppt:input.uploadHint', { mb: maxUploadMB })}
                </p>
                <p className="text-xs text-[var(--color-fg-muted)]">{t('ppt:input.uploadTypes')}</p>
                <input
                  ref={fileInput}
                  type="file"
                  className="hidden"
                  accept=".docx,.doc,.pdf,.txt,.md,.markdown,.pptx,.ppt"
                  onChange={(event) => setUpload(event.target.files?.[0] ?? null)}
                />
                <div className="mt-1 flex gap-2">
                  <Button size="sm" variant="secondary" disabled={busy} onClick={() => fileInput.current?.click()}>
                    {t('ppt:input.uploadPick')}
                  </Button>
                  {upload ? (
                    <Button size="sm" variant="ghost" onClick={() => setUpload(null)}>
                      <X size={13} aria-hidden className="mr-1" />
                      {t('common:actions.clear')}
                    </Button>
                  ) : null}
                </div>
              </div>
            ) : (
              <Textarea
                value={content}
                disabled={busy}
                rows={inputType === 1 ? 3 : 10}
                maxLength={inputType === 1 ? 500 : 8000}
                onChange={(event) => setContent(event.target.value)}
                placeholder={t(`ppt:input.placeholders.${typeKey}`)}
              />
            )}
          </div>

          {/* Quick starts: one click fills the topic (only where free text applies). */}
          {(inputType === 1 || inputType === 6) && (
            <div className="mt-4">
              <p className="text-xs text-[var(--color-fg-muted)]">{t('ppt:input.examples.label')}</p>
              <div className="mt-2 flex flex-wrap gap-1.5">
                {(['one', 'two', 'three'] as const).map((key) => (
                  <button
                    key={key}
                    type="button"
                    disabled={busy}
                    onClick={() => setContent(t(`ppt:input.examples.${key}`))}
                    className={cn(
                      'rounded-full border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-1 text-xs',
                      'text-[var(--color-fg-muted)] interactive hover:border-[var(--color-border-strong)] hover:text-[var(--color-fg)]',
                      'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                    )}
                  >
                    {t(`ppt:input.examples.${key}`)}
                  </button>
                ))}
              </div>
            </div>
          )}

          <div className="mt-5 flex flex-wrap items-center justify-between gap-3 border-t border-[var(--color-divider)] pt-4">
            <p className="text-xs text-[var(--color-fg-muted)]">
              {billed
                ? t('ppt:input.priceHint', { price })
                : price > 0
                  ? t('ppt:input.freeHintPriced', {
                      price,
                      defaultValue: 'Free on this deployment right now (the configured price is {{price}} credits per deck).',
                    })
                  : t('ppt:input.freeHint')}
            </p>
            <Button disabled={busy} onClick={onSubmit}>
              {busy ? (
                <Loader2 size={14} aria-hidden className="mr-1.5 animate-spin" />
              ) : (
                <Sparkles size={14} aria-hidden className="mr-1.5" />
              )}
              {t('ppt:input.submit')}
            </Button>
          </div>
        </section>

        <section className="rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] p-5 shadow-[var(--shadow-xs)]">
          <h2 className="flex items-center gap-2 text-[15px] font-medium text-[var(--color-fg)]">
            <SlidersHorizontal size={15} aria-hidden className="text-[var(--color-fg-muted)]" />
            {t('ppt:input.optionsTitle')}
          </h2>
          <p className="mt-1.5 text-xs leading-relaxed text-[var(--color-fg-muted)]">{t('ppt:input.optionsLead')}</p>

          <div className="mt-4 grid gap-4">
            <Field label={t('ppt:input.length')} htmlFor="aippt-length">
              <div className="inline-flex items-center rounded-full bg-[var(--color-bg-muted)] p-0.5">
                {AI_PPT_LENGTHS.map((value) => (
                  <button
                    key={value}
                    type="button"
                    disabled={busy}
                    onClick={() => setForm({ ...form, length: value })}
                    aria-pressed={form.length === value}
                    className={cn(
                      'rounded-full px-3 py-1 text-xs interactive',
                      form.length === value
                        ? 'bg-[var(--color-surface)] font-medium text-[var(--color-fg)]'
                        : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
                    )}
                  >
                    {t(`ppt:input.lengths.${value}`)}
                  </button>
                ))}
              </div>
            </Field>

            <Field label={t('ppt:input.lang')} htmlFor="aippt-lang">
              <select
                id="aippt-lang"
                disabled={busy}
                value={form.lang}
                onChange={(event) => setForm({ ...form, lang: event.target.value })}
                className="h-9 w-full rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 text-sm text-[var(--color-fg)]"
              >
                {(langOptions.length > 0 ? langOptions : [{ name: form.lang, value: form.lang }]).map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.name}
                  </option>
                ))}
              </select>
            </Field>

            {sceneOptions.length > 0 ? (
              <Field label={t('ppt:input.scene')} htmlFor="aippt-scene">
                <select
                  id="aippt-scene"
                  disabled={busy}
                  value={form.scene}
                  onChange={(event) => setForm({ ...form, scene: event.target.value })}
                  className="h-9 w-full rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 text-sm text-[var(--color-fg)]"
                >
                  <option value="">{t('ppt:input.anyOption')}</option>
                  {sceneOptions.map((option) => (
                    <option key={option.value} value={option.value}>
                      {option.name}
                    </option>
                  ))}
                </select>
              </Field>
            ) : null}

            {audienceOptions.length > 0 ? (
              <Field label={t('ppt:input.audience')} htmlFor="aippt-audience">
                <select
                  id="aippt-audience"
                  disabled={busy}
                  value={form.audience}
                  onChange={(event) => setForm({ ...form, audience: event.target.value })}
                  className="h-9 w-full rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 text-sm text-[var(--color-fg)]"
                >
                  <option value="">{t('ppt:input.anyOption')}</option>
                  {audienceOptions.map((option) => (
                    <option key={option.value} value={option.value}>
                      {option.name}
                    </option>
                  ))}
                </select>
              </Field>
            ) : null}

            <Field
              label={t('ppt:input.prompt')}
              htmlFor="aippt-prompt"
              hint={t('ppt:input.promptHint', { count: form.prompt.length })}
            >
              <Input
                id="aippt-prompt"
                value={form.prompt}
                disabled={busy}
                maxLength={50}
                onChange={(event) => setForm({ ...form, prompt: event.target.value })}
                placeholder={t('ppt:input.promptPlaceholder')}
              />
            </Field>
          </div>

          {/* A taste of the catalogue: the covers double as a preview of what the
              next step offers, and give this column visual weight. */}
          {previewTemplates.length > 0 ? (
            <div className="mt-5 border-t border-[var(--color-divider)] pt-4">
              <p className="text-xs text-[var(--color-fg-muted)]">{t('ppt:input.defaultTemplate.label')}</p>
              <div className="mt-2 grid grid-cols-3 gap-2">
                {previewTemplates.slice(0, 3).map((item) => (
                  <div
                    key={item.id}
                    className="aspect-[16/9] overflow-hidden rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)]"
                  >
                    {item.coverUrl ? (
                      <img
                        src={aipptApi.resourceUrl(item.coverUrl)}
                        alt={item.name}
                        loading="lazy"
                        className="h-full w-full object-cover"
                      />
                    ) : null}
                  </div>
                ))}
              </div>
              <p className="mt-2 text-[11px] leading-relaxed text-[var(--color-fg-muted)]">
                {t('ppt:input.defaultTemplate.hint')}
              </p>
            </div>
          ) : null}
        </section>
      </div>
    </div>
  )
}

// ----- step 2: outline ------------------------------------------------------

interface StepOutlineProps {
  outline: string
  setOutline: (value: string) => void
  headings: ReturnType<typeof parseAiPPTHeadings>
  stats: ReturnType<typeof aiPPTOutlineStats>
  warnings: string[]
  streaming: boolean
  busy: boolean
  onStop: () => void
  onRewrite: () => void
  onRegenerate: () => void
  onNext: () => void
}

function StepOutline(props: StepOutlineProps) {
  const { t } = useTranslation('ppt')
  const {
    outline, setOutline, headings, stats, warnings, streaming, busy,
    onStop, onRewrite, onRegenerate, onNext,
  } = props
  return (
    <div className="grid min-h-0 flex-1 gap-4 lg:grid-cols-[1.4fr_0.6fr]">
      <section className="flex min-h-0 flex-col rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)]">
        <div className="flex flex-wrap items-center gap-2 border-b border-[var(--color-divider)] px-4 py-2.5">
          <h2 className="text-sm font-medium text-[var(--color-fg)]">{t('ppt:outline.title')}</h2>
          <span className="text-xs text-[var(--color-fg-muted)]">
            {t('ppt:outline.counts', { chapters: stats.chapters, pages: stats.pages })}
          </span>
          {streaming ? (
            <span className="inline-flex items-center gap-1.5 text-xs text-[var(--color-accent)]">
              <Loader2 size={12} aria-hidden className="animate-spin" />
              {t('ppt:outline.streaming')}
            </span>
          ) : null}
          <div className="ml-auto flex items-center gap-1.5">
            {streaming ? (
              <Button size="sm" variant="outline" onClick={onStop}>
                <Square size={12} aria-hidden className="mr-1.5" />
                {t('common:actions.stop')}
              </Button>
            ) : (
              <>
                <Button size="sm" variant="ghost" disabled={busy} onClick={onRegenerate}>
                  <RefreshCw size={13} aria-hidden className="mr-1.5" />
                  {t('ppt:outline.regenerate')}
                </Button>
                <Button size="sm" variant="secondary" disabled={busy || !outline.trim()} onClick={onRewrite}>
                  <Wand2 size={13} aria-hidden className="mr-1.5" />
                  {t('ppt:outline.rewrite')}
                </Button>
                <Button size="sm" disabled={busy || !outline.trim()} onClick={onNext}>
                  {t('ppt:outline.next')}
                  <ArrowRight size={13} aria-hidden className="ml-1.5" />
                </Button>
              </>
            )}
          </div>
        </div>
        {warnings.length > 0 && !streaming ? (
          <ul className="flex flex-wrap gap-2 border-b border-[var(--color-divider)] px-4 py-2 text-xs text-[var(--color-fg-muted)]">
            {warnings.map((warning) => (
              <li key={warning} className="rounded-full bg-[var(--color-bg-muted)] px-2 py-0.5">
                {t(`ppt:outline.warnings.${warning}`)}
              </li>
            ))}
          </ul>
        ) : null}
        <Textarea
          value={outline}
          onChange={(event) => setOutline(event.target.value)}
          spellCheck={false}
          autoFocus={streaming}
          className="min-h-0 flex-1 resize-none rounded-none border-0 font-mono text-[13px] leading-relaxed focus-visible:ring-0"
          placeholder={t('ppt:outline.placeholder')}
        />
        {/* The streamed text IS the progress bar; a bare spinner told the user
            nothing while the model was already writing. */}
        {streaming && outline.length === 0 ? (
          <p className="flex items-center gap-2 px-4 py-3 text-xs text-[var(--color-fg-muted)]">
            <Loader2 size={12} aria-hidden className="animate-spin" />
            {t('ppt:outline.waiting')}
          </p>
        ) : null}
        {streaming && outline.length > 0 ? (
          <p className="px-4 pb-2 text-right text-[11px] text-[var(--color-fg-muted)]">
            {t('ppt:outline.progress', { count: outline.length })}
          </p>
        ) : null}
      </section>

      <aside className="min-h-0 overflow-y-auto rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
        <h3 className="text-sm font-medium text-[var(--color-fg)]">{t('ppt:outline.structure')}</h3>
        <p className="mt-1 text-xs text-[var(--color-fg-muted)]">
          {t('ppt:outline.structureHint', { paragraphs: stats.paragraphs })}
        </p>
        <ul className="mt-3 space-y-1">
          {headings.length === 0 ? (
            <li className="text-xs text-[var(--color-fg-muted)]">{t('ppt:outline.empty')}</li>
          ) : (
            headings.map((heading, index) => (
              <li
                key={`${heading.line}-${index}`}
                className={cn(
                  'truncate text-xs',
                  heading.level === 1 && 'font-medium text-[var(--color-fg)]',
                  heading.level === 2 && 'text-[var(--color-fg)]',
                  heading.level === 3 && 'pl-3 text-[var(--color-fg-muted)]',
                  heading.level === 4 && 'pl-6 text-[var(--color-fg-muted)]',
                )}
                title={heading.text}
              >
                {heading.text}
              </li>
            ))
          )}
        </ul>
      </aside>
    </div>
  )
}

// ----- step 3: template -----------------------------------------------------

interface StepTemplateProps {
  template: ApiAiPPTTemplate | null
  onSelect: (template: ApiAiPPTTemplate) => void
  /** Drops the selection when the chosen template is deleted from the gallery. */
  onClearSelection: () => void
  busy: boolean
  price: number
  billed: boolean
  onBack: () => void
  onGenerate: () => void
}

function StepTemplate(props: StepTemplateProps) {
  const { t } = useTranslation('ppt')
  const { template, onSelect, onClearSelection, busy, price, billed, onBack, onGenerate } = props
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-sm font-medium text-[var(--color-fg)]">{t('ppt:template.title')}</h2>
        <span className="text-xs text-[var(--color-fg-muted)]">
          {template ? t('ppt:template.selected', { name: template.name }) : t('ppt:template.lead')}
        </span>
        <div className="ml-auto flex items-center gap-2">
          <Button size="sm" variant="ghost" disabled={busy} onClick={onBack}>
            <ArrowLeft size={13} aria-hidden className="mr-1.5" />
            {t('ppt:template.back')}
          </Button>
          <Button size="sm" disabled={busy} onClick={onGenerate}>
            {busy ? (
              <Loader2 size={14} aria-hidden className="mr-1.5 animate-spin" />
            ) : (
              <Presentation size={14} aria-hidden className="mr-1.5" />
            )}
            {billed ? t('ppt:template.generateBilled', { price }) : t('ppt:template.generate')}
          </Button>
        </div>
      </div>
      <TemplatePicker
        selectedId={template?.id ?? null}
        onSelect={onSelect}
        onDeleted={(id) => {
          if (template?.id === id) onClearSelection()
        }}
        disabled={busy}
      />
    </div>
  )
}

// ----- step 4: result -------------------------------------------------------

interface StepResultProps {
  deck: ApiAiPPTDeck
  onDeckChange: (deck: ApiAiPPTDeck) => void
  onDone: () => void
  onNew: () => void
  onRewrite: () => void
  /** Admin-configured price of one generation (shown even while it is not charged). */
  price: number
  editPrice: number
  editBilled: boolean
}

function StepResult(props: StepResultProps) {
  const { t } = useTranslation(['ppt', 'common'])
  const { deck, onDeckChange, onDone, onNew, onRewrite, price, editPrice, editBilled } = props
  const [renaming, setRenaming] = useState(false)
  const [subject, setSubject] = useState(deck.subject)
  const [previewData, setPreviewData] = useState<ArrayBuffer | null>(null)
  const [previewOpen, setPreviewOpen] = useState(false)
  const [previewError, setPreviewError] = useState<string | null>(null)
  const [previewLoading, setPreviewLoading] = useState(false)
  const [busy, setBusy] = useState(false)
  const [templateOpen, setTemplateOpen] = useState(false)
  const [template, setTemplate] = useState<ApiAiPPTTemplate | null>(null)
  const [editorOpen, setEditorOpen] = useState(false)

  useEffect(() => setSubject(deck.subject), [deck.subject])

  const cover = deck.cover_url ? aipptApi.resourceUrl(deck.cover_url) : null

  async function fetchFile(): Promise<Blob | null> {
    if (!deck.file_id) return null
    return authApi.myFileContentBlob('file', deck.file_id)
  }

  async function loadPreviewData() {
    setPreviewError(null)
    setPreviewLoading(true)
    try {
      const blob = await fetchFile()
      if (!blob) {
        setPreviewError(t('ppt:result.mirrorPending'))
        return
      }
      setPreviewData(await blob.arrayBuffer())
    } catch (err) {
      setPreviewError(err instanceof Error ? err.message : t('ppt:result.previewFailed'))
    } finally {
      setPreviewLoading(false)
    }
  }

  async function openPreview() {
    setPreviewOpen(true)
    if (!previewData) await loadPreviewData()
  }

  // Load the rendered deck as soon as this step shows, so the panel is useful
  // without an extra click (the file is a few hundred KB, read from our origin).
  useEffect(() => {
    if (deck.file_id && !previewData && !previewLoading && !previewError) {
      void loadPreviewData()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [deck.file_id])

  async function download() {
    try {
      const blob = await fetchFile()
      if (!blob) {
        toast.warning(t('ppt:result.mirrorPending'))
        return
      }
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = `${deck.subject || 'AI PPT'}.pptx`
      document.body.appendChild(anchor)
      anchor.click()
      anchor.remove()
      URL.revokeObjectURL(url)
    } catch (err) {
      toast.error(t('ppt:errors.generic'), err instanceof Error ? err.message : undefined)
    }
  }

  async function saveRename() {
    const value = subject.trim()
    if (!value || value === deck.subject) {
      setRenaming(false)
      return
    }
    setBusy(true)
    try {
      const result = await aipptApi.renameDeck(deck.id, value)
      onDeckChange(result.deck)
      toast.success(t('ppt:result.renamed'))
    } catch (err) {
      toast.error(t('ppt:errors.generic'), err instanceof ApiError ? err.message : undefined)
    } finally {
      setBusy(false)
      setRenaming(false)
    }
  }

  async function applyTemplate() {
    if (!template) return
    setBusy(true)
    try {
      const result = await aipptApi.changeTemplate(deck.id, template.id)
      onDeckChange(result.deck)
      setTemplateOpen(false)
      setTemplate(null)
      onDone()
      toast.success(
        editBilled ? t('ppt:result.templateChangedBilled', { price: editPrice }) : t('ppt:result.templateChanged'),
      )
    } catch (err) {
      toast.error(t('ppt:errors.upstream'), err instanceof ApiError ? err.message : undefined)
    } finally {
      setBusy(false)
    }
  }

  async function retryMirror() {
    setBusy(true)
    try {
      const result = await aipptApi.generate(deck.id, { template_id: deck.template_id, markdown: deck.outline })
      onDeckChange(result.deck)
      onDone()
    } catch (err) {
      toast.error(t('ppt:errors.generic'), err instanceof ApiError ? err.message : undefined)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="grid min-h-0 flex-1 gap-4 lg:grid-cols-[0.9fr_1.1fr]">
      <section className="min-h-0 overflow-y-auto rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
        <div className="aspect-[16/9] w-full overflow-hidden rounded-[10px] bg-[var(--color-bg-muted)]">
          {cover ? (
            <img src={cover} alt="" className="h-full w-full object-cover" />
          ) : (
            <div className="flex h-full w-full items-center justify-center text-[var(--color-fg-muted)]">
              <Presentation size={22} aria-hidden />
            </div>
          )}
        </div>

        <div className="mt-3 flex items-center gap-2">
          {renaming ? (
            <Input
              value={subject}
              autoFocus
              maxLength={120}
              onChange={(event) => setSubject(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter') void saveRename()
                if (event.key === 'Escape') setRenaming(false)
              }}
              onBlur={() => void saveRename()}
            />
          ) : (
            <>
              <h2 className="min-w-0 flex-1 truncate text-sm font-medium text-[var(--color-fg)]">
                {deck.subject || t('ppt:result.untitled')}
              </h2>
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={t('ppt:result.rename')}
                onClick={() => setRenaming(true)}
              >
                <Pencil size={13} aria-hidden />
              </Button>
            </>
          )}
        </div>
        <p className="mt-1 text-xs text-[var(--color-fg-muted)]">
          {deck.template_name || deck.template_id || t('ppt:result.defaultTemplate')}
          {deck.credits > 0 ? ` · ${t('ppt:decks.credits', { credits: deck.credits })}` : ''}
        </p>
        {/* Summary strip: what this deck is, what it cost and where the file is. */}
        <dl className="mt-3 grid grid-cols-3 gap-2 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5 text-xs">
          <div className="min-w-0">
            <dt className="text-[11px] text-[var(--color-fg-muted)]">{t('ppt:resultSummary.template')}</dt>
            <dd className="truncate text-[var(--color-fg)]">
              {deck.template_name || deck.template_id || t('ppt:result.defaultTemplate')}
            </dd>
          </div>
          <div className="min-w-0">
            <dt className="text-[11px] text-[var(--color-fg-muted)]">{t('ppt:resultSummary.credits')}</dt>
            <dd className="text-[var(--color-fg)]">{deck.credits > 0 ? deck.credits : '—'}</dd>
          </div>
          <div className="min-w-0">
            <dt className="text-[11px] text-[var(--color-fg-muted)]">{t('ppt:resultSummary.saved')}</dt>
            <dd className="truncate text-[var(--color-fg)]">
              {deck.file_id ? t('ppt:resultSummary.saved') : t('ppt:resultSummary.saving')}
            </dd>
          </div>
        </dl>
        {/* The admin-configured per-deck price stays visible even while the
            deployment does not charge it, so the cost of a generation is never a
            surprise. */}
        {price > 0 ? (
          <p className="mt-2 text-[11px] text-[var(--color-fg-subtle)]">
            {t('ppt:result.priceNote', {
              price,
              defaultValue: 'Admin-configured price: {{price}} credits per deck.',
            })}
          </p>
        ) : null}
        {deck.error ? <p className="mt-2 text-xs text-[var(--color-danger)]">{deck.error}</p> : null}

        <div className="mt-4 flex flex-wrap gap-2">
          <Button size="sm" disabled={busy || !deck.file_id} onClick={() => void download()}>
            <Download size={13} aria-hidden className="mr-1.5" />
            {t('ppt:result.download')}
          </Button>
          {/* Slide-level editing is the vendor editor's job; our own UI owns
              creation, templates and the outline. */}
          <Button size="sm" variant="secondary" disabled={busy || !deck.ppt_id} onClick={() => setEditorOpen(true)}>
            <Pencil size={13} aria-hidden className="mr-1.5" />
            {t('ppt:result.edit')}
          </Button>
          <Button size="sm" variant="secondary" disabled={busy || !deck.file_id} onClick={() => void openPreview()}>
            {t('ppt:result.preview')}
          </Button>
          {!deck.file_id && deck.status === 'ready' ? (
            <Button size="sm" variant="outline" disabled={busy} onClick={() => void retryMirror()}>
              <RefreshCw size={13} aria-hidden className="mr-1.5" />
              {t('ppt:result.retrySave')}
            </Button>
          ) : null}
          <Button size="sm" variant="outline" disabled={busy} onClick={() => setTemplateOpen(true)}>
            {t('ppt:result.changeTemplate')}
          </Button>
          <Button size="sm" variant="ghost" disabled={busy} onClick={onRewrite}>
            <Wand2 size={13} aria-hidden className="mr-1.5" />
            {t('ppt:outline.rewrite')}
          </Button>
          <Button size="sm" variant="ghost" onClick={onNew}>
            <Sparkles size={13} aria-hidden className="mr-1.5" />
            {t('ppt:result.newDeck')}
          </Button>
        </div>
      </section>

      <section className="hidden min-h-0 flex-col overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] lg:flex">
        <div className="shrink-0 border-b border-[var(--color-divider)] px-4 py-2.5">
          <h3 className="text-sm font-medium text-[var(--color-fg)]">{t('ppt:result.previewTitle')}</h3>
          <p className="mt-0.5 text-xs text-[var(--color-fg-muted)]">{t('ppt:result.previewLead')}</p>
        </div>
        {/* The renderer fills this box and scrolls internally, so it needs a
            definite height — otherwise only the first slide is reachable. */}
        <div className="min-h-0 flex-1">
          <DocumentPreview
            name={`${deck.subject || 'AI PPT'}.pptx`}
            mimeType={PPTX_MIME}
            backendKind="doc"
            data={previewData ?? undefined}
            loading={previewLoading}
            error={previewError ?? undefined}
            onRetry={() => void loadPreviewData()}
          />
        </div>
      </section>

      <Dialog open={previewOpen} onOpenChange={setPreviewOpen}>
        <DialogContent className="max-w-5xl">
          <DialogHeader>
            <DialogTitle>{deck.subject || t('ppt:result.untitled')}</DialogTitle>
            <DialogDescription>{t('ppt:result.previewScrollHint')}</DialogDescription>
          </DialogHeader>
          <div className="h-[70vh] overflow-hidden rounded-[10px] border border-[var(--color-border)]">
            <DocumentPreview
              name={`${deck.subject || 'AI PPT'}.pptx`}
              mimeType={PPTX_MIME}
              backendKind="doc"
              data={previewData ?? undefined}
              loading={previewLoading}
              error={previewError ?? undefined}
              onRetry={() => void loadPreviewData()}
            />
          </div>
        </DialogContent>
      </Dialog>

      <Dialog open={templateOpen} onOpenChange={setTemplateOpen}>
        <DialogContent className="max-w-4xl">
          <DialogHeader>
            <DialogTitle>{t('ppt:result.changeTemplate')}</DialogTitle>
            <DialogDescription>
              {editBilled
                ? t('ppt:result.changeTemplateBilled', { price: editPrice })
                : t('ppt:result.changeTemplateLead')}
            </DialogDescription>
          </DialogHeader>
          <div className="h-[55vh] min-h-[280px]">
            <TemplatePicker
              selectedId={template?.id ?? deck.template_id}
              onSelect={setTemplate}
              onDeleted={(id) => {
                if (template?.id === id) setTemplate(null)
              }}
              disabled={busy}
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setTemplateOpen(false)}>
              {t('common:actions.cancel')}
            </Button>
            <Button disabled={busy || !template} onClick={() => void applyTemplate()}>
              {t('ppt:result.applyTemplate')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* The vendor's editor, used only for slide-level editing. */}
      <DocmeeEditorDialog
        deck={editorOpen ? deck : null}
        onClose={() => setEditorOpen(false)}
        onSynced={(next) => {
          onDeckChange(next)
          onDone()
          // The bytes changed: drop the cached preview so the panel reloads.
          setPreviewData(null)
          setPreviewError(null)
        }}
      />
    </div>
  )
}
