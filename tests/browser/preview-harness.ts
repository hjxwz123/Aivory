/**
 * Dev/test-only harness for the chat's HTML preview panel.
 *
 * Mounts the real `ArtifactPanel` and exposes the artifact store, so a test can
 * open a preview exactly the way clicking the button in a code block does.
 *
 * DETECTING WHETHER THE FRAME RENDERED: the preview iframe is sandboxed without
 * `allow-same-origin`, so the parent page cannot read its DOM — that isolation is
 * deliberate and must not be weakened for a test. Instead the artifact's own
 * markup posts a message back (scripts are allowed in the opaque-origin sandbox,
 * and cross-origin `postMessage` is fine). If the frame never loads its srcDoc,
 * the message never arrives.
 */
import { createElement } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import '@/i18n'
import '@/styles/globals.css'
import { ArtifactPanel } from '@/components/chat/artifact-panel'
import { TooltipProvider } from '@/components/ui/tooltip'
import { autoOpenPreview, useArtifactPanel } from '@/store/artifact-panel'

interface PreviewProbe {
  /** Payloads received from the sandboxed preview frame, in order. */
  renders: Array<Record<string, unknown>>
  /** srcDoc length currently set on the frame, or null when absent. */
  srcDocLength: number | null
  panelOpen: boolean
  /**
   * Geometry of the frame and its containers.
   *
   * A frame whose document ran but whose box is 0×0 is exactly the reported
   * "blank preview", and it is invisible to a DOM-only check — the artifact's
   * script still posts its marker. Measuring the box is what distinguishes
   * "did not load" from "loaded but not visible".
   */
  frameRect: { width: number; height: number } | null
  panelRect: { width: number; height: number } | null
}

const probe: PreviewProbe = {
  renders: [],
  srcDocLength: null,
  panelOpen: false,
  frameRect: null,
  panelRect: null,
}
Reflect.set(window, '__PREVIEW__', probe)
Reflect.set(window, '__ARTIFACT_STORE__', useArtifactPanel)
// The streaming path in code-block.tsx goes through autoOpenPreview, not the
// store directly, so the harness has to expose that too for a faithful repro.
Reflect.set(window, '__AUTO_OPEN__', autoOpenPreview)

window.addEventListener('message', (event: MessageEvent) => {
  const data = event.data as { previewMarker?: string } | null
  if (data && typeof data.previewMarker === 'string') {
    // Keep the whole payload: an artifact can report computed styles back, which
    // is how the test checks that the injected Tailwind runtime actually ran.
    probe.renders.push(data as { marker: string })
  }
})

// Poll the frame's srcDoc length from the PARENT side, which is readable: only
// the frame's document is cross-origin, its attributes are ours.
window.setInterval(() => {
  const frame = document.querySelector('iframe')
  probe.srcDocLength = frame ? (frame.getAttribute('srcdoc')?.length ?? 0) : null
  probe.panelOpen = useArtifactPanel.getState().open
  const frameBox = frame?.getBoundingClientRect()
  probe.frameRect = frameBox ? { width: frameBox.width, height: frameBox.height } : null
  const panelBox = document.querySelector('aside')?.getBoundingClientRect()
  probe.panelRect = panelBox ? { width: panelBox.width, height: panelBox.height } : null
}, 100)

const container = document.getElementById('root')
if (!container) throw new Error('#root missing')

createRoot(container).render(
  createElement(
    // Mirrors the providers App.tsx wraps the shell in — the panel's header
    // renders Tooltips, which throw without a provider.
    TooltipProvider,
    { delayDuration: 280, skipDelayDuration: 120 },
    createElement(MemoryRouter, null, createElement(ArtifactPanel)),
  ),
)
