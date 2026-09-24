/**
 * Folder-aware grouping for the composer's attachment chips.
 *
 * A shared project is hundreds of files, so showing one chip per file buries the
 * conversation under a wall of chips that all say the same thing. The folder the
 * user picked is the unit they think in, so the chip rail shows ONE node per
 * folder and keeps the individual files behind it.
 *
 * What is grouped, and what is deliberately not:
 *
 * - Only files uploaded from a directory picker carry `relPath`, so an ordinary
 *   single-file upload is never swallowed into some folder.
 * - The group key is the FIRST path segment. That is the folder's own name,
 *   which the server prefixes onto every stored path (`folderRelativeName`), and
 *   it matches what the user saw in the picker.
 * - A group of one still renders as a folder: it was picked as a folder, and
 *   collapsing it would make the file appear to jump between layouts.
 * - Sending is unaffected — every member file is still an attachment with its own
 *   id, so the message carries them all.
 */

/** The fields grouping needs; satisfied by `PendingAttachment` and `Attachment`. */
export interface FolderGroupableAttachment {
  id: string
  name: string
  /** Path inside the uploaded folder; absent/empty for a single-file upload. */
  relPath?: string
}

export interface FolderGroup<T> {
  /** The folder's name — the first segment of every member's `relPath`. */
  folder: string
  /** Member files, in the order they were attached. */
  members: T[]
}

export interface GroupedAttachments<T> {
  /** One entry per uploaded folder, in first-seen order. */
  folders: Array<FolderGroup<T>>
  /** Attachments that are not part of any folder, in their original order. */
  loose: T[]
}

/**
 * Split attachments into folder groups and loose files.
 *
 * `relPath` is treated as untrusted display input: a path with no usable first
 * element (empty, "." or "..") is left loose rather than inventing a folder
 * named "..".
 */
export function groupAttachmentsByFolder<T extends FolderGroupableAttachment>(
  attachments: readonly T[],
): GroupedAttachments<T> {
  const groups = new Map<string, FolderGroup<T>>()
  const loose: T[] = []

  for (const attachment of attachments) {
    const folder = folderRootOf(attachment.relPath)
    if (!folder) {
      loose.push(attachment)
      continue
    }
    const existing = groups.get(folder)
    if (existing) existing.members.push(attachment)
    else groups.set(folder, { folder, members: [attachment] })
  }

  return { folders: [...groups.values()], loose }
}

/** The folder name a relative path belongs to, or "" when it is not in one. */
export function folderRootOf(relPath: string | undefined): string {
  if (!relPath) return ''
  const first = relPath.replace(/\\/g, '/').split('/')[0] ?? ''
  if (!first || first === '.' || first === '..') return ''
  return first
}

/** One directory node of a drawer tree, with everything under it. */
export interface FolderTreeNode<T> {
  /** Directory name (its last path segment). */
  name: string
  /** Path from the uploaded folder's root down to this directory. */
  path: string
  /** Files directly inside this directory, in listing order. */
  files: T[]
  /** Subdirectories, in first-seen order. */
  children: Array<FolderTreeNode<T>>
  /** Files anywhere under this node (its own + every descendant's). */
  fileCount: number
  /** Combined bytes of those files. */
  size: number
}

export interface FolderTreeNodeMeta {
  /**
   * The file's folder-relative path. NOT a display fallback: a name or URL is
   * not a path, and feeding one in would invent directories that do not exist.
   */
  relPath?: string
  size?: number
}

/**
 * Build a directory tree for the files drawer.
 *
 * The drawer used to list every uploaded file flat, so a 300-file project was
 * 300 interchangeable rows. Grouping by `relPath` puts each picked folder back
 * together, with the subdirectories the user saw in their own file manager.
 *
 * `relPath` is untrusted display input: only the FIRST segment identifies a
 * folder, and a path with none (a single-file upload) stays at the top level
 * rather than being invented into a folder named "." or "..".
 */
export function fileFolderTree<T>(
  files: readonly T[],
  meta: (file: T) => FolderTreeNodeMeta,
): { rootFiles: T[]; folders: Array<FolderTreeNode<T>> } {
  const rootFiles: T[] = []
  const folders = new Map<string, FolderTreeNode<T>>()

  const ensureNode = (segments: string[]): FolderTreeNode<T> | null => {
    if (!segments.length) return null
    const path = segments.join('/')
    let node = folders.get(path)
    if (!node) {
      node = { name: segments[segments.length - 1], path, files: [], children: [], fileCount: 0, size: 0 }
      folders.set(path, node)
      // Registering the node in `folders` is what preserves first-seen order,
      // but only the root of each picked folder goes in the top-level list.
      if (segments.length > 1) {
        const parent = ensureNode(segments.slice(0, -1))
        if (parent && !parent.children.includes(node)) parent.children.push(node)
      }
    }
    return node
  }

  for (const file of files) {
    const info = meta(file)
    const path = (info.relPath ?? '').replace(/\\/g, '/')
    const segments = path.split('/').filter((segment) => segment.length > 0)
    // A path needs a folder AND a file inside it, and the folder must be a real
    // name: an absolute "/x.ts", "./x.ts" or "../x.ts" would otherwise invent a
    // directory called "", "." or "..". A single segment means the file was not
    // uploaded inside a folder, so it stays at the top level.
    const folder = segments[0] ?? ''
    if (path.startsWith('/') || segments.length < 2 || !folder || folder === '.' || folder === '..') {
      rootFiles.push(file)
      continue
    }
    const size = Number.isFinite(info.size) ? (info.size as number) : 0
    // Walk every level so intermediate directories exist even when only a deep
    // file was uploaded, and every ancestor counts the file.
    for (let depth = 1; depth < segments.length; depth += 1) {
      const node = ensureNode(segments.slice(0, depth))
      if (!node) continue
      node.fileCount += 1
      node.size += size
    }
    const leaf = ensureNode(segments.slice(0, -1))
    if (leaf) leaf.files.push(file)
  }

  return {
    rootFiles,
    folders: [...folders.values()].filter((node) => !node.path.includes('/')),
  }
}

/** The member's path shown inside an expanded folder, relative to the folder. */
export function pathWithinFolder(relPath: string | undefined, folder: string): string {
  if (!relPath) return ''
  const normalized = relPath.replace(/\\/g, '/')
  const prefix = `${folder}/`
  return normalized.startsWith(prefix) ? normalized.slice(prefix.length) : normalized
}

/** Total bytes of a folder group, for the chip's size line. */
export function folderGroupSize(members: ReadonlyArray<{ size: number }>): number {
  return members.reduce((total, member) => total + (Number.isFinite(member.size) ? member.size : 0), 0)
}

/** One element of the chip rail, in the order it should render. */
export type ChipRailItem<T> =
  | { type: 'folder'; folder: string; members: T[] }
  | { type: 'file'; attachment: T }

/**
 * Flatten grouped attachments back into a single ordered rail.
 *
 * `groupAttachmentsByFolder` groups by folder, which would otherwise move every
 * folder ahead of every loose file and make the rail reshuffle as files are
 * added. This keeps each folder at the position of its FIRST file, so the rail
 * only ever appends.
 */
export function chipRailItems<T extends FolderGroupableAttachment>(
  attachments: readonly T[],
): Array<ChipRailItem<T>> {
  const items: Array<ChipRailItem<T>> = []
  const folderAt = new Map<string, number>()

  for (const attachment of attachments) {
    const folder = folderRootOf(attachment.relPath)
    if (!folder) {
      items.push({ type: 'file', attachment })
      continue
    }
    const index = folderAt.get(folder)
    if (index === undefined) {
      folderAt.set(folder, items.length)
      items.push({ type: 'folder', folder, members: [attachment] })
      continue
    }
    const existing = items[index]
    if (existing.type === 'folder') existing.members.push(attachment)
  }

  return items
}
