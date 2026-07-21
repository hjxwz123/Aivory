import { describe, expect, it } from 'vitest'
import {
  OoxmlArchiveLimitError,
  validateOoxmlArchive,
  type OoxmlArchiveLimits,
} from './ooxml-archive'

interface TestEntry {
  compressed: number
  uncompressed: number
  encrypted?: boolean
  diskNumberStart?: number
  localHeaderOffset?: number
  name?: string
}

function archive(entries: TestEntry[]): ArrayBuffer {
  const encoder = new TextEncoder()
  const names = entries.map((entry, index) => encoder.encode(entry.name ?? `entry-${index}.xml`))
  const directoryBytes = entries.reduce((total, _entry, index) => total + 46 + names[index].byteLength, 0)
  const bytes = new Uint8Array(directoryBytes + 22)
  const view = new DataView(bytes.buffer)
  let offset = 0

  entries.forEach((entry, index) => {
    const name = names[index]
    view.setUint32(offset, 0x02014b50, true)
    view.setUint16(offset + 8, entry.encrypted ? 1 : 0, true)
    view.setUint32(offset + 20, entry.compressed, true)
    view.setUint32(offset + 24, entry.uncompressed, true)
    view.setUint16(offset + 28, name.byteLength, true)
    view.setUint16(offset + 34, entry.diskNumberStart ?? 0, true)
    view.setUint32(offset + 42, entry.localHeaderOffset ?? 0, true)
    bytes.set(name, offset + 46)
    offset += 46 + name.byteLength
  })

  view.setUint32(offset, 0x06054b50, true)
  view.setUint16(offset + 8, entries.length, true)
  view.setUint16(offset + 10, entries.length, true)
  view.setUint32(offset + 12, directoryBytes, true)
  view.setUint32(offset + 16, 0, true)
  return bytes.buffer
}

const limits: OoxmlArchiveLimits = {
  maxEntries: 3,
  maxEntryUncompressedBytes: 1_000,
  maxTotalUncompressedBytes: 1_500,
  maxCompressionRatio: 20,
}

describe('validateOoxmlArchive', () => {
  it('accepts a bounded central directory without decompressing entries', () => {
    expect(() => validateOoxmlArchive(archive([
      { compressed: 100, uncompressed: 500 },
      { compressed: 100, uncompressed: 600 },
    ]), limits)).not.toThrow()
  })

  it('rejects excessive entry count, expanded size, and compression ratio', () => {
    expect(() => validateOoxmlArchive(archive([
      { compressed: 1, uncompressed: 1 },
      { compressed: 1, uncompressed: 1 },
      { compressed: 1, uncompressed: 1 },
      { compressed: 1, uncompressed: 1 },
    ]), limits)).toThrow(OoxmlArchiveLimitError)
    expect(() => validateOoxmlArchive(archive([
      { compressed: 100, uncompressed: 900 },
      { compressed: 100, uncompressed: 700 },
    ]), limits)).toThrow(OoxmlArchiveLimitError)
    expect(() => validateOoxmlArchive(archive([{ compressed: 10, uncompressed: 500 }]), limits))
      .toThrow(OoxmlArchiveLimitError)
  })

  it('rejects malformed and encrypted archives as invalid input', () => {
    expect(() => validateOoxmlArchive(new ArrayBuffer(32), limits)).toThrow(/Invalid OOXML archive/)
    expect(() => validateOoxmlArchive(archive([{ compressed: 10, uncompressed: 10, encrypted: true }]), limits))
      .toThrow(/encrypted entries/)
    expect(() => validateOoxmlArchive(archive([{ compressed: 10, uncompressed: 10, diskNumberStart: 1 }]), limits))
      .toThrow(/multi-disk ZIP files/)
    expect(() => validateOoxmlArchive(archive([{
      compressed: 10,
      uncompressed: 10,
      localHeaderOffset: 0xffffffff,
    }]), limits)).toThrow(OoxmlArchiveLimitError)
  })
})
