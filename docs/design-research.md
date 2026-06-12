# Design Research — Aurelia

> Multi-source design intelligence guiding the visual & interaction language of Aurelia, an editorial-feeling AI conversation product.

---

## 1. Brief

We're building **Aurelia**, a high-end AI conversation web app. The brand reads like an editorial magazine that happens to think — warm paper-toned light surfaces, deep graphite dark surfaces, restrained accents, serif voice on display moments, sans body for the workhorse. The product borrows the calm of Claude, the IA of ChatGPT, and the lightness of Gemini, while remaining unambiguously its own thing.

This document captures what was studied, what we will borrow, what we will deliberately avoid, and how we translate those decisions into Aurelia's system.

---

## 2. References studied

| Source | Why it matters | What we examined |
|---|---|---|
| **Claude (Anthropic)** | Closest visual cousin of our target — warm, editorial, restrained | Marketing site, product chrome, message rhythm, composer card, dark mode treatment |
| **ChatGPT (OpenAI)** | Most refined information architecture in the category | Sidebar grouping (Today / Yesterday / Previous N days), model picker placement, settings shell, branch pager, share flow |
| **Gemini (Google)** | Multimodal IA + airy whitespace + subtle motion | Composer multimodal entry points, gem badges, streaming feedback, suggestion chips |
| **assistant-ui (Yonom)** | Reference for primitives architecture | Thread/Composer/Message primitives, runtime provider model, tool UI registration |
| **shadcn/ui + Radix + Tailwind v4** | Implementation substrate | Token mapping via `@theme`, OKLCH support, Radix accessibility, cmdk command palette |
| **Premium AI case studies** | Outside the big three for fresh thinking | Perplexity, Linear, Vercel v0, Raycast AI, Granola, Mistral Le Chat, Cursor |

> ⚠️ **Network-research caveat.** This research was conducted entirely from internal knowledge — see `docs/assumptions.md`. All findings are directional and should be verified visually against the live products before final design lock if accuracy of any specific value matters.

---

## 3. Key observations per source

### 3.1 Claude

- Reads as *editorial software*, not "AI chatbot". The warm paper palette and serif headlines deliberately invoke books and longform writing.
- Visually aggressive *restraint*: one brand color, near-monochrome chrome, hairline borders, almost no shadows. Hierarchy is carried by type and whitespace.
- Composer is the visual center of gravity — a self-contained rounded card with internal toolbar (attachments, voice, model picker, send).
- Sidebar uses borders over shadows for elevation. Conversation grouping is relative-time: Today / Yesterday / Previous 7 / Previous 30.
- Dark mode is *warm* dark (graphite, not blue-black) — a rare and signature choice.
- Code blocks render with a subtle dark surface even in light mode, giving code visual ownership without harsh contrast.

### 3.2 ChatGPT

- Most evolved IA in the space. Sidebar groups by relative time bucket and supports archive / share / rename / delete / star on hover.
- Model picker lives top-left of the composer area as a textual button rather than a dropdown — feels like switching voices, not selecting a setting.
- Empty state is a single large centered prompt + a small suggestion-chip strip — calmer than the busy "what can I help with?" gallery many products use.
- Inline message actions appear on hover only — copy, regenerate, edit (user), like/dislike (assistant), read-aloud, share.
- Branch pager (`◀ 2/3 ▶`) is a tertiary control after editing a user turn — the right primitive for reasoned exploration.

### 3.3 Gemini

- Airier than Claude — more whitespace, slightly larger leading, softer surfaces.
- Multimodal entry points are first-class in the composer: image, document, deep research, canvas-style outputs.
- Subtle micro-animations: streaming shimmer for thinking states, soft hover swells on suggestion cards.
- "Gem" badges (small pills near the workspace avatar) for tier signaling.
- Settings stays inside a modal-feeling overlay rather than a separate route — preserves context.

### 3.4 assistant-ui

- Composable primitives: `ThreadPrimitive.Root`, `ComposerPrimitive`, `MessagePrimitive`, `BranchPickerPrimitive`, tool-UI registration via `makeAssistantToolUI`.
- Runtime layer is pluggable — works with Vercel AI SDK, custom backends, mocks.
- Ships *unstyled*; visual layer is entirely on us. This matches Aurelia's goal of total visual ownership.

### 3.5 shadcn/ui + Radix + Tailwind v4

- shadcn gives us *owned* component source — we copy it in, then deeply retheme. Default `border-input`, `rounded-md`, `bg-background` are renamed to Aurelia tokens.
- Radix primitives ship correct accessibility for Dialog, DropdownMenu, Tooltip, Tabs, Popover, etc.
- Tailwind v4's `@theme inline` directive lets us declare design tokens once in CSS and have them flow through utility classes.
- `cmdk` is the canonical command palette engine.

### 3.6 Case studies (premium AI sites)

- **Perplexity** — citation chips as first-class UI primitive, expandable source cards on hover. Single saturated teal accent.
- **Linear** — concentric-radius discipline, scroll-pinned product demos, sub-200ms easing curves.
- **Vercel v0** — product *is* the hero: composer on the homepage, no separate "Try it" CTA.
- **Raycast AI** — per-feature accent gradients, command-K as a marketing surface.
- **Granola** — paper-white background, hand-drawn-feeling spot illustrations, slow editorial motion.
- **Mistral Le Chat** — refused chat bubble metaphor, headline serif paired with mono.

---

## 4. Translation rules — what we borrow & how

| We borrow | From | How we translate |
|---|---|---|
| Warm paper background + warm ink text | Claude | Aurelia palette: bg `oklch(98% 0.006 80)`, fg `oklch(22% 0.012 60)` — derived siblings, not lifted hex |
| Single saturated brand accent, reserved for state | Claude / Perplexity / Linear | Aurelia clay `oklch(62% 0.14 38)` — sibling of terracotta family, distinct from Claude's exact hue |
| Editorial display serif + humanist sans body | Claude / Perplexity / Granola | Source Serif 4 (Google Fonts, OFL) for headlines + Inter for body + JetBrains Mono for code |
| Sidebar grouped by relative time | ChatGPT | We use "Today / Yesterday / Previous 7 days / Previous 30 days / Older" — same logic, different copy |
| Model picker inside composer | Claude / ChatGPT | Anchored button with Popover capability blurbs |
| Empty state with serif greeting + suggestion chips | ChatGPT / Gemini | Centered greeting in serif + 4 suggestion cards (lift on hover) |
| Composable primitives architecture | assistant-ui | We mirror the Thread / Message / Composer split even though we ship a mock runtime |
| Citation chips as a UI primitive | Perplexity | `CitationChip` (superscript chip → HoverCard with title / domain / favicon) |
| Concentric radius rule | Linear | Inner radius = outer radius − padding; codified in tokens |
| Sub-200ms easing, single curve | Linear / Raycast | One easing token `cubic-bezier(0.2, 0.8, 0.2, 1)` for 90% of transitions |
| Borders over shadows for elevation | Claude | Hairline border tokens; shadows reserved for popovers/dialogs only |
| Streaming caret over spinner | Claude / ChatGPT | 2px clay caret blinks at 1Hz at stream tail |

---

## 5. Anti-patterns — what we deliberately refuse

- ❌ **Pure white / pure black.** We never use `#FFF` or `#000`. Even our most neutral surfaces have a warm cast.
- ❌ **Glassmorphism / heavy blur.** Peaked in 2022. Use solid surfaces with hairline borders.
- ❌ **Floating 3D blob gradients.** Stripe-style hero blobs read as derivative in 2026.
- ❌ **Neon / cyberpunk accents.** Migrated downmarket. Current premium tier is desaturated.
- ❌ **Chat-bubble metaphor as the marketing hero.** Every AI product does it; we lead with type and a calm composer card.
- ❌ **Stock photography.** None of the premium tier uses it. We use product UI screenshots, hand-drawn spot illustrations (SVG only), and typography to fill space.
- ❌ **Auto-playing hero video.** Replaced by scroll-pinned interactive demos.
- ❌ **Hard drop shadows or "lift" presets.** Soft, warm-tinted ambient shadows only.
- ❌ **Mixed icon weights/styles.** Lucide stroke at 1.5px across the entire app.
- ❌ **Spring physics / bounce motion** inside product chrome. Bounce is reserved for landing surfaces only.
- ❌ **Default Radix / shadcn styling left intact.** Every visible primitive is rethemed.
- ❌ **Browser-native alert / confirm / prompt / select dropdown.** All replaced.

---

## 6. Brand voice

- **Editorial.** We use proper hyphenation (`—`), single quotes for asides, and tight sentence structure.
- **Quiet.** No exclamation marks in product chrome. The marketing surface allows one carefully placed punctuation moment per page, max.
- **Specific.** Suggestion chips name concrete tasks ("Outline a research plan") rather than vague invitations ("Get started").
- **First-person plural** ("we") in marketing, **second-person singular** ("you") in product chrome and settings.

---

## 7. Visual signature checklist

A page passes "feels like Aurelia" if it satisfies:

- [ ] Paper-warm background (light) or warm-graphite (dark), never gray-blue
- [ ] Serif present in at least one place; sans dominates body
- [ ] Exactly one accent color visible on the screen
- [ ] All borders are hairlines (1px at the device pixel ratio)
- [ ] At least one "breathing" empty band of ≥48px somewhere
- [ ] No more than two type weights visible in chrome
- [ ] No pure white surface and no pure black text
- [ ] Focus rings are clay, not blue
- [ ] Code surfaces are tinted, not neutral gray
- [ ] Any motion present is ≤320ms and uses the shared easing curve

---

## 8. References (notional, for further verification)

- claude.ai — Anthropic product surface (visual, IA, motion)
- chat.openai.com — OpenAI product surface (IA, branch pager, share)
- gemini.google.com — Google product surface (multimodal, suggestion strips)
- assistant-ui.com / github.com/Yonom/assistant-ui — React primitives
- ui.shadcn.com — component substrate
- tailwindcss.com/docs/v4-beta — token system
- perplexity.ai, linear.app, v0.dev, raycast.com, granola.ai, mistral.ai — premium tier case studies

When network access is available, walk this list and validate any specific hex / spacing value used in the implementation.
