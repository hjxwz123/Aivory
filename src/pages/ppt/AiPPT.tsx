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
  ChevronDown,
  Coins,
  Check,
  Download,
  FileUp,
  FileText,
  Link2,
  ListTree,
  Loader2,
  Pencil,
  Presentation,
  Plus,
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
  DialogBody,
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { SegmentedControl } from '@/components/ui/segmented-control'
import { ThemeToggle } from '@/components/ui/theme-toggle'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/hooks/use-toast'
import { normalizeLanguage, SUPPORTED_LANGUAGES } from '@/i18n'
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
import { useWorkspaces } from '@/store/workspaces'
import { subscribeAccessInvalidation } from '@/lib/access-events'

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
  const activeWorkspaceId = useWorkspaces((state) => state.activeId)
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
  const [rendering, setRendering] = useState(false)
  const [options, setOptions] = useState<Record<string, { name: string; value: string }[]>>({})
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
    abortRef.current?.abort()
    setStreaming(false)
    setBusy(false)
    setDeck(null)
    setOutline('')
    setView('create')
    setStep('input')
    void loadConfig()
  }, [activeWorkspaceId, loadConfig])

  useEffect(() => subscribeAccessInvalidation(() => { void loadConfig(true) }), [loadConfig])

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
          aipptApi.scopedPath(`/me/ppt/decks/${encodeURIComponent(deckID)}/outline`),
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
    setRendering(true)
    try {
      const result = await aipptApi.generate(deck.id, {
        template_id: template?.id ?? deck.template_id,
        markdown: outlineRef.current || outline,
      })
      setDeck(result.deck)
      setAvailable(result.credits_available)
      setRefreshToken((value) => value + 1)
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
      setRendering(false)
      setBusy(false)
    }
  }, [deck, outline, setAvailable, t, template])

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
    setView('create')
  }, [stopStreaming])

  const creditChip = priced ? (
    <span className="inline-flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs text-[var(--color-fg-muted)]">
      <Coins size={14} aria-hidden className="shrink-0" />
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
  } else if (config.allowed === false) {
    body = (
      <EmptyState
        icon={<Presentation size={22} aria-hidden />}
        title={t('ppt:permissionDenied.title')}
        description={t('ppt:permissionDenied.description')}
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
    body = <DeckList onOpen={openDeck} onCreate={resetFlow} refreshToken={refreshToken} />
  } else {
    body = (
      <div className="flex min-h-0 flex-1 flex-col gap-5">
        <StepCrumbs
          step={step}
          availableSteps={[
            'input',
            ...(deck ? ['outline' as const] : []),
            ...(deck && outline.trim() && warnings.length === 0 ? ['template' as const] : []),
            ...(deck?.status === 'ready' ? ['result' as const] : []),
          ]}
          disabled={busy || streaming}
          onJump={setStep}
        />
        {step === 'input' ? (
          <StepInput
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
            onRegenerate={() => { if (deck) void runOutline(deck.id, { ...form }) }}
            onNext={() => setStep('template')}
          />
        ) : null}
        {step === 'template' ? (
          rendering ? (
            <RenderStage
              template={template}
              subject={deck?.subject ?? ''}
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
        actions={
          <div className="flex items-center gap-2">
            {config?.enabled && view === 'decks' ? (
              <Button size="sm" variant="secondary" leadingIcon={<Plus size={14} aria-hidden />} onClick={resetFlow}>
                {t('ppt:result.newDeck')}
              </Button>
            ) : null}
            <ThemeToggle />
          </div>
        }
      />

      <main className="flex min-h-0 flex-1 flex-col px-5 pb-5 sm:px-8 sm:pb-6">
        <div className="mx-auto flex min-h-0 w-full max-w-[var(--layout-content-max-w)] flex-1 flex-col">
          {config?.enabled ? (
            <div className="mb-5 flex shrink-0 flex-wrap items-center justify-between gap-x-5 gap-y-3 border-b border-[var(--color-divider)] pb-3 pt-2">
              <fieldset disabled={busy || streaming} className="min-w-0 disabled:opacity-60">
                <SegmentedControl
                  label={t('ppt:title')}
                  value={view}
                  options={[
                    { value: 'create', label: t('ppt:view.create') },
                    { value: 'decks', label: t('ppt:view.decks') },
                  ]}
                  onChange={setView}
                />
              </fieldset>
              {creditChip}
            </div>
          ) : null}
          {body}
        </div>
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
          <DialogBody>
            <Textarea
              value={rewriteQuestion}
              onChange={(event) => setRewriteQuestion(event.target.value)}
              placeholder={t('ppt:outline.rewritePlaceholder')}
              rows={3}
            />
          </DialogBody>
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
  availableSteps,
  disabled: locked,
  onJump,
}: {
  step: Step
  availableSteps: Step[]
  disabled: boolean
  onJump: (step: Step) => void
}) {
  const { t } = useTranslation('ppt')
  const steps: Step[] = ['input', 'outline', 'template', 'result']
  const currentIndex = steps.indexOf(step)
  return (
    <ol aria-label={t('workflow.steps')} className="flex shrink-0 items-center text-xs">
      {steps.map((value, index) => {
        const disabled = locked || !availableSteps.includes(value)
        const done = index < currentIndex
        const current = index === currentIndex
        return (
          <li key={value} className={cn('flex min-w-0 items-center', index < steps.length - 1 && 'flex-1')}>
            <button
              type="button"
              disabled={disabled}
              onClick={() => onJump(value)}
              aria-current={current ? 'step' : undefined}
              className={cn(
                'inline-flex min-h-10 shrink-0 items-center gap-1.5 rounded-[8px] px-1 sm:gap-2 sm:px-2 interactive',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                current
                  ? 'font-medium text-[var(--color-fg)]'
                  : done
                    ? 'text-[var(--color-fg)]'
                    : 'text-[var(--color-fg-muted)]',
                'enabled:hover:bg-[var(--color-bg-muted)] disabled:cursor-default',
              )}
            >
              <span
                className={cn(
                  'inline-flex size-6 shrink-0 items-center justify-center rounded-full text-[11px]',
                  current
                    ? 'bg-[var(--color-accent)] text-[var(--color-accent-fg)]'
                    : done
                      ? 'bg-[var(--color-accent-soft)] text-[var(--color-accent)]'
                      : 'bg-[var(--color-bg-muted)]',
                )}
              >
                {done ? <Check size={12} aria-hidden /> : index + 1}
              </span>
              {t(`ppt:steps.${value}`)}
            </button>
            {index < steps.length - 1 ? (
              <span
                aria-hidden
                className="mx-1 h-px min-w-1 flex-1 bg-[var(--color-divider)] sm:mx-4"
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
    price, billed, inputType, setInputType, content, setContent, upload, setUpload,
    form, setForm, langOptions, sceneOptions, audienceOptions, maxUploadMB, busy, onSubmit,
  } = props
  const fileInput = useRef<HTMLInputElement | null>(null)
  const [dragging, setDragging] = useState(false)
  const typeKey = aiPPTTypeKey(inputType)
  const sourceIcons = { topic: Sparkles, text: FileText, url: Link2, upload: FileUp, markdown: ListTree }
  const languages = langOptions.length > 0
    ? langOptions
    : SUPPORTED_LANGUAGES.map((language) => ({ name: language.label, value: language.code }))

  function chooseFile(file: File | null) {
    if (busy || !file) return
    if (file.size > maxUploadMB * 1024 * 1024) {
      toast.warning(t('input.uploadTooLarge', { mb: maxUploadMB }))
      return
    }
    setUpload(file)
  }

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto max-w-[900px] pb-4 pt-1 sm:pt-4">
        <div className="mb-6">
          <h2 className="text-[24px] font-semibold leading-snug tracking-tight text-[var(--color-fg)]">
            {t('input.title')}
          </h2>
        </div>

        <fieldset disabled={busy} className="min-w-0">
          <legend className="sr-only">{t('input.source')}</legend>
          <div className="mb-3 flex flex-wrap gap-1" role="group" aria-label={t('input.source')}>
            {AI_PPT_INPUT_TYPES.map((value) => {
              const key = aiPPTTypeKey(value) as keyof typeof sourceIcons
              const Icon = sourceIcons[key]
              return (
                <button
                  key={value}
                  type="button"
                  onClick={() => setInputType(value)}
                  aria-pressed={inputType === value}
                  className={cn(
                    'inline-flex min-h-10 items-center gap-2 rounded-[8px] px-3 text-[13px] interactive',
                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:opacity-60',
                    inputType === value
                      ? 'bg-[var(--color-bg-muted)] font-medium text-[var(--color-fg)]'
                      : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                  )}
                >
                  <Icon size={15} aria-hidden />
                  {t(`input.types.${key}`)}
                </button>
              )
            })}
          </div>

          <div className="overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] focus-within:border-[var(--color-border-strong)]">
            {inputType === 2 ? (
              <div
                onDragOver={(event) => {
                  event.preventDefault()
                  if (!busy) setDragging(true)
                }}
                onDragLeave={() => setDragging(false)}
                onDrop={(event) => {
                  event.preventDefault()
                  setDragging(false)
                  chooseFile(event.dataTransfer.files?.[0] ?? null)
                }}
                className={cn(
                  'flex min-h-[190px] flex-col items-center justify-center gap-2 px-5 py-6 text-center transition-colors',
                  dragging && 'bg-[var(--color-accent-soft)]',
                )}
              >
                <FileUp size={24} aria-hidden className="mb-1 text-[var(--color-fg-muted)]" />
                <p className="max-w-full break-all text-sm font-medium text-[var(--color-fg)]">
                  {upload ? upload.name : t('input.uploadHint', { mb: maxUploadMB })}
                </p>
                <p className="text-xs text-[var(--color-fg-muted)]">{t('input.uploadTypes')}</p>
                <input
                  ref={fileInput}
                  type="file"
                  className="hidden"
                  accept=".docx,.doc,.pdf,.txt,.md,.markdown,.pptx,.ppt"
                  onChange={(event) => {
                    chooseFile(event.target.files?.[0] ?? null)
                    event.target.value = ''
                  }}
                />
                <div className="mt-2 flex gap-2">
                  <Button size="sm" variant="secondary" onClick={() => fileInput.current?.click()}>
                    {t('input.uploadPick')}
                  </Button>
                  {upload ? (
                    <Button size="sm" variant="ghost" onClick={() => setUpload(null)} leadingIcon={<X size={13} aria-hidden />}>
                      {t('common:actions.clear')}
                    </Button>
                  ) : null}
                </div>
              </div>
            ) : (
              <Textarea
                aria-label={t(`input.types.${typeKey}`)}
                value={content}
                disabled={busy}
                rows={inputType === 1 || inputType === 5 ? 5 : 8}
                maxLength={inputType === 1 ? 500 : 8000}
                onChange={(event) => setContent(event.target.value)}
                placeholder={t(`input.placeholders.${typeKey}`)}
                className="resize-y rounded-none border-0 bg-transparent p-5 text-[15px] leading-7 placeholder:text-[var(--color-fg-muted)] focus:bg-transparent"
              />
            )}
            <div className="flex flex-col gap-3 border-t border-[var(--color-divider)] px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
              <p className="text-xs leading-5 text-[var(--color-fg-muted)]">
                {billed ? t('input.priceHint', { price }) : t('input.freeHint')}
              </p>
              <Button
                size="sm"
                className="max-sm:w-full"
                disabled={busy || (inputType === 2 ? !upload : !content.trim())}
                loading={busy}
                trailingIcon={<ArrowRight size={14} aria-hidden />}
                onClick={onSubmit}
              >
                {t('input.submit')}
              </Button>
            </div>
          </div>
        </fieldset>

        {(inputType === 1 || inputType === 6) && (
          <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1">
            <span className="text-xs text-[var(--color-fg-muted)]">{t('input.examples.label')}</span>
            {(['one', 'two', 'three'] as const).map((key) => (
              <button
                key={key}
                type="button"
                disabled={busy}
                onClick={() => setContent(t(`input.examples.${key}`))}
                className="min-h-9 rounded-[6px] px-1 text-xs text-[var(--color-fg-muted)] underline-offset-4 interactive hover:text-[var(--color-fg)] hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:opacity-50"
              >
                {t(`input.examples.${key}`)}
              </button>
            ))}
          </div>
        )}

        <fieldset disabled={busy} className="mt-6 min-w-0 border-t border-[var(--color-divider)] pt-5">
          <legend className="sr-only">{t('input.optionsTitle')}</legend>
          <div className="grid gap-5 sm:grid-cols-2">
            <div className="min-w-0">
              <p className="mb-2 text-[13px] font-medium text-[var(--color-fg)]">{t('input.length')}</p>
              <SegmentedControl
                label={t('input.length')}
                value={form.length}
                options={AI_PPT_LENGTHS.map((value) => ({ value, label: t(`input.lengths.${value}`) }))}
                onChange={(value) => setForm({ ...form, length: value })}
                fullWidthOnMobile
              />
            </div>
            <Field label={t('input.lang')} htmlFor="aippt-lang">
              <Select value={form.lang} disabled={busy} onValueChange={(value) => setForm({ ...form, lang: value })}>
                <SelectTrigger id="aippt-lang" className="h-9"><SelectValue /></SelectTrigger>
                <SelectContent>
                  {languages.map((option) => <SelectItem key={option.value} value={option.value}>{option.name}</SelectItem>)}
                </SelectContent>
              </Select>
            </Field>
          </div>
          <details className="group mt-4">
            <summary className="flex min-h-10 w-fit cursor-pointer list-none items-center gap-2 rounded-[6px] text-[13px] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] [&::-webkit-details-marker]:hidden">
              <SlidersHorizontal size={14} aria-hidden />
              {t('input.optionsTitle')}
              <ChevronDown size={13} aria-hidden className="transition-transform group-open:rotate-180 motion-reduce:transition-none" />
            </summary>
            <p className="mb-4 text-xs leading-5 text-[var(--color-fg-muted)]">{t('input.optionsLead')}</p>
            <div className="grid gap-4 sm:grid-cols-2">
              {([{ key: 'scene', options: sceneOptions }, { key: 'audience', options: audienceOptions }] as const).map((field) => (
                field.options.length > 0 ? (
                  <Field key={field.key} label={t(`input.${field.key}`)} htmlFor={`aippt-${field.key}`}>
                    <Select
                      disabled={busy}
                      value={form[field.key] || '__auto'}
                      onValueChange={(value) => setForm({ ...form, [field.key]: value === '__auto' ? '' : value })}
                    >
                      <SelectTrigger id={`aippt-${field.key}`} className="h-9"><SelectValue /></SelectTrigger>
                      <SelectContent>
                        <SelectItem value="__auto">{t('input.anyOption')}</SelectItem>
                        {field.options.filter((option) => option.value).map((option) => (
                          <SelectItem key={option.value} value={option.value}>{option.name}</SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </Field>
                ) : null
              ))}
              <div className="sm:col-span-2">
                <Field label={t('input.prompt')} htmlFor="aippt-prompt" hint={t('input.promptHint', { count: form.prompt.length })}>
                  <Input
                    id="aippt-prompt"
                    value={form.prompt}
                    disabled={busy}
                    maxLength={50}
                    onChange={(event) => setForm({ ...form, prompt: event.target.value })}
                    placeholder={t('input.promptPlaceholder')}
                    className="placeholder:text-[var(--color-fg-muted)]"
                  />
                </Field>
              </div>
            </div>
          </details>
        </fieldset>
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
    <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto lg:grid lg:grid-cols-[minmax(0,1fr)_240px] lg:overflow-hidden">
      <section className="flex min-h-[420px] flex-1 flex-col overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] lg:min-h-0">
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
          <div className="ml-auto flex flex-wrap items-center gap-1.5 max-sm:w-full max-sm:justify-end">
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
                <Button size="sm" disabled={busy || !outline.trim() || warnings.length > 0} onClick={onNext}>
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
          aria-label={t('ppt:outline.title')}
          value={outline}
          onChange={(event) => setOutline(event.target.value)}
          spellCheck={false}
          readOnly={streaming || busy}
          className="min-h-[260px] flex-1 resize-none rounded-none border-0 bg-transparent px-5 py-4 font-mono text-[13px] leading-7 placeholder:text-[var(--color-fg-muted)] focus-visible:ring-0 lg:min-h-0"
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

      <aside className="shrink-0 px-1 py-2 lg:min-h-0 lg:overflow-y-auto lg:px-3">
        <h3 className="text-sm font-medium text-[var(--color-fg)]">{t('ppt:outline.structure')}</h3>
        <p className="mt-1 text-xs text-[var(--color-fg-muted)]">
          {t('ppt:outline.structureHint', { paragraphs: stats.paragraphs })}
        </p>
        <ul className="mt-4 space-y-2.5">
          {headings.length === 0 ? (
            <li className="text-xs text-[var(--color-fg-muted)]">{t('ppt:outline.empty')}</li>
          ) : (
            headings.map((heading, index) => (
              <li
                key={`${heading.line}-${index}`}
                className={cn(
                  'break-words text-xs leading-5',
                  heading.level === 1 && 'font-medium text-[var(--color-fg)]',
                  heading.level === 2 && 'pt-2 font-medium text-[var(--color-fg)]',
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
        <div className="ml-auto flex flex-wrap items-center gap-2 max-sm:w-full max-sm:justify-between">
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

  async function fetchFile(): Promise<Blob | null> {
    if (!deck.file_id) return null
    return authApi.myFileContentBlob('file', deck.file_id)
  }

  const [previewRevision, setPreviewRevision] = useState(0)

  useEffect(() => {
    let cancelled = false
    setPreviewData(null)
    setPreviewError(null)
    if (!deck.file_id) {
      setPreviewLoading(false)
      return
    }
    setPreviewLoading(true)
    void authApi.myFileContentBlob('file', deck.file_id)
      .then((blob) => blob.arrayBuffer())
      .then((data) => {
        if (!cancelled) setPreviewData(data)
      })
      .catch((error: unknown) => {
        if (!cancelled) setPreviewError(error instanceof Error ? error.message : t('ppt:result.previewFailed'))
      })
      .finally(() => {
        if (!cancelled) setPreviewLoading(false)
      })
    return () => { cancelled = true }
  }, [deck.file_id, previewRevision, t])

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
      setPreviewRevision((value) => value + 1)
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
      const result = await aipptApi.refreshFile(deck.id)
      onDeckChange(result.deck)
      onDone()
    } catch (err) {
      toast.error(t('ppt:errors.generic'), err instanceof ApiError ? err.message : undefined)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto lg:overflow-hidden">
      <div className="flex shrink-0 flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 flex-1 items-center gap-2 max-sm:basis-full">
          {renaming ? (
            <Input
              aria-label={t('ppt:result.rename')}
              value={subject}
              autoFocus
              maxLength={120}
              disabled={busy}
              onChange={(event) => setSubject(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter') void saveRename()
                if (event.key === 'Escape') setRenaming(false)
              }}
              onBlur={() => void saveRename()}
            />
          ) : (
            <>
              <h2 className="min-w-0 break-words text-lg font-semibold leading-snug text-[var(--color-fg)]">
                {deck.subject || t('ppt:result.untitled')}
              </h2>
              <Button size="icon-sm" variant="ghost" disabled={busy} aria-label={t('ppt:result.rename')} onClick={() => setRenaming(true)}>
                <Pencil size={14} aria-hidden />
              </Button>
            </>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2 max-sm:w-full">
          <Button size="sm" className="max-sm:flex-1" variant="secondary" disabled={busy || !deck.ppt_id} onClick={() => setEditorOpen(true)} leadingIcon={<Pencil size={14} aria-hidden />}>
            {t('ppt:result.edit')}
          </Button>
          <Button size="sm" className="max-sm:flex-1" disabled={busy || !deck.file_id} onClick={() => void download()} leadingIcon={<Download size={14} aria-hidden />}>
            {t('ppt:result.download')}
          </Button>
        </div>
      </div>

      <div className="flex flex-col gap-5 lg:grid lg:min-h-0 lg:flex-1 lg:grid-cols-[minmax(0,1fr)_230px]">
        <section className="flex h-[52svh] min-h-[320px] flex-col overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] lg:h-auto lg:min-h-0">
          <div className="flex shrink-0 items-center justify-between gap-3 border-b border-[var(--color-divider)] px-4 py-2">
            <h3 className="text-[13px] font-medium text-[var(--color-fg)]">{t('ppt:result.previewTitle')}</h3>
            <Button size="sm" variant="ghost" disabled={!deck.file_id} onClick={() => setPreviewOpen(true)}>
              {t('ppt:result.expandPreview')}
            </Button>
          </div>
          <div className="min-h-0 flex-1">
            {deck.file_id ? (
              <DocumentPreview
                name={`${deck.subject || 'AI PPT'}.pptx`}
                mimeType={PPTX_MIME}
                backendKind="doc"
                data={previewData ?? undefined}
                loading={previewLoading}
                error={previewError ?? undefined}
                onRetry={() => setPreviewRevision((value) => value + 1)}
              />
            ) : (
              <EmptyState
                icon={<Presentation size={24} aria-hidden />}
                title={t('ppt:result.previewTitle')}
                description={t('ppt:result.mirrorPending')}
                action={<Button size="sm" variant="secondary" loading={busy} onClick={() => void retryMirror()}>{t('ppt:result.retrySave')}</Button>}
              />
            )}
          </div>
        </section>

        <aside className="pb-4 lg:min-h-0 lg:overflow-y-auto">
          <p className="flex items-start gap-2 text-xs leading-5 text-[var(--color-fg-muted)]" role="status">
            {deck.file_id ? <Check size={15} aria-hidden className="mt-0.5 shrink-0" /> : <FileUp size={15} aria-hidden className="mt-0.5 shrink-0" />}
            {deck.file_id ? t('ppt:result.saved') : t('ppt:result.mirrorPending')}
          </p>
          <dl className="mt-5 space-y-4 text-[13px]">
            <div>
              <dt className="text-xs text-[var(--color-fg-muted)]">{t('ppt:result.template')}</dt>
              <dd className="mt-1 break-words text-[var(--color-fg)]">{deck.template_name || t('ppt:result.defaultTemplate')}</dd>
            </div>
            <div>
              <dt className="text-xs text-[var(--color-fg-muted)]">{t('ppt:result.credits')}</dt>
              <dd className="mt-1 tabular-nums text-[var(--color-fg)]">{t('ppt:decks.credits', { credits: deck.credits })}</dd>
            </div>
          </dl>
          {deck.error ? <p className="mt-3 break-words text-xs leading-5 text-[var(--color-danger)]">{deck.error}</p> : null}
          <div className="mt-5 flex flex-col items-start gap-1 border-t border-[var(--color-divider)] pt-3">
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => setTemplateOpen(true)} leadingIcon={<Presentation size={14} aria-hidden />}>
              {t('ppt:result.changeTemplate')}
            </Button>
            <Button size="sm" variant="ghost" disabled={busy} onClick={onRewrite} leadingIcon={<Wand2 size={14} aria-hidden />}>
              {t('ppt:outline.rewrite')}
            </Button>
            <Button size="sm" variant="ghost" disabled={busy} onClick={onNew} leadingIcon={<Plus size={14} aria-hidden />}>
              {t('ppt:result.newDeck')}
            </Button>
          </div>
        </aside>
      </div>

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
              onRetry={() => setPreviewRevision((value) => value + 1)}
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
          setPreviewRevision((value) => value + 1)
        }}
      />
    </div>
  )
}
