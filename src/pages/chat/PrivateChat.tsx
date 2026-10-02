import { useCallback, useEffect, useRef, useState, type ChangeEvent, type FormEvent } from 'react'
import { flushSync } from 'react-dom'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ArrowUp, ImagePlus, Menu, ShieldOff, Square, X } from 'lucide-react'
import { modelsApi } from '@/api'
import type { ApiModel } from '@/api/types'
import { streamSSE } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tooltip } from '@/components/ui/tooltip'
import { HOME_SWAP_STATE, HomeLayout } from '@/components/chat/home-layout'
import { PrivateMessageRow, type PrivateDisplayMessage } from '@/components/chat/private-message-row'
import { PRIVATE_IMAGE_TYPES, privateImageURL, readPrivateImage, validatePrivateHistory, type PrivateImage, type PrivateMessage } from '@/lib/private-chat'
import { blockReload } from '@/lib/sync-guards'
import { isModelCatalogReadyForScope } from '@/lib/model-selection'
import { workspaceModelPolicyKey } from '@/lib/workspace-permissions'
import { useMediaQuery } from '@/hooks/use-media-query'
import { useAutosizeTextarea } from '@/hooks/use-autosize-textarea'
import { cn } from '@/lib/utils'
import { pulseComposerShell } from '@/lib/composer-pulse'
import { runViewTransition } from '@/lib/view-transition'
import { useModels } from '@/store/models'
import { useUI } from '@/store/ui'
import { useWorkspaces } from '@/store/workspaces'
import { usePrivateChatPermission } from '@/hooks/use-private-chat-permission'

/**
 * Wire history derived from the visible transcript: strictly alternating
 * completed exchanges plus a trailing pending user turn. Failed turns whose
 * assistant reply never produced text are skipped as a pair, so the server's
 * alternation rule holds after regenerate / error / edit-and-resend.
 */
function historyFor(display: PrivateDisplayMessage[]): PrivateMessage[] {
  const history: PrivateMessage[] = []
  for (let index = 0; index < display.length; index++) {
    const message = display[index]
    if (message.role !== 'user' || (!message.text.trim() && !message.images?.length)) continue
    const userEntry: PrivateMessage = { role: 'user', text: message.text, ...(message.images?.length ? { images: message.images } : {}) }
    const reply = display[index + 1]
    if (!reply) {
      history.push(userEntry)
    } else if (reply.role === 'assistant' && reply.text.trim() && !reply.streaming) {
      history.push(userEntry, { role: 'assistant', text: reply.text })
      index++
    }
  }
  return history
}

interface PrivateModelCatalog {
  models: ApiModel[]
  defaultId: string
  visionOutsource: boolean
}

function privateCatalog(models: ApiModel[], defaultId: string, visionOutsource: boolean): PrivateModelCatalog {
  return {
    models: models.filter((item) => item.kind === 'chat' && item.enabled && !item.fast),
    defaultId,
    visionOutsource,
  }
}

function pickPrivateModel(catalog: PrivateModelCatalog): string {
  return catalog.models.some((item) => item.id === catalog.defaultId)
    ? catalog.defaultId
    : catalog.models[0]?.id ?? ''
}

/**
 * The shared picker catalog, when it already answers for this scope. Entering
 * private mode from the home screen then needs no model request of its own,
 * so the composer is usable on the first frame.
 */
function cachedPrivateCatalog(workspaceId: string | null | undefined): PrivateModelCatalog | null {
  const catalog = useModels.getState()
  const policy = workspaceId ? useWorkspaces.getState().policies[workspaceId] : undefined
  const ready = isModelCatalogReadyForScope({
    loaded: catalog.loaded,
    loadedScope: catalog.loadedScope,
    loadedPolicyKey: catalog.loadedPolicyKey,
    expectedScope: workspaceId ?? null,
    expectedPolicyKey: workspaceModelPolicyKey(workspaceId, policy),
  })
  if (!ready || catalog.error) return null
  return privateCatalog(catalog.models, catalog.defaultId, catalog.visionAvailable)
}

export default function PrivateChat() {
  const { t } = useTranslation('chat')
  const navigate = useNavigate()
  const { workspaceId, allowed, canUpload } = usePrivateChatPermission()
  const isDesktop = useMediaQuery('(min-width: 1024px)')
  const isPhone = useMediaQuery('(max-width: 639px)')
  const [initialCatalog] = useState(() => cachedPrivateCatalog(workspaceId))
  const [models, setModels] = useState<ApiModel[]>(() => initialCatalog?.models ?? [])
  const [modelId, setModelId] = useState(() => (initialCatalog ? pickPrivateModel(initialCatalog) : ''))
  const [loadingModels, setLoadingModels] = useState(!initialCatalog)
  // §4.6: this page filters its own model list, so the flag travels with the
  // same catalog (shared store or /api/models response) it was filtered from.
  const [visionOutsource, setVisionOutsource] = useState(initialCatalog?.visionOutsource ?? false)
  const [messages, setMessages] = useState<PrivateDisplayMessage[]>([])
  const [draft, setDraft] = useState('')
  const [images, setImages] = useState<PrivateImage[]>([])
  const [readingImages, setReadingImages] = useState(false)
  const [streaming, setStreaming] = useState(false)
  const [error, setError] = useState('')
  const fileRef = useRef<HTMLInputElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const formRef = useRef<HTMLFormElement>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const controllerRef = useRef<AbortController | null>(null)
  const epochRef = useRef(0)
  const sequenceRef = useRef(0)
  const imageReadRef = useRef(false)
  const model = models.find((item) => item.id === modelId)
  const hasImageHistory = messages.some((message) => message.images?.length)

  const clear = useCallback(() => {
    epochRef.current++
    controllerRef.current?.abort()
    controllerRef.current = null
    imageReadRef.current = false
    setMessages([])
    setDraft('')
    setImages([])
    setStreaming(false)
    setReadingImages(false)
    setError('')
    if (fileRef.current) fileRef.current.value = ''
  }, [])

  useEffect(() => {
    let alive = true
    const applyCatalog = (catalog: PrivateModelCatalog) => {
      setModels(catalog.models)
      setVisionOutsource(catalog.visionOutsource)
      setModelId((current) => (
        catalog.models.some((item) => item.id === current) ? current : pickPrivateModel(catalog)
      ))
    }
    const cached = cachedPrivateCatalog(workspaceId)
    if (cached) {
      applyCatalog(cached)
      setLoadingModels(false)
    } else {
      setLoadingModels(true)
      setModels([])
      setModelId('')
      void modelsApi.list(workspaceId ?? undefined).then((response) => {
        if (!alive) return
        applyCatalog(privateCatalog(response.models, response.default_id, Boolean(response.vision_available)))
      }).catch(() => {
        if (alive) setError('private_model_unavailable')
      }).finally(() => {
        if (alive) setLoadingModels(false)
      })
    }
    window.addEventListener('pagehide', clear)
    const onPageShow = (event: PageTransitionEvent) => {
      if (event.persisted) clear()
    }
    window.addEventListener('pageshow', onPageShow)
    return () => {
      alive = false
      clear()
      window.removeEventListener('pagehide', clear)
      window.removeEventListener('pageshow', onPageShow)
    }
  }, [clear, workspaceId])

  useEffect(() => {
    const element = scrollRef.current
    if (element) element.scrollTop = element.scrollHeight
  }, [messages])

  useAutosizeTextarea(inputRef, draft, isPhone ? 7 : 12)
  // Entering private mode lands in the input, as the ordinary home does.
  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  // Like the ordinary home, this page renders its own mobile controls, so the
  // layout's brand bar never flashes while switching between the two.
  useEffect(() => {
    useUI.getState().setPageOwnsTopBar(true)
    return () => useUI.getState().setPageOwnsTopBar(false)
  }, [])

  useEffect(() => {
    if (messages.length || draft || images.length || readingImages || streaming) return blockReload()
  }, [messages.length, draft, images.length, readingImages, streaming])

  async function pickImages(event: ChangeEvent<HTMLInputElement>) {
    const files = Array.from(event.target.files ?? [])
    event.target.value = ''
    if (!allowed || !canUpload || !(model?.vision || visionOutsource) || controllerRef.current || imageReadRef.current || files.length === 0) return
    const imageCount = messages.reduce((count, message) => count + (message.images?.length ?? 0), images.length + files.length)
    if (imageCount > 16) {
      setError('private_image_limit')
      return
    }
    const epoch = epochRef.current
    imageReadRef.current = true
    setReadingImages(true)
    setError('')
    try {
      const picked = await Promise.all(files.map(readPrivateImage))
      if (epoch === epochRef.current) setImages((current) => [...current, ...picked])
    } catch (cause) {
      if (epoch === epochRef.current) setError(cause instanceof Error ? cause.message : 'private_image_invalid')
    } finally {
      if (epoch === epochRef.current) {
        imageReadRef.current = false
        setReadingImages(false)
      }
    }
  }

  /** Shared streaming core: validates nothing (callers do), owns the row
   *  lifecycle, epoch guards, and the stop/error/stopped transitions. */
  async function runStream(activeModel: ApiModel, requestHistory: PrivateMessage[], display: PrivateDisplayMessage[]) {
    if (!allowed) return
    const epoch = epochRef.current
    const controller = new AbortController()
    controllerRef.current = controller
    const assistantId = ++sequenceRef.current
    const assistant: PrivateDisplayMessage = { id: assistantId, role: 'assistant', text: '', reasoning: '', generatedImages: [], createdAt: Date.now(), streaming: true }
    setMessages([...display, assistant])
    setError('')
    setStreaming(true)
    const sync = (settled = false) => setMessages((current) => current.map((message) => (
      message.id === assistantId ? { ...assistant, streaming: !settled } : message
    )))
    let done = false
    try {
      for await (const frame of streamSSE('/private-chat', { model_id: activeModel.id, messages: requestHistory, ...(workspaceId ? { workspace_id: workspaceId } : {}) }, controller.signal)) {
        if (epoch !== epochRef.current) return
        const payload = frame.data as { type?: string; text?: string; url?: string; code?: string }
        const type = payload.type ?? frame.event
        if (type === 'text_delta' && typeof payload.text === 'string') assistant.text += payload.text
        if (type === 'thinking_delta' && typeof payload.text === 'string') assistant.reasoning += payload.text
        if (type === 'image' && typeof payload.url === 'string' && /^data:image\/(png|jpeg|webp|gif);base64,[A-Za-z0-9+/=]+$/.test(payload.url)) {
          assistant.generatedImages = [...assistant.generatedImages!, payload.url]
        }
        if (type === 'error') throw new Error(payload.code || 'private_provider_error')
        if (type === 'done') done = true
        sync()
      }
      if (!done) throw new Error('private_stream_interrupted')
      sync(true)
    } catch (cause) {
      if (epoch !== epochRef.current) return
      if (controller.signal.aborted) assistant.stopped = true
      else assistant.error = cause instanceof Error ? cause.message : 'private_provider_error'
      sync(true)
    } finally {
      if (epoch === epochRef.current) {
        controllerRef.current = null
        setStreaming(false)
        inputRef.current?.focus()
      }
    }
  }

  async function send(event: FormEvent) {
    event.preventDefault()
    if (!allowed || controllerRef.current || imageReadRef.current || !model || (!draft.trim() && !images.length)) return
    const text = draft.trim()
    const userRow: PrivateDisplayMessage = { id: ++sequenceRef.current, role: 'user', text, images: images.length ? [...images] : undefined, createdAt: Date.now() }
    const requestHistory: PrivateMessage[] = [...historyFor(messages), { role: 'user', text, ...(userRow.images?.length ? { images: userRow.images } : {}) }]
    try {
      validatePrivateHistory(requestHistory, model.id, model.vision, visionOutsource)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'private_invalid_request')
      return
    }
    setDraft('')
    setImages([])
    await runStream(model, requestHistory, [...messages, userRow])
  }

  /** Re-stream the newest answer: drop its reply row and reuse the question. */
  async function regenerate(id: number) {
    if (!allowed || controllerRef.current || imageReadRef.current || !model) return
    const index = messages.findIndex((message) => message.id === id)
    if (index < 1 || messages[index].role !== 'assistant') return
    const display = messages.slice(0, index)
    const requestHistory = historyFor(display)
    try {
      validatePrivateHistory(requestHistory, model.id, model.vision, visionOutsource)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'private_invalid_request')
      return
    }
    await runStream(model, requestHistory, display)
  }

  /** Replace a past question and re-answer: truncate in-memory history there. */
  async function editAndResend(id: number, text: string) {
    if (!allowed || controllerRef.current || imageReadRef.current || !model) return
    const index = messages.findIndex((message) => message.id === id)
    const original = messages[index]
    if (index < 0 || original.role !== 'user') return
    const edited: PrivateDisplayMessage = { ...original, id: ++sequenceRef.current, text, createdAt: Date.now() }
    const display = messages.slice(0, index)
    const requestHistory = [...historyFor(display), { role: 'user', text, ...(edited.images?.length ? { images: edited.images } : {}) } as PrivateMessage]
    try {
      validatePrivateHistory(requestHistory, model.id, model.vision, visionOutsource)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'private_invalid_request')
      return
    }
    await runStream(model, requestHistory, [...display, edited])
  }

  /** Home suggestions land in the draft for the user to finish. */
  function fillDraft(prompt: string) {
    if (streaming) return
    setDraft(prompt.slice(0, 12000))
    pulseComposerShell(formRef.current)
    requestAnimationFrame(() => {
      const input = inputRef.current
      if (!input) return
      input.focus()
      input.setSelectionRange(input.value.length, input.value.length)
    })
  }

  const errorKey = `private.errors.${error}`
  const errorNotice = error
    ? <p role="alert" className="mb-3 text-sm text-[var(--color-danger)]">{t(errorKey, { defaultValue: t('private.errors.private_provider_error') })}</p>
    : null
  // Sized like the ordinary composer (editor 72px + 50px toolbar), so entering
  // or leaving private mode swaps the input without moving anything around it.
  const canSend = allowed && !!model && !readingImages && (!!draft.trim() || images.length > 0)
  const composer = (
    <form ref={formRef} onSubmit={(event) => void send(event)} data-vt-composer="" className="chat-composer-shell relative isolate min-w-0 w-full rounded-popup border-0 bg-[var(--color-surface)]">
      {images.length > 0 && <div className="flex gap-2 overflow-x-auto px-3 pb-1 pt-3">{images.map((image, index) => (
        <div key={index} className="relative shrink-0">
          <img src={privateImageURL(image)} alt={t('private.image', { index: index + 1 })} className="size-16 rounded-[8px] object-cover" />
          <Button size="icon-sm" variant="secondary" className="absolute -right-1 -top-1" aria-label={t('private.removeImage', { index: index + 1 })} disabled={streaming || readingImages} onClick={() => setImages((current) => current.filter((_, imageIndex) => index !== imageIndex))}><X size={12} aria-hidden /></Button>
        </div>
      ))}</div>}
      <textarea ref={inputRef} aria-label={t('private.placeholder')} value={draft} onChange={(event) => setDraft(event.target.value)} placeholder={t(isPhone ? 'private.placeholderMobile' : 'private.placeholder')} rows={1} maxLength={12000} disabled={streaming} autoComplete="off" spellCheck={false} autoCorrect="off" autoCapitalize="off" data-gramm="false" className="block min-h-[4.5rem] w-full resize-none bg-transparent px-4 pb-1 pt-3 text-[0.9375rem] leading-[1.55] outline-none placeholder:text-[var(--color-fg-muted)] max-sm:min-h-[3.25rem] max-sm:text-[1rem]" onKeyDown={(event) => {
        if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing && event.keyCode !== 229) {
          event.preventDefault()
          event.currentTarget.form?.requestSubmit()
        }
      }} />
      <div className="flex min-w-0 items-center gap-1 px-2.5 pb-2.5 pt-1">
        {/* The rest of the screen matches the ordinary home, so the mode is
            named here, inside the one surface that differs. */}
        <Tooltip content={t('private.footer')}>
          <span className="inline-flex h-7 shrink-0 items-center gap-1 rounded-full bg-[var(--color-accent-soft)] pl-2 pr-2.5 text-[12px] font-medium text-[var(--color-accent)]">
            <ShieldOff size={12} aria-hidden />
            {t('private.badge')}
          </span>
        </Tooltip>
        <div className="min-w-0 max-w-[min(70%,20rem)]">
          <Select value={modelId} onValueChange={(value) => {
            if (!models.some((item) => item.id === value)) return
            setModelId(value)
            setImages([])
            setError('')
          }} disabled={streaming || readingImages || loadingModels || models.length === 0}>
            <SelectTrigger aria-label={t('private.model')} className="h-9 border-0 bg-transparent px-2 text-xs shadow-none"><SelectValue placeholder={t(loadingModels ? 'private.loadingModels' : 'private.noModels')} /></SelectTrigger>
            <SelectContent>{models.map((item) => <SelectItem key={item.id} value={item.id} disabled={hasImageHistory && !item.vision && !visionOutsource}>{item.label}</SelectItem>)}</SelectContent>
          </Select>
        </div>
        {(model?.vision || visionOutsource) && canUpload && <>
          <input ref={fileRef} type="file" accept={PRIVATE_IMAGE_TYPES.join(',')} multiple className="hidden" onChange={(event) => void pickImages(event)} aria-label={t('private.addImage')} />
          <Tooltip content={t('private.addImage')}><Button variant="ghost" size="icon" loading={readingImages} disabled={streaming || readingImages} aria-label={t('private.addImage')} onClick={() => fileRef.current?.click()}><ImagePlus size={18} aria-hidden /></Button></Tooltip>
        </>}
        <div className="ml-auto">
          {/* Same primary action as the ordinary composer: ink when ready,
              muted when there is nothing to send. */}
          {streaming ? (
            <button key="stop" type="button" aria-label={t('private.stop')} onClick={() => controllerRef.current?.abort()} className="inline-flex size-9 shrink-0 items-center justify-center rounded-full interactive hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-sm:size-11">
              <span className="inline-flex size-8 items-center justify-center rounded-full bg-[var(--color-fg)] text-[var(--color-fg-inverted)] animate-[action-swap_160ms_var(--ease-out)]"><Square size={12} fill="currentColor" aria-hidden /></span>
            </button>
          ) : (
            <button key="send" type="submit" aria-label={t('private.send')} disabled={!canSend} className={cn('inline-flex size-9 shrink-0 items-center justify-center rounded-full interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-sm:size-11', canSend ? 'hover:opacity-90' : 'cursor-not-allowed')}>
              <span className={cn('inline-flex size-8 items-center justify-center rounded-full animate-[action-swap_160ms_var(--ease-out)]', canSend ? 'bg-[var(--color-fg)] text-[var(--color-fg-inverted)]' : 'bg-[var(--color-bg-muted)] text-[var(--color-fg-faint)]')}><ArrowUp size={15} aria-hidden /></span>
            </button>
          )}
        </div>
      </div>
    </form>
  )
  const headerActions = (
    <>
      <Button variant="ghost" size="sm" onClick={clear} disabled={!messages.length && !draft && !images.length && !readingImages}>{t('private.clear')}</Button>
      <Tooltip content={t('private.exit')}>
        <button
          type="button"
          aria-label={t('private.exit')}
          aria-pressed
          onClick={() => {
            clear()
            void runViewTransition('private', () => flushSync(() => navigate('/', { state: HOME_SWAP_STATE })))
          }}
          className="inline-flex size-11 items-center justify-center rounded-[10px] bg-[var(--color-accent-soft)] text-[var(--color-accent)] interactive hover:text-[var(--color-accent-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        >
          <ShieldOff size={19} aria-hidden />
        </button>
      </Tooltip>
    </>
  )

  // A fresh private conversation is the ordinary home screen with the private
  // composer in place of the regular one.
  if (messages.length === 0) {
    return <HomeLayout variant="private" composer={composer} notice={errorNotice} onSuggestion={fillDraft} />
  }

  return (
    <div className="relative flex min-h-0 flex-1 flex-col overflow-hidden bg-[var(--color-bg)] text-[var(--color-fg)]">
      {isDesktop ? (
        <header className="flex items-center gap-3 h-[var(--layout-topbar-h)] px-4 sm:px-6 bg-[var(--color-bg)]/85 backdrop-blur-sm">
          <div className="flex-1 min-w-0 flex flex-col">
            <h1 className="font-medium text-[var(--color-fg)] text-[15px] truncate">{t('private.title')}</h1>
          </div>
          {headerActions}
        </header>
      ) : (
        <header className="grid grid-cols-[var(--tap-min)_1fr_auto] items-center gap-1 h-[var(--layout-topbar-h-mobile)] px-2 bg-[var(--color-bg)]/85 backdrop-blur-sm">
          <button
            type="button"
            aria-label={t('commandMenu.actions.toggleSidebar')}
            onClick={() => useUI.getState().setNavOpen(true)}
            className="inline-flex items-center justify-center size-[var(--tap-min)] rounded-[10px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <Menu size={18} aria-hidden />
          </button>
          <div className="min-w-0 flex flex-col items-center">
            <h1 className="max-w-full truncate text-[14px] font-medium text-[var(--color-fg)] leading-tight">{t('private.title')}</h1>
          </div>
          <div className="flex items-center gap-1">{headerActions}</div>
        </header>
      )}

      <div className="relative flex flex-1 min-h-0 flex-col">
        <div ref={scrollRef} data-scroll-root className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden scrollbar-thin">
          <div className="chat-thread flex flex-col px-[var(--layout-gutter-mobile)] sm:px-6 lg:px-8 py-8 mx-auto w-full max-w-[var(--layout-message-max-w)]" role="log" aria-label={t('private.title')} aria-live="polite" aria-atomic="false" aria-relevant="additions text">
            {messages.map((message, index) => (
              <PrivateMessageRow
                key={message.id}
                message={message}
                model={message.role === 'assistant' ? model : undefined}
                isLastAssistant={index === messages.length - 1 && message.role === 'assistant'}
                locked={streaming || readingImages}
                onRegenerate={() => void regenerate(message.id)}
                onEdit={(text) => void editAndResend(message.id, text)}
              />
            ))}
          </div>
        </div>
        <div className="mx-auto w-full shrink-0 max-w-[var(--layout-message-max-w)] px-3 pb-2 sm:px-8 sm:pb-4">
          {errorNotice}
          {composer}
        </div>
      </div>
    </div>
  )
}
