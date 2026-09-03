import { afterEach, describe, expect, it, vi } from 'vitest'

import { adminApi } from '@/api'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('administrator channel model import API', () => {
  it('discovers models from an unsaved channel configuration', async () => {
    const response = {
      models: [{ request_id: 'gpt-5', label: 'GPT-5', description: '', kind: 'chat' }],
      discovered: 2,
      skipped_unsupported: 1,
    }
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(JSON.stringify(response), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    const body = {
      type: 'openai' as const,
      api_format: 'responses' as const,
      base_url: 'https://api.example.com/v1',
      api_key: 'secret',
    }
    await expect(adminApi.discoverChannelModels(body)).resolves.toEqual(response)

    const [url, options] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/admin/channels/models/discover')
    expect(options).toMatchObject({ method: 'POST', credentials: 'include' })
    expect(JSON.parse(String(options?.body))).toEqual(body)
  })

  it('discovers models from an encoded saved channel without sending credentials', async () => {
    const response = {
      models: [{ request_id: 'gpt-5.1', label: 'GPT-5.1', description: '', kind: 'chat' }],
      discovered: 1,
      skipped_unsupported: 0,
    }
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(JSON.stringify(response), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(adminApi.discoverSavedChannelModels('channel / one')).resolves.toEqual(response)

    const [url, options] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/admin/channels/channel%20%2F%20one/models/discover')
    expect(options).toMatchObject({ method: 'POST', credentials: 'include' })
    expect(options?.body).toBeUndefined()
  })

  it('creates a selected model batch under the encoded channel', async () => {
    const response = { requested: 2, created: 2, skipped_existing: 0, skipped_duplicate: 0 }
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(JSON.stringify(response), {
        status: 201,
        headers: { 'content-type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)
    const models = [
      { request_id: 'gpt-5', label: 'GPT-5', description: '', kind: 'chat' as const },
      { request_id: 'gpt-image-1', label: 'GPT Image', description: '', kind: 'image' as const },
    ]

    await expect(adminApi.createChannelModelsBatch('channel / one', models)).resolves.toEqual(response)

    const [url, options] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/admin/channels/channel%20%2F%20one/models/batch')
    expect(options).toMatchObject({ method: 'POST', credentials: 'include' })
    expect(JSON.parse(String(options?.body))).toEqual({ models })
  })

  it('posts to the encoded channel-scoped import endpoint and returns the import counts', async () => {
    const response = {
      discovered: 8,
      created: 5,
      skipped_existing: 2,
      skipped_unsupported: 1,
    }
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(JSON.stringify(response), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(adminApi.importChannelModels('channel / one')).resolves.toEqual(response)

    expect(fetchMock).toHaveBeenCalledOnce()
    const [url, options] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/admin/channels/channel%20%2F%20one/models/import')
    expect(options).toMatchObject({ method: 'POST', credentials: 'include' })
  })
})
