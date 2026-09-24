/**
 * Folder picking for the composer.
 *
 * The app has always used `<input type="file" webkitdirectory>`, which is the
 * only *standard* way to let a user choose a directory. In practice it is not
 * enough on its own: it is a non-standard attribute, and a host webview that
 * ignores it hands back files whose `webkitRelativePath` is empty. Every file
 * then fails the "is inside a folder" test, so the upload silently does nothing
 * — which is exactly what a user reports as "folder upload doesn't react".
 *
 * The File System Access API (`showDirectoryPicker`) is the modern equivalent
 * and is available in Chromium-based shells (including desktop webviews). It
 * returns real directory handles, so the tree can be walked explicitly and the
 * paths are correct by construction rather than by trusting the picker to fill
 * in a magic property.
 *
 * This module is the seam: the composer prefers `showDirectoryPicker()` and
 * falls back to the hidden input, and the tree walk is a pure-ish async
 * function over handle-shaped objects so it is testable without a browser.
 */

/** The subset of `FileSystemDirectoryHandle` the walk needs. */
export interface DirectoryHandleLike {
  name: string
  values: () => AsyncIterable<DirectoryEntryLike>
}

/** The subset of `FileSystemFileHandle` the walk needs. */
export interface FileHandleLike {
  kind: 'file'
  name: string
  getFile: () => Promise<File>
}

/**
 * A directory handle as it appears *inside* a parent's `values()`. Real handles
 * always carry `kind`; requiring it here is what lets the walk discriminate a
 * file from a directory.
 */
export type DirectoryChildLike = Omit<DirectoryHandleLike, 'kind'> & { kind: 'directory' }

/** A directory entry: a file handle, or a directory handle to descend into. */
export type DirectoryEntryLike = FileHandleLike | DirectoryChildLike

export interface PickedFolderFile {
  file: File
  /** Path relative to the picked folder, "/"-separated, folder name included. */
  path: string
}

export interface PickedFolder {
  /** The picked directory's own name — the first segment of every `path`. */
  name: string
  files: PickedFolderFile[]
  /** True when a cap (depth or file count) stopped the walk early. */
  truncated: boolean
  /** Paths whose bytes could not be read (permission, removed mid-walk). */
  failed: string[]
}

/** How deep the walk descends. A guard against a pathological/cyclic tree. */
export const FOLDER_MAX_DEPTH = 16

/** How many files the walk will collect before it stops. */
export const FOLDER_MAX_WALKED_FILES = 20000

function isFileEntry(entry: DirectoryEntryLike): entry is FileHandleLike {
  return entry.kind === 'file'
}

/**
 * Walk a picked directory into a flat list of files with their relative paths.
 *
 * `walkDirectory(handle).files[n].path` is "<folder>/<sub>/<file>", which is the
 * exact shape `folderUploadFields` and the server expect, so a modern pick needs
 * no `webkitRelativePath` at all.
 *
 * Descent is recursive, so a nested subdirectory is enumerated exactly like the
 * root — that is the whole point of walking handles instead of trusting the
 * input's magic property. Which of these files is actually *worth* uploading is
 * `selectFolderFiles`'s job, deliberately kept separate so the walk stays a
 * plain enumeration.
 *
 * Neither a cap nor an unreadable entry throws away the rest of the walk: a
 * single file that cannot be read (permission denied, deleted mid-walk) is
 * recorded in `failed` and the remaining directories still upload, and reaching
 * a cap sets `truncated` so the caller can say so instead of quietly shipping
 * half the folder.
 */
export async function walkDirectory(
  handle: DirectoryHandleLike,
  options: { maxDepth?: number; maxFiles?: number } = {},
): Promise<PickedFolder> {
  const maxDepth = options.maxDepth ?? FOLDER_MAX_DEPTH
  const maxFiles = options.maxFiles ?? FOLDER_MAX_WALKED_FILES
  const files: PickedFolderFile[] = []
  const failed: string[] = []
  // A handle can be reached twice in a tree (symlinked/shared directories); the
  // first path wins so a file is never listed — or uploaded — twice.
  const seen = new Set<File>()
  let truncated = false

  const visit = async (dir: DirectoryHandleLike | DirectoryChildLike, prefix: string, depth: number): Promise<void> => {
    if (depth > maxDepth) {
      truncated = true
      return
    }
    for await (const entry of dir.values()) {
      if (files.length >= maxFiles) {
        truncated = true
        return
      }
      const path = `${prefix}/${entry.name}`
      if (isFileEntry(entry)) {
        let file: File
        try {
          file = await entry.getFile()
        } catch {
          // One unreadable file must not abort the whole folder.
          failed.push(path)
          continue
        }
        if (seen.has(file)) continue
        seen.add(file)
        files.push({ file, path })
        continue
      }
      await visit(entry, path, depth + 1)
    }
  }

  try {
    await visit(handle, handle.name, 1)
  } catch (error) {
    // The read failed part-way. Whatever was already enumerated is still worth
    // offering; rethrow only when that is nothing at all.
    if (!files.length) throw error
    truncated = true
  }
  return { name: handle.name, files, truncated, failed }
}

/** Minimal view of `window` for the optional File System Access API. */
type DirectoryPickerHost = Window & {
  showDirectoryPicker?: (options?: {
    mode?: 'read' | 'readwrite'
    id?: string
  }) => Promise<DirectoryHandleLike>
}

/** True when the host can open a real directory picker. */
export function canPickDirectoryWithFileSystemApi(): boolean {
  return typeof window !== 'undefined' && typeof (window as DirectoryPickerHost).showDirectoryPicker === 'function'
}

/**
 * Open the native directory picker and walk the chosen tree.
 *
 * Returns `null` when the host has no File System Access API — the caller must
 * then fall back to the hidden `webkitdirectory` input. A dismissed picker
 * rejects with `AbortError`, which the caller swallows as "the user changed
 * their mind" rather than as a failure.
 */
export async function pickFolderWithFileSystemApi(): Promise<PickedFolder | null> {
  const host = window as DirectoryPickerHost
  if (typeof host.showDirectoryPicker !== 'function') return null
  const handle = await host.showDirectoryPicker({ mode: 'read', id: 'aivory-folder-upload' })
  return walkDirectory(handle)
}

/** True when the rejection is the user dismissing the picker. */
export function isPickerCancellation(error: unknown): boolean {
  return Boolean(error) && (error as { name?: string }).name === 'AbortError'
}
