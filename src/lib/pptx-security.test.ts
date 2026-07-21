import { DOMParser as XmlDomParser, XMLSerializer as XmlDomSerializer } from '@xmldom/xmldom'
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'

import { preservePptxParagraphTextFills, removeExternalPptxResources } from './pptx-security'

import type { PptxFiles, PresentationData } from '@aiden0z/pptx-renderer'

const DRAWINGML_NAMESPACE = 'http://schemas.openxmlformats.org/drawingml/2006/main'

beforeAll(() => {
  vi.stubGlobal('DOMParser', XmlDomParser)
  vi.stubGlobal('XMLSerializer', XmlDomSerializer)
})

afterAll(() => {
  vi.unstubAllGlobals()
})

function relationships() {
  return new Map([
    ['internal', { type: 'image', target: '../media/image.png' }],
    ['external-image', { type: 'image', target: 'https://example.com/image.png', targetMode: 'External' }],
    ['external-hyperlink', { type: 'hyperlink', target: 'https://example.com', targetMode: 'external' }],
  ])
}

describe('removeExternalPptxResources', () => {
  it('removes every external relationship before rendering, including disguised hyperlinks', () => {
    const slideRels = relationships()
    const layoutRels = relationships()
    const masterRels = relationships()
    const presentation = {
      slides: [{ rels: slideRels }],
      layouts: new Map([['layout', { rels: layoutRels }]]),
      masters: new Map([['master', { rels: masterRels }]]),
    } as unknown as PresentationData

    removeExternalPptxResources(presentation)

    for (const rels of [slideRels, layoutRels, masterRels]) {
      expect([...rels.keys()]).toEqual(['internal'])
    }
  })
})

describe('preservePptxParagraphTextFills', () => {
  it('promotes paragraph default fills without overriding explicit run fills', () => {
    const slide = `<?xml version="1.0"?>
      <p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
             xmlns:a="${DRAWINGML_NAMESPACE}">
        <a:p>
          <a:pPr><a:defRPr sz="1600"><a:solidFill><a:srgbClr val="142D41"/></a:solidFill></a:defRPr></a:pPr>
          <a:r><a:t>Inherited</a:t></a:r>
          <a:r><a:rPr b="1"/><a:t>Inherited with formatting</a:t></a:r>
          <a:r><a:rPr><a:solidFill><a:srgbClr val="FFFFFF"/></a:solidFill></a:rPr><a:t>Explicit</a:t></a:r>
          <a:r><a:rPr><a:noFill/></a:rPr><a:t>Explicit no fill</a:t></a:r>
        </a:p>
      </p:sld>`
    const files = {
      slides: new Map([['ppt/slides/slide1.xml', slide]]),
      slideLayouts: new Map(),
      slideMasters: new Map(),
    } as unknown as PptxFiles

    preservePptxParagraphTextFills(files)

    const document = new XmlDomParser().parseFromString(
      files.slides.get('ppt/slides/slide1.xml')!,
      'application/xml',
    )
    const runs = Array.from(document.getElementsByTagNameNS(DRAWINGML_NAMESPACE, 'r'))
    const colors = runs.map((run) =>
      run.getElementsByTagNameNS(DRAWINGML_NAMESPACE, 'srgbClr').item(0)?.getAttribute('val'),
    )

    expect(colors).toEqual(['142D41', '142D41', 'FFFFFF', undefined])
    const inheritedRunProperties = runs[0].getElementsByTagNameNS(DRAWINGML_NAMESPACE, 'rPr').item(0)
    expect(inheritedRunProperties?.localName).toBe('rPr')
    expect(inheritedRunProperties?.namespaceURI).toBe(DRAWINGML_NAMESPACE)
    expect(runs[1].getElementsByTagNameNS(DRAWINGML_NAMESPACE, 'rPr').item(0)?.getAttribute('b')).toBe('1')
    expect(runs[3].getElementsByTagNameNS(DRAWINGML_NAMESPACE, 'noFill')).toHaveLength(1)
    expect(runs[3].getElementsByTagNameNS(DRAWINGML_NAMESPACE, 'solidFill')).toHaveLength(0)
  })
})
