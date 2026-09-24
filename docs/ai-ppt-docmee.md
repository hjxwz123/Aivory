# AI PPT (Docmee / 文多多, API mode)

Aivory generates presentations through the Docmee V2 API and renders them in its
own UI. Nothing of the vendor's interface is embedded: the browser only talks to
our endpoints, and billing runs through the same credit ledger as the rest of the
product — **one flat price per generated deck**.

> The earlier integration embedded Docmee's iframe UI SDK. It was removed: the
> vendor's look could not be reconciled with the product's, and the iframe gave us
> no control over the flow. `docmee_domain`, `docmee_sdk_url`,
> `docmee_sdk_base_url` and `docmee_creator_version` are retired (dropped on
> migration), and `src/lib/docmee-sdk.ts` / `src/lib/aippt-billing.ts` are gone.

## Flow

```
input (topic / text / URL / file / Markdown)
  → POST /api/me/ppt/tasks              open a vendor task + our deck row
  → POST /api/me/ppt/decks/:id/outline  stream the outline (our SSE)   [editable]
  → GET  /api/me/ppt/templates          pick a template (covers proxied)
  → POST /api/me/ppt/decks/:id/pptx     render, charge, mirror the .pptx
  → GET  /api/me/ppt/decks              "My decks" (our records)
```

Everything Docmee-facing happens server-side (`server/internal/api/aippt_client.go`,
`aippt_handlers.go`): the Api-Key stays in settings, the short-lived vendor token
is minted and cached per user, and the vendor's own error envelope is translated
into our typed errors.

## Verified vendor behaviour (probed against the live API)

The published docs leave several things open; these were confirmed by running the
real flow and shape the implementation:

| Finding | Consequence |
| --- | --- |
| Failures arrive as **HTTP 200 with `code != 0`** (e.g. `1010` method/param, `5001` task failed) | every call inspects `code`; a non-zero code becomes an `aiPPTError` → our `502 upstream_error` |
| `generateContent` with `stream=true` ends with `status=4` carrying **only the outline tree** | the Markdown is accumulated from the `text` deltas (and `stream=false` returns `data.text` complete, ~30s) |
| `POST /api/ppt/v2/options` → `1010`; it is **GET** | the options handler uses GET |
| `templates` returns a **bare array** in `data` (no pagination envelope) | the handler adds `page`/`size`/`has_more` for the UI |
| Template covers are **403 without the temporary token** (`chatmee.cn/...png`) | covers load through `GET /api/me/ppt/resource?url=…`, which injects the token server-side (host-allowlisted) |
| `generatePptx` is fast (~1.7s) and returns `pptInfo`, sometimes without `fileUrl` | the mirror path asks `downloadPptx` when `fileUrl` is missing |
| `downloadPptx` returns a **2-hour Aliyun OSS signed URL** | the render is downloaded and stored as a normal user file (`files` row), so it never expires and appears in *My files* |
| `loadPptxMarkdown` works; `loadPptx`/`listPptx` are not needed | deck lists come from our own `aippt_decks` table; the outline comes from the vendor |
| Upload path: `type=2` + multipart `file` works (docx parsed in ~3.6s) | the upload input is proxied with the configured size cap |
| `updatePptTemplate` takes **`pptId`** (not `id`) and defaults to `sync=false` | sending `id` answers `{"code":-1,"message":"参数错误"}`, which reached users as a bare "service rejected the request"; the client sends `pptId` + `sync=true` so the re-layout is finished before the file is refreshed |
| `uploadTemplate` (`type=4`) answers with the new template object; a `type=4` page then lists it with `num` pages and `pageCoverUrls: null` until Docmee's AI marking finishes | the picker reloads **Mine** after an upload; the template is usable for `generatePptx` right away (verified), while a broken marking only shows up as odd content placement |
| A `type=4` page mixes the caller's own uploads (`userId` = `…_<uid>`) with account-public templates (`userId` = `""`) | the handler marks entries `owned` and only those may be renamed/deleted |
| `updateTemplate` / `delTemplateId` want the **user token**, not the Api-Key (the Api-Key answers `模板不存在` for a uid-level template; a *system* template is refused with `1003 无权限访问`) | rename/delete run under the caller's token; account-public templates are refused for both |
| An **Api-Key upload** creates an account-owned template (`userId` = the account id) that **no uid can see**; only `updateUserTemplate` (`isPublic: true`) publishes it (`userId` becomes `""`) | the admin panel pairs the upload with a "share with all users" switch, and the list reports which templates are shared — otherwise an admin upload silently helps nobody |
| Uploading a template costs **1 vendor credit** | the admin page states the cost, and uploads are administrator-only |
| Vendor pricing: **1 credit per generated deck**, +1 per `updateContent` call, +1 per template re-layout | `docmee_credits_per_ppt` / `docmee_edit_credits` must be priced to cover it (≈¥0.32–0.50 per vendor credit) |

## Billing

- `POST /decks/:id/pptx` opens a credit hold (`store.ReserveCredits`) **before**
  the vendor renders, answers `402 insufficient_credits` when the balance is
  short, then settles under the upstream ppt id
  (`store.SettleCreditReservationByKey`). The ledger row is keyed by that deck id,
  so a retry, a double click or a re-render can never debit twice.
- A failed render releases the hold; a deck that already carries a charge is
  re-rendered for free (the UI's "Save to my files" and "Change template" paths).
- Charging is gated by the **platform-wide credit system**, exactly like chat:
  `billingEnabled` requires `docmee_credits_per_ppt > 0` **and** a non-zero
  `credits_per_usd` rate. With `credits_per_usd = 0` (Admin → Credits & quotas →
  *模型成本内部换算*) the whole credit system is off, chat is free as well, and a
  generation costs nothing. The configured per-deck price stays visible to users
  either way.
- **Every AI PPT event is published to `usage_logs`/`usage_stats`** (purpose
  `ppt`), whether or not it was charged:
  - one row per generated deck (`store.AiPPTGenerateUsageMemo` — the upstream ppt
    id, the same key the settlement uses, so a re-render cannot log twice), and
  - one row per charged edit (`store.AiPPTEditUsageMemo`: `rewrite` / `template`,
    keyed by the attempt id, because every edit is its own charge).
  The row's `credits` column carries what the ledger actually took (0 = free), so
  the row records the CALL while every billing total stays exact.
- Edits (AI rewrite / template change) are free unless `docmee_edit_credits > 0`.

### What the admin sees

Admin → **Usage & billing** → *Usage* lists those rows next to chat/image rows:
the purpose reads **AI PPT 生成**, the conversation column shows the deck title
with a `PPT · 生成 / AI 改写 / 更换模板` tag, a **Credits** column carries the
charge, and a click opens the call detail (event, subject, Docmee ppt id, our
deck record id, template, deck status, user, time, charged credits). The detail is
resolved server-side by `store.attachAiPPTUsageDetails` from the row's memo, so a
deck that was deleted afterwards still reports its event and its charge.

## Admin settings

| Setting | Default | Purpose |
| --- | --- | --- |
| `docmee_enabled` | follows the key | Master switch. Unset ⇒ on when a key exists; an explicit `false` always wins. |
| `docmee_api_key` | *(empty)* | Docmee open-platform API key. Server-side only, masked on read. |
| `docmee_api_base_url` | `https://docmee.cn` | API origin (international build or a self-hosted proxy). |
| `docmee_credits_per_ppt` | `10` | Flat price per generated deck. `0` = free. |
| `docmee_edit_credits` | `0` | Price of one edit (AI rewrite / template change). `0` = free. |
| `docmee_default_template_id` | *(empty)* | Fallback template when a render request has none. |
| `docmee_max_upload_mb` | `50` | Per-file cap for the upload input. |
| `docmee_token_hours` | `2` | Vendor token lifetime (`timeOfHours`); `0` = vendor default. |
| `docmee_sdk_url` | pinned CDN build | Editor SDK script (self-hosted copy / internal mirror). |
| `docmee_domain` | *(empty)* | Editor origin for the international build (`https://app.xpptx.com`). |

Optional environment fallbacks (used when the stored setting is blank):
`DOCMEE_API_KEY`, `DOCMEE_API_BASE_URL`.

The settings page also shows the **vendor's own balance** (`GET
/api/admin/aippt/vendor` → `availableCount`/`usedCount`), because Docmee bills the
deployment separately from our users' credits.

### Enabling it

- The **Enable AI PPT** switch saves on flip (its own PATCH), so it cannot be lost
  by refreshing before the section's Save button is pressed.
- The Save button only sends `docmee_enabled` when the admin expressed an intent:
  the switch they flipped, or "true" because they just supplied a key. It never
  writes a *derived* `false` (that used to outrank the "unset follows the key"
  default and left a configured integration reporting "disabled" after a refresh,
  and would have disabled deployments whose key comes from `DOCMEE_API_KEY`).
- URL fields accept a bare host (`docmee.cn`) and gain an `https://` scheme; values
  that cannot be a URL are rejected, and a rejected PATCH changes nothing.

## Data model

`aippt_decks` (one row per generation, owned by the user):

| column | notes |
| --- | --- |
| `id` | our id (`ppt_…`), used by every endpoint |
| `workspace_id` | creation scope; empty for personal decks, immutable afterward |
| `task_id` / `ppt_id` | vendor identifiers |
| `subject`, `outline` | display name + the Markdown the deck was built from |
| `status` | `draft` → `outline_ready` → `generating` → `ready`/`failed` |
| `template_id`, `template_name`, `cover_url` | chosen template |
| `file_id` | the mirrored `.pptx` in our `files` table (preview/download/share) |
| `credits`, `error`, `options_json` | what was charged, why it failed, the inputs |

## Permissions

- Platform administrators set `allow_ai_ppt` on each user group. Site admins
  bypass the group restriction, as with other group capabilities.
- Workspace admins set `allow_ai_ppt` in the workspace policy and
  `can_use_ai_ppt` for ordinary members. Workspace admins and owners bypass
  member limits, but not their own user-group limit or the workspace switch.
- Effective access is the intersection: deployment enabled, group allowed,
  workspace allowed, and member allowed. Guests cannot use AI PPT. Personal
  space has only the deployment and group gates. Missing fields in older rows
  default to allowed, preserving existing installations.
- All `/api/me/ppt/*` operations re-check permission on every request. Clients
  pass `workspace_id` as a query parameter for workspace operations, including
  template covers and SSE outlines. Deck routes also require that scope to
  match the deck's immutable `workspace_id`; switching to personal space cannot
  reopen a workspace deck after permission is revoked. Legacy decks remain
  personal. The generated `.pptx` is still mirrored to the creator's own files.

## Endpoints

All require a signed-in user and never accept a price from the client.

| Method + path | Purpose |
| --- | --- |
| `GET /api/me/ppt/config` | feature flags, prices, upload cap, balance |
| `GET /api/me/ppt/options` | vendor enumerations (cached 30 min) |
| `GET /api/me/ppt/templates` | template page (cached 15 min; user templates are always read through) |
| `POST /api/me/ppt/templates` | upload a custom template (`type=4`, .pptx only, cap = `docmee_max_upload_mb`) |
| `POST /api/me/ppt/templates/:id/rename` | rename one of the caller's own custom templates |
| `DELETE /api/me/ppt/templates/:id` | delete one of the caller's own custom templates |
| `GET /api/me/ppt/resource?url=` | cover/file proxy (host allowlist) |
| `POST /api/me/ppt/tasks` | create task (JSON or multipart upload) |
| `GET /api/me/ppt/decks` | the caller's decks |
| `GET`/`DELETE /api/me/ppt/decks/:id` | one deck (delete also drops the file) |
| `POST /api/me/ppt/decks/:id/outline` | our SSE: `delta` / `done` / `error`; also the AI-rewrite path |
| `POST /api/me/ppt/decks/:id/pptx` | render + charge + mirror |
| `POST /api/me/ppt/decks/:id/template` | re-layout with another template (vendor `updatePptTemplate` + file refresh) |
| `POST /api/me/ppt/decks/:id/rename` | rename (ours + vendor) |
| `GET /api/me/ppt/decks/:id/editor` | one-time session to open the vendor's **editor** for this deck |
| `POST /api/me/ppt/decks/:id/refresh-file` | re-pull the (possibly edited) deck into the mirrored file |
| `GET /api/admin/aippt/vendor` | deployment's vendor balance (admin) |
| `GET /api/admin/aippt/templates` | the deployment's own templates + how many are shared (admin) |
| `POST /api/admin/aippt/templates` | upload/overwrite a deployment template; `public=true` also publishes it (admin) |
| `POST /api/admin/aippt/templates/:id/public` | publish (`is_public: true`) or withdraw a deployment template (admin) |
| `DELETE /api/admin/aippt/templates/:id` | delete a deployment template (admin) |

## Custom templates

The template picker offers **Upload template**: the browser posts a `.pptx` to
`POST /api/me/ppt/templates`, which forwards it to Docmee's `uploadTemplate` with
`type=4` under the caller's own token. Docmee learns and marks up the file
server-side, so it appears under **Mine** a moment later — the picker switches to
that tab, reloads and pre-selects it. Only `.pptx` is accepted, capped by
`docmee_max_upload_mb` (Docmee suggests 960×540 standard-size templates).

Overwriting a **public** (Api-Key-level) template is a different trust domain:
Docmee only allows it with the Api-Key, so that path is admin-only.

### Deployment templates (Admin → Credits & quotas)

The Docmee block carries a **Deployment templates** panel (`DocmeeTemplateAdmin`):
upload or overwrite a `.pptx`, see the deployment's templates, share/unshare and
delete them. It exists because of the account-owned/published split above — an
administrator who only uploads gets a template that users cannot pick, so the
panel always reports which templates are actually shared.

- `POST /api/admin/aippt/templates` accepts `public=true`, which publishes the
  new template in the same request; if the publish fails the response still
  carries `template_id` (502) so the template can be published from the list
  instead of being re-uploaded.
- `GET /api/admin/aippt/templates` lists what the **Api-Key** sees: the
  deployment's own templates. A user's private upload never appears there.

Uploads can also be **renamed** and **deleted** from the card itself
(`POST …/templates/:id/rename`, `DELETE …/templates/:id`). Both run under the
caller's token and both check ownership first — a `type=4` page also lists the
deployment's shared templates, so the handler marks each entry `owned` (its
`userId` ends with the caller's upstream uid) and answers `404 template_not_found`
for anything else, without calling the vendor.

Template requirements worth telling an uploader: **16:9, i.e. 960×540** (Docmee's
own guidance is to fix a non-standard deck in Office under *Design → Slide size →
33.867 × 19.05 cm*). Docmee AI-marks the uploaded deck to learn its layout; if a
generated deck places content oddly, that marking can be corrected by hand at
`https://docmee.cn/marker/{templateId}?token={apiKey}`.

## Rendering feedback

`generatePptx` returns no per-page progress. While the request is pending, the UI
shows the selected template and an indeterminate status, without estimating a
percentage or marking unconfirmed processing steps as complete. The preview
opens as soon as the response arrives. The status indicator respects
`prefers-reduced-motion`.

The creation page shares the site's header, theme tokens and form controls.
Creation and saved decks remain accessible on small screens, template covers keep
their 16:9 ratio, and the result has an inline preview on desktop and mobile.
Changing a template or syncing the editor reloads the preview even if the file id
is unchanged. Retrying a file save uses `refresh-file` rather than rendering again.

## Editing slides

Creation, templates and the outline live in our own UI, but slide-level editing
is the one thing a bespoke front end cannot reasonably rebuild — so the result
step offers **Edit slides**, which opens Docmee's editor for that deck:

- the browser gets a one-time `{token, ppt_id, sdk_url, domain}` hand-off (never
  the Api-Key), then mounts the vendor's editor iframe;
- the editor saves upstream, so closing it pulls the deck back with
  `refresh-file`: the mirrored `.pptx` in the user's files is replaced and the
  previous copy is retired;
- `docmee_sdk_url` (pinned CDN build, or a self-hosted copy) and `docmee_domain`
  (international build origin) exist only for this surface — the frontend can also
  override the SDK URL at build time with `VITE_AIVORY_DOCMEE_SDK_URL`;
- the cover proxy is a signature-exempt GET (`/api/me/ppt/resource`) because an
  `<img>` cannot attach the request proof; it still requires a session and a
  vendor-host allowlist.

## Tests

- `server/internal/api/aippt_handlers_test.go` — proxying (options/templates),
  resource host allowlist, outline streaming + persistence, render → single charge
  → mirrored file, fail-closed without credits, ownership, typed upstream errors,
  upload path, deck lifecycle, template switch (asserts the vendor's `pptId`/`sync`
  parameters and that no re-render happens), template rename/delete + the
  foreign-template refusal, admin template list/publish/delete and the
  upload-and-publish-in-one-step path (including a publish failure that keeps the
  uploaded id), and that every AI PPT route is really wired through the router.
- `server/internal/api/docmee_handlers_test.go` — config payload (no secrets, no
  retired fields), configuration/off-switch states, upstream error mapping, admin
  round-trip, URL normalization.
- `server/internal/api/docmee_usage_test.go` — the usage row: one per deck,
  correct credits, backfill, the AI PPT deck detail, and that an unbilled
  generation is still recorded as a call with 0 credits.
- `server/internal/store/aippt_usage_test.go` — the memo format and the deck
  resolution behind a `ppt` row (generate by vendor id, edit by deck id, deleted
  deck keeps the event, non-PPT rows carry no detail).
- `server/internal/store/credits_test.go` — settle-by-key idempotency.
- `tests/frontend/lib/aippt-outline.test.ts` — Markdown outline parsing/warnings.
- `tests/frontend/lib/aippt-admin-settings.test.ts` — the enable-flag rules.

## Known limits

- **Slide-level editing** happens in the vendor's editor (see above); our own pptx
  editor is not wired into this flow. If the editor is unreachable, the deck is
  still complete: outline AI-rewrite, template change and rename all run server-side.
- A deck whose mirror failed stays usable upstream (`status=ready`, no `file_id`);
  the UI offers "Save to my files" to retry.
- One deployment serves one Docmee region/account (`docmee_api_base_url`).
