import type { ApiSandboxFile } from '@/api/types'
import type { Attachment } from '@/types/chat'

export interface SandboxBrowserEntry {
  name: string
  path: string
  type: 'directory' | 'file'
  size: number
}

function pathParts(value: string): string[] | null {
  if (!value || value.startsWith('/') || value.includes('\\')) return null
  const parts = value.split('/')
  if (parts.some((part) => !part || part === '.' || part === '..')) return null
  return parts
}

/** Build immediate children for a virtual directory from the flat sidecar list. */
export function sandboxEntriesAtPath(files: ApiSandboxFile[], currentPath: string): SandboxBrowserEntry[] {
  const currentParts = currentPath ? pathParts(currentPath) : []
  if (!currentParts) return []

  const directories = new Map<string, SandboxBrowserEntry>()
  const directFiles: SandboxBrowserEntry[] = []
  for (const file of files) {
    const parts = pathParts(file.path)
    if (!parts || parts.length <= currentParts.length) continue
    if (currentParts.some((part, index) => parts[index] !== part)) continue

    const remainder = parts.slice(currentParts.length)
    const childPath = [...currentParts, remainder[0]].join('/')
    if (remainder.length > 1) {
      if (!directories.has(remainder[0])) {
        directories.set(remainder[0], {
          name: remainder[0],
          path: childPath,
          type: 'directory',
          size: 0,
        })
      }
      continue
    }
    directFiles.push({
      name: remainder[0],
      path: file.path,
      type: 'file',
      size: Math.max(0, file.size || 0),
    })
  }

  const byName = (a: SandboxBrowserEntry, b: SandboxBrowserEntry) =>
    a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' })
  return [...Array.from(directories.values()).sort(byName), ...directFiles.sort(byName)]
}

export function sandboxParentPath(currentPath: string): string {
  const parts = pathParts(currentPath)
  return parts?.slice(0, -1).join('/') ?? ''
}

export function sandboxFileKind(name: string): Attachment['kind'] {
  const ext = name.slice(name.lastIndexOf('.') + 1).toLowerCase()
  if (['png', 'jpg', 'jpeg', 'jpe', 'jfif', 'gif', 'webp', 'bmp', 'tif', 'tiff', 'heic', 'heif', 'avif', 'ico', 'svg'].includes(ext)) return 'image'
  if (ext === 'pdf') return 'pdf'
  if (['doc', 'docx', 'ppt', 'pptx', 'rtf', 'odt', 'odp'].includes(ext)) return 'doc'
  if (['csv', 'tsv', 'xls', 'xlsx', 'xlsm', 'ods'].includes(ext)) return 'sheet'
  if (['py', 'js', 'jsx', 'ts', 'tsx', 'go', 'rs', 'java', 'c', 'cpp', 'h', 'hpp', 'sh', 'sql', 'json', 'yaml', 'yml', 'toml', 'html', 'css', 'md'].includes(ext)) return 'code'
  return 'other'
}
