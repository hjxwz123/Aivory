export type ImageAttachmentCapability = 'allowed' | 'blocked' | 'unknown'

export interface ImageAttachmentModel {
  kind?: string
  vision?: boolean
}

export interface FileIdentity {
  name: string
  type: string
}

const IMAGE_EXTENSIONS = new Set([
  'png',
  'jpg',
  'jpeg',
  'jpe',
  'jfif',
  'gif',
  'webp',
  'bmp',
  'tif',
  'tiff',
  'heic',
  'heif',
  'avif',
  'ico',
  // SVG is rejected by the default upload policy, but it is still an image
  // for capability gating and must never slip through as a generic document.
  'svg',
])

/**
 * Resolves whether the active turn can send image attachments. Fast mode uses
 * only the anonymous capability returned by /api/models, never the hidden
 * fast-model record. Image-generation models always accept reference images.
 */
export function resolveImageAttachmentCapability(
  model: ImageAttachmentModel | null | undefined,
  options: { fast: boolean; fastVision: boolean },
): ImageAttachmentCapability {
  // Image-generation models accept reference images directly. This takes
  // precedence over a stale fast-mode preference carried across routes.
  if (model?.kind === 'image') return 'allowed'
  if (options.fast) return options.fastVision ? 'allowed' : 'blocked'
  if (!model) return 'unknown'
  return model.kind === 'chat' && model.vision === true ? 'allowed' : 'blocked'
}

/** Browser MIME metadata is often blank for HEIC/AVIF, so extension wins. */
export function isImageFileLike(file: FileIdentity): boolean {
  const ext = file.name.toLowerCase().match(/\.([a-z0-9]+)$/)?.[1] ?? ''
  return IMAGE_EXTENSIONS.has(ext) || file.type.toLowerCase().startsWith('image/')
}

export function filterFilesForImageCapability<T extends FileIdentity>(
  files: readonly T[],
  capability: ImageAttachmentCapability,
): { accepted: T[]; rejectedImages: T[] } {
  if (capability === 'allowed') return { accepted: [...files], rejectedImages: [] }
  const rejectedImages = files.filter(isImageFileLike)
  return {
    accepted: files.filter((file) => !isImageFileLike(file)),
    rejectedImages,
  }
}

// The input hint is only a convenience; filterFilesForImageCapability remains
// authoritative for file picks, drops and clipboard images.
export const NON_IMAGE_ATTACHMENT_ACCEPT = [
  'text/*',
  'application/pdf',
  'application/json',
  'application/xml',
  'application/rtf',
  'application/msword',
  'application/vnd.ms-excel',
  'application/vnd.ms-powerpoint',
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  '.pdf',
  '.doc',
  '.docx',
  '.ppt',
  '.pptx',
  '.xls',
  '.xlsx',
  '.csv',
  '.tsv',
  '.txt',
  '.md',
  '.markdown',
  '.json',
  '.yaml',
  '.yml',
  '.xml',
  '.log',
  '.rtf',
  '.py',
  '.go',
  '.js',
  '.ts',
  '.tsx',
  '.jsx',
  '.rs',
  '.java',
  '.c',
  '.cc',
  '.cpp',
  '.h',
  '.hpp',
  '.sql',
  '.toml',
  '.ini',
  '.env',
].join(',')
