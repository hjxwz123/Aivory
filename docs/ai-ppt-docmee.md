# AI PPT (Docmee / 文多多 AiPPT iframe)

Aivory embeds the Docmee presentation workbench as an iframe and bills it through
the same credit ledger the rest of the product uses: **one flat price per
generated deck**.

This is the "接入方案二" integration shape — the Docmee UI SDK is loaded as a
plain `<script>` from a URL pinned in admin settings (no npm dependency, no
build-time coupling to a third party), and the page talks to it through its
`onMessage` callback.

## What lives where

| Concern | Owner |
| --- | --- |
| Docmee API key, API base URL, SDK URL, per-deck price | Admin → **Credits and quotas** → *AI PPT (文多多)* |
| Per-user iframe token (`createApiToken`) | Server (`POST /api/me/ppt/*`), cached ~20 min per user |
| Credit hold / settle / refund | Server, on the shared `credit_ledger` + `credit_reservations` tables |
| Iframe lifecycle + event wiring | `src/pages/ppt/AiPPT.tsx`, `src/lib/docmee-sdk.ts` |
| Client-side de-duplication of billing events | `src/lib/aippt-billing.ts` |

The browser never receives the Docmee API key. It only ever sees a short-lived
`token` bound to a per-user Docmee `uid` (a salted hash of the local user id, so
internal ids never leave the deployment).

## Billing protocol

A generation moves through three authenticated calls. All of them require a
signed-in user; none of them accept a price from the client.

1. `GET /api/me/ppt/token` — **no billing.** Mints (or reuses) the iframe token.
   Kept separate from the hold so merely opening the page never reserves credits.
2. `POST /api/me/ppt/attempt` — opens a generation attempt and **holds
   `docmee_credits_per_ppt`** against the user's balance (`store.ReserveCredits`,
   TTL 30 min). Answers `402 insufficient_credits` when the balance cannot cover
   it, which the page turns into a "top up" dialog and a `false` return from
   `beforeGenerate` — so generation is blocked *before* any upstream work.
3. `POST /api/me/ppt/charge` `{attempt_id, ppt_id}` — **settles** the hold under
   the upstream PPT id (`store.SettleCreditReservationByKey`). The ledger row is
   keyed by that PPT id, so a replayed `charge`/`afterGenerate` event, a page
   reload re-reporting the same deck, or two concurrent requests debit exactly
   once. A duplicate attempt for an already-billed deck has its own hold
   refunded and answers `already_charged: true`.

`POST /api/me/ppt/release` `{attempt_id}` refunds an attempt whose generation
failed or was abandoned. It is idempotent, and only the hold's owner may release
it. A hold that is never settled expires on its own (30 min), so closing the tab
cannot strand a user's credits.

Client-side, the tracker distinguishes two ways of leaving the page: a hold taken
while the user was only browsing (nothing had started generating) is refunded
immediately, while a hold whose generation already started is left in place —
releasing it would hand out a deck that the upstream may still produce. That hold
simply expires if the charge event never arrives.

Notes:

- Billing is considered **off** (generation is free) unless both
  `docmee_credits_per_ppt > 0` **and** the platform-wide `credits_per_usd > 0`.
  A self-hosted deployment that never turned credits on gets the feature for
  free instead of an unpayable prompt.
- A deck that somehow reaches the `charge` event without a live hold still bills
  correctly: the tracker opens a hold at settle time. If the balance is short at
  that moment the debit fails and the page reports it — the safe direction, but
  keeping `credits_per_ppt` affordable is what prevents the situation.

## Admin settings

| Setting | Default | Purpose |
| --- | --- | --- |
| `docmee_enabled` | follows the key | Master switch. Unset ⇒ on when a key exists; an explicit `false` always wins (park the integration without deleting credentials). |
| `docmee_api_key` | *(empty)* | Docmee open-platform API key. Masked on read; the display mask is ignored on write. |
| `docmee_credits_per_ppt` | `10` | Flat price per generated deck. `0` = free. |
| `docmee_creator_version` | `v2` | `v2` = conversational creator (passes `content`), `v1` = step-by-step. |
| `docmee_api_base_url` | `https://docmee.cn` | Server-side `createApiToken` endpoint. Change for the international build or a self-hosted proxy. |
| `docmee_domain` | *(empty)* | Passed to the SDK as `DOMAIN`. Empty ⇒ China build; the international build uses `https://app.xpptx.com`. |
| `docmee_sdk_url` | pinned jsDelivr build | Iframe SDK script URL. Point it at a self-hosted copy or an internal mirror to drop the third-party dependency at runtime. |
| `docmee_sdk_base_url` | *(empty)* | Passed to the SDK as `baseURL` when the browser must reach Docmee through your own domain (§ Docmee's nginx 接口转发 guide). |
| `docmee_token_hours` | `2` | Upstream token lifetime (`timeOfHours`); `0` = Docmee default. |

Optional environment fallbacks (used when the stored setting is blank):
`DOCMEE_API_KEY`, `DOCMEE_API_BASE_URL`, `DOCMEE_DOMAIN`, `DOCMEE_SDK_URL`,
`DOCMEE_SDK_BASE_URL` — the same pattern as the MinerU integration.

### Enabling it from the admin UI

- The **Enable AI PPT** switch saves on flip (its own PATCH), so it cannot be lost
  by refreshing before the section's Save button is pressed.
- The section's Save button writes the key, price, version and URLs. It only sends
  `docmee_enabled` when the admin expressed an intent in that session: the switch
  they flipped, or "true" because they just supplied a key. It never writes a
  *derived* `false` — that used to persist an explicit off state, which outranks
  the "unset follows the key" default and left a configured integration reporting
  "disabled" after every refresh (and would have disabled deployments whose key
  comes from `DOCMEE_API_KEY`, where the form field is legitimately blank).
- If the flag is off while a key is configured, the section says so and points at
  the switch.
- URL fields accept a bare host (`app.xpptx.com`) and gain an `https://` scheme;
  values that cannot be a URL are rejected. A rejected PATCH changes nothing, so a
  typo cannot half-apply a configuration.
- After a successful save the page refreshes the shared runtime config, so the
  sidebar entry and the `/ppt` page react without a full reload.

## Self-hosting the SDK or proxying Docmee

Docmee's own guidance is to start on their CDN and self-host later. Two
independent knobs cover the production shapes:

- **Self-host the SDK file**: download the Docmee iframe SDK and set
  `docmee_sdk_url` to your copy. Nothing else changes.
- **Proxy the API through your domain**: set `docmee_domain` (and
  `docmee_sdk_base_url` if the SDK should call your proxy) so the browser talks
  to your origin, and `docmee_api_base_url` so the server mints tokens through
  the same proxy.

## International build

Set `docmee_domain` to `https://app.xpptx.com` and point
`docmee_api_base_url` at the international API origin your account uses. Both
values are per-deployment, so one Aivory instance serves a single Docmee region.

## Access and gating

The integration is gated by the admin switch alone: `DOCMEE_ENABLED`-style
per-group permissions are deliberately not part of this change, because a group
permission would also need a new row in the group editor's permission matrix.
Any signed-in member can use the page once an administrator enables it; the
per-deck price is what limits consumption.

## Tests

- `server/internal/store/credits_test.go` — settle-by-key idempotency, refunds,
  foreign/released holds.
- `server/internal/api/docmee_handlers_test.go` — token caching, hold
  open/refuse, single debit per deck, release guards, free-when-credits-off, and
  that upstream error text never reaches the browser.
- `tests/frontend/lib/aippt-billing.test.ts` — the client-side event tracker
  (one hold, one settle, refunds, retries after failure).
- `tests/frontend/lib/aippt-admin-settings.test.ts` — the admin form's enable-flag
  rules (never persist a derived `false`; a supplied key activates the feature).
- `TestDocmeeAdminSettingsRoundTripKeepsTheIntegrationEnabled` and
  `TestDocmeeAdminSettingsNormalizesURLs` — the admin save → reload path, masked
  key handling, and URL normalization.
