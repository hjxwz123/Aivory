import { describe, expect, it, vi } from 'vitest'

import { createAiPPTBilling, type AiPPTBillingDeps } from '@/lib/aippt-billing'
import type { ApiAiPPTAttempt, ApiAiPPTCharge } from '@/api/types'

function attempt(id: string, available = 15): ApiAiPPTAttempt {
  return {
    attempt_id: id,
    expires_at: Math.floor(Date.now() / 1000) + 1800,
    credits_per_ppt: 10,
    credits_charged: false,
    credits_available: available,
  }
}

function charge(credits = 10, available = 5, already = false): ApiAiPPTCharge {
  return {
    credits,
    already_charged: already,
    credits_per_ppt: 10,
    credits_available: available,
  }
}

function setup(overrides: Partial<AiPPTBillingDeps> = {}) {
  const deps: AiPPTBillingDeps = {
    attempt: vi.fn(async () => attempt('ppt_1')),
    charge: vi.fn(async () => charge()),
    release: vi.fn(async () => ({ released: true })),
    ...overrides,
  }
  const balances: number[] = []
  const charged: Array<{ credits: number; alreadyCharged: boolean }> = []
  const billing = createAiPPTBilling({
    ...deps,
    onBalance: (available) => balances.push(available),
    onCharged: (outcome) => charged.push(outcome),
  })
  return { deps, billing, balances, charged }
}

describe('AI PPT billing tracker', () => {
  it('holds one attempt and reuses it for repeated generation starts', async () => {
    const { deps, billing, balances } = setup()

    const first = await billing.ensure()
    const second = await billing.ensure()

    expect(deps.attempt).toHaveBeenCalledTimes(1)
    expect(first.attempt_id).toBe('ppt_1')
    expect(second.attempt_id).toBe('ppt_1')
    expect(billing.current()?.attempt_id).toBe('ppt_1')
    // The server's held-balance figure is reported, not a local guess.
    expect(balances).toEqual([15])
  })

  it('bills a deck exactly once even when charge and afterGenerate both fire', async () => {
    const { deps, billing, charged } = setup()

    await billing.settle('deck-1')
    await billing.settle('deck-1')
    await billing.settle('deck-1')

    expect(deps.charge).toHaveBeenCalledTimes(1)
    expect(deps.charge).toHaveBeenCalledWith('ppt_1', 'deck-1')
    expect(billing.settled()).toBe(true)
    expect(charged).toEqual([{ credits: 10, alreadyCharged: false, creditsAvailable: 5 }])
  })

  it('collapses concurrent settles into a single debit', async () => {
    const { deps, billing } = setup({
      charge: vi.fn(async () => {
        await new Promise((resolve) => setTimeout(resolve, 5))
        return charge()
      }),
    })

    const [a, b] = await Promise.all([billing.settle('deck-2'), billing.settle('deck-2')])

    expect(deps.charge).toHaveBeenCalledTimes(1)
    expect(a).toEqual(b)
  })

  it('reports a duplicate charge without taking a second debit', async () => {
    const { deps, billing, charged } = setup({
      charge: vi.fn(async () => charge(10, 15, true)),
    })

    await billing.settle('deck-3')

    expect(deps.charge).toHaveBeenCalledTimes(1)
    expect(charged[0]).toEqual({ credits: 10, alreadyCharged: true, creditsAvailable: 15 })
  })

  it('refunds an abandoned attempt and opens a fresh one next time', async () => {
    const { deps, billing } = setup()

    await billing.ensure()
    await billing.fail()

    expect(deps.release).toHaveBeenCalledWith('ppt_1')
    expect(billing.current()).toBeNull()

    vi.mocked(deps.attempt).mockResolvedValueOnce(attempt('ppt_2', 15))
    await billing.settle('deck-4')

    expect(deps.attempt).toHaveBeenCalledTimes(2)
    expect(deps.charge).toHaveBeenCalledWith('ppt_2', 'deck-4')
  })

  it('never refunds an attempt that was already billed', async () => {
    const { deps, billing } = setup()

    await billing.settle('deck-5')
    await billing.fail()

    expect(deps.release).not.toHaveBeenCalled()
  })

  it('keeps the attempt open when a settle fails, so a retry can bill it', async () => {
    const failure = new Error('charge failed')
    const { deps, billing } = setup({
      charge: vi
        .fn<AiPPTBillingDeps['charge']>()
        .mockRejectedValueOnce(failure)
        .mockResolvedValueOnce(charge()),
    })

    await expect(billing.settle('deck-6')).rejects.toThrow('charge failed')
    expect(billing.settled()).toBe(false)
    expect(billing.current()?.attempt_id).toBe('ppt_1')

    await expect(billing.settle('deck-6')).resolves.toEqual({
      credits: 10,
      alreadyCharged: false,
      creditsAvailable: 5,
    })
    expect(deps.charge).toHaveBeenCalledTimes(2)
  })

  it('opens a fresh hold for the next deck instead of reusing the billed attempt', async () => {
    const { deps, billing } = setup()

    // Deck 1: hold + settle.
    await billing.settle('deck-a')
    expect(deps.attempt).toHaveBeenCalledTimes(1)
    expect(deps.charge).toHaveBeenNthCalledWith(1, 'ppt_1', 'deck-a')

    // Deck 2 must take its OWN hold — reusing the consumed attempt would make
    // the second deck free.
    vi.mocked(deps.attempt).mockResolvedValueOnce(attempt('ppt_2', 15))
    await billing.settle('deck-b')

    expect(deps.attempt).toHaveBeenCalledTimes(2)
    expect(deps.charge).toHaveBeenNthCalledWith(2, 'ppt_2', 'deck-b')
    expect(deps.charge).toHaveBeenCalledTimes(2)
  })

  it('does not re-open a hold for a duplicate deck event', async () => {
    const { deps, billing } = setup()

    await billing.settle('deck-dup')
    await billing.settle('deck-dup')
    await billing.settle('deck-dup')

    expect(deps.attempt).toHaveBeenCalledTimes(1)
    expect(deps.charge).toHaveBeenCalledTimes(1)
  })

  it('treats a repeated settle without an upstream id as already billed', async () => {
    const { deps, billing } = setup()

    await billing.settle()
    await billing.settle()

    expect(deps.attempt).toHaveBeenCalledTimes(1)
    expect(deps.charge).toHaveBeenCalledTimes(1)
  })

  it('surfaces a refused hold (insufficient credits) to the caller', async () => {
    const refusal = Object.assign(new Error('insufficient credits'), { status: 402 })
    const { deps, billing } = setup({
      attempt: vi.fn(async () => {
        throw refusal
      }),
    })

    await expect(billing.settle('deck-7')).rejects.toBe(refusal)
    expect(deps.charge).not.toHaveBeenCalled()
    expect(billing.current()).toBeNull()
  })
})
