import { beforeEach, describe, expect, it, vi } from 'vitest'

const apiMocks = vi.hoisted(() => ({
  sandboxFiles: vi.fn(),
}))

vi.mock('@/api/endpoints', () => ({
  conversationsApi: apiMocks,
}))

import { useConversationFiles } from '@/store/conversation-files'
import { useHtmlPreview } from '@/store/html-preview'
import { useInlineThreadDrawer } from '@/store/inline-thread'
import { useSandboxFiles } from '@/store/sandbox-files'

describe('sandbox files drawer', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    apiMocks.sandboxFiles.mockResolvedValue({
      session: 'sid',
      files: [{ path: 'outputs/report.pdf', size: 100 }],
    })
    useConversationFiles.setState({ open: false })
    useHtmlPreview.setState({ open: false })
    useInlineThreadDrawer.setState({ open: false })
    useSandboxFiles.setState({
      open: false,
      conversationId: null,
      files: [],
      session: '',
      currentPath: '',
      loading: false,
      unavailable: false,
      error: false,
    })
  })

  it('opens read-only data for one conversation and closes the other side panels', async () => {
    useConversationFiles.setState({ open: true })
    useHtmlPreview.setState({ open: true })
    useInlineThreadDrawer.setState({ open: true })

    useSandboxFiles.getState().openDrawer('conv-1')
    await vi.waitFor(() => expect(useSandboxFiles.getState().loading).toBe(false))

    expect(apiMocks.sandboxFiles).toHaveBeenCalledWith('conv-1')
    expect(useSandboxFiles.getState()).toMatchObject({
      open: true,
      conversationId: 'conv-1',
      session: 'sid',
      files: [{ path: 'outputs/report.pdf', size: 100 }],
    })
    expect(useConversationFiles.getState().open).toBe(false)
    expect(useHtmlPreview.getState().open).toBe(false)
    expect(useInlineThreadDrawer.getState().open).toBe(false)
  })
})
