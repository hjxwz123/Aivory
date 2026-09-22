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

/** The folder name a relative path belongs to, or "" when it is not in one. */export function folderRootOf(relPath: string | undefined): string {
  if (!relPath) return ''
  const first = relPath.replace(/\\/g, '/').split('/')[0] ?? ''
  if (!first || first === '.' || first === '..') return ''
  return first
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
