/**
 * Dev/test-only harness for the FILE preview / editor flow in the artifact panel.
 *
 * `preview-harness.ts` covers the HTML artifact path; this one mounts the same
 * real `ArtifactPanel` with a `file` source (fetching a fixture) so the
 * preview -> edit -> back-to-preview transition and the desktop split layout can
 * be measured in a real browser. `run-panel.mjs` drives it.
 *
 * The panel is rendered next to a flex-grow stand-in for the conversation
 * column, which is the geometry the real chat shell gives it.
 */
import { createElement, useEffect } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import '@/i18n'
import '@/styles/globals.css'
import { ArtifactPanel } from '@/components/chat/artifact-panel'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useArtifactPanel } from '@/store/artifact-panel'

const params = new URLSearchParams(window.location.search)
const fixture = params.get('file') ?? 'sample.docx'
const filename = params.get('name') ?? fixture
const fixtureDir = params.get('dir') ?? 'ooxml'

interface PanelProbe {
  ready: boolean
  error: string | null
  open: boolean
  mode: 'view' | 'edit' | 'unknown'
}

const probe: PanelProbe = { ready: false, error: null, open: false, mode: 'unknown' }
Reflect.set(window, '__PANEL__', probe)
Reflect.set(window, '__ARTIFACT_STORE__', useArtifactPanel)

/** A tall flex column stands in for the conversation beside the drawer. */
function Shell() {
  useEffect(() => {
    void (async () => {
      try {
        const response = await fetch(`/tests/fixtures/${fixtureDir}/${fixture}`)
        if (!response.ok) throw new Error(`fixture ${fixture} -> HTTP ${response.status}`)
        useArtifactPanel.getState().openArtifact({
          type: 'file',
          name: filename,
          url: `/tests/fixtures/${fixtureDir}/${fixture}`,
          kind: 'document',
          authenticated: false,
        })
        probe.open = true
      } catch (error) {
        probe.error = error instanceof Error ? error.message : String(error)
      }
    })()
  }, [])

  return createElement(
    'div',
    { className: 'flex h-svh w-full overflow-hidden' },
    createElement('div', { className: 'flex-1 min-w-0', id: 'conversation' }, 'conversation'),
    createElement(ArtifactPanel),
  )
}

const container = document.getElementById('root')
if (!container) throw new Error('#root missing')

createRoot(container).render(
  createElement(
    // Mirrors the providers App.tsx wraps the shell in — the panel header
    // renders Tooltips, which throw without a provider.
    TooltipProvider,
    { delayDuration: 280, skipDelayDuration: 120 },
    createElement(MemoryRouter, null, createElement(Shell)),
  ),
)
probe.ready = true
