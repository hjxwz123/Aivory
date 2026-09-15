import { afterEach, describe, expect, it, vi } from 'vitest'
import { projectsApi } from '@/api'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('project deletion API', () => {
  it('keeps project conversations by default', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await projectsApi.remove('project/one')

    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/projects/project%2Fone')
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({ method: 'DELETE' })
  })

  it('requests permanent conversation deletion when selected', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await projectsApi.remove('project-1', true)

    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/projects/project-1?delete_conversations=true')
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({ method: 'DELETE' })
  })
})
