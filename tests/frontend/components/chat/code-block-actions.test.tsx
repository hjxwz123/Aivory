// @vitest-environment jsdom
import { act, createElement, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import { CodeBlock } from '@/components/chat/code-block'

const mocks = vi.hoisted(() => ({ run: vi.fn(), preview: vi.fn(), copy: vi.fn(), highlight: vi.fn() }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock('@/components/ui/tooltip', () => ({ Tooltip: ({ children }: { children: ReactNode }) => children }))
vi.mock('@/hooks/use-clipboard', () => ({ useCopy: () => ({ copied: false, copy: mocks.copy }) }))
vi.mock('@/hooks/use-html-preview-share', () => ({ useHTMLPreviewShare: () => ({ sharing: false, copied: false, copyLink: vi.fn() }) }))
vi.mock('@/store/theme', () => ({ useTheme: () => 'light' }))
vi.mock('@/lib/syntax/use-code-highlight', () => ({ useCodeHighlight: mocks.highlight }))
vi.mock('@/lib/pyodide-runner', () => ({ runPython: mocks.run }))
vi.mock('@/store/artifact-panel', () => ({
  autoOpenPreview: vi.fn(),
  useArtifactPanel: Object.assign(() => false, { getState: () => ({ openArtifact: mocks.preview, syncHtml: vi.fn() }) }),
}))
vi.mock('@/components/chat/code-run-output', () => ({ CodeRunOutput: () => null }))

describe('code actions with wrapping enabled', () => {
  it('copies and runs the original code without recalculating highlights on wrap changes', async () => {
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
    mocks.highlight.mockReturnValue({ html: '<span>original</span>' })
    mocks.run.mockReturnValue({ promise: Promise.resolve({ stdout: '', stderr: '', figures: [], durationMs: 1 }), cancel: vi.fn() })
    const container = document.createElement('div')
    document.body.appendChild(container)
    const root = createRoot(container)
    const code = '\tprint("a long original line")\n'
    try {
      act(() => root.render(createElement(CodeBlock, { code, lang: 'python' })))
      const highlights = mocks.highlight.mock.calls.length
      act(() => container.querySelector<HTMLButtonElement>('[aria-label="code.wrap"]')!.click())
      expect(mocks.highlight).toHaveBeenCalledTimes(highlights)
      act(() => container.querySelector<HTMLButtonElement>('[aria-label="actions.copy"]')!.click())
      expect(mocks.copy).toHaveBeenCalledWith(code)
      await act(async () => container.querySelector<HTMLButtonElement>('[aria-label="code.run"]')!.click())
      expect(mocks.run).toHaveBeenCalledWith(code, expect.any(Object))
      const html = '<!doctype html><html><body>original</body></html>'
      act(() => root.render(createElement(CodeBlock, { code: html, lang: 'html' })))
      act(() => container.querySelector<HTMLButtonElement>('[aria-label="code.preview"]')!.click())
      expect(mocks.preview).toHaveBeenCalledWith(expect.objectContaining({ html, type: 'html' }))
    } finally {
      act(() => root.unmount())
      container.remove()
      vi.unstubAllGlobals()
    }
  })
})
