/**
 * resizeImageForUpload — client-side downscale so an admin-uploaded announcement
 * image stays within a sane range. The server's icon endpoint caps uploads at
 * 256 KiB and does NOT resize, so a large photo would otherwise be rejected
 * outright. We cap the longest edge and re-encode to JPEG, dropping quality until
 * the result fits the byte budget. Small images that already fit are returned
 * untouched (so a crisp small PNG keeps its transparency/format).
 */
import { envNum } from '@/lib/env-config'

const DEFAULT_MAX_DIM = envNum('VITE_AURELIA_DEFAULT_MAX_DIM', 1280)
const DEFAULT_MAX_BYTES = envNum('VITE_AURELIA_DEFAULT_MAX_BYTES', 240 * 1024) // headroom under the server's 256 KiB cap
const QUALITY_START = envNum('VITE_AURELIA_QUALITY_START', 0.9)
const QUALITY_FLOOR = envNum('VITE_AURELIA_QUALITY_FLOOR', 0.4)
const QUALITY_STEP = envNum('VITE_AURELIA_QUALITY_STEP', 0.12)

function canvasToBlob(canvas: HTMLCanvasElement, quality: number): Promise<Blob | null> {
  return new Promise((resolve) => canvas.toBlob((b) => resolve(b), 'image/jpeg', quality))
}

export async function resizeImageForUpload(
  file: File,
  maxDim = DEFAULT_MAX_DIM,
  maxBytes = DEFAULT_MAX_BYTES,
): Promise<File> {
  if (!file.type.startsWith('image/') || typeof createImageBitmap !== 'function') return file

  let bitmap: ImageBitmap
  try {
    bitmap = await createImageBitmap(file)
  } catch {
    return file // undecodable here → let the server validate/reject
  }
  try {
    const { width, height } = bitmap
    const scale = Math.min(1, maxDim / Math.max(width, height))
    // Already within the dimension AND byte budget → keep the original as-is.
    if (scale === 1 && file.size <= maxBytes) return file

    const w = Math.max(1, Math.round(width * scale))
    const h = Math.max(1, Math.round(height * scale))
    const canvas = document.createElement('canvas')
    canvas.width = w
    canvas.height = h
    const ctx = canvas.getContext('2d')
    if (!ctx) return file
    // JPEG has no alpha — flatten transparency onto white instead of black.
    ctx.fillStyle = '#ffffff'
    ctx.fillRect(0, 0, w, h)
    ctx.drawImage(bitmap, 0, 0, w, h)

    let quality = QUALITY_START
    let blob = await canvasToBlob(canvas, quality)
    while (blob && blob.size > maxBytes && quality > QUALITY_FLOOR) {
      quality = Math.round((quality - QUALITY_STEP) * 100) / 100
      blob = await canvasToBlob(canvas, quality)
    }
    if (!blob) return file
    const name = file.name.replace(/\.[^.]+$/, '') + '.jpg'
    return new File([blob], name, { type: 'image/jpeg' })
  } finally {
    bitmap.close()
  }
}
