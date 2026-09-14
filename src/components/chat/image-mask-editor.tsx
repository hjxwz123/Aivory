import { useEffect, useRef, useState, useId, type PointerEvent, type KeyboardEvent } from 'react'
import { Brush, Eraser, Undo2, Redo2, Trash2, Send, RefreshCw, Eye, EyeOff, ImageIcon, Scan, X, CircleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { Tooltip } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'
import { imageApi } from '@/api/endpoints'
import { drawMaskStroke, exportImageMask, maskPoint, validMaskDimensions, type MaskPoint, type MaskStroke } from '@/lib/image-mask'
import type { ArtifactRef } from '@/types/chat'
import styles from './image-mask-editor.module.css'

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
  const [showSelection, setShowSelection] = useState(true)
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
      const padding = getComputedStyle(stage)
      const width = Math.max(1, stage.clientWidth - parseFloat(padding.paddingLeft) - parseFloat(padding.paddingRight))
      const height = Math.max(1, stage.clientHeight - parseFloat(padding.paddingTop) - parseFloat(padding.paddingBottom))
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
    const context = canvas?.getContext('2d', { willReadFrequently: true })
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
    if (busyRef.current || !showSelection || active.current || event.button !== 0) return
    event.preventDefault()
    event.currentTarget.focus()
    event.currentTarget.setPointerCapture(event.pointerId)
    const stroke: MaskStroke = { points: [pointAt(event)], radius: brush * event.currentTarget.width / event.currentTarget.getBoundingClientRect().width / 2, erase }
    active.current = { id: event.pointerId, stroke }
    drawMaskStroke(event.currentTarget.getContext('2d', { willReadFrequently: true })!, stroke)
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
    if (busyRef.current || !showSelection) return
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
      <Button size="icon-lg" variant="ghost" className={styles.tool} aria-label={label} aria-pressed={pressed} disabled={disabled || busy} onClick={action}>{icon}</Button>
    </Tooltip>
  )

  return (
    <Dialog open onOpenChange={(open) => { if (!open && !busyRef.current) onClose() }}>
      <DialogContent size="full" showClose={false} aria-describedby={undefined} className={styles.dialog} onInteractOutside={(event) => event.preventDefault()}>
        <DialogHeader className={styles.header}>
          <ImageIcon size={21} className={styles.headerIcon} aria-hidden />
          <div className={styles.heading}>
            <DialogTitle>{t('imageEdit.title')}</DialogTitle>
            <p className={styles.filename} title={image.filename}>{image.filename}</p>
          </div>
          <Button size="icon-lg" variant="ghost" className={styles.close} aria-label={t('aria.close', { ns: 'common' })} disabled={busy} onClick={onClose}><X size={18} /></Button>
        </DialogHeader>
        <div className={styles.body}>
          <div className={styles.workspace}>
            <div className={styles.toolbar}>
              <div role="group" aria-label={t('imageEdit.selection')} className={styles.modes}>
                {tool(t('imageEdit.brush'), <Brush size={18} />, () => { setErase(false); setShowSelection(true) }, !source, !erase)}
                {tool(t('imageEdit.eraser'), <Eraser size={18} />, () => { setErase(true); setShowSelection(true) }, !source, erase)}
              </div>
              <span className={styles.separator} aria-hidden />
              <div className={styles.history}>
                {tool(t('imageEdit.undo'), <Undo2 size={18} />, () => { const last = strokes.current.pop(); if (last) redo.current.push(last); redraw() }, !history.undo)}
                {tool(t('imageEdit.redo'), <Redo2 size={18} />, () => { const last = redo.current.pop(); if (last) strokes.current.push(last); redraw() }, !history.redo)}
                {tool(t('imageEdit.clear'), <Trash2 size={18} />, () => { strokes.current = []; redo.current = []; redraw() }, !history.undo)}
              </div>
              <div className={styles.viewControl}>
                {tool(t('imageEdit.showSelection'), showSelection ? <Eye size={18} /> : <EyeOff size={18} />, () => { finishStroke(); setShowSelection((value) => !value); setCursor(null) }, !source, showSelection)}
              </div>
            </div>
            <div ref={stageRef} className={styles.stage} aria-busy={!source && !loadError}>
              {source ? (
                <div className={styles.image} style={display}>
                  <img src={source.url} alt={image.filename} draggable={false} />
                  <canvas ref={canvasRef} width={source.width} height={source.height} tabIndex={busy || !showSelection ? -1 : 0} aria-disabled={busy || !showSelection} aria-label={t('imageEdit.selection')} aria-keyshortcuts="ArrowUp ArrowDown ArrowLeft ArrowRight Space Enter" onKeyDown={keyboardDraw}
                    onFocus={(event) => { const rect = event.currentTarget.getBoundingClientRect(); keyboardPoint.current = { x: rect.width / 2, y: rect.height / 2 }; setCursor({ ...keyboardPoint.current }) }}
                    onBlur={() => setCursor(null)}
                    onPointerDown={pointerDown} onPointerMove={pointerMove} onPointerUp={finishStroke} onPointerCancel={finishStroke} onLostPointerCapture={finishStroke} onPointerLeave={() => setCursor(null)}
                    className={styles.canvas} style={{ opacity: showSelection ? 0.5 : 0, cursor: busy ? 'wait' : showSelection ? 'none' : 'default' }} />
                  {cursor && !busy && showSelection ? <div aria-hidden className={cn(styles.cursor, erase && styles.eraserCursor)} style={{ width: brush, height: brush, left: cursor.x - brush / 2, top: cursor.y - brush / 2 }} /> : null}
                </div>
              ) : loadError ? <div role="alert" className={styles.loadError}><CircleAlert size={24} aria-hidden /><span>{loadError}</span><Button variant="secondary" size="sm" leadingIcon={<RefreshCw size={16} />} onClick={() => setLoadAttempt((value) => value + 1)}>{t('imageEdit.retry')}</Button></div> : <div className={cn(styles.skeleton, 'animate-pulse motion-reduce:animate-none')}><ImageIcon size={32} aria-hidden /></div>}
            </div>
            <div className={styles.imageInfo}>
              <Scan size={14} aria-hidden />
              <span>{source ? `${source.width} \u00d7 ${source.height} px` : '\u2014'}</span>
            </div>
          </div>
          <div className={styles.inspector}>
            <div className={styles.brushControl}>
              <div className={styles.controlHeading}>
                <label htmlFor={`${fieldId}-brush`}>{t('imageEdit.brushSize')}</label>
                <output htmlFor={`${fieldId}-brush`} className={styles.brushValue}>{brush}<span>px</span></output>
              </div>
              <div className={styles.brushSlider}>
                <span className={styles.brushPreview} aria-hidden><span style={{ width: 4 + brush / 6, height: 4 + brush / 6 }} /></span>
                <input id={`${fieldId}-brush`} type="range" min="4" max="120" value={brush} onChange={(event) => setBrush(Number(event.target.value))} disabled={busy || !source} />
              </div>
            </div>
            <div className={styles.promptControl}>
              <label htmlFor={fieldId}>{t('imageEdit.prompt')}</label>
              <Textarea id={fieldId} value={prompt} onChange={(event) => setPrompt(event.target.value)} disabled={busy} maxLength={32000} className={styles.prompt} aria-describedby={error ? `${fieldId}-error` : undefined} />
              {error ? <p id={`${fieldId}-error`} role="alert" className={styles.error}><CircleAlert size={16} aria-hidden /><span>{error}</span></p> : null}
            </div>
          </div>
        </div>
        <DialogFooter className={styles.footer}>
          <div className={styles.model} title={modelLabel}><ImageIcon size={15} aria-hidden /><span>{modelLabel}</span></div>
          <Button variant="ghost" className={styles.cancel} disabled={busy} onClick={onClose}>{t('imageEdit.cancel')}</Button>
          <Button loading={busy} leadingIcon={<Send size={16} />} className={styles.submit} disabled={!source || !prompt.trim() || !history.undo} onClick={() => void submit()}>{t('imageEdit.submit')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
