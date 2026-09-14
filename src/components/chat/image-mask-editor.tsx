import { useEffect, useRef, useState, useId, type PointerEvent, type KeyboardEvent } from 'react'
import { Brush, Eraser, Undo2, Redo2, Trash2, Send, RefreshCw } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { Tooltip } from '@/components/ui/tooltip'
import { imageApi } from '@/api/endpoints'
import { drawMaskStroke, exportImageMask, maskPoint, validMaskDimensions, type MaskPoint, type MaskStroke } from '@/lib/image-mask'
import type { ArtifactRef } from '@/types/chat'

interface Props {
  image: ArtifactRef
  modelLabel: string
  onClose: () => void
  onSubmit: (prompt: string, mask: Blob) => Promise<void>
}

export function ImageMaskEditor({ image, modelLabel, onClose, onSubmit }: Props) {
  const { t } = useTranslation('chat')
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const stageRef = useRef<HTMLDivElement>(null)
  const [display, setDisplay] = useState({ width: 0, height: 0 })
  const strokes = useRef<MaskStroke[]>([])
  const redo = useRef<MaskStroke[]>([])
  const active = useRef<{ id: number; stroke: MaskStroke } | null>(null)
  const [history, setHistory] = useState({ undo: 0, redo: 0 })
  const [source, setSource] = useState<{ url: string; width: number; height: number } | null>(null)
  const [loadAttempt, setLoadAttempt] = useState(0)
  const [loadError, setLoadError] = useState('')
  const [error, setError] = useState('')
  const [brush, setBrush] = useState(32)
  const [erase, setErase] = useState(false)
  const [prompt, setPrompt] = useState('')
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)
  const [cursor, setCursor] = useState<MaskPoint | null>(null)
  const keyboardPoint = useRef<MaskPoint>({ x: 0, y: 0 })
  const fieldId = useId()

  useEffect(() => {
    const controller = new AbortController()
    let objectURL = ''
    setLoadError('')
    void imageApi.artifactBlob(image.id, controller.signal).then(async (blob) => {
      if (controller.signal.aborted) return
      objectURL = URL.createObjectURL(blob)
      const decoded = new Image()
      decoded.src = objectURL
      await decoded.decode()
      if (controller.signal.aborted) return
      if (!validMaskDimensions(decoded.naturalWidth, decoded.naturalHeight)) {
        setLoadError(t('imageEdit.tooLarge'))
        return
      }
      setSource({ url: objectURL, width: decoded.naturalWidth, height: decoded.naturalHeight })
    }).catch(() => { if (!controller.signal.aborted) setLoadError(t('imageEdit.loadFailed')) })
    return () => { controller.abort(); if (objectURL) URL.revokeObjectURL(objectURL) }
  }, [image.id, loadAttempt, t])

  useEffect(() => {
    if (!source || !stageRef.current) return
    const stage = stageRef.current
    const resize = () => {
      const width = Math.max(1, stage.clientWidth - 24)
      const height = Math.max(1, stage.clientHeight - 24)
      const scale = Math.min(width / source.width, height / source.height)
      setDisplay({ width: source.width * scale, height: source.height * scale })
    }
    const observer = new ResizeObserver(resize)
    observer.observe(stage)
    resize()
    return () => observer.disconnect()
  }, [source])

  function updateHistory() { setHistory({ undo: strokes.current.length, redo: redo.current.length }) }
  function redraw() {
    const canvas = canvasRef.current
    const context = canvas?.getContext('2d')
    if (!canvas || !context) return
    context.clearRect(0, 0, canvas.width, canvas.height)
    for (const stroke of strokes.current) drawMaskStroke(context, stroke)
    updateHistory()
  }
  function finishStroke() {
    if (!active.current) return
    strokes.current.push(active.current.stroke)
    redo.current = []
    active.current = null
    // Pointer moves paint incrementally for responsive feedback; redraw once
    // on release so the canonical stroke and its antialiasing match undo/redo.
    redraw()
  }
  function pointAt(event: PointerEvent<HTMLCanvasElement>) {
    const canvas = event.currentTarget
    const rect = canvas.getBoundingClientRect()
    setCursor({ x: event.clientX - rect.left, y: event.clientY - rect.top })
    return maskPoint(event.clientX, event.clientY, rect, canvas.width, canvas.height)
  }
  function pointerDown(event: PointerEvent<HTMLCanvasElement>) {
    if (busyRef.current || active.current || event.button !== 0) return
    event.preventDefault()
    event.currentTarget.focus()
    event.currentTarget.setPointerCapture(event.pointerId)
    const stroke: MaskStroke = { points: [pointAt(event)], radius: brush * event.currentTarget.width / event.currentTarget.getBoundingClientRect().width / 2, erase }
    active.current = { id: event.pointerId, stroke }
    drawMaskStroke(event.currentTarget.getContext('2d')!, stroke)
    setError('')
  }
  function pointerMove(event: PointerEvent<HTMLCanvasElement>) {
    const point = pointAt(event)
    if (!active.current || active.current.id !== event.pointerId || busyRef.current) return
    const stroke = active.current.stroke
    const previous = stroke.points[stroke.points.length - 1]
    stroke.points.push(point)
    drawMaskStroke(event.currentTarget.getContext('2d')!, { ...stroke, points: [previous, point] })
  }
  function keyboardDraw(event: KeyboardEvent<HTMLCanvasElement>) {
    if (busyRef.current) return
    const canvas = event.currentTarget
    const rect = canvas.getBoundingClientRect()
    const point = keyboardPoint.current
    const step = event.shiftKey ? 20 : 5
    const directions: Record<string, MaskPoint> = { ArrowLeft: { x: -step, y: 0 }, ArrowRight: { x: step, y: 0 }, ArrowUp: { x: 0, y: -step }, ArrowDown: { x: 0, y: step } }
    if (directions[event.key]) {
      event.preventDefault()
      point.x = Math.max(0, Math.min(rect.width, point.x + directions[event.key].x))
      point.y = Math.max(0, Math.min(rect.height, point.y + directions[event.key].y))
      setCursor({ ...point })
    } else if (event.key === ' ' || event.key === 'Enter') {
      event.preventDefault()
      strokes.current.push({ points: [maskPoint(rect.left + point.x, rect.top + point.y, rect, canvas.width, canvas.height)], radius: brush * canvas.width / rect.width / 2, erase })
      redo.current = []
      redraw()
    }
  }
  async function submit() {
    if (busyRef.current || !canvasRef.current || !prompt.trim()) return
    finishStroke()
    busyRef.current = true
    setBusy(true)
    setError('')
    try {
      const mask = await exportImageMask(canvasRef.current)
      if (!mask) { setError(t('imageEdit.selectionRequired')); return }
      await onSubmit(prompt.trim(), mask)
      onClose()
    } catch {
      setError(t('imageEdit.submitFailed'))
    } finally {
      busyRef.current = false
      setBusy(false)
    }
  }

  const tool = (label: string, icon: React.ReactNode, action: () => void, disabled = false, pressed?: boolean) => (
    <Tooltip content={label}>
      <Button size="icon-lg" variant={pressed ? 'secondary' : 'ghost'} aria-label={label} aria-pressed={pressed} disabled={disabled || busy} onClick={action}>{icon}</Button>
    </Tooltip>
  )

  return (
    <Dialog open onOpenChange={(open) => { if (!open && !busyRef.current) onClose() }}>
      <DialogContent size="full" closeDisabled={busy} aria-describedby={undefined} className="h-[min(90dvh,760px)] overflow-hidden rounded-lg" onInteractOutside={(event) => event.preventDefault()}>
        <DialogHeader className="pr-12">
          <DialogTitle>{t('imageEdit.title')}</DialogTitle>
          <p className="mt-1 truncate text-xs text-[var(--color-fg-muted)]">{modelLabel}</p>
        </DialogHeader>
        <div className="flex min-h-0 flex-1 flex-col overflow-y-auto border-t border-[var(--color-divider)] md:flex-row">
          <div className="flex min-w-0 shrink-0 flex-col bg-[var(--color-bg-subtle)] md:min-h-0 md:flex-1">
            <div className="flex flex-wrap items-center gap-0.5 border-b border-[var(--color-divider)] px-2 py-1">
              {tool(t('imageEdit.brush'), <Brush size={18} />, () => setErase(false), !source, !erase)}
              {tool(t('imageEdit.eraser'), <Eraser size={18} />, () => setErase(true), !source, erase)}
              {tool(t('imageEdit.undo'), <Undo2 size={18} />, () => { const last = strokes.current.pop(); if (last) redo.current.push(last); redraw() }, !history.undo)}
              {tool(t('imageEdit.redo'), <Redo2 size={18} />, () => { const last = redo.current.pop(); if (last) strokes.current.push(last); redraw() }, !history.redo)}
              {tool(t('imageEdit.clear'), <Trash2 size={18} />, () => { strokes.current = []; redo.current = []; redraw() }, !history.undo)}
              <label className="ml-auto flex items-center gap-2 px-2 text-xs text-[var(--color-fg-muted)]">
                <span className="sr-only">{t('imageEdit.brushSize')}</span>
                <input type="range" min="4" max="120" value={brush} onChange={(event) => setBrush(Number(event.target.value))} disabled={busy} className="h-10 w-24 accent-[var(--color-accent)]" />
                <output className="w-10 tabular-nums">{brush} px</output>
              </label>
            </div>
            <div ref={stageRef} className="flex h-[42dvh] min-h-[200px] items-center justify-center overflow-hidden p-3 md:h-auto md:min-h-0 md:flex-1" aria-busy={!source && !loadError}>
              {source ? (
                <div className="relative shrink-0 overflow-hidden" style={display}>
                  <img src={source.url} alt={image.filename} className="block h-full w-full object-contain" draggable={false} />
                  <canvas ref={canvasRef} width={source.width} height={source.height} tabIndex={0} aria-label={t('imageEdit.selection')} aria-keyshortcuts="ArrowUp ArrowDown ArrowLeft ArrowRight Space Enter" onKeyDown={keyboardDraw}
                    onFocus={(event) => { const rect = event.currentTarget.getBoundingClientRect(); keyboardPoint.current = { x: rect.width / 2, y: rect.height / 2 }; setCursor({ ...keyboardPoint.current }) }}
                    onBlur={() => setCursor(null)}
                    onPointerDown={pointerDown} onPointerMove={pointerMove} onPointerUp={finishStroke} onPointerCancel={finishStroke} onLostPointerCapture={finishStroke} onPointerLeave={() => setCursor(null)}
                    className="absolute inset-0 h-full w-full touch-none opacity-50 outline-offset-[-2px] focus-visible:outline-2 focus-visible:outline-[var(--color-ring)]" style={{ cursor: busy ? 'wait' : 'crosshair' }} />
                  {cursor && !busy ? <div aria-hidden className="pointer-events-none absolute rounded-full border-2 border-white bg-black/10 shadow-[0_0_0_1px_black]" style={{ width: brush, height: brush, left: cursor.x - brush / 2, top: cursor.y - brush / 2 }} /> : null}
                </div>
              ) : loadError ? <div role="alert" className="flex flex-col items-center gap-3 p-4 text-sm text-[var(--color-danger)]"><span>{loadError}</span><Button variant="secondary" size="sm" leadingIcon={<RefreshCw size={16} />} onClick={() => setLoadAttempt((value) => value + 1)}>{t('imageEdit.retry')}</Button></div> : <div className="h-48 w-full animate-pulse rounded bg-[var(--color-bg-muted)] motion-reduce:animate-none" />}
            </div>
          </div>
          <div className="flex shrink-0 flex-col gap-2 border-t border-[var(--color-divider)] p-4 md:w-72 md:border-t-0 md:border-l">
            <label htmlFor={fieldId} className="text-sm font-medium text-[var(--color-fg)]">{t('imageEdit.prompt')}</label>
            <Textarea id={fieldId} value={prompt} onChange={(event) => setPrompt(event.target.value)} disabled={busy} maxLength={32000} className="min-h-24 resize-none md:flex-1" />
            {error ? <p role="alert" className="text-sm text-[var(--color-danger)]">{error}</p> : null}
          </div>
        </div>
        <DialogFooter>
          <Button variant="ghost" disabled={busy} onClick={onClose}>{t('imageEdit.cancel')}</Button>
          <Button loading={busy} leadingIcon={<Send size={15} />} disabled={!source || !prompt.trim() || !history.undo} onClick={() => void submit()}>{t('imageEdit.submit')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
