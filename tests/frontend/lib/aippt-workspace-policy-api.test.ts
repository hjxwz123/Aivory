import { describe, expect, it, vi } from 'vitest'

const apiMock = vi.hoisted(() => vi.fn().mockResolvedValue({}))
vi.mock('@/api/client', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/api/client')>(),
  api: apiMock,
}))

import { workspacesApi } from '@/api/endpoints'

describe('AI PPT workspace policy API', () => {
  it('sends the AI PPT switch using the server snake_case field', async () => {
    await workspacesApi.updatePolicy('space-1', { AllowAiPPT: false })
    expect(apiMock).toHaveBeenCalledWith('/workspaces/space-1/policy', {
      method: 'PATCH',
      body: { allow_ai_ppt: false },
    })
  })
})
