import JSZip from 'jszip'

/**
 * Thin ZIP layer over the OOXML container formats (.xlsx / .docx / .pptx).
 *
 * The surgical-editing contract lives here: an entry that is never passed to
 * `write` is handed back to JSZip untouched, so its decompressed bytes in the
 * rebuilt archive are identical to the source. Callers prove that with tests
 * rather than trusting it — see tests/frontend/lib/ooxml.
 *
 * Note the guarantee is on *entry content*, not on the archive bytes: any ZIP
 * writer may pick different compression, so two archives of the same content
 * legitimately differ byte-for-byte.
 */
export interface OoxmlArchive {
  /** File entry names (no directories), in archive order. */
  names: string[]
  has(name: string): boolean
  read(name: string): Promise<Uint8Array>
  /** Replace an entry's content. `string` is encoded as UTF-8. */
  write(name: string, content: Uint8Array | string): void
  /** Rebuild the archive. Untouched entries keep their original content. */
  build(mimeType: string): Promise<Blob>
}

export async function loadOoxmlArchive(data: ArrayBuffer): Promise<OoxmlArchive> {
  const zip = await JSZip.loadAsync(data)

  return {
    names: Object.keys(zip.files).filter((name) => !zip.files[name]?.dir),

    has(name) {
      return Boolean(zip.files[name]) && !zip.files[name]?.dir
    },

    async read(name) {
      const entry = zip.file(name)
      if (!entry) throw new Error(`OOXML archive has no entry: ${name}`)
      return entry.async('uint8array')
    },

    write(name, content) {
      zip.file(name, content)
    },

    build(mimeType) {
      return zip.generateAsync({ type: 'blob', mimeType })
    },
  }
}

const XML_DECLARATION = /^\s*<\?xml[^?]*\?>/

/**
 * OOXML part files start with an XML declaration. `XMLSerializer` emits its own
 * (and drops `standalone="yes"`), so the original declaration is carried over
 * verbatim instead of trusting the serializer's.
 */
export function xmlDeclarationOf(source: string): string {
  return XML_DECLARATION.exec(source)?.[0] ?? '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
}

export function decodeXml(bytes: Uint8Array): string {
  return new TextDecoder('utf-8', { fatal: false }).decode(bytes)
}

export function encodeXml(text: string): Uint8Array {
  return new TextEncoder().encode(text)
}
