const END_OF_CENTRAL_DIRECTORY = 0x06054b50
const CENTRAL_DIRECTORY_ENTRY = 0x02014b50
const MIN_END_RECORD_BYTES = 22
const MAX_ZIP_COMMENT_BYTES = 65_535

export interface OoxmlArchiveLimits {
  maxEntries: number
  maxEntryUncompressedBytes: number
  maxTotalUncompressedBytes: number
  maxCompressionRatio: number
}

export const OOXML_PREVIEW_ARCHIVE_LIMITS: Readonly<OoxmlArchiveLimits> = Object.freeze({
  maxEntries: 4_000,
  maxEntryUncompressedBytes: 32 * 1024 * 1024,
  maxTotalUncompressedBytes: 128 * 1024 * 1024,
  maxCompressionRatio: 500,
})

export class OoxmlArchiveLimitError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'OoxmlArchiveLimitError'
  }
}

function invalidArchive(message: string): Error {
  return new Error(`Invalid OOXML archive: ${message}`)
}

function findEndRecord(view: DataView): number {
  const firstCandidate = view.byteLength - MIN_END_RECORD_BYTES
  const lastCandidate = Math.max(0, firstCandidate - MAX_ZIP_COMMENT_BYTES)

  for (let offset = firstCandidate; offset >= lastCandidate; offset -= 1) {
    if (view.getUint32(offset, true) !== END_OF_CENTRAL_DIRECTORY) continue
    const commentBytes = view.getUint16(offset + 20, true)
    if (offset + MIN_END_RECORD_BYTES + commentBytes === view.byteLength) return offset
  }

  throw invalidArchive('missing central directory')
}

/**
 * Reads only ZIP central-directory metadata. No entry is decompressed, which
 * makes this a cheap guard before DOCX/XLSX parsers see an untrusted archive.
 */
export function validateOoxmlArchive(
  data: ArrayBuffer,
  limits: OoxmlArchiveLimits = OOXML_PREVIEW_ARCHIVE_LIMITS,
): void {
  if (data.byteLength < MIN_END_RECORD_BYTES) throw invalidArchive('file is too short')

  const view = new DataView(data)
  const endOffset = findEndRecord(view)
  const diskNumber = view.getUint16(endOffset + 4, true)
  const centralDirectoryDisk = view.getUint16(endOffset + 6, true)
  const entriesOnDisk = view.getUint16(endOffset + 8, true)
  const entryCount = view.getUint16(endOffset + 10, true)
  const directoryBytes = view.getUint32(endOffset + 12, true)
  const directoryOffset = view.getUint32(endOffset + 16, true)

  if (diskNumber !== 0 || centralDirectoryDisk !== 0 || entriesOnDisk !== entryCount) {
    throw invalidArchive('multi-disk ZIP files are not supported')
  }
  if (entryCount === 0xffff || directoryBytes === 0xffffffff || directoryOffset === 0xffffffff) {
    throw new OoxmlArchiveLimitError('ZIP64 OOXML files are not supported in preview')
  }
  if (entryCount > limits.maxEntries) {
    throw new OoxmlArchiveLimitError(`archive contains more than ${limits.maxEntries} entries`)
  }
  if (directoryOffset + directoryBytes > endOffset) {
    throw invalidArchive('central directory is out of bounds')
  }

  let offset = directoryOffset
  let totalUncompressedBytes = 0
  for (let index = 0; index < entryCount; index += 1) {
    if (offset + 46 > endOffset || view.getUint32(offset, true) !== CENTRAL_DIRECTORY_ENTRY) {
      throw invalidArchive('malformed central directory entry')
    }

    const flags = view.getUint16(offset + 8, true)
    const compressedBytes = view.getUint32(offset + 20, true)
    const uncompressedBytes = view.getUint32(offset + 24, true)
    const filenameBytes = view.getUint16(offset + 28, true)
    const extraBytes = view.getUint16(offset + 30, true)
    const commentBytes = view.getUint16(offset + 32, true)
    const diskNumberStart = view.getUint16(offset + 34, true)
    const localHeaderOffset = view.getUint32(offset + 42, true)

    if ((flags & 0x1) !== 0) throw invalidArchive('encrypted entries are not supported')
    if (diskNumberStart !== 0) throw invalidArchive('multi-disk ZIP files are not supported')
    if (
      compressedBytes === 0xffffffff ||
      uncompressedBytes === 0xffffffff ||
      localHeaderOffset === 0xffffffff
    ) {
      throw new OoxmlArchiveLimitError('ZIP64 entries are not supported in preview')
    }
    if (uncompressedBytes > limits.maxEntryUncompressedBytes) {
      throw new OoxmlArchiveLimitError('an archive entry is too large to preview')
    }

    totalUncompressedBytes += uncompressedBytes
    if (totalUncompressedBytes > limits.maxTotalUncompressedBytes) {
      throw new OoxmlArchiveLimitError('the expanded archive is too large to preview')
    }
    if (
      uncompressedBytes > 0 &&
      uncompressedBytes / Math.max(1, compressedBytes) > limits.maxCompressionRatio
    ) {
      throw new OoxmlArchiveLimitError('the archive compression ratio is unsafe')
    }

    offset += 46 + filenameBytes + extraBytes + commentBytes
  }

  if (offset !== directoryOffset + directoryBytes) {
    throw invalidArchive('central directory size does not match its entries')
  }
}
