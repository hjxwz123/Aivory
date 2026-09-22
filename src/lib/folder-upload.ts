/**
 * Folder-upload selection rules.
 *
 * A directory picker happily hands back a project's `node_modules`, build
 * output and lockfiles — tens of thousands of files that are both useless to the
 * model and fatal to an upload that is one request per file. Everything that
 * decides "what actually goes up" lives here, as a pure function, so the rules
 * are unit-testable and the composer stays a rendering concern.
 *
 * Two independent filters, in this order:
 *
 * 1. SKIP RULES drop paths that are never worth uploading, regardless of what
 *    the server's allowlist says (dependency trees, VCS metadata, build output,
 *    editor/OS noise). These are cheap string checks on the relative path.
 * 2. The admin's extension allowlist decides the rest. The server re-validates
 *    every file anyway (`accept`-style client checks are advisory), but
 *    pre-filtering means one unsupported file cannot fail the whole batch, and
 *    the user is told exactly what was left out.
 */

/** Directory names that are never uploaded. Matched case-insensitively. */
export const FOLDER_SKIP_DIRS: readonly string[] = [
  'node_modules',
  '.git',
  '.svn',
  '.hg',
  '.idea',
  '.vscode',
  '.venv',
  'venv',
  '__pycache__',
  '.pytest_cache',
  '.mypy_cache',
  '.ruff_cache',
  '.next',
  '.nuxt',
  '.turbo',
  '.parcel-cache',
  'dist',
  'build',
  'out',
  'target',
  'coverage',
  '.cache',
  '.gradle',
  '.terraform',
  'vendor',
  'Pods',
  'DerivedData',
]

/** Exact file names that are never uploaded. Matched case-insensitively. */
export const FOLDER_SKIP_FILES: readonly string[] = [
  '.DS_Store',
  'Thumbs.db',
  'desktop.ini',
]

/** Default ceiling on how many files one folder upload may contain. */
export const FOLDER_MAX_FILES_DEFAULT = 300
/** Default ceiling on the folder's combined bytes. */
export const FOLDER_MAX_TOTAL_BYTES_DEFAULT = 200 * 1024 * 1024

export interface FolderLimits {
  maxFiles: number
  maxTotalBytes: number
  /** Extensions (lowercase, no dot) the server accepts. Empty = allow all. */
  allowedExtensions: readonly string[]
}

export interface FolderCandidate {
  /** Path relative to the picked folder, "/"-separated ("src/app/main.ts"). */
  path: string
  fileName: string
  size: number
}

export type FolderSkipReason =
  | 'skipped-dir'
  | 'skipped-file'
  | 'extension'
  | 'too-many'
  | 'too-large'
  | 'no-extension'

export interface FolderSelection<T extends FolderCandidate> {
  accepted: T[]
  /** One representative path per skip reason, for an honest UI summary. */
  skipped: Array<{ reason: FolderSkipReason; path: string; count: number }>
  /** Total bytes of `accepted`. */
  acceptedBytes: number
  /** True when the file-count cap truncated the selection. */
  truncated: boolean
}

/**
 * Turn a picked file's path into the two multipart fields the server expects,
 * or null when the path cannot describe a folder upload.
 *
 * This is the contract between the browser's directory picker and
 * `uploadFileHandler`: the server requires BOTH `folder_name` (one path segment)
 * and `rel_path` (the path including that folder), and rejects a `rel_path`
 * whose last segment is not the attached filename. Keeping the rule here — pure
 * and tested — means the composer cannot drift from the server.
 *
 * A path with a single segment ("a.txt") is NOT a folder upload: the user
 * attached one file, and the server should treat it exactly as before.
 */
export function folderUploadFields(
  relativePath: string,
  fileName: string,
): { folderName: string; relPath: string } | null {
  const normalized = relativePath.replace(/\\/g, '/')
  // An absolute path is not a folder-relative path. The server rejects it
  // outright, so the file is uploaded as a plain attachment instead of as a
  // folder member whose path the server would refuse.
  if (normalized.startsWith('/')) return null
  const segments = normalized.split('/').filter((segment) => segment.length > 0)
  if (segments.length < 2) return null
  const [folderName] = segments
  if (!folderName || folderName === '.' || folderName === '..') return null
  // The server refuses a rel_path that contradicts the filename, so drop the
  // folder claim rather than sending a request that is guaranteed to 400.
  if (segments[segments.length - 1] !== fileName) return null
  return { folderName, relPath: segments.join('/') }
}

function pathSegments(relPath: string): string[] {
  return relPath.replace(/\\/g, '/').split('/').filter(Boolean)
}

function extensionOf(fileName: string): string {
  const dot = fileName.lastIndexOf('.')
  if (dot <= 0 || dot === fileName.length - 1) return ''
  return fileName.slice(dot + 1).toLowerCase()
}

/**
 * Decide which picked files are uploaded.
 *
 * Order is deliberate: skip rules are applied by DIRECTORY first so one excluded
 * `node_modules` costs a single string comparison per file rather than a
 * per-file extension check, and the caps are applied last so the summary reports
 * the real reason a file was left out (a file inside `dist` is "skipped-dir",
 * not "too-many").
 */
export function selectFolderFiles<T extends FolderCandidate>(
  candidates: readonly T[],
  limits: FolderLimits,
): FolderSelection<T> {
  const allowed = limits.allowedExtensions.length
    ? new Set(limits.allowedExtensions.map((e) => e.toLowerCase().replace(/^\./, '')))
    : null

  const accepted: T[] = []
  const skipped = new Map<FolderSkipReason, { path: string; count: number }>()
  let acceptedBytes = 0
  let truncated = false

  const record = (reason: FolderSkipReason, path: string) => {
    const existing = skipped.get(reason)
    if (existing) existing.count += 1
    else skipped.set(reason, { path, count: 1 })
  }

  for (const candidate of candidates) {
    const segments = pathSegments(candidate.path)
    const dirSegments = segments.slice(0, -1)
    if (dirSegments.some((segment) => FOLDER_SKIP_DIRS.includes(segment))) {
      record('skipped-dir', candidate.path)
      continue
    }
    if (FOLDER_SKIP_FILES.includes(candidate.fileName)) {
      record('skipped-file', candidate.path)
      continue
    }
    const extension = extensionOf(candidate.fileName)
    if (!extension) {
      // The server rejects extensionless files outright, so filter them here
      // rather than spending a request to learn that.
      record('no-extension', candidate.path)
      continue
    }
    if (allowed && !allowed.has(extension)) {
      record('extension', candidate.path)
      continue
    }
    if (accepted.length >= limits.maxFiles) {
      record('too-many', candidate.path)
      truncated = true
      continue
    }
    if (acceptedBytes + candidate.size > limits.maxTotalBytes) {
      record('too-large', candidate.path)
      truncated = true
      continue
    }
    accepted.push(candidate)
    acceptedBytes += candidate.size
  }

  return {
    accepted,
    skipped: [...skipped.entries()].map(([reason, value]) => ({ reason, ...value })),
    acceptedBytes,
    truncated,
  }
}
