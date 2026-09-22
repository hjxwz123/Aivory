/**
 * Minimal, surgical `.pptx` text editing.
 *
 * Only `<a:t>` text nodes are rewritten; the archive is otherwise passed
 * through untouched by archive.ts, so layout, theme, master, media and every
 * element the editor does not model survive a save.
 *
 * Namespace note: unlike SpreadsheetML (which uses a DEFAULT namespace and
 * therefore needs `createElementNS`), PresentationML uses the prefixes `p:` and
 * `a:`. This module never creates elements — it only replaces the text content
 * of existing nodes — so that class of bug cannot occur here. Lookups still go
 * through a namespace-aware helper, because prefix matching is not guaranteed
 * across parsers.
 */
import { xmlDeclarationOf } from '@/lib/ooxml/archive'
import { parseXml } from '@/lib/ooxml/xlsx'

export const PRESENTATION_NS = 'http://schemas.openxmlformats.org/presentationml/2006/main'
export const DRAWING_NS = 'http://schemas.openxmlformats.org/drawingml/2006/main'
export const OFFICE_REL_NS = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships'
export const PACKAGE_REL_NS = 'http://schemas.openxmlformats.org/package/2006/relationships'
export const XML_NS = 'http://www.w3.org/XML/1998/namespace'

export const PPTX_MIME = 'application/vnd.openxmlformats-officedocument.presentationml.presentation'

export interface SlideRef {
  /** 1-based label for the chrome. */
  name: string
  /** Archive path, e.g. `ppt/slides/slide1.xml`. */
  path: string
}

/**
 * Match by qualified name first (`p:sldId`), then by namespace + local name.
 * Both spellings are normal, and parsers differ in which one matches.
 */
function byTag(
  parent: Document | Element,
  qualified: string,
  ns: string,
  local: string,
): Element[] {
  const prefixed = Array.from(parent.getElementsByTagName(qualified))
  if (prefixed.length > 0) return prefixed
  return Array.from(parent.getElementsByTagNameNS(ns, local))
}

function normalizePartPath(target: string): string {
  const raw = target.startsWith('/') ? target.slice(1) : `ppt/${target.replace(/^\.\//, '')}`
  // Resolve any "a/b/../c" without pulling in a path library.
  const parts: string[] = []
  for (const segment of raw.split('/')) {
    if (segment === '.' || segment === '') continue
    if (segment === '..') parts.pop()
    else parts.push(segment)
  }
  return parts.join('/')
}

/** Slide order comes from `sldIdLst`; the relationship maps it to a part. */
export function resolveSlides(presentationXml: string, relsXml: string): SlideRef[] {
  const presentation = parseXml(presentationXml)
  const rels = parseXml(relsXml)

  const targets = new Map<string, string>()
  for (const rel of Array.from(rels.getElementsByTagName('Relationship'))) {
    const id = rel.getAttribute('Id')
    const target = rel.getAttribute('Target')
    if (id && target) targets.set(id, target)
  }

  const slides: SlideRef[] = []
  for (const sldId of byTag(presentation, 'p:sldId', PRESENTATION_NS, 'sldId')) {
    const relId = sldId.getAttributeNS(OFFICE_REL_NS, 'id') ?? sldId.getAttribute('r:id')
    const target = relId ? targets.get(relId) : undefined
    if (!target) continue
    slides.push({ name: `Slide ${slides.length + 1}`, path: normalizePartPath(target) })
  }
  return slides
}

/**
 * Every `<a:t>` on a slide, in document order. This includes runs inside fields
 * (for example a cached slide number), which are editable but rarely useful to
 * change — the editor labels them by position rather than pretending otherwise.
 */
export function slideTextElements(doc: Document): Element[] {
  return byTag(doc, 'a:t', DRAWING_NS, 't')
}

export function parseSlideText(source: string): string[] {
  return slideTextElements(parseXml(source)).map((node) => node.textContent ?? '')
}

/** Text nodes in a slide that carry meaningful content worth showing first. */
export function countEditableRuns(source: string): number {
  return parseSlideText(source).length
}

export interface SlideWriteResult {
  xml: string
  /** How many runs actually changed. */
  changed: number
}

/**
 * Replace the text of the given runs (keyed by index into `parseSlideText`).
 * `xml:space="preserve"` is applied whenever the new text has leading or
 * trailing whitespace, which XML would otherwise collapse.
 */
export function writeSlideText(source: string, edits: Map<number, string>): SlideWriteResult {
  const doc = parseXml(source)
  const nodes = slideTextElements(doc)
  let changed = 0

  for (const [index, text] of edits) {
    const node = nodes[index]
    if (!node) continue
    if ((node.textContent ?? '') === text) continue
    node.textContent = text
    if (text !== text.trim()) {
      node.setAttributeNS(XML_NS, 'xml:space', 'preserve')
    }
    changed += 1
  }

  if (changed === 0) return { xml: source, changed: 0 }

  const serialized = new XMLSerializer()
    .serializeToString(doc)
    .replace(/^\s*<\?xml[^?]*\?>/, '')
    .replace(/^\s+/, '')
  return { xml: `${xmlDeclarationOf(source)}\n${serialized}`, changed }
}
