import { useCallback, useEffect, useRef, useState, type ChangeEvent, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ArrowUp, ImagePlus, Menu, ShieldOff, Square, X } from 'lucide-react'
import { modelsApi } from '@/api'
import type { ApiModel } from '@/api/types'
import { streamSSE } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tooltip } from '@/components/ui/tooltip'
import { PrivateMessageRow, type PrivateDisplayMessage } from '@/components/chat/private-message-row'
import { PRIVATE_IMAGE_TYPES, privateImageURL, readPrivateImage, validatePrivateHistory, type PrivateImage, type PrivateMessage } from '@/lib/private-chat'
import { blockReload } from '@/lib/sync-guards'
import { useMediaQuery } from '@/hooks/use-media-query'
import { useUI } from '@/store/ui'
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

export default function PrivateChat() {
  const { t } = useTranslation('chat')
  const navigate = useNavigate()
  const { workspaceId, allowed, canUpload } = usePrivateChatPermission()
  const isDesktop = useMediaQuery('(min-width: 1024px)')
  const [models, setModels] = useState<ApiModel[]>([])
  const [modelId, setModelId] = useState('')
  const [loadingModels, setLoadingModels] = useState(true)
  // §4.6: this page keeps its own model list, so the flag is read from the same
  // /api/models response rather than the shared store.
  const [visionOutsource, setVisionOutsource] = useState(false)
  const [messages, setMessages] = useState<PrivateDisplayMessage[]>([])
  const [draft, setDraft] = useState('')
  const [images, setImages] = useState<PrivateImage[]>([])
  const [readingImages, setReadingImages] = useState(false)
  const [streaming, setStreaming] = useState(false)
  const [error, setError] = useState('')
  const fileRef = useRef<HTMLInputElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)
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
    setLoadingModels(true)
    setModels([])
    setModelId('')
    void modelsApi.list(workspaceId ?? undefined).then((response) => {
      if (!alive) return
      const available = response.models.filter((item) => item.kind === 'chat' && item.enabled && !item.fast)
      setModels(available)
      setVisionOutsource(Boolean(response.vision_available))
      setModelId(available.some((item) => item.id === response.default_id) ? response.default_id : available[0]?.id ?? '')
    }).catch(() => {
      if (alive) setError('private_model_unavailable')
    }).finally(() => {
      if (alive) setLoadingModels(false)
    })
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

  const errorKey = `private.errors.${error}`
  const composer = (
    <form onSubmit={(event) => void send(event)} className="chat-composer-shell relative isolate min-w-0 w-full rounded-popup border-0 bg-[var(--color-surface)] p-3">
      {images.length > 0 && <div className="mb-2 flex gap-2 overflow-x-auto py-1">{images.map((image, index) => (
        <div key={index} className="relative shrink-0">
          <img src={privateImageURL(image)} alt={t('private.image', { index: index + 1 })} className="size-16 rounded-[8px] object-cover" />
          <Button size="icon-sm" variant="secondary" className="absolute -right-1 -top-1" aria-label={t('private.removeImage', { index: index + 1 })} disabled={streaming || readingImages} onClick={() => setImages((current) => current.filter((_, imageIndex) => index !== imageIndex))}><X size={12} aria-hidden /></Button>
        </div>
      ))}</div>}
      <textarea ref={inputRef} aria-label={t('private.placeholder')} value={draft} onChange={(event) => setDraft(event.target.value)} placeholder={t('private.placeholder')} rows={3} maxLength={12000} disabled={streaming} autoComplete="off" spellCheck={false} autoCorrect="off" autoCapitalize="off" data-gramm="false" className="block max-h-48 min-h-20 w-full resize-none bg-transparent px-1 py-2 text-[0.9375rem] leading-relaxed outline-none placeholder:text-[var(--color-fg-muted)]" onKeyDown={(event) => {
        if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing && event.keyCode !== 229) {
          event.preventDefault()
          event.currentTarget.form?.requestSubmit()
        }
      }} />
      <div className="flex min-w-0 items-center gap-2">
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
          {streaming ? <Button size="icon" variant="secondary" aria-label={t('private.stop')} onClick={() => controllerRef.current?.abort()}><Square size={14} fill="currentColor" aria-hidden /></Button> : <Button type="submit" size="icon" className="rounded-full" aria-label={t('private.send')} disabled={!allowed || !model || readingImages || (!draft.trim() && !images.length)}><ArrowUp size={18} aria-hidden /></Button>}
        </div>
      </div>
    </form>
  )
  const headerActions = (
    <>
      <Button variant="ghost" size="sm" onClick={clear} disabled={!messages.length && !draft && !images.length && !readingImages}>{t('private.clear')}</Button>
      <Tooltip content={t('private.exit')}><Button size="icon-lg" variant="ghost" aria-label={t('private.exit')} aria-pressed onClick={() => { clear(); navigate('/') }}><ShieldOff size={19} aria-hidden /></Button></Tooltip>
    </>
  )

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

      {messages.length === 0 ? (
        isDesktop ? (
          <div className="relative flex flex-1 min-h-0 flex-col items-center justify-center overflow-y-auto px-6 py-10 text-center">
            <h2 className="text-balance font-sans text-[2.5rem] font-semibold leading-[1.12] tracking-tight text-[var(--color-fg)]">{t('private.title')}</h2>
            <div className="mt-10 w-full max-w-[44rem] text-left">
              {error && <p role="alert" className="mb-3 text-sm text-[var(--color-danger)]">{t(errorKey, { defaultValue: t('private.errors.private_provider_error') })}</p>}
              {composer}
            </div>
          </div>
        ) : (
          <>
            <div className="flex min-h-0 flex-1 flex-col items-center justify-center px-6 text-center">
              <h2 className="text-balance font-sans text-[1.6rem] font-semibold leading-[1.14] tracking-tight text-[var(--color-fg)]">{t('private.title')}</h2>
            </div>
            <div className="mx-auto w-full shrink-0 max-w-[48rem] px-3 pb-2">
              {error && <p role="alert" className="mb-3 text-sm text-[var(--color-danger)]">{t(errorKey, { defaultValue: t('private.errors.private_provider_error') })}</p>}
              {composer}
            </div>
          </>
        )
      ) : (
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
            {error && <p role="alert" className="mb-3 text-sm text-[var(--color-danger)]">{t(errorKey, { defaultValue: t('private.errors.private_provider_error') })}</p>}
            {composer}
          </div>
        </div>
      )}
    </div>
  )
}
