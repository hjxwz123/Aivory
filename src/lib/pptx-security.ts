import type { PptxFiles, PresentationData } from '@aiden0z/pptx-renderer'

type RelationshipMap = Map<string, { type: string; target: string; targetMode?: string }>

const DRAWINGML_NAMESPACE = 'http://schemas.openxmlformats.org/drawingml/2006/main'
const TEXT_FILL_ELEMENTS = ['solidFill', 'gradFill'] as const
const EXPLICIT_FILL_ELEMENTS = ['noFill', 'solidFill', 'gradFill', 'blipFill', 'pattFill', 'grpFill'] as const

function isElement(node: Node): node is Element {
  return node.nodeType === 1
}

function directChild(element: Element, localName: string) {
  return Array.from(element.childNodes).find(
    (child): child is Element => isElement(child) && child.localName === localName,
  )
}

function hasTextFill(element: Element) {
  return EXPLICIT_FILL_ELEMENTS.some((name) => directChild(element, name))
}

function normalizeParagraphTextFills(xml: string) {
  const document = new DOMParser().parseFromString(xml, 'application/xml')
  if (document.getElementsByTagName('parsererror').length > 0) return xml

  let changed = false
  for (const paragraph of Array.from(document.getElementsByTagNameNS(DRAWINGML_NAMESPACE, 'p'))) {
    const defaultRunProperties = directChild(directChild(paragraph, 'pPr') ?? paragraph, 'defRPr')
    if (!defaultRunProperties) continue

    const defaultFill = TEXT_FILL_ELEMENTS.map((name) => directChild(defaultRunProperties, name)).find(
      (fill): fill is Element => fill !== undefined,
    )
    if (!defaultFill) continue

    for (const run of Array.from(paragraph.childNodes).filter(
      (child): child is Element => isElement(child) && child.localName === 'r',
    )) {
      let runProperties = directChild(run, 'rPr')
      if (runProperties && hasTextFill(runProperties)) continue

      if (!runProperties) {
        const prefix = defaultRunProperties.prefix ? `${defaultRunProperties.prefix}:` : ''
        runProperties = document.createElementNS(DRAWINGML_NAMESPACE, `${prefix}rPr`)
        run.insertBefore(runProperties, run.firstChild)
      }
      runProperties.appendChild(defaultFill.cloneNode(true))
      changed = true
    }
  }

  return changed ? new XMLSerializer().serializeToString(document) : xml
}

/** Preserves explicit paragraph text colors that pptx-renderer 1.2.4 otherwise masks with fontRef. */
export function preservePptxParagraphTextFills(files: PptxFiles) {
  for (const parts of [files.slides, files.slideLayouts, files.slideMasters]) {
    for (const [path, xml] of parts) parts.set(path, normalizeParagraphTextFills(xml))
  }
}

/** Removes relationships that could make a PPTX preview initiate a network request. */
export function removeExternalPptxResources(presentation: PresentationData) {
  const sanitize = (relationships: RelationshipMap) => {
    for (const [id, relationship] of relationships) {
      if (relationship.targetMode?.trim().toLowerCase() === 'external') relationships.delete(id)
    }
  }

  for (const slide of presentation.slides) sanitize(slide.rels)
  for (const layout of presentation.layouts.values()) sanitize(layout.rels)
  for (const master of presentation.masters.values()) sanitize(master.rels)
}
