// @vitest-environment jsdom
import JSZip from 'jszip'
import { describe, expect, it } from 'vitest'
import { decodeXml, encodeXml, loadOoxmlArchive } from '@/lib/ooxml/archive'
import {
  DRAWING_NS,
  PPTX_MIME,
  parseSlideText,
  resolveSlides,
  writeSlideText,
} from '@/lib/ooxml/pptx'

const HEAD = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n'
const P = 'http://schemas.openxmlformats.org/presentationml/2006/main'
const A = DRAWING_NS
const R = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships'
const PKG = 'http://schemas.openxmlformats.org/package/2006/relationships'

const SLIDE1 = 'ppt/slides/slide1.xml'
const SLIDE2 = 'ppt/slides/slide2.xml'
const BINARY = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x99])

/** One shape, two paragraphs, one run each — plus a two-run paragraph. */
function slideXml(title: string, body: string): string {
  return `${HEAD}<p:sld xmlns:p="${P}" xmlns:a="${A}" xmlns:r="${R}"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>${title}</a:t></a:r></a:p><a:p><a:r><a:t>${body}</a:t></a:r></a:p><a:p><a:r><a:t>bold part</a:t></a:r><a:r><a:t> plain part</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`
}

async function buildFixture(): Promise<ArrayBuffer> {
  const zip = new JSZip()
  zip.file(
    '[Content_Types].xml',
    `${HEAD}<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`,
  )
  zip.file(
    '_rels/.rels',
    `${HEAD}<Relationships xmlns="${PKG}"><Relationship Id="rId1" Type="${R}/officeDocument" Target="ppt/presentation.xml"/></Relationships>`,
  )
  zip.file(
    'ppt/presentation.xml',
    `${HEAD}<p:presentation xmlns:p="${P}" xmlns:r="${R}"><p:sldIdLst><p:sldId id="256" r:id="rId2"/><p:sldId id="257" r:id="rId3"/></p:sldIdLst></p:presentation>`,
  )
  // Deliberately out of order to prove slide order follows sldIdLst, not the rels map.
  zip.file(
    'ppt/_rels/presentation.xml.rels',
    `${HEAD}<Relationships xmlns="${PKG}"><Relationship Id="rId3" Type="${R}/slide" Target="slides/slide2.xml"/><Relationship Id="rId2" Type="${R}/slide" Target="slides/slide1.xml"/></Relationships>`,
  )
  zip.file(SLIDE1, slideXml('Quarterly Review', 'Revenue is up'))
  zip.file(SLIDE2, slideXml('Appendix', 'Raw numbers'))
  zip.file('ppt/theme/theme1.xml', `${HEAD}<a:theme xmlns:a="${A}" name="Office"/>`)
  zip.file('ppt/media/image1.png', BINARY)
  return (await zip.generateAsync({ type: 'uint8array' })).buffer as ArrayBuffer
}

async function entriesOf(data: ArrayBuffer): Promise<Map<string, Uint8Array>> {
  const zip = await JSZip.loadAsync(data)
  const out = new Map<string, Uint8Array>()
  for (const [name, entry] of Object.entries(zip.files)) {
    if (entry.dir) continue
    out.set(name, await entry.async('uint8array'))
  }
  return out
}

function byteDiff(a: Uint8Array, b: Uint8Array): string | null {
  if (a.length !== b.length) return `length ${a.length} != ${b.length}`
  for (let i = 0; i < a.length; i += 1) if (a[i] !== b[i]) return `byte ${i}`
  return null
}

describe('pptx surgical text editing', () => {
  it('resolves slides in sldIdLst order, not relationship order', async () => {
    const archive = await loadOoxmlArchive(await buildFixture())
    const slides = resolveSlides(
      decodeXml(await archive.read('ppt/presentation.xml')),
      decodeXml(await archive.read('ppt/_rels/presentation.xml.rels')),
    )

    expect(slides.map((slide) => slide.path)).toEqual([SLIDE1, SLIDE2])
    expect(slides.map((slide) => slide.name)).toEqual(['Slide 1', 'Slide 2'])
  })

  it('reads text runs per slide in document order', async () => {
    const archive = await loadOoxmlArchive(await buildFixture())
    const source = decodeXml(await archive.read(SLIDE1))

    expect(parseSlideText(source)).toEqual([
      'Quarterly Review',
      'Revenue is up',
      'bold part',
      ' plain part',
    ])
  })

  it('rewrites only the targeted runs and preserves every other entry byte-for-byte', async () => {
    const original = await buildFixture()
    const archive = await loadOoxmlArchive(original)
    const source = decodeXml(await archive.read(SLIDE1))

    const { xml, changed } = writeSlideText(
      source,
      new Map([
        [0, 'Q4 Review'],
        [2, 'bold & <emphasised>'],
      ]),
    )
    expect(changed).toBe(2)
    archive.write(SLIDE1, encodeXml(xml))

    const before = await entriesOf(original)
    const after = await entriesOf(await (await archive.build(PPTX_MIME)).arrayBuffer())

    expect([...after.keys()].sort()).toEqual([...before.keys()].sort())

    for (const [name, bytes] of before) {
      if (name === SLIDE1) continue
      // slide2, theme, media, content-types: everything untouched must match.
      expect({ entry: name, difference: byteDiff(bytes, after.get(name)!) }).toEqual({
        entry: name,
        difference: null,
      })
    }

    expect(parseSlideText(decodeXml(after.get(SLIDE1)!))).toEqual([
      'Q4 Review',
      'Revenue is up',
      'bold & <emphasised>',
      ' plain part',
    ])
    // The run nobody edited keeps its exact text, including its leading space.
    expect(parseSlideText(decodeXml(after.get(SLIDE2)!))[1]).toBe('Raw numbers')
  })

  it('keeps leading and trailing spaces via xml:space, which XML would otherwise collapse', async () => {
    const archive = await loadOoxmlArchive(await buildFixture())
    const source = decodeXml(await archive.read(SLIDE1))

    const { xml } = writeSlideText(source, new Map([[1, '  padded  ']]))
    expect(xml).toContain('xml:space="preserve"')
    expect(parseSlideText(xml)[1]).toBe('  padded  ')

    // No padding needed, so no marker is added.
    const plain = writeSlideText(source, new Map([[1, 'plain']]))
    expect(plain.xml).not.toContain('xml:space')
  })

  it('returns the source untouched when no run actually changes', async () => {
    const archive = await loadOoxmlArchive(await buildFixture())
    const source = decodeXml(await archive.read(SLIDE1))

    const noop = writeSlideText(source, new Map([[0, 'Quarterly Review']]))
    expect(noop.changed).toBe(0)
    expect(noop.xml).toBe(source)

    const outOfRange = writeSlideText(source, new Map([[99, 'nowhere']]))
    expect(outOfRange.changed).toBe(0)
    expect(outOfRange.xml).toBe(source)
  })

  it('escapes markup in edited text so the part stays well-formed', async () => {
    const archive = await loadOoxmlArchive(await buildFixture())
    const source = decodeXml(await archive.read(SLIDE1))

    const { xml } = writeSlideText(source, new Map([[1, 'a & b < c > d "e"']]))
    const parsed = new DOMParser().parseFromString(xml, 'application/xml')

    expect(parsed.getElementsByTagName('parsererror')).toHaveLength(0)
    expect(parseSlideText(xml)[1]).toBe('a & b < c > d "e"')
  })
})
