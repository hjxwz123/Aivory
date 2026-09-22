# In-Browser PPTX Editing + HTML Editor for a Narrow Panel — Research Report

**Prepared:** 2026-09-21
**Context:** Commercial **closed-source** React 19 + Vite + TypeScript AI-chat product. Needs a right-hand panel that can preview **and edit** `.pptx`, and separately edit raw HTML source.
**Method:** `web_fetch` / `web_search` only (this environment's shell has no outbound network). Version/date/`license` facts below come from npm registry metadata, GitHub API metadata, and vendor docs fetched on 2026-09-21.

---

## 0. Executive summary (read this first)

1. **The licensing finding is the decision-driver, not the technology.** Every credible open-source browser PPTX *editor* is **AGPL-3.0**: PPTist (AGPL-3.0), ONLYOFFICE sdkjs/web-apps (AGPL-3.0 + Section 7 terms), `ranuts/document` (AGPL-3.0). AGPL-3.0 §13 requires that if you let users interact with your product over a network, you must offer them the **complete corresponding source of your whole application**. For a closed-source commercial AI-chat product, dropping any of these in as-is is a license violation unless you buy a commercial exception. Only `pptxgenjs` (MIT) and `pptxtojson` (MIT) are permissive — and **neither edits an existing deck**; `pptxgenjs` can only *generate*.
2. **The user's prior belief about PPTist was wrong, in the good direction.** PPTist **does** import `.pptx`. It depends on `pptxtojson` (browser-side PPTX → JSON parser) and exports PPTX via `pptxgenjs`. Its own README claims ~85%+ import fidelity and ~95%+ export fidelity.
3. **PPTist is Vue 3, not React.** Embedding it in a React 19 app means either an iframe/postMessage boundary or a large port. Its README also states outright that it is *not* out-of-the-box and not meant to be an "Office PPT editing relay station."
4. **For "real" Office-grade editing with no server, `ranuts/document` is the most interesting new option** (OnlyOffice `sdkjs` + `web-apps` + the `x2t` WASM converter running fully client-side, ~1,948★, actively developed, v0.0.6 on 2026-09-02). It is still AGPL-3.0 and its Section 7 terms **obligate you to keep the ONLYOFFICE logo visible**.
5. **Building a canvas PPTX editor from scratch on `pptxgenjs` is the trap.** `pptxgenjs` has no reader. You still need a parser (`pptxtojson`, MIT — that part is fine). What you actually pay for is the editor. Reference point: PPTist has been in development since **December 2020** and is still shipping import-fidelity fixes as of **2026-09-19**. Honest planning numbers in §1.7.
6. **HTML panel: use CodeMirror 6, not Monaco.** CodeMirror 6 basic setup is **~119 kB gzip**; a realistic HTML+CSS+JS setup lands roughly **140–175 kB gzip** and is trimmable (drop autocomplete/lint/search). Monaco's own README says mobile browsers are **not supported**, and its shipped bundles are megabytes (§2.2). For a narrow right-hand pane, CodeMirror wins on size, width behavior, and touch.
7. **TinyMCE and CKEditor 5 both confirmed hostile/nonviable for closed-source:** TinyMCE 8 is **GPLv2+**, and when self-hosted **for commercial use the editor refuses to run without a commercial license key + `licensekeymanager` addon**. CKEditor 5 is **dual-licensed GPL2+ / commercial** (`SEE LICENSE IN LICENSE.md` on npm). If you want a WYSIWYG that is genuinely MIT, the shortlist is **Tiptap 3, Lexical, ProseMirror, Slate** — with the caveat that structured editors cannot round-trip *arbitrary* HTML (§2.5).

---

# TOPIC 1 — In-browser PPTX editing (open source)

## 1.1 Summary matrix

| Project | URL | Latest version / date | License | Parse existing `.pptx` | **Edit** | Export back to `.pptx` | Deployment | Maintenance |
|---|---|---|---|---|---|---|---|---|
| **PPTist** | [github.com/pipipi-pikachu/PPTist](https://github.com/pipipi-pikachu/PPTist) | No tagged releases; `package.json` = `2.0.0`; last commit **2026-09-19** | **AGPL-3.0** (old Apache-2.0 snapshot from 2022-05 frozen/unmaintained; paid commercial license ¥2,999/yr, ¥5,699 perpetual) | ✅ Yes — via `pptxtojson`, ~85%+ claimed | ✅ Yes (Vue 3 canvas editor; text/shape/image/chart/table/audio/video/formula) | ✅ Yes — via `pptxgenjs`, ~95%+ claimed | **Pure browser** | **Very active** — 9,348★, 1,772 forks, commits within 2 days of this report |
| **`pptxtojson`** | [github.com/pipipi-pikachu/pptxtojson](https://github.com/pipipi-pikachu/pptxtojson) | **v2.0.1, 2026-04-15** | **MIT** | ✅ Yes (this is its whole job) | ❌ No | ❌ No | Pure browser | Active |
| **`pptxgenjs`** | [github.com/gitbrent/PptxGenJS](https://github.com/gitbrent/PptxGenJS) | **v4.0.1, 2025-06-26** (v4.0.0 2025-05-04; previous v3.12.0 was 2023-03-20) | **MIT** | ❌ **No reader at all** | ❌ No | ✅ Yes (generation only) | Pure browser (v4.0.0 fixed Vite + Web Worker detection) | Maintained, slow cadence; single maintainer |
| **`ranuts/document`** | [github.com/ranuts/document](https://github.com/ranuts/document) — live: [edit.chaxus.com](https://edit.chaxus.com/) | **v0.0.6, published 2026-09-02** | **AGPL-3.0** + AGPL §7 additional terms (**ONLYOFFICE logo must stay**) | ✅ Yes (OnlyOffice engine) | ✅ Yes — genuine Office-grade editing | ✅ Yes, incl. File System Access write-back to the user's own file | **Pure browser** (static build; WASM). Docker image exists but is just a static server | Very active — 1,948★, 212 forks, pushed 2026-09-12 |
| **ONLYOFFICE Docs / DocumentServer** | [github.com/ONLYOFFICE/DocumentServer](https://github.com/ONLYOFFICE/DocumentServer) | **v9.4.0, 2026-05-19** | **AGPL-3.0** + additional terms (attribution, no trademark rights) | ✅ Yes | ✅ Yes — full editors | ✅ Yes | **Requires server** (Docker/Linux/Windows; `.deb` ~700 MB, `.exe` ~1.04 GB) | Very active |
| **LibreOffice / Collabora Online (CODE)** | [collaboraonline/online](https://github.com/CollaboraOnline/online) | not verified this session | MPL-2.0 (Online) + LGPL-3.0 (core) — **see uncertainties** | ✅ Yes | ✅ Yes | ✅ Yes | **Requires server** | Active |
| **`pptx-preview`** | [npmjs.com/package/pptx-preview](https://www.npmjs.com/package/pptx-preview) | **1.0.7, 2025-10-17** | **ISC** | ✅ Yes (preview only) | ❌ No | ❌ No | Pure browser | Low activity, niche |
| **`pptxjs` (npm)** | npm | `0.0.0` — placeholder, 358 bytes | ISC | ❌ (squatted/stub) | ❌ | ❌ | — | **Dead / not a real package** |
| **`onlyoffice-x2t-wasm`** | [github.com/cryptpad/onlyoffice-x2t-wasm](https://github.com/cryptpad/onlyoffice-x2t-wasm) | pushed 2026-04-23 | **No license file** (AGPL-3.0 upstream core; see uncertainties) | ✅ (conversion) | ❌ (converter only) | ✅ (conversion) | Pure browser (WASM) | Maintained; 187★ |

**Commercial / not open source (marked clearly):** Aspose.Slides for JS (paid), Syncfusion PptxEditor (commercial; free Community License only for small orgs), WPS Web Office SDK (commercial), Zoho Office Integrator (commercial), Google Slides API (Google-native slides model — **no direct `.pptx` binary edit**; import/export only), Microsoft Office for the web (commercial). See §1.6.

## 1.2 PPTist — verified in detail

Source: [README.md fetched 2026-09-21](https://raw.githubusercontent.com/pipipi-pikachu/PPTist/master/README.md), [`package.json`](https://raw.githubusercontent.com/pipipi-pikachu/PPTist/master/package.json), [GitHub API repo metadata](https://api.github.com/repos/pipipi-pikachu/PPTist), [commit list](https://api.github.com/repos/pipipi-pikachu/PPTist/commits?per_page=3).

**Correction to the brief:** import is **not** limited/absent.

| Feature line from README | Value |
|---|---|
| Import | **PPTX (overall fidelity ~85%+)**, JSON, `.pptist` files |
| Export | **PPTX (overall fidelity ~95%+)**, JSON, images, PDF (print), `.pptist` |
| Stack | Vue 3.5 + TypeScript + Vite 5 + Pinia; **no UI component library** |
| PPTX import library | `pptxtojson@^2.2.0` |
| PPTX export library | `pptxgenjs@^3.12.0` (pinned to v3 line — **note: current pptxgenjs is v4.0.1**) |
| Rich-text editing inside slides | **ProseMirror** (`prosemirror-*` packages) |
| Charts | ECharts 6 |
| Mobile | "Basic editing" (add/delete/copy pages; insert text/image/rect/circle; move/scale/rotate/layers; limited styling) + basic preview |

**Repo status:** created 2020-12-10; 9,348★ / 1,772 forks / 111 watchers; 22 open issues; `pushed_at` 2026-09-19; most recent commits include `perf: 优化导入效果` ("optimise import quality") on 2026-09-19 and a README update on 2026-08-16. **No GitHub releases and no tags** — `tags` and `releases` endpoints both return `[]`, so "latest version" is effectively `master` (the demo site tracks `master`).

**License — the blocker for this product.** README, verbatim position: "本项目禁止闭源商用" (**closed-source commercial use is prohibited**). Options offered by the author:
1. Comply with **AGPL-3.0** (full copyleft, incl. the network-service clause);
2. Use the **Apache-2.0 version** — but that snapshot is frozen at **May 2022 and is explicitly unmaintained**, so you would be adopting a 4-year-stale codebase;
3. Become a significant contributor (not "violate first, contribute later");
4. **Buy an independent commercial license by email** — **¥2,999 / 1 year**, **¥5,699 / perpetual** (pre-tax). The author states this grants no API/SDK/hosting/support/custom-dev, and there is no pre-built commercial build.

**Author's own positioning (relevant to your use case)** — the README rates:
- "AI PPT Generation Tool": ⭐⭐
- "PPT File Preview Tool": ⭐⭐ (good for review/browsing; **not pixel-perfect**; animations, special charts, deep nesting and advanced styles will differ)
- "Office PPT Authoring Tool": ⭐⭐ — "supports import/export of local PPTX ... **100% fidelity cannot be guaranteed**"
- "Web Slide Editing/Presentation App": ⭐⭐⭐⭐⭐

And explicitly: *"I hope you use PPTist to build a presentation product different from Office PPT, rather than just an editing relay station for Office PPT."*

**Practical implication:** PPTist is a strong *reference implementation* and a plausible **commercial-license purchase** (¥5,699 perpetual is cheap relative to building it) — but it is **Vue**, so plan an iframe + `postMessage` boundary, or budget a React port that you will have to re-do on every upstream fix.

## 1.3 `pptxtojson` — the MIT parser (this is the piece you can safely use)

Source: [README fetched 2026-09-21](https://raw.githubusercontent.com/pipipi-pikachu/pptxtojson/master/README.md), [releases API](https://api.github.com/repos/pipipi-pikachu/pptxtojson/releases?per_page=2).

- **License: MIT** (Copyright © 2020–present pipipi-pikachu). ✅ Usable in closed-source commercial code.
- **Latest: v2.0.1, published 2026-04-15.** Prior tag `1.5.0` / 2025-06-22.
- **Browser-first**: reads `.pptx` directly in the browser from an `ArrayBuffer`; Node.js support is marked "experimental, v1.5.0+".
- **API:** `parse(file, { imageMode: 'base64'|'blob'|'both'|'none', videoMode, audioMode, singleLineSpacingFactor })` → `{ slides[], themeColors[], size{width,height}, usedFonts[] }`.
- **Elements produced:** `text`, `image`, `shape`, `table`, `chart`, `video`, `audio`, `math`, `diagram` (SmartArt), `group` — with position/size, fill (color/image/gradient/pattern), border, shadow, rotation, flips, z-order, text inset, autofit + `fontScale`, hyperlinks, speaker `note`, slide `transition`, and `layoutElements` (master/layout).
- **All numeric lengths are in `pt`.** (0.x used px — a migration gotcha.)
- **Text is emitted as HTML rich text** (inline styles + block styles, bullets, numbered lists).
- **Stated fidelity:** "roughly **80%+** overall in layout and styling"; "for PPTX files manually created and edited from scratch by ordinary users ... **95%+**". Explicitly worse for complex templates, complex masters, deeply nested groups, special shape effects, complex gradients, non-standard shapes, complex SmartArt.
- **Lineage:** describes itself as borrowing heavily from [PPTX2HTML](https://github.com/g21589/PPTX2HTML) and [PPTXjs](https://github.com/meshesha/PPTXjs) — but outputs JSON rather than HTML.

**This is the strategically important artifact in Topic 1:** a permissively-licensed browser parser that gets you ~85% of a real deck into a structured, editable model. `pptxgenjs` (MIT) gets you back out. What is missing is the ~everything-else (§1.7).

## 1.4 `ranuts/document` — serverless OnlyOffice (the interesting 2026 development)

Sources: [GitHub repo API](https://api.github.com/repos/ranuts/document), [readme.md](https://raw.githubusercontent.com/ranuts/document/main/readme.md), [package.json](https://raw.githubusercontent.com/ranuts/document/main/package.json), [NOTICE](https://raw.githubusercontent.com/ranuts/document/main/NOTICE), [releases API](https://api.github.com/repos/ranuts/document/releases?per_page=3).

- **What it is:** the ONLYOFFICE editors (`sdkjs` + `web-apps`) plus the `x2t` conversion engine compiled to WebAssembly, running **entirely in the browser tab**. Nothing is uploaded; installable as a PWA; works offline after first load.
- **Formats:** edit `.docx`, `.xlsx`, `.csv`, `.pptx`; also opens `.doc`, `.odt`, `.rtf`, `.txt`, `.xls`, `.ods`, `.ppt`, `.odp`, PDF (annotate/fill/export).
- **Deployment:** static build (`pnpm build` → `dist/`), Docker image `ghcr.io/ranuts/document:latest`. **No server-side processing.**
- **Embedding:** designed for exactly your use case — iframe + full `postMessage` API (`document:open-url`, `document:opened`, `document:saved`, ...), `readonly=1`, `embed=1`; docs at `docs/embed-api.md`. Also surfaced as an npm component `@ranui/preview` and as WebMCP tools.
- **Save model:** File System Access API write-back into the user's original file where Chromium allows; otherwise download. Autosave snapshots in IndexedDB, auto-deleted after 7 days.
- **Status:** created 2025-06-08; 1,948★ / 212 forks; `pushed_at` 2026-09-12; latest release **v0.0.6 (2026-09-02)** with a very large changelog; heavy E2E suite (Playwright driving the real editor + real WASM).
- **Vendored engine:** ONLYOFFICE `sdkjs`/`web-apps` **9.3.0.133 (build:1)**; converter from [cryptpad/onlyoffice-x2t-wasm](https://github.com/cryptpad/onlyoffice-x2t-wasm).
- **Weight:** NOTICE refers to `x2t.wasm` as a **~42 MB** binary (shipped brotli-compressed because it exceeds host per-file limits), plus a vendored font library and vendored Monaco. This is a real first-load/DX consideration for an in-app right-hand panel — you'd want it lazily loaded/iframed.

**License — same AGPL wall, plus a branding obligation.** AGPL-3.0, with AGPL §7 additional terms inherited from ONLYOFFICE:
- §7(b): **you must retain the original product logo when distributing** — this project shows the ONLYOFFICE logo in the editor header and an About pane, and has tests that fail if someone removes them.
- §7(e): no trademark rights granted.
- The project is a **derivative work of ONLYOFFICE** and states it is not affiliated with or endorsed by Ascensio System SIA.
- AGPL §13 network clause applies as normal.

For a closed-source commercial product this means: **not usable as-is**; it is a proof that serverless Office-grade PPTX editing is technically achievable in 2026, and a good architectural reference (worker-based WASM conversion, postMessage embed contract, File System Access write-back).

## 1.5 ONLYOFFICE Docs — as requested, summarised

Sources: [DocumentServer releases API](https://api.github.com/repos/ONLYOFFICE/DocumentServer/releases?per_page=2), [ONLYOFFICE Docs 9.4 blog post, 2026-05-19](https://www.onlyoffice.com/blog/2026/05/onlyoffice-docs-9-4).

- **Latest: v9.4.0, published 2026-05-19** (previous v9.3.1, 2026-03-03). Artifacts: `onlyoffice-documentserver_amd64.deb` (~700 MB), `x86_64.rpm`, `aarch64.rpm`, `arm64.deb`, `onlyoffice-documentserver.exe` (~1.04 GB).
- **Capability:** it *is* a full office suite — `.docx`, `.xlsx`, **`.pptx`**, forms, PDF; real-time collaborative editing; OOXML round-trip is its core competency. For PPTX editing specifically this is the highest-fidelity open-source option by a wide margin.
- **License (v9.4 "license update"):** **GNU AGPL v3.0, with additional terms that must be included in all copies and distributions** — emphasising attribution, copyright notices, and clear labelling of modified versions; **trademarks are governed by a separate Trademark Policy and are not granted**. ([blog](https://www.onlyoffice.com/blog/2026/05/onlyoffice-docs-9-4))
- **Caveats for a closed-source commercial product:**
  1. **AGPL-3.0 + §7 terms.** Using it as a web service triggers the AGPL source-provision obligation for *your* application. You must either open-source or buy a commercial edition (Enterprise / Developer).
  2. **Requires a server.** Docker/Linux/Windows DocumentServer. This contradicts a "pure browser panel" architecture — unless you adopt the `ranuts/document` WASM approach (§1.4), which then inherits the branding obligation.
  3. **Good news from 9.4:** the **20 simultaneous-connection limit on the open-source Community version has been removed**, code minification removed, consolidated to a single process, and RabbitMQ + database dependencies dropped. So *technically* the Community edition is now far easier to self-host at scale — the constraint is now purely licensing, not the connection cap.
  4. ~700 MB–1 GB install artifacts and the operational surface (conversion service, fonts, caching) are non-trivial for a small team.

## 1.6 LibreOffice / Collabora, and the commercial options

**LibreOffice / Collabora Online (server-side):**
- `LibreOfficeKit` / `Collabora Online (CODE)` renders and edits `.pptx` via LibreOffice **Impress**, in the browser, **server-side** (needs the LibreOffice core running in a container).
- License: `CollaboraOnline/online` is **MPL-2.0**; the LibreOffice core it drives is **LGPL-3.0** — but Collabora also sells commercial editions with support/SLA. ⚠️ **I could not fetch this repo during this session** (both an API and a raw fetch failed), so **treat the CODE license details as unverified for 2026** and re-check before relying on them.
- Generally: fidelity on complex decks is below PowerPoint and below ONLYOFFICE; the operational burden (a full LibreOffice in a container, per-conversion) is the highest of the open-source options. Fits a "render/convert" tier better than an interactive editor tier.
- Nextcloud's `richdocuments` app is the common integration reference.

**Commercial (clearly not open source — no GO for a no-license-fee plan, listed for completeness):**

| Product | Model | Notes |
|---|---|---|
| **Aspose.Slides for JS** | Paid, per-developer + deployment; free trial with watermark/evaluation limits | Strong PPTX fidelity, has JS/Node distributions; genuinely commercial. Pricing pages: [purchase.aspose.com](https://purchase.aspose.com/pricing/). ❌ not open source |
| **Syncfusion PptxEditor (React)** | Commercial; free **Community License** only for companies < $1M revenue and ≤5 devs | React component for viewing/editing PPTX; excellent DX/docs. EULA: [syncfusion license PDF](https://www.syncfusion.com/Content/downloads/syncfusion_license.pdf). ❌ not open source |
| **WPS Web Office SDK** | Commercial | Full browser office suite SDK; enterprise pricing, not published. ❌ not open source |
| **Zoho Office Integrator** | Commercial (per-user/API) | ❌ not open source |
| **Google Slides API** | Free quota + paid; **Google-native slide model** | ⚠️ Important nuance: the Slides API manipulates *Google Slides* presentations, not arbitrary `.pptx` binaries. You can `files.import` a `.pptx` and `files.export` back to `application/vnd.openxmlformats-officedocument.presentationml.presentation`, but there is no in-place PPTX XML editing, and round-trip fidelity is Google's, not yours. Requires Google auth + network. ❌ not open source |
| **Microsoft Office for the web / Office.js** | Commercial / M365 licensing | The gold standard for PPTX fidelity; not a library you can embed in a third-party closed-source app without M365 terms. ❌ not open source |

**Also checked and rejected (as the brief anticipated):**
- **`reveal.js`** — presentation *playback* framework (HTML/JS slides). No PPTX parsing, no editing. ❌
- **`WebODF`** — ODF (`.odt`/`.odp`) viewer/editor, not OOXML/`.pptx`. ❌
- **`pptxjs` on npm** — version `0.0.0`, a 358-byte stub pointing at `github.com/hom/docxjs`. **Not a usable package.** (The real historical `PPTXjs` by meshesha is a jQuery-based *renderer*, not an editor — `pptxtojson` credits it as prior art.)
- **`@jvmr/pptxgenjs`** — does not exist on npm (404).
- **`pptx-preview`** (ISC, 1.0.7) — pure-front-end PPTX **preview** only; no edit, no export.

## 1.7 Feasibility & effort: build a canvas PPTX editor from scratch on `pptxgenjs`

**The brief's framing needs one correction.** You do **not** need to write a parser — `pptxtojson` (MIT, v2.0.1, 2026-04-15) already parses PPTX in the browser to a structured JSON model with ~80–95% fidelity. So the "no pptx parser to load existing decks" gap is *closed* for free. What remains is everything the parser deliberately does not do.

**What you get for free (MIT):**
- Read path: `pptxtojson` → slides, elements, positions (pt), fills, borders, shadows, rotation, groups, tables, charts, SmartArt, notes, transitions, theme colors.
- Write path: `pptxgenjs` v4.0.1 → text/table/shape/image/chart/media slides, masters/layouts, export to `.pptx` blob.

**What is missing and must be built (the actual cost):**
1. **Model + round-trip mapping layer.** PPTist's model ≠ pptxtojson's output ≠ pptxgenjs's input. You write and maintain three-way converters, and reconcile units (pt), rotation/flip semantics, autofit/`fontScale`, text insets, and z-order.
2. **Canvas renderer with real fidelity.** `pptxgenjs` does not render anything to the screen. You render the model — including gradients, pattern fills, image crops (`geom`/`rect`), shape paths + `keypoints` (adjustment handles), SVG path viewBox, text autofit and vertical text, and formulas. Roughly comparable to writing a mini-Figma.
3. **Interaction layer.** Selection (marquee + point), drag/resize/rotate handles, snap guides and magnetic alignment, multi-select, grouping, z-order, locking, copy/paste (internal + external image paste), context menus, keyboard shortcuts, **undo/redo across all of it**.
4. **Rich-text editing inside shapes.** Rich text in a rotated, scaled, clipped box is genuinely hard. PPTist solves this with **ProseMirror**; you would too — another large surface (fonts, lists, indentation, line/paragraph spacing, sub/superscript, inline code, vertical text).
5. **Charts, tables, media, SmartArt, formulas.** Chart editing (data + type conversion + theming), table row/column ops + merges, audio/video embeds, LaTeX formulas, SmartArt. Each is a mini-project.
6. **Masters/layouts/themes.** Importing and *meaningfully* preserving master/layout inheritance — the single biggest source of fidelity loss in every project in this space.
7. **Export fidelity engineering.** PPTX "needs repair" dialogs, hyperlink/table auto-paging bugs, SVG-in-master issues are a permanent whack-a-mole (visible in pptxgenjs's own 4.0.x changelogs).
8. **Mobile/touch, accessibility, theming, i18n, performance for large decks.**

**Rough effort estimates (engineering judgement, NOT measured data — flag as such):**

| Scope | Developer-days | Notes |
|---|---|---|
| Read-only canvas viewer (parse + render + pan/zoom + thumbnails) | **30–60** | `pptxtojson` + custom renderer; this is the realistic first milestone |
| Viewer + basic element editing (select/move/resize/rotate/text edit/undo) | **120–200** | + interaction layer + ProseMirror text engine |
| Above + shapes, images, tables, charts, z-order, grouping, snapping, notes | **300–500** | |
| Above + masters/layouts/themes, media, formulas, SmartArt, ~95% export round-trip, mobile | **600–1,000+** | i.e. *years of one developer*, which is exactly the shape of PPTist (started 2020-12, still fixing import fidelity 2026-09) |

**Recommendation for this product:** do **not** build from scratch. The buy-vs-build is lopsided — PPTist plus a ¥5,699 perpetual commercial license is roughly *two weeks* of one developer's loaded cost, for a codebase with 5.7 years of accumulated PPTX edge-case handling. Then choose deliberately between:
- **(A) Iframe PPTist + commercial license** — fastest, Vue/Rect boundary via `postMessage`, and you own the fidelity ceiling of PPTist's ~85/95%.
- **(B) Iframe `ranuts/document`** — best fidelity and true serverless, but you cannot keep the product closed-source under AGPL (§1.4).
- **(C) Self-host ONLYOFFICE Docs (paid commercial edition)** — best fidelity and a real support contract; server dependency + cost. Community edition is now connection-unlimited but still AGPL.
- **(D) If the panel only needs *generation* from your AI's own output (no editing of user-uploaded decks)** — `pptxgenjs` (MIT) + a read-only preview (`pptx-preview` ISC) is fully permissive, small, and cheap. **This may actually match an AI-chat product's real need better than an editor does; worth challenging the requirement.**

---

# TOPIC 2 — HTML editor for a narrow right-hand panel

## 2.1 CodeMirror 6 — recommended

Sources: npm registry metadata (`@codemirror/state` `latest` = **6.7.5**; `@codemirror/state` history shows active releases through **6.5.4, 2025-12-31** and later), [codemirror.net/docs/ref](https://codemirror.net/docs/ref/), [Bundlephobia](https://bundlephobia.com/package/codemirror).

| Item | Value |
|---|---|
| Current version | Core `@codemirror/state` **6.7.5**; the `codemirror` meta-package **6.0.2**; ecosystem packages release continuously |
| License | **MIT** (per npm metadata for every `@codemirror/*` package; author Marijn Haverbeke) |
| Architecture | Modular, **~6 core packages**: `@codemirror/state`, `@codemirror/view`, `@codemirror/language`, `@codemirror/commands`, `@codemirror/search`, `@codemirror/autocomplete`, `@codemirror/lint`, plus Lezer grammars (`@codemirror/lang-html` brings CSS + JS) |
| Bundle size — `codemirror` meta (basic setup) | **373,186 B raw / ~118,750 B gzip** (Bundlephobia, `codemirror@6.0.2`) |
| Bundle size — realistic HTML/CSS/JS panel | **~140–175 kB gzip** ⚠️ *estimate* = basic setup + `lang-html` + `lang-css` + `lang-javascript` + Lezer parsers. Not independently measured in this session. |
| Trimmable | Yes, materially: dropping `@codemirror/autocomplete` (~75.9 kB raw), `@codemirror/lint` (~31.6 kB) and `@codemirror/search` (~41.3 kB) removes a large share of the meta-package's weight if you hand-pick extensions instead of using `basicSetup` |
| React integration | **`@uiw/react-codemirror` 4.25.11**, **MIT**, `@babel/runtime` + `codemirror@^6` + `@codemirror/state` + `@codemirror/commands` + `@codemirror/theme-one-dark`; peer `react >=17` / `react-dom >=17` ✅ (React 19 satisfies it); Bundlephobia: **151,015 B raw / ~48,779 B gzip** for the wrapper alone (small — the editor core is the cost) |
| Narrow panel | **Good fit.** Single-column text flow, no fixed minimum width, gutter/line numbers can be disabled, `EditorView.lineWrapping` handles long lines, CSS-driven so a 200–280 px pane is fine |
| Mobile / touch | Uses native DOM selection + `contenteditable`, so it inherits the platform's touch behaviour; there is a dedicated `drawSelection` extension for custom selection rendering. Community reports the usual `contenteditable` mobile rough edges (soft-keyboard/IME quirks) — ⚠️ see uncertainties. For a desktop-first AI product's side panel this is a non-issue |

Verdict: **default choice.** MIT, small, no workers, no Vite pain, degrades gracefully in a narrow pane.

## 2.2 Monaco Editor — not recommended for this panel

Sources: npm registry (`monaco-editor` `dist-tags.latest` = **0.56.0**; `next` = `0.56.0-dev-20260625`), [README.md fetched 2026-09-21](https://raw.githubusercontent.com/microsoft/monaco-editor/main/README.md), [Bundlephobia](https://bundlephobia.com/package/monaco-editor), npm metadata for `@monaco-editor/react`.

| Item | Value |
|---|---|
| Current version | **0.56.0** (plus a `0.56.0-dev-20260625` `next` tag) |
| License | **MIT** |
| Bundle size (shipped `dist`) | The published package's **asset list is enormous**: bundled JS chunks of **6,612,405 B** (gzip ~1,397,289 B) and **4,045,954 B** (gzip ~1,032,841 B), plus ~90 more chunks; CSS ~167 kB raw / 25 kB gzip; `codicon.ttf` ~141 kB. **i.e. multiple megabytes** even before your own code, with lazy-loaded language workers on top |
| React integration | **`@monaco-editor/react` 4.7.0**, MIT, depends on `@monaco-editor/loader@^1.5.0`; peers `react` `^16.8 \|\| ^17 \|\| ^18 \|\| ^19` ✅ React 19 OK; `monaco-editor >=0.25.0 <1` |
| Vite bundling pain | **Real.** Language services run in **Web Workers**; with Vite you must wire `MonacoEnvironment.getWorker` / `?worker` imports or use a plugin (`vite-plugin-monaco-editor`, `@dvaji/vite-plugin-monaco-editor`, `vite-plugin-monaco-editor-esm`). The README's own FAQ covers "Could not create web worker". The AMD build is **deprecated** |
| Narrow panel | **Poor.** Monaco is built with fixed editor chrome (minimap, overview ruler, fixed-size glyph margins, fixed-width decorations). You can hide the minimap, but the layout is desktop-IDE-shaped and doesn't reflow like a simple text area |
| Mobile / touch | **Not supported — vendor-confirmed.** Monaco README FAQ: *"Is the editor supported in mobile browsers or mobile web app frameworks? **No.**"* ([README](https://raw.githubusercontent.com/microsoft/monaco-editor/main/README.md)); longstanding tracking issue [microsoft/monaco-editor#246 "Any Plan for Mobile"](https://github.com/microsoft/monaco-editor/issues/246) remains open |
| Ecosystem note | ⚠️ Monaco is what VS Code uses, but **VS Code extensions do not work** in Monaco (README FAQ). Also note: the `ranuts/document` vendored ONLYOFFICE build *ships Monaco* (per its NOTICE) — evidence of how heavyweight an embedding gets |

Verdict: only justified if you need TypeScript-grade IntelliSense in a full-width IDE surface. For a **narrow, touch-capable side panel** it is the wrong tool on size, width and mobile grounds.

## 2.3 Other editor options

| Option | License | Version/size | Verdict for a narrow HTML source panel |
|---|---|---|---|
| **Ace** (`ace-builds`) | **BSD-3-Clause** | **1.44.0**; ~55 MB unpacked (all modes/themes; you ship only what you use) | Mature, still maintained, small runtime for a handful of modes. Historically weaker touch handling and an older architecture than CM6. Reasonable fallback |
| **Prism.js + a textarea overlay** (e.g. CodeJar-style) | **MIT** | Prism core + a few grammars is **tens of kB**; CodeJar is ~2 kB | **Smallest possible.** No real editing features (no undo stack quality, no bracket matching). Good if this panel is genuinely secondary |
| **Sandpack** (CodeSandbox) | Apache-2.0 (verify) | Large — it's a bundler + preview runtime | Overkill: solves "run code", not "edit a string". ❌ |
| **StackBlitz WebContainers** | Commercial (proprietary; free tier + paid) | WASM Node runtime | ❌ Massive, commercial, network-dependent, fundamentally not a text editor |
| **Comparison reading** | — | [pkgpulse: Monaco vs CodeMirror 6 vs Sandpack 2026](https://www.pkgpulse.com/guides/monaco-editor-vs-codemirror-6-vs-sandpack-in-browser-2026), [npm-compare: @monaco-editor/react vs react-ace vs react-codemirror](https://npm-compare.com/@monaco-editor/react,react-ace,react-codemirror) | Community sources; the size/mobile conclusions above are corroborated by the vendor data I fetched directly |

## 2.4 ⚠️ TinyMCE and CKEditor 5 — licence verification (explicitly requested)

### TinyMCE — **GPLv2+, and self-hosted commercial use is actively blocked**

Sources: [TinyMCE 8 license-key docs](https://www.tiny.cloud/docs/tinymce/latest/license-key/) and [TinyMCE 7 license-key docs](https://www.tiny.cloud/docs/tinymce/7/license-key/), both fetched 2026-09-21; npm metadata (`tinymce` latest = **8.9.1**, `license: "SEE LICENSE IN license.md"`).

- **Current licence: GNU General Public License Version 2 or later (GPLv2+)** — not MIT, not LGPL. TinyMCE 7's docs state this directly; it did not revert.
- **The enforcement changed in v7 and hardened in v8.** The docs, verbatim:
  - v7: *"A new configuration option called `'license_key'` requires developers to make a conscious decision to use TinyMCE with the GPLv2+ license or with a commercial license."* Self-hosted without a valid key → **console log warning only**.
  - **v8 (current):** *"When using TinyMCE 8 in a self-hosted environment, a license key must be provided and it must be valid. Otherwise, **the editor will be disabled**. ... In addition, when using TinyMCE 8 in a self-hosted environment **for commercial use, a commercial license key manager addon is required in order for the editor to operate**."*
- Key formats: `license_key: 'gpl'` (GPL intent), `T8LK:...` (commercial self-hosted), `GPL+T8LK:...` (GPL project needing premium features). Loading from Tiny Cloud requires an API key instead — and is already under Tiny's commercial licence.
- The `licensekeymanager` addon loads automatically when required and need not be listed in `plugins`.
- **Consequence for you:** using TinyMCE 8 self-hosted in your closed-source commercial app means the editor **will not function** without a paid commercial licence key. Your only GPL route is to license *your entire application* under GPLv2+ — which is incompatible with closed-source distribution. **TinyMCE is not viable here without a purchase.**

### CKEditor 5 — **dual-licensed GPL2+ / commercial** (no LGPL)

Sources: [`packages/ckeditor5/LICENSE.md` fetched 2026-09-21](https://raw.githubusercontent.com/ckeditor/ckeditor5/master/packages/ckeditor5/LICENSE.md); [CKEditor licensing options](https://ckeditor.com/legal/ckeditor-licensing-options/) (page returned only its title via fetch — the LICENSE.md text below is the authoritative artifact); npm metadata (`ckeditor5` latest = **48.5.1**, `license: "SEE LICENSE IN LICENSE.md"`).

Verbatim from LICENSE.md:

> **CKEditor 5** ... Copyright (c) 2003–2026, CKSource Holding sp. z o.o. All rights reserved.
> Licensed under a dual-license model, this software is available under:
> * the **GNU General Public License Version 2 or later** (see COPYING.GPL),
> * or **commercial license terms from CKSource Holding sp. z o.o.**
> ... If you are using CKEditor under commercial terms, you are free to remove the COPYING.GPL file with the full copy of a GPL license.

- Note this is **GPLv2+, not LGPL** — a change from CKEditor 4's LGPL era. For a closed-source product the copyleft reaches your application, so **you need the commercial licence**.
- Also relevant: `ckeditor5@48.5.1` unpacks to **~42.5 MB** — the most heavyweight WYSIWYG on this list, and much of the modern feature set (collaboration, revision history, export, AI, import/export, track changes) is **commercial-only or paid add-on**.
- ⚠️ The CKSource support-KB article ([support.ckeditor.com/hc/en-us/articles/115002452529](https://support.ckeditor.com/hc/en-us/articles/115002452529-Under-which-licenses-does-CKEditor-come)) returned **HTTP 403 (Cloudflare challenge)** to this fetch; the conclusion above rests on the in-repo LICENSE.md, which is authoritative.

**Bottom line for both:** for a closed-source commercial product, **TinyMCE 8 requires a commercial key to even boot, and CKEditor 5 requires a commercial licence to avoid GPLv2+ copyleft.** Neither is a free option in 2026.

## 2.5 Mature open-source WYSIWYG editors for "edit and preview" (2026)

Sources: npm registry `latest` metadata for each (fetched 2026-09-21), Bundlephobia for sizes, GitHub commits API for maintenance recency.

| Editor | License | Version (2026-09-21) | Packaged size (raw / gzip, Bundlephobia) | Arbitrary-HTML round-trip fidelity | Maintenance | Verdict for your panel |
|---|---|---|---|---|---|---|
| **Tiptap 3** (`@tiptap/core`) | **MIT** | **3.31.3** (v2 line still at 2.27.3) | core **113,934 B / 34,819 B**; **StarterKit 337,362 B / 105,469 B** (all deps) | ⚠️ **Schema-bound.** Built on ProseMirror: HTML is parsed through your node/mark schema on input and serialized back on output. Anything outside the schema is dropped or normalised — so **not** a faithful round-trip for arbitrary HTML | **Very active** (commits 2026-09-17) | **Best MIT choice** if you want a *structured* rich-text editor. Excellent React 19 support, headless (you own the UI — good for a narrow pane) |
| **Lexical** (Meta) | **MIT** | **0.51.0** (`nightly` 0.51.1-nightly.20260918.0) | core **195,527 B / 62,158 B**; `@lexical/html` adds HTML import/export | ⚠️ **Schema-bound**, same caveat as Tiptap. `@lexical/html` gives `$generateHtmlFromNodes` / `$generateNodesFromDOM` — good within its node set, lossy outside it | Active (Meta-maintained; nightly builds) | Strong alternative to Tiptap; more framework-ish, more work to assemble |
| **ProseMirror** | **MIT** | `prosemirror-view` **1.42.4** | `prosemirror-view` 903,885 B unpacked; full toolkit comparable to Tiptap's dependency total | ⚠️ **Schema-bound** (it *is* the schema engine Tiptap uses). `DOMParser`/`DOMSerializer` round-trip within your schema | Active (upstream monorepo last touched 2026-04-01; individual packages release continuously) | Lowest-level, most control, most work. Choose only if Tiptap's API is in your way |
| **Quill** | **BSD-3-Clause** | **2.0.3** (released 2024-11-30; **last commit 2025-07-25**) | **197,224 B / 57,468 B** | ⚠️ Delta-based model; HTML in/out is a conversion, lossy for anything outside its formats | ⚠️ **Slowing** — no commits in ~14 months at time of writing | Simple and small, but the maintenance signal is weaker and fidelity is limited |
| **GrapesJS** | **BSD-3-Clause** | **0.23.6** (0.23.6 released 2026-08-25) | **1,103,086 B / 286,959 B** — by far the largest | ✅ **Best arbitrary-HTML fidelity of the WYSIWYG options** — it's a *page builder*: it loads real HTML/CSS, edits it visually, and emits HTML/CSS back. It also bundles CodeMirror 5 for source view | Active (2026-08-25) | ✅ Strong pick **if the goal is "edit real HTML visually"**. ❌ Poor pick for a *narrow* pane — it wants canvas + side panels + a wide layout |
| **Slate** | **MIT** | **0.126.2** | (not measured; framework only) | ⚠️ No HTML model at all — you define the document model and write your own serializer/deserializer | Active (2026-09-13, release automation) | Maximum flexibility, maximum work. Low-level framework, not a product |
| **Froala** | **Commercial** (npm `license` field literally = `https://www.froala.com/wysiwyg-editor/pricing`) | 5.4.0 | 11,717,968 B unpacked | Good | Commercial | ❌ Paid; note it's the product Tiny ships a migration guide *from* |
| **TinyMCE** | **GPLv2+ / commercial** | 8.9.1 | 12,048,012 B unpacked | Good | Very active | ❌ See §2.4 |
| **CKEditor 5** | **GPL2+ / commercial** | 48.5.1 | 42,479,305 B unpacked | Good (traditional contenteditable) | Very active | ❌ See §2.4 |

**The critical distinction for "round-trip arbitrary HTML with reasonable fidelity":** every *structured/schema-based* editor (Tiptap, Lexical, ProseMirror, Slate, Quill) is **lossy by design** for HTML outside its schema. If your requirement is truly "load this HTML file, let the user edit it, save back something recognisably the same", the only honest options are:
1. **A code editor (CodeMirror 6) + a live preview iframe** — 100% fidelity to the source text, zero HTML mutation, and exactly what a "raw HTML source" panel should do. **This is what I'd recommend for the stated requirement, and it is also the smallest and cheapest option.**
2. **GrapesJS** — if the user must edit *visually*, not as source, and the HTML is real-world; accepts the size and width costs.
3. **Native `contenteditable` with a strict no-normalisation policy** — maximum fidelity, but you re-implement undo, sanitisation and selection handling.

⚠️ **Sanitisation note (security, not licensing):** every WYSIWYG above that ingests arbitrary HTML feeds it into a live DOM. In an AI-chat product where HTML may be model-generated or user-supplied, you must sanitise (e.g. DOMPurify) before rendering **and** decide whether `<script>`/`<iframe>`/event-handler attributes are preserved on save. A source-text CodeMirror panel sidesteps this entirely for the edit path, since nothing is executed until you choose to preview it — and even then you should preview in a sandboxed iframe.

## 2.6 Recommendation for the HTML panel

| If the requirement is… | Use |
|---|---|
| Edit **raw HTML source** (the literal words in the brief) | **CodeMirror 6** + `@uiw/react-codemirror`, `@codemirror/lang-html`; lazy-load the editor chunk; render a **sandboxed iframe** preview beside/below it |
| A **visual** rich-text editor whose output is HTML, on permissive terms | **Tiptap 3** (MIT, headless, React 19-ready) — accept schema-bound fidelity, or **Lexical** (MIT) |
| Edit **real-world page HTML visually** | **GrapesJS** (BSD-3-Clause) — but it needs width, not a narrow rail |
| Never touch `pptx` licensing risk / never pay an editor licence | CodeMirror 6 or Tiptap — both MIT |

---

# Uncertain / could not verify

1. **Collabora Online (CODE) licensing for 2026.** `github.com/CollaboraOnline/online` failed to fetch (two attempts: API and HTML). The commonly cited split — **MPL-2.0** for `CollaboraOnline/online`, **LGPL-3.0** for the LibreOffice core, plus paid Collabora editions — is **from prior knowledge, not verified this session.** Re-verify before planning on it. Same for the exact newest CODE release.
2. **CodeMirror 6 realistic bundle size (the ~140–175 kB gzip figure) is an estimate.** Only the `codemirror@6.0.2` meta-package number (**118,750 B gzip**) is measured (Bundlephobia). The HTML/CSS/JS-language figure was not independently built and measured, and depends heavily on whether you use `basicSetup` or hand-pick extensions. **Measure it in your own Vite build before committing to a performance budget.**
3. **CodeMirror 6 mobile/touch.** I could **not** find an authoritative vendor statement on mobile support (unlike Monaco, which explicitly says "No"). Evidence is indirect: it relies on native DOM selection/`contenteditable` and ships a `drawSelection` extension; community reports show the usual `contenteditable` soft-keyboard/IME issues (e.g. [QwenLM/qwen-code#5958](https://github.com/QwenLM/qwen-code/issues/5958)). Treat "works acceptably on mobile" as **plausible but unverified**.
4. **`onlyoffice-x2t-wasm` has no LICENSE file** in its GitHub metadata (`license: null`). Its upstream (`ONLYOFFICE/core`) is AGPL-3.0, so AGPL is the safe assumption — but **the WASM converter's licensing as distributed by cryptpad is not stated** and should be confirmed before any use.
5. **PPTist's claimed fidelity numbers (85% import / 95% export)** are the **author's own README claims**, not independently measured. The author explicitly warns animations, special charts, deep nesting, non-standard elements and advanced styles will differ, and that 100% fidelity cannot be guaranteed. Test with *your* real sample decks.
6. **PPTist has no tagged releases and no GitHub releases** (both API endpoints return `[]`); `package.json` says `2.0.0`. Any "version" you cite for PPTist is really "master as of the commit date (2026-09-19)". Its demo site is not a supported service, per its own README.
7. **PPTist pins `pptxgenjs@^3.12.0` while pptxgenjs's current release is v4.0.1.** I did not verify whether PPTist has been updated to v4 or whether v3 is retained deliberately (v4 changed Node/browser detection and Vite/Web Worker behaviour). Check before assuming v4 compatibility.
8. **Syncfusion / Aspose / WPS / Zoho / Google Slides current 2026 pricing** could not be read (pricing pages returned empty content or PDFs). Only the *licence model* (commercial, not open source) is asserted; **do not quote specific prices**.
9. **Google Slides API "no direct PPTX editing"** is asserted from the API's known Google-native slide model; I did not fetch the current API reference this session to re-confirm the import/export MIME behaviour in 2026.
10. **TinyMCE pricing tiers** — `tiny.cloud/pricing/` returned empty content to fetch. The *mechanism* (self-hosted commercial use is hard-blocked in v8 without a commercial key) is verified from Tiny's own docs; the **cost** is not.
11. **The developer-day estimates in §1.7 are engineering judgement, not measurements.** They are anchored on PPTist's observed timeline (started 2020-12, still fixing import fidelity in 2026-09) but should be treated as an order-of-magnitude planning frame, not a quote.
12. **`pptx-preview` (ISC) and the historical `PPTXjs` renderer** were verified only at the package-metadata level; I did not evaluate their rendering fidelity firsthand.

---

## Source index

**PPTX:**
- PPTist — https://github.com/pipipi-pikachu/PPTist · README https://raw.githubusercontent.com/pipipi-pikachu/PPTist/master/README.md · package.json https://raw.githubusercontent.com/pipipi-pikachu/PPTist/master/package.json · repo API https://api.github.com/repos/pipipi-pikachu/PPTist · commits https://api.github.com/repos/pipipi-pikachu/PPTist/commits?per_page=3
- pptxtojson — https://github.com/pipipi-pikachu/pptxtojson · README https://raw.githubusercontent.com/pipipi-pikachu/pptxtojson/master/README.md · releases https://api.github.com/repos/pipipi-pikachu/pptxtojson/releases?per_page=2
- pptxgenjs — https://github.com/gitbrent/PptxGenJS · releases https://api.github.com/repos/gitbrent/PptxGenJS/releases?per_page=3 · npm https://registry.npmjs.org/pptxgenjs/latest
- ranuts/document — https://github.com/ranuts/document · live https://edit.chaxus.com/ · readme https://raw.githubusercontent.com/ranuts/document/main/readme.md · NOTICE https://raw.githubusercontent.com/ranuts/document/main/NOTICE · releases https://api.github.com/repos/ranuts/document/releases?per_page=3
- onlyoffice-x2t-wasm — https://github.com/cryptpad/onlyoffice-x2t-wasm
- ONLYOFFICE DocumentServer — https://github.com/ONLYOFFICE/DocumentServer · releases https://api.github.com/repos/ONLYOFFICE/DocumentServer/releases?per_page=2 · Docs 9.4 blog https://www.onlyoffice.com/blog/2026/05/onlyoffice-docs-9-4
- Collabora Online — https://github.com/CollaboraOnline/online (**fetch failed — unverified**)
- pptx-preview — https://www.npmjs.com/package/pptx-preview · pptxjs stub https://www.npmjs.com/package/pptxjs
- Commercial: [Syncfusion licensing PDF](https://www.syncfusion.com/Content/downloads/syncfusion_license.pdf) · [Syncfusion React licensing](https://ej2.syncfusion.com/react/documentation/licensing/overview) · [Aspose pricing](https://purchase.aspose.com/pricing/) · [Aspose.Slides Cloud pricing](https://docs.aspose.cloud/slides/pricing-plan/)

**HTML editors:**
- CodeMirror reference https://codemirror.net/docs/ref/ · `@uiw/react-codemirror` https://registry.npmjs.org/@uiw/react-codemirror/latest · Bundlephobia codemirror https://bundlephobia.com/package/codemirror · @uiw/react-codemirror https://bundlephobia.com/package/@uiw/react-codemirror
- Monaco README https://raw.githubusercontent.com/microsoft/monaco-editor/main/README.md · `@monaco-editor/react` https://registry.npmjs.org/@monaco-editor/react/latest · Bundlephobia https://bundlephobia.com/package/monaco-editor · mobile issue https://github.com/microsoft/monaco-editor/issues/246
- TinyMCE licence (v8) https://www.tiny.cloud/docs/tinymce/latest/license-key/ · (v7) https://www.tiny.cloud/docs/tinymce/7/license-key/ · https://www.tiny.cloud/get-tiny/
- CKEditor 5 LICENSE.md https://raw.githubusercontent.com/ckeditor/ckeditor5/master/packages/ckeditor5/LICENSE.md · licensing options https://ckeditor.com/legal/ckeditor-licensing-options/ · docs https://ckeditor.com/docs/ckeditor5/latest/getting-started/licensing/license-and-legal.html
- Tiptap https://registry.npmjs.org/@tiptap/core/latest · https://tiptap.dev/docs/editor/api/utilities/html · Lexical https://registry.npmjs.org/lexical/latest · https://lexical.dev/docs/concepts/serialization · ProseMirror https://registry.npmjs.org/prosemirror-view/latest · Quill https://registry.npmjs.org/quill/latest · GrapesJS https://registry.npmjs.org/grapesjs/latest · Slate https://registry.npmjs.org/slate/latest · Froala https://registry.npmjs.org/froala-editor/latest · Ace https://registry.npmjs.org/ace-builds/latest
- Comparisons: https://www.pkgpulse.com/guides/monaco-editor-vs-codemirror-6-vs-sandpack-in-browser-2026 · https://npm-compare.com/@monaco-editor/react,react-ace,react-codemirror
