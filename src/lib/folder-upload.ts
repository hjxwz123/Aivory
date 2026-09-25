export const FOLDER_MAX_FILES_DEFAULT = 300
export const FOLDER_MAX_TOTAL_BYTES_DEFAULT = 200 * 1024 * 1024
export const FOLDER_MAX_FILE_BYTES = 40 * 1024 * 1024

export interface FolderFile {
  file: File
  path: string
}

export function containsDroppedDirectory(items: DataTransferItemList | null, files: FileList | null): boolean {
  if (files && Array.from(files).some((file) => file.webkitRelativePath)) return true
  if (!items) return false
  return Array.from(items).some((item) => {
    if (item.kind !== 'file') return false
    const entry = (item as DataTransferItem & {
      webkitGetAsEntry?: () => { isDirectory: boolean } | null
    }).webkitGetAsEntry?.()
    return entry?.isDirectory === true
  })
}

export type SandboxFolderSelection =
  | { ok: true; folder: string; paths: string[]; files: File[] }
  | { ok: false; reason: 'empty' | 'paths' | 'limit' }

export function folderUploadFields(
  relativePath: string,
  fileName: string,
  knownFolder = '',
): { folderName: string; relPath: string } | null {
  const normalized = relativePath.replace(/\\/g, '/')
  const segments = normalized.split('/')
  const folderName = knownFolder || segments[0]
  if (!folderName || folderName.includes('/') || folderName.includes('\\') ||
      folderName === '.' || folderName === '..' ||
      segments.length < (knownFolder ? 1 : 2) ||
      segments.some((segment) => !segment || segment === '.' || segment === '..' ||
        new TextEncoder().encode(segment).length > 200 || /[\x00-\x1f\x7f]/.test(segment)) ||
      segments[segments.length - 1] !== fileName) return null
  const relPath = segments.join('/')
  if (segments.length > 32 || new TextEncoder().encode(relPath).length > 1024) return null
  return { folderName, relPath }
}

export function prepareSandboxFolderUpload(entries: readonly FolderFile[], knownFolder = ''): SandboxFolderSelection {
  if (!entries.length) return { ok: false, reason: 'empty' }
  const folder = knownFolder || entries[0].path.replace(/\\/g, '/').split('/')[0]
  if (!folder || folder === '.' || folder === '..' || folder.includes('/') || folder.includes('\\') ||
      new TextEncoder().encode(folder).length > 200) return { ok: false, reason: 'paths' }
  if (entries.length > FOLDER_MAX_FILES_DEFAULT ||
      entries.some(({ file }) => file.size > FOLDER_MAX_FILE_BYTES) ||
      entries.reduce((sum, { file }) => sum + file.size, 0) > FOLDER_MAX_TOTAL_BYTES_DEFAULT) {
    return { ok: false, reason: 'limit' }
  }
  const paths: string[] = []
  const seen = new Set<string>()
  for (const { file, path } of entries) {
    const fields = folderUploadFields(path, file.name, folder)
    if (!fields || fields.folderName !== folder) return { ok: false, reason: 'paths' }
    const fullPath = fields.relPath.startsWith(`${folder}/`) ? fields.relPath : `${folder}/${fields.relPath}`
    if (seen.has(fullPath) || !folderUploadFields(fullPath, file.name, folder)) {
      return { ok: false, reason: 'paths' }
    }
    seen.add(fullPath)
    paths.push(fullPath)
  }
  return { ok: true, folder, paths, files: entries.map((entry) => entry.file) }
}
