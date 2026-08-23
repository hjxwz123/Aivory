import type { ArtifactRef } from '@/types/chat'

const SANDBOX_OUTPUT_PATH = /(?:sandbox:\/+workspace\/outputs\/|\/workspace\/outputs\/)([^\s)\]}>]+)/gi
const PROTECTED_CODE = /(```[\s\S]*?(?:```|$)|~~~[\s\S]*?(?:~~~|$)|`[^`\n]*(?:`|$))/g
const TRAILING_PUNCTUATION = /[.,;:!?，。；：！？]+$/

function decodePathSegment(value: string): string | undefined {
  try {
    return decodeURIComponent(value)
  } catch {
    return undefined
  }
}

function extensionOf(value: string): string {
  const match = value.match(/(\.[a-z0-9]{1,12})$/i)
  return match?.[1]?.toLowerCase() ?? ''
}

function resolveArtifact(rawFilename: string, artifacts: ArtifactRef[]): ArtifactRef | undefined {
  const raw = rawFilename.normalize('NFC')
  const decoded = decodePathSegment(raw)?.normalize('NFC')
  const exact = artifacts.filter((artifact) => {
    const filename = artifact.filename.normalize('NFC')
    return filename === raw || filename === decoded || encodeURIComponent(filename).toLowerCase() === raw.toLowerCase()
  })
  if (exact.length === 1) return exact[0]
  if (exact.length > 1) return undefined

  // Some models truncate a percent-encoded multibyte filename (for example
  // "%E8.docx"). It is impossible to decode, but mapping remains unambiguous
  // when this message contains exactly one artifact with the same extension.
  if (!raw.includes('%')) return undefined
  const extension = extensionOf(decoded ?? raw)
  if (!extension) return undefined
  const byExtension = artifacts.filter((artifact) => extensionOf(artifact.filename) === extension)
  return byExtension.length === 1 ? byExtension[0] : undefined
}

function rewriteTextSegment(segment: string, artifacts: ArtifactRef[]): string {
  return segment.replace(SANDBOX_OUTPUT_PATH, (full, capturedFilename: string) => {
    const suffix = capturedFilename.match(TRAILING_PUNCTUATION)?.[0] ?? ''
    const filename = suffix ? capturedFilename.slice(0, -suffix.length) : capturedFilename
    const artifact = resolveArtifact(filename, artifacts)
    if (!artifact?.id) return full
    return `/api/artifacts/${encodeURIComponent(artifact.id)}${suffix}`
  })
}

/**
 * Replace sandbox-internal output links with the authenticated artifact route
 * for the same assistant message. Unknown and ambiguous paths remain untouched
 * and are rejected later by the markdown URL sanitizer.
 */
export function rewriteSandboxArtifactLinks(markdown: string, artifacts?: ArtifactRef[]): string {
  if (!markdown || !artifacts?.length) return markdown

  let output = ''
  let cursor = 0
  PROTECTED_CODE.lastIndex = 0
  let match: RegExpExecArray | null
  while ((match = PROTECTED_CODE.exec(markdown)) !== null) {
    output += rewriteTextSegment(markdown.slice(cursor, match.index), artifacts)
    output += match[0]
    cursor = match.index + match[0].length
  }
  output += rewriteTextSegment(markdown.slice(cursor), artifacts)
  return output
}
