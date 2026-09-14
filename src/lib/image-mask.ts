export const MAX_MASK_PIXELS = 16 * 1024 * 1024
export const MAX_MASK_EDGE = 8192

export interface MaskPoint { x: number; y: number }
export interface MaskStroke {
  points: MaskPoint[]
  radius: number
  erase: boolean
}

export function validMaskDimensions(width: number, height: number): boolean {
  return width > 0 && height > 0 && width <= MAX_MASK_EDGE && height <= MAX_MASK_EDGE && width * height <= MAX_MASK_PIXELS
}

export function maskPoint(clientX: number, clientY: number, rect: Pick<DOMRect, 'left' | 'top' | 'width' | 'height'>, width: number, height: number): MaskPoint {
  return {
    x: Math.max(0, Math.min(width, (clientX - rect.left) * width / rect.width)),
    y: Math.max(0, Math.min(height, (clientY - rect.top) * height / rect.height)),
  }
}

export function drawMaskStroke(context: CanvasRenderingContext2D, stroke: MaskStroke) {
  if (!stroke.points.length) return
  context.save()
  context.globalCompositeOperation = stroke.erase ? 'destination-out' : 'source-over'
  context.fillStyle = context.strokeStyle = '#0891b2'
  context.lineCap = context.lineJoin = 'round'
  context.lineWidth = stroke.radius * 2
  const first = stroke.points[0]
  context.beginPath()
  context.arc(first.x, first.y, stroke.radius, 0, Math.PI * 2)
  context.fill()
  if (stroke.points.length > 1) {
    context.beginPath()
    context.moveTo(first.x, first.y)
    for (const point of stroke.points.slice(1)) context.lineTo(point.x, point.y)
    context.stroke()
  }
  context.restore()
}

// OpenAI edits fully transparent pixels. The displayed cyan selection has the
// opposite alpha convention; threshold antialiased edges to preserve all marks.
export function selectionToMask(pixels: Uint8ClampedArray): boolean {
  let selected = false
  for (let i = 0; i < pixels.length; i += 4) {
    const editable = pixels[i + 3] > 0
    selected ||= editable
    pixels[i] = pixels[i + 1] = pixels[i + 2] = 0
    pixels[i + 3] = editable ? 0 : 255
  }
  return selected
}

export async function exportImageMask(canvas: HTMLCanvasElement): Promise<Blob | null> {
  const context = canvas.getContext('2d')
  if (!context) throw new Error('canvas unavailable')
  const pixels = context.getImageData(0, 0, canvas.width, canvas.height)
  if (!selectionToMask(pixels.data)) return null
  const output = document.createElement('canvas')
  output.width = canvas.width
  output.height = canvas.height
  output.getContext('2d')!.putImageData(pixels, 0, 0)
  return new Promise((resolve, reject) => output.toBlob((blob) => {
    output.width = output.height = 0
    if (blob) resolve(blob)
    else reject(new Error('mask export failed'))
  }, 'image/png'))
}
