/**
 * Prepare chat image attachments without making users manage provider byte
 * limits themselves. Provider-compatible images at or below 3 MiB stay
 * byte-for-byte unchanged. Formats commonly produced by phones but rejected by
 * vision APIs (HEIC/HEIF, TIFF, BMP, AVIF, ICO) are always converted first.
 * Larger browser-decodable images are re-encoded at the original dimensions;
 * the canvas is only reduced when quality alone cannot meet the byte budget.
 */

export const IMAGE_UPLOAD_PASSTHROUGH_BYTES = 3 * 1024 * 1024
export const IMAGE_UPLOAD_TARGET_BYTES = IMAGE_UPLOAD_PASSTHROUGH_BYTES - 64 * 1024

const MAX_CANVAS_EDGE = 8192
const MAX_CANVAS_PIXELS = 32_000_000
const MAX_RESIZE_PASSES = 10
const HIGH_QUALITY = 0.94
const LOW_QUALITY = 0.5
const QUALITY_SEARCH_STEPS = 5
const RASTER_IMAGE_EXTENSIONS = new Set([
  'png', 'jpg', 'jpeg', 'jpe', 'jfif', 'gif', 'webp', 'bmp', 'tif', 'tiff',
  'heic', 'heif', 'avif', 'ico',
])
const PROVIDER_COMPATIBLE_IMAGE_MIMES = new Set([
  'image/jpeg',
  'image/png',
  'image/webp',
  'image/gif',
])
const PROVIDER_COMPATIBLE_IMAGE_EXTENSIONS = new Set([
  'jpg', 'jpeg', 'jpe', 'jfif', 'png', 'webp', 'gif',
])

interface DecodedImage {
  source: CanvasImageSource
  width: number
  height: number
  release: () => void
}

export interface PrepareImageOptions {
  /** Compatible original files no larger than this stay byte-for-byte unchanged. */
  passthroughBytes?: number
  /** Compressed output must fit under this provider/server-safe budget. */
  targetBytes?: number
}

function isRasterImageFile(file: File): boolean {
  const mimeType = file.type.toLowerCase()
  if (mimeType === 'image/svg+xml') return false
  const extension = file.name.toLowerCase().match(/\.([a-z0-9]+)$/)?.[1] ?? ''
  return RASTER_IMAGE_EXTENSIONS.has(extension) || mimeType.startsWith('image/')
}

function isProviderCompatibleImage(file: File): boolean {
  const mimeType = file.type.toLowerCase().trim()
  const extension = file.name.toLowerCase().match(/\.([a-z0-9]+)$/)?.[1] ?? ''
  // If either piece of metadata explicitly identifies a format vision APIs do
  // not reliably accept, convert it. This also covers iOS files whose MIME is
  // empty but whose filename still ends in HEIC/HEIF.
  if (mimeType.startsWith('image/') && !PROVIDER_COMPATIBLE_IMAGE_MIMES.has(mimeType)) return false
  if (RASTER_IMAGE_EXTENSIONS.has(extension) && !PROVIDER_COMPATIBLE_IMAGE_EXTENSIONS.has(extension)) return false
  return PROVIDER_COMPATIBLE_IMAGE_MIMES.has(mimeType) || PROVIDER_COMPATIBLE_IMAGE_EXTENSIONS.has(extension)
}

async function decodeImage(file: File): Promise<DecodedImage | null> {
  if (typeof createImageBitmap === 'function') {
    try {
      const bitmap = await createImageBitmap(file, { imageOrientation: 'from-image' })
      return {
        source: bitmap,
        width: bitmap.width,
        height: bitmap.height,
        release: () => bitmap.close(),
      }
    } catch {
      // Fall through to an HTMLImageElement. Safari can decode formats such as
      // HEIC here even when createImageBitmap cannot.
    }
  }

  if (typeof Image === 'undefined' || typeof URL === 'undefined') return null
  const objectURL = URL.createObjectURL(file)
  const image = new Image()
  image.decoding = 'async'
  try {
    await new Promise<void>((resolve, reject) => {
      image.onload = () => resolve()
      image.onerror = () => reject(new Error('image decode failed'))
      image.src = objectURL
    })
    if (!image.naturalWidth || !image.naturalHeight) {
      URL.revokeObjectURL(objectURL)
      return null
    }
    return {
      source: image,
      width: image.naturalWidth,
      height: image.naturalHeight,
      release: () => URL.revokeObjectURL(objectURL),
    }
  } catch {
    URL.revokeObjectURL(objectURL)
    return null
  }
}

function canvasToBlob(canvas: HTMLCanvasElement, mimeType: string, quality: number): Promise<Blob | null> {
  return new Promise((resolve) => {
    try {
      canvas.toBlob(resolve, mimeType, quality)
    } catch {
      resolve(null)
    }
  })
}

function outputName(originalName: string, mimeType: string): string {
  const stem = originalName.replace(/\.[^./\\]+$/, '') || 'image'
  const extension = mimeType === 'image/jpeg' ? 'jpg' : mimeType === 'image/png' ? 'png' : 'webp'
  return `${stem}.${extension}`
}

function safeInitialDimensions(width: number, height: number): { width: number; height: number } {
  const pixels = width * height
  const scale = Math.min(
    1,
    MAX_CANVAS_EDGE / Math.max(width, height),
    Math.sqrt(MAX_CANVAS_PIXELS / Math.max(1, pixels)),
  )
  return {
    width: Math.max(1, Math.round(width * scale)),
    height: Math.max(1, Math.round(height * scale)),
  }
}

function drawImage(
  decoded: DecodedImage,
  width: number,
  height: number,
  mimeType: string,
): HTMLCanvasElement | null {
  const canvas = document.createElement('canvas')
  try {
    canvas.width = width
    canvas.height = height
    const context = canvas.getContext('2d')
    if (!context) {
      canvas.width = 1
      canvas.height = 1
      return null
    }
    context.imageSmoothingEnabled = true
    context.imageSmoothingQuality = 'high'
    if (mimeType === 'image/jpeg') {
      context.fillStyle = '#ffffff'
      context.fillRect(0, 0, width, height)
    }
    context.drawImage(decoded.source, 0, 0, width, height)
    return canvas
  } catch {
    // Large canvases can fail synchronously on memory-constrained Safari. Free
    // any allocated backing store and let the caller retry at a smaller size.
    canvas.width = 1
    canvas.height = 1
    return null
  }
}

function releaseCanvas(canvas: HTMLCanvasElement) {
  // Resetting the dimensions releases the backing store promptly instead of
  // waiting for GC, which matters when several phone photos are attached.
  canvas.width = 1
  canvas.height = 1
}

async function highestQualityBlob(
  canvas: HTMLCanvasElement,
  mimeType: string,
  targetBytes: number,
): Promise<{ blob: Blob | null; fits: boolean }> {
  if (mimeType === 'image/png') {
    const png = await canvasToBlob(canvas, mimeType, 1)
    if (!png || png.type !== mimeType) return { blob: null, fits: false }
    return { blob: png, fits: png.size <= targetBytes }
  }

  const high = await canvasToBlob(canvas, mimeType, HIGH_QUALITY)
  if (high && high.type === mimeType && high.size <= targetBytes) {
    return { blob: high, fits: true }
  }

  const low = await canvasToBlob(canvas, mimeType, LOW_QUALITY)
  if (!low || low.type !== mimeType) return { blob: null, fits: false }
  if (low.size > targetBytes) {
    const smallest = !high || high.type !== mimeType || low.size <= high.size ? low : high
    return { blob: smallest, fits: false }
  }

  let best = low
  let lower = LOW_QUALITY
  let upper = HIGH_QUALITY
  for (let step = 0; step < QUALITY_SEARCH_STEPS; step += 1) {
    const quality = (lower + upper) / 2
    const candidate = await canvasToBlob(canvas, mimeType, quality)
    if (!candidate || candidate.type !== mimeType) {
      upper = quality
      continue
    }
    if (candidate.size <= targetBytes) {
      best = candidate
      lower = quality
    } else {
      upper = quality
    }
  }
  return { blob: best, fits: true }
}

async function compressDecodedImage(
  file: File,
  decoded: DecodedImage,
  targetBytes: number,
): Promise<File | null> {
  // WebP keeps transparency and usually preserves more detail per byte. JPEG is
  // retained for JPEG inputs and is also the compatibility fallback when this
  // browser cannot encode WebP.
  const extension = file.name.toLowerCase().match(/\.([a-z0-9]+)$/)?.[1] ?? ''
  const isJPEG =
    file.type.toLowerCase() === 'image/jpeg' || ['jpg', 'jpeg', 'jpe', 'jfif'].includes(extension)
  // PNG is the alpha-preserving fallback when WebP encoding is unavailable.
  // JPEG remains the last-resort compatibility format after transparency-safe
  // options have had a chance to meet the byte budget.
  const mimeTypes = isJPEG ? ['image/jpeg'] : ['image/webp', 'image/png', 'image/jpeg']

  for (const mimeType of mimeTypes) {
    let { width, height } = safeInitialDimensions(decoded.width, decoded.height)
    let smallest: Blob | null = null

    for (let pass = 0; pass < MAX_RESIZE_PASSES; pass += 1) {
      const canvas = drawImage(decoded, width, height, mimeType)
      if (!canvas) {
        width = Math.max(1, Math.floor(width * 0.75))
        height = Math.max(1, Math.floor(height * 0.75))
        continue
      }
      let encoded: Awaited<ReturnType<typeof highestQualityBlob>>
      try {
        encoded = await highestQualityBlob(canvas, mimeType, targetBytes)
      } finally {
        releaseCanvas(canvas)
      }
      if (encoded.blob && (!smallest || encoded.blob.size < smallest.size)) smallest = encoded.blob
      if (encoded.fits && encoded.blob) {
        return new File([encoded.blob], outputName(file.name, mimeType), {
          type: mimeType,
          lastModified: file.lastModified,
        })
      }

      const sizeRatio = encoded.blob ? Math.sqrt(targetBytes / Math.max(1, encoded.blob.size)) : 0.8
      const scale = Math.max(0.5, Math.min(0.86, sizeRatio * 0.94))
      const nextWidth = Math.max(1, Math.floor(width * scale))
      const nextHeight = Math.max(1, Math.floor(height * scale))
      if (nextWidth === width && nextHeight === height) break
      width = nextWidth
      height = nextHeight
    }

    if (smallest && smallest.size <= targetBytes) {
      return new File([smallest], outputName(file.name, mimeType), {
        type: mimeType,
        lastModified: file.lastModified,
      })
    }
  }
  return null
}

export async function prepareImageForUpload(
  file: File,
  options: PrepareImageOptions = {},
): Promise<File> {
  const passthroughBytes = Math.max(1, options.passthroughBytes ?? IMAGE_UPLOAD_PASSTHROUGH_BYTES)
  const requestedTarget = Math.max(1, options.targetBytes ?? IMAGE_UPLOAD_TARGET_BYTES)
  const targetBytes = Math.max(1, Math.min(requestedTarget, passthroughBytes - 1))
  if (!isRasterImageFile(file)) return file
  if (file.size <= passthroughBytes && isProviderCompatibleImage(file)) return file
  if (typeof document === 'undefined') return file

  try {
    const decoded = await decodeImage(file)
    if (!decoded) return file
    try {
      return (await compressDecodedImage(file, decoded, targetBytes)) ?? file
    } finally {
      decoded.release()
    }
  } catch {
    // Compression is an optimization around a real upload. Never leave the
    // attachment chip stuck in "uploading" because a browser codec or canvas
    // implementation threw unexpectedly.
    return file
  }
}
