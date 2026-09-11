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
import { PrivateMarkdown } from '@/components/chat/private-markdown'
import { PRIVATE_IMAGE_TYPES, privateImageURL, readPrivateImage, validatePrivateHistory, type PrivateImage, type PrivateMessage } from '@/lib/private-chat'
import { blockReload } from '@/lib/sync-guards'
import { cn } from '@/lib/utils'
import { useUI } from '@/store/ui'

interface DisplayMessage extends PrivateMessage {
  id: number
  reasoning?: string
  generatedImages?: string[]
}

export default function PrivateChat() {
  const { t } = useTranslation('chat')
  const navigate = useNavigate()
  const [models, setModels] = useState<ApiModel[]>([])
  const [modelId, setModelId] = useState('')
  const [loadingModels, setLoadingModels] = useState(true)
  const [messages, setMessages] = useState<DisplayMessage[]>([])
  const [draft, setDraft] = useState('')
  const [images, setImages] = useState<PrivateImage[]>([])
  const [readingImages, setReadingImages] = useState(false)
  const [streaming, setStreaming] = useState(false)
  const [error, setError] = useState('')
  const fileRef = useRef<HTMLInputElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const controllerRef = useRef<AbortController | null>(null)
  const historyRef = useRef<PrivateMessage[]>([])
  const epochRef = useRef(0)
  const sequenceRef = useRef(0)
  const imageReadRef = useRef(false)
  const model = models.find((item) => item.id === modelId)
  const hasImageHistory = historyRef.current.some((message) => message.images?.length)

  const clear = useCallback(() => {
    epochRef.current++
    controllerRef.current?.abort()
    controllerRef.current = null
    historyRef.current = []
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
    void modelsApi.list().then((response) => {
      if (!alive) return
      const available = response.models.filter((item) => item.kind === 'chat' && item.enabled && !item.fast)
      setModels(available)
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
  }, [clear])

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
    if (!model?.vision || controllerRef.current || imageReadRef.current || files.length === 0) return
    const imageCount = historyRef.current.reduce((count, message) => count + (message.images?.length ?? 0), images.length + files.length)
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

  async function send(event: FormEvent) {
    event.preventDefault()
    if (controllerRef.current || imageReadRef.current || !model || (!draft.trim() && !images.length)) return
    const userMessage: PrivateMessage = { role: 'user', text: draft.trim(), ...(images.length ? { images } : {}) }
    const requestHistory = [...historyRef.current, userMessage]
    try {
      validatePrivateHistory(requestHistory, model.id, model.vision)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'private_invalid_request')
      return
    }
    const epoch = epochRef.current
    const controller = new AbortController()
    controllerRef.current = controller
    const assistantId = ++sequenceRef.current
    const assistant: DisplayMessage = { id: assistantId, role: 'assistant', text: '', reasoning: '', generatedImages: [] }
    const previousDisplay = messages
    setMessages([...messages, { ...userMessage, id: ++sequenceRef.current }, { ...assistant }])
    setDraft('')
    setImages([])
    setError('')
    setStreaming(true)
    let done = false
    try {
      for await (const frame of streamSSE('/private-chat', { model_id: model.id, messages: requestHistory }, controller.signal)) {
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
        setMessages((current) => current.map((message) => message.id === assistantId ? { ...assistant } : message))
      }
      if (!done) throw new Error('private_stream_interrupted')
      if (assistant.text.trim()) historyRef.current = [...requestHistory, { role: 'assistant', text: assistant.text }]
    } catch (cause) {
      if (epoch !== epochRef.current) return
      setError(controller.signal.aborted ? 'private_stopped' : cause instanceof Error ? cause.message : 'private_provider_error')
      if (assistant.text.trim()) {
        historyRef.current = [...requestHistory, { role: 'assistant', text: assistant.text }]
      } else {
        setMessages(previousDisplay)
        setDraft(userMessage.text)
        setImages(userMessage.images ?? [])
      }
    } finally {
      if (epoch === epochRef.current) {
        controllerRef.current = null
        setStreaming(false)
        inputRef.current?.focus()
      }
    }
  }

  const errorKey = `private.errors.${error}`
  return (
    <div className="relative flex min-h-0 flex-1 flex-col overflow-hidden bg-[var(--color-bg)] text-[var(--color-fg)]">
      <header className="flex h-14 shrink-0 items-center justify-between gap-3 px-3 sm:px-6">
        <div className="lg:hidden">
          <Button size="icon-lg" variant="ghost" aria-label={t('commandMenu.actions.toggleSidebar')} onClick={() => useUI.getState().setNavOpen(true)}><Menu size={17} aria-hidden /></Button>
        </div>
        <div className="ml-auto flex items-center gap-2">
          <Button variant="ghost" size="sm" onClick={clear} disabled={!messages.length && !draft && !images.length && !readingImages}>{t('private.clear')}</Button>
          <Tooltip content={t('private.exit')}><Button size="icon-lg" variant="ghost" aria-label={t('private.exit')} aria-pressed onClick={() => { clear(); navigate('/') }}><ShieldOff size={19} aria-hidden /></Button></Tooltip>
        </div>
      </header>

      <div className={cn('flex min-h-0 flex-1 flex-col', messages.length === 0 && 'sm:justify-center sm:overflow-y-auto sm:py-12')}>
      <div ref={scrollRef} className={cn('min-h-0 flex-1 overflow-y-auto overscroll-contain', messages.length === 0 && 'sm:flex-none sm:overflow-visible')}>
        {messages.length === 0 ? (
          <div className="mx-auto flex min-h-full max-w-2xl flex-col items-center justify-center px-6 py-10 text-center sm:py-0">
            <h1 className="text-balance font-sans text-[1.6rem] font-semibold leading-[1.14] tracking-tight sm:text-[2.5rem] sm:leading-[1.12]">{t('private.title')}</h1>
          </div>
        ) : (
          <div className="mx-auto max-w-3xl space-y-8 px-4 py-8 sm:px-6" role="log" aria-label={t('private.title')}>
            {messages.map((message) => (
              <article key={message.id} className="min-w-0">
                <p className="mb-2 text-xs font-medium text-[var(--color-fg-muted)]">{message.role === 'user' ? t('private.you') : t('private.assistant')}</p>
                {message.images?.length ? <div className="mb-3 flex flex-wrap gap-2">{message.images.map((image, index) => <img key={index} src={privateImageURL(image)} alt={t('private.image', { index: index + 1 })} className="max-h-60 max-w-full rounded-[10px] object-contain" />)}</div> : null}
                {message.reasoning && <details className="mb-4 text-sm text-[var(--color-fg-muted)]"><summary className="cursor-pointer py-1">{t('private.reasoning')}</summary><p className="mt-2 whitespace-pre-wrap break-words leading-relaxed">{message.reasoning}</p></details>}
                {message.role === 'user' ? <p className="whitespace-pre-wrap break-words text-[0.9375rem] leading-relaxed [overflow-wrap:anywhere]">{message.text}</p> : <PrivateMarkdown text={message.text} />}
                {message.generatedImages?.map((image, index) => <img key={index} src={image} alt={t('private.image', { index: index + 1 })} className="mt-3 max-h-96 max-w-full rounded-[10px] object-contain" />)}
              </article>
            ))}
          </div>
        )}
      </div>

      <div className={cn('mx-auto w-full shrink-0 px-3 pb-2 sm:px-8 sm:pb-4', messages.length === 0 ? 'max-w-[48rem] sm:mt-10' : 'max-w-[var(--layout-message-max-w)]')}>
        {error && <p role="alert" className="mb-3 text-sm text-[var(--color-danger)]">{t(errorKey, { defaultValue: t('private.errors.private_provider_error') })}</p>}
        {streaming && <p role="status" className="mb-2 text-xs text-[var(--color-fg-muted)]">{t('private.responding')}</p>}
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
                <SelectContent>{models.map((item) => <SelectItem key={item.id} value={item.id} disabled={hasImageHistory && !item.vision}>{item.label}</SelectItem>)}</SelectContent>
              </Select>
            </div>
            {model?.vision && <>
              <input ref={fileRef} type="file" accept={PRIVATE_IMAGE_TYPES.join(',')} multiple className="hidden" onChange={(event) => void pickImages(event)} aria-label={t('private.addImage')} />
              <Tooltip content={t('private.addImage')}><Button variant="ghost" size="icon" loading={readingImages} disabled={streaming || readingImages} aria-label={t('private.addImage')} onClick={() => fileRef.current?.click()}><ImagePlus size={18} aria-hidden /></Button></Tooltip>
            </>}
            <div className="ml-auto">
              {streaming ? <Button size="icon" variant="secondary" aria-label={t('private.stop')} onClick={() => controllerRef.current?.abort()}><Square size={14} fill="currentColor" aria-hidden /></Button> : <Button type="submit" size="icon" className="rounded-full" aria-label={t('private.send')} disabled={!model || readingImages || (!draft.trim() && !images.length)}><ArrowUp size={18} aria-hidden /></Button>}
            </div>
          </div>
        </form>
      </div>
      </div>
    </div>
  )
}
