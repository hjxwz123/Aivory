/**
 * Client half of the AI PPT billing protocol (§ AI PPT / Docmee iframe).
 *
 * The iframe emits the same lifecycle events more than once (retries, remounts,
 * a reloaded page re-reporting the deck it just generated), so the page must not
 * translate "an event arrived" into "bill the user". This tracker owns that
 * translation and guarantees:
 *
 *   - at most one live credit hold at a time (repeated `beforeGenerate` reuses
 *     the open attempt instead of holding twice);
 *   - at most one settle per attempt, and at most one settle per upstream deck
 *     id, even when `charge` and `afterGenerate` both fire or the SDK retries;
 *   - a billed attempt is never reused for the next deck — the next generation
 *     opens a fresh hold;
 *   - an attempt that failed or was abandoned is refunded exactly once.
 *
 * The server stays authoritative: it keys the debit on the upstream PPT id, so a
 * duplicate settle is a no-op there as well.
 */
import type { ApiAiPPTAttempt, ApiAiPPTCharge } from '@/api/types'

export interface AiPPTBillingDeps {
  /** Open a hold; rejects with a 402 ApiError when the balance is short. */
  attempt: () => Promise<ApiAiPPTAttempt>
  charge: (attemptId: string, pptId?: string) => Promise<ApiAiPPTCharge>
  release: (attemptId: string) => Promise<unknown>
  /** Called with the authoritative spendable balance after every transition. */
  onBalance?: (available: number) => void
  /** Called after a successful (or deduplicated) settle. */
  onCharged?: (outcome: AiPPTChargeOutcome) => void
}

export interface AiPPTChargeOutcome {
  credits: number
  alreadyCharged: boolean
  creditsAvailable: number
}

export interface AiPPTBilling {
  /** The open (unbilled) attempt, or null when none is held. */
  current(): ApiAiPPTAttempt | null
  /** True while the current attempt has been billed and not yet replaced. */
  settled(): boolean
  /** Open (or reuse) an attempt. Rejects with the API error when it cannot. */
  ensure(): Promise<ApiAiPPTAttempt>
  /** Bill the current attempt once. Returns null when there is nothing to bill. */
  settle(pptId?: string): Promise<AiPPTChargeOutcome | null>
  /** Refund the open attempt after a failure/abandonment. Never throws. */
  fail(): Promise<void>
  /** Drop local state (unmount). A live hold still expires server-side. */
  reset(): void
}

export function createAiPPTBilling(deps: AiPPTBillingDeps): AiPPTBilling {
  let current: ApiAiPPTAttempt | null = null
  let settling: { attemptId: string; promise: Promise<AiPPTChargeOutcome | null> } | null = null
  let releasing: Promise<void> | null = null
  /** Deck ids billed in this page session — the duplicate-event short-circuit. */
  const billedDecks = new Set<string>()

  function reportBalance(available: number) {
    if (Number.isFinite(available)) deps.onBalance?.(available)
  }

  function ensure(): Promise<ApiAiPPTAttempt> {
    // A billed attempt must never be handed out again: doing so would settle
    // nothing for the NEXT deck and give it away for free.
    if (current && !current.credits_charged) return Promise.resolve(current)
    return deps.attempt().then((opened) => {
      current = opened
      reportBalance(opened.credits_available)
      return opened
    })
  }

  async function settle(pptId?: string): Promise<AiPPTChargeOutcome | null> {
    const deckId = (pptId ?? '').trim()
    // Same deck reported twice: the first settle already billed it.
    if (deckId && billedDecks.has(deckId)) return null
    // Without an upstream id, "the current attempt is already billed" is the only
    // duplicate signal available — and opening a fresh hold would double-bill.
    if (!deckId && current?.credits_charged) return null

    const opened = await ensure()
    // De-duplicate concurrent events for the SAME attempt.
    if (settling && settling.attemptId === opened.attempt_id) return settling.promise

    const promise = deps
      .charge(opened.attempt_id, deckId || undefined)
      .then((charged) => {
        // Mark consumed before notifying listeners: a listener that re-enters
        // settle() must hit a dedupe guard, not a second debit.
        if (current?.attempt_id === opened.attempt_id) {
          current = { ...opened, credits_charged: true }
        }
        if (deckId) billedDecks.add(deckId)
        reportBalance(charged.credits_available)
        const outcome: AiPPTChargeOutcome = {
          credits: charged.credits,
          alreadyCharged: Boolean(charged.already_charged),
          creditsAvailable: charged.credits_available,
        }
        deps.onCharged?.(outcome)
        return outcome
      })
      .catch((error: unknown) => {
        // A failed settle leaves the attempt open so a retry can bill it; the
        // server still dedupes on the upstream PPT id.
        if (settling?.attemptId === opened.attempt_id) settling = null
        throw error
      })
    settling = { attemptId: opened.attempt_id, promise }
    return promise
  }

  function fail(): Promise<void> {
    if (releasing) return releasing
    const held = current
    if (!held || held.credits_charged) return Promise.resolve()
    current = null
    releasing = deps
      .release(held.attempt_id)
      .then(() => undefined)
      .catch(() => undefined) // refunds are best-effort; the hold also expires
      .finally(() => {
        releasing = null
      })
    return releasing
  }

  return {
    current: () => current,
    settled: () => Boolean(current?.credits_charged),
    ensure,
    settle,
    fail,
    reset: () => {
      current = null
      billedDecks.clear()
    },
  }
}
