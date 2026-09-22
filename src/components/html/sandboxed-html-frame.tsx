import { useEffect, useRef, useState } from 'react'
import { buildHtmlPreviewDocument } from '@/lib/html-preview-document'

/**
 * SandboxedHtmlFrame — the single place in the app that renders untrusted HTML.
 *
 * Both the assistant's live streaming preview (chat/code-block) and the Files /
 * attachment preview of a `.html` upload go through here, so the security
 * posture below cannot drift between the two surfaces.
 *
 * Security rests on the opaque origin, NOT on blocking the network:
 * - no `allow-same-origin` → opaque origin; the frame can never read our
 *   cookies, storage, or DOM, so external resources it loads can't reach our
 *   data. (NEVER add allow-same-origin: with allow-scripts it voids the sandbox.)
 * - `allow-popups allow-popups-to-escape-sandbox` lets a user-clicked link open
 *   normally in a new tab; popups are user-initiated and open as ordinary
 *   top-level tabs governed by normal browser security.
 * - `buildHtmlPreviewDocument` injects `<base target="_blank" rel="noopener
 *   noreferrer">` so link clicks open a new tab without exposing our window via
 *   `opener`, plus an `upgrade-insecure-requests` CSP so `http://` subresources
 *   aren't blocked as mixed content on an https deployment.
 * - no `allow-forms` / `allow-modals` / `allow-downloads` → the page can't
 *   submit forms, throw native dialogs, or drop files.
 * - `referrerPolicy="no-referrer"` keeps our URL out of any subresource it loads.
 */
export const SANDBOXED_HTML_FRAME_SANDBOX = 'allow-scripts allow-popups allow-popups-to-escape-sandbox'

export interface SandboxedHtmlFrameProps {
  /** Raw HTML source. Wrapped in the preview document before rendering. */
  doc: string
  /** Accessible frame title; also used as the iframe document title. */
  title: string
  /**
   * Changing this value remounts the frame, discarding the current document —
   * a reload that does not depend on `doc` changing.
   */
  reloadKey?: number | string
  className?: string
}

export function SandboxedHtmlFrame({ doc, title, reloadKey, className }: SandboxedHtmlFrameProps) {
  /**
   * Bumped whenever the document changes, so the frame is REBUILT rather than
   * having `srcdoc` mutated in place.
   *
   * Mutating `srcdoc` normally navigates, but when that navigation is dropped
   * the frame keeps showing the previous (possibly empty) document and nothing
   * recovers it until the element is recreated — which is exactly what the
   * panel's refresh button does, and exactly the reported "preview is blank
   * until I refresh once" symptom. Recreating is the path that is known to work,
   * so it is the default one here. The cost is nil for a static file preview and
   * already absorbed by the streaming debounce, since a `srcdoc` change resets
   * the document's JS state anyway.
   */
  const [generation, setGeneration] = useState(0)
  const previousDoc = useRef(doc)
  useEffect(() => {
    // Skips the mount so the first document is not loaded twice; only an actual
    // change rebuilds the frame.
    if (previousDoc.current === doc) return
    previousDoc.current = doc
    setGeneration((value) => value + 1)
  }, [doc])

  return (
    <iframe
      key={`${reloadKey ?? 0}:${generation}`}
      title={title}
      sandbox={SANDBOXED_HTML_FRAME_SANDBOX}
      referrerPolicy="no-referrer"
      srcDoc={buildHtmlPreviewDocument(doc)}
      className={className ?? 'block size-full border-0 bg-[var(--color-preview-canvas)]'}
    />
  )
}
