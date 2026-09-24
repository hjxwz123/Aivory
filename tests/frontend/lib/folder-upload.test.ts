import { describe, expect, it } from 'vitest'
import {
  FOLDER_MAX_FILES_DEFAULT,
  FOLDER_MAX_TOTAL_BYTES_DEFAULT,
  folderUploadFields,
  selectFolderFiles,
  type FolderLimits,
} from '@/lib/folder-upload'

/**
 * `selectFolderFiles` is generic over the candidate, so the tests drive it with
 * the part of a candidate the rules actually read — no `File` needed.
 */
function candidate(path: string, size = 10, folder?: string) {
  return { file: new File(['x'], path), path, fileName: path.split('/').pop() ?? path, size, folder }
}

const LIMITS: FolderLimits = {
  maxFiles: FOLDER_MAX_FILES_DEFAULT,
  maxTotalBytes: FOLDER_MAX_TOTAL_BYTES_DEFAULT,
  allowedExtensions: ['ts', 'tsx', 'md', 'json', 'txt'],
}

function reasons(selection: ReturnType<typeof selectFolderFiles>) {
  return Object.fromEntries(selection.skipped.map((s) => [s.reason, s.count]))
}

describe('folder upload selection', () => {
  it('keeps the files worth uploading and preserves their relative paths', () => {
    const selection = selectFolderFiles(
      [
        candidate('my-project/README.md'),
        candidate('my-project/src/app/main.ts'),
        candidate('my-project/src/app/main.test.ts'),
      ],
      LIMITS,
    )
    expect(selection.accepted.map((f) => f.path)).toEqual([
      'my-project/README.md',
      'my-project/src/app/main.ts',
      'my-project/src/app/main.test.ts',
    ])
    expect(selection.acceptedBytes).toBe(30)
    expect(selection.skipped).toEqual([])
  })

  it('drops dependency trees, VCS metadata and build output before considering extensions', () => {
    const selection = selectFolderFiles(
      [
        candidate('p/node_modules/left-pad/index.js'),
        candidate('p/.git/config'),
        candidate('p/dist/bundle.js'),
        candidate('p/build/out.exe'),
        candidate('p/__pycache__/mod.pyc'),
        candidate('p/src/keep.ts'),
      ],
      LIMITS,
    )
    expect(selection.accepted.map((f) => f.path)).toEqual(['p/src/keep.ts'])
    // .js/.exe/.pyc would ALSO have failed the extension allowlist; the skip
    // rules run first so the user is told the honest reason.
    expect(reasons(selection)).toEqual({ 'skipped-dir': 5 })
  })

  it('drops OS noise files by name', () => {
    const selection = selectFolderFiles(
      [candidate('p/.DS_Store'), candidate('p/Thumbs.db'), candidate('p/notes.txt')],
      LIMITS,
    )
    expect(selection.accepted.map((f) => f.path)).toEqual(['p/notes.txt'])
    expect(reasons(selection)).toEqual({ 'skipped-file': 2 })
  })

  it('filters by the admin allowlist and reports the extension reason', () => {
    const selection = selectFolderFiles(
      [candidate('p/a.exe'), candidate('p/b.bin'), candidate('p/c.ts')],
      LIMITS,
    )
    expect(selection.accepted.map((f) => f.path)).toEqual(['p/c.ts'])
    expect(reasons(selection)).toEqual({ extension: 2 })
  })

  it('keeps files at the picked folder root, whose path has no directory part', () => {
    // A modern directory picker reports the folder name separately, so a file
    // at the folder's root arrives as a single-segment path. It must be uploaded
    // — dropping it is how whole projects lost their README/Makefile/LICENSE.
    const selection = selectFolderFiles(
      [
        candidate('README.md', 10, 'my-project'),
        candidate('src/main.ts', 10, 'my-project'),
      ],
      LIMITS,
    )
    expect(selection.accepted.map((f) => f.path)).toEqual(['README.md', 'src/main.ts'])
    expect(selection.skipped).toEqual([])
  })

  it('does not mistake a root file for a skipped directory name', () => {
    // "build" as a FILE at the root is not the "build" output directory.
    const selection = selectFolderFiles([candidate('build', 10, 'my-project')], {
      ...LIMITS,
      allowedExtensions: [],
    })
    // It has no extension, so the honest reason is the extension rule — not
    // "skipped-dir", which would have been read off the folder name.
    expect(reasons(selection)).toEqual({ 'no-extension': 1 })
  })

  it('still skips a real skipped directory nested inside the folder', () => {
    const selection = selectFolderFiles([candidate('p/node_modules/x.ts', 10, 'p')], LIMITS)
    expect(reasons(selection)).toEqual({ 'skipped-dir': 1 })
  })

  it('rejects extensionless files, which the server cannot classify', () => {
    const selection = selectFolderFiles([candidate('p/Makefile'), candidate('p/ok.txt')], LIMITS)
    expect(selection.accepted.map((f) => f.path)).toEqual(['p/ok.txt'])
    expect(reasons(selection)).toEqual({ 'no-extension': 1 })
  })

  it('applies the file-count cap and reports how many were left out', () => {
    const selection = selectFolderFiles(
      [candidate('p/a.txt'), candidate('p/b.txt'), candidate('p/c.txt'), candidate('p/d.txt')],
      { ...LIMITS, maxFiles: 2 },
    )
    expect(selection.accepted.map((f) => f.path)).toEqual(['p/a.txt', 'p/b.txt'])
    expect(selection.truncated).toBe(true)
    expect(reasons(selection)).toEqual({ 'too-many': 2 })
  })

  it('applies the byte cap without splitting a file', () => {
    const selection = selectFolderFiles(
      [candidate('p/a.txt', 60), candidate('p/b.txt', 60), candidate('p/c.txt', 10)],
      { ...LIMITS, maxTotalBytes: 100 },
    )
    // a fits (60); b would exceed (120 > 100) so it is skipped; c then fits.
    expect(selection.accepted.map((f) => f.path)).toEqual(['p/a.txt', 'p/c.txt'])
    expect(selection.truncated).toBe(true)
    expect(reasons(selection)).toEqual({ 'too-large': 1 })
  })

  it('allows every extension when the allowlist is empty', () => {
    const selection = selectFolderFiles([candidate('p/a.exe')], { ...LIMITS, allowedExtensions: [] })
    expect(selection.accepted.map((f) => f.path)).toEqual(['p/a.exe'])
  })

  it('normalizes Windows separators reported by a desktop picker', () => {
    const selection = selectFolderFiles([candidate('p\\src\\main.ts')], LIMITS)
    // The candidate path is what the caller passes through; the SKIP check reads
    // it separator-agnostically, so a backslash path is not mistaken for one
    // unknown directory named "p\\src".
    expect(selection.accepted).toHaveLength(1)
    expect(reasons(selectFolderFiles([candidate('p\\node_modules\\x.ts')], LIMITS))).toEqual({
      'skipped-dir': 1,
    })
  })
})

// The composer sends these two multipart fields; the server rejects the request
// unless both are present and consistent. This is the contract that keeps a
// folder from silently uploading flat.
describe('folder upload request fields', () => {
  it('reports the folder name and the full relative path', () => {
    expect(folderUploadFields('my-project/src/main.ts', 'main.ts')).toEqual({
      folderName: 'my-project',
      relPath: 'my-project/src/main.ts',
    })
  })

  it('treats a single-segment path as a plain file, not a folder', () => {
    expect(folderUploadFields('main.ts', 'main.ts')).toBeNull()
    expect(folderUploadFields('', 'main.ts')).toBeNull()
  })

  it('uses the known folder for a file at the picked folder root', () => {
    // The modern picker reports the folder itself, so a root file's own path is
    // a single segment — the server composes "my-project/main.ts" from the pair.
    expect(folderUploadFields('main.ts', 'main.ts', 'my-project')).toEqual({
      folderName: 'my-project',
      relPath: 'main.ts',
    })
    expect(folderUploadFields('src/main.ts', 'main.ts', 'my-project')).toEqual({
      folderName: 'my-project',
      relPath: 'src/main.ts',
    })
  })

  it('ignores a known folder that contradicts an absolute or mismatched path', () => {
    expect(folderUploadFields('/etc/passwd', 'passwd', 'my-project')).toBeNull()
    expect(folderUploadFields('src/main.ts', 'other.ts', 'my-project')).toBeNull()
  })

  it('normalizes Windows separators into the server-side form', () => {
    expect(folderUploadFields('my-project\\src\\main.ts', 'main.ts')).toEqual({
      folderName: 'my-project',
      relPath: 'my-project/src/main.ts',
    })
  })

  it('drops the folder claim when the path contradicts the attached filename', () => {
    // Sending this would be a guaranteed 400 ("relative path does not match the
    // filename"), so it must not be claimed as a folder upload at all.
    expect(folderUploadFields('my-project/src/main.ts', 'other.ts')).toBeNull()
  })

  it('never produces an absolute or traversing path', () => {
    expect(folderUploadFields('/etc/passwd', 'passwd')).toBeNull()
    expect(folderUploadFields('../evil/a.txt', 'a.txt')).toBeNull()
  })
})
