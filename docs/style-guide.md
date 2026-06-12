# Style Guide — Aurelia

## 1. Voice keywords

> Warm. Editorial. Calm. Restrained. Thoughtful. Premium-by-subtraction.

We are not loud. We do not flatter. We do not surprise the user with motion. We respect reading.

## 2. Color strategy

All colors are declared in OKLCH for perceptual uniformity. The single source of truth is `src/styles/tokens.css`. Components only consume CSS variables / Tailwind utilities — they never hardcode color.

### 2.1 Light theme spine

| Token | OKLCH | Use |
|---|---|---|
| `--color-bg` | `oklch(98% 0.006 80)` | Page background |
| `--color-bg-muted` | `oklch(96.5% 0.008 80)` | Recessed band |
| `--color-surface` | `oklch(99% 0.004 85)` | Cards, popovers |
| `--color-surface-raised` | `oklch(100% 0 0)` (used sparingly) | Dialog |
| `--color-surface-sunken` | `oklch(95% 0.008 80)` | Inputs, code wells |
| `--color-fg` | `oklch(22% 0.012 60)` | Primary text |
| `--color-fg-muted` | `oklch(45% 0.012 60)` | Metadata |
| `--color-border` | `oklch(89% 0.010 75)` | Hairline |
| `--color-accent` | `oklch(62% 0.14 38)` | Clay primary |
| `--color-secondary` | `oklch(60% 0.06 120)` | Sage AI signifier |
| `--color-danger` | `oklch(60% 0.18 25)` | Rust |

### 2.2 Dark theme

Dark mode is *not inverted light*. We redesigned the palette around warm graphite (`oklch(16% 0.008 70)`) with lifted accents:

- Background lifts to `oklch(16-24%)` tiers.
- Accent shifts up to `oklch(72% 0.13 38)` so it doesn't muddy on dark.
- Shadows lean on opacity rather than blur.

### 2.3 Accent discipline

- The clay accent appears at most once per screen as a *primary* element — primary CTA, send button, or focus ring.
- Secondary sage is used only in AI moments (assistant rail, citation strip, gem badge).
- Banners use semantic tokens (success/warning/danger), each with paired soft + strong shades.

### 2.4 Selection

`::selection` uses `color-mix(in oklch, var(--color-accent) 25%, transparent)` — warmth in the selection, never browser default blue.

## 3. Typography strategy

| Font | Family | Use |
|---|---|---|
| Serif | `Source Serif 4` (Google, OFL) | Display headlines, empty-state greeting, assistant voice on marketing |
| Sans | `Inter` | Body, UI chrome, settings |
| Mono | `JetBrains Mono` | Code blocks, kbd, model IDs, tabular figures |

### 3.1 Scale

| Token | Size | Line height | Use |
|---|---|---|---|
| `text-xs` | 12 | 16 | Captions, kbd |
| `text-sm` | 13 | 18 | Metadata, labels |
| `text-base` | 15 | 24 | Chat body, default |
| `text-md` | 16 | 26 | Forms |
| `text-lg` | 18 | 28 | Sub-section titles |
| `text-xl` | 22 | 30 | Card titles |
| `text-2xl` | 28 | 36 | Page titles |
| `text-3xl` | 34 | 42 | Major headers |
| `text-4xl` | 44 | 52 | Hero |
| `text-5xl` | 56 | 60 | Brand statement |
| `text-6xl` | 72 | 76 | Landing only |

### 3.2 Weights

- 400 body
- 500 UI chrome
- 600 section heads
- 700+ reserved for serif display only — never inside chrome

### 3.3 Numerals

- Tabular figures (`font-variant-numeric: tabular-nums`) on counters, timestamps, usage meters.
- Proportional everywhere else.

## 4. Whitespace strategy

- 4px base grid. Common steps: 4 / 8 / 12 / 16 / 24 / 32 / 48 / 64 / 96 / 160.
- Marketing sections at minimum 96px vertical rhythm; large hero at 160–240px.
- Message rhythm: 24–32px between turns, 12px inside a turn.
- Generous left padding on chat column (≥ 24px on mobile, 64px on desktop).
- Settings rows at 16–24px vertical with 1px hairline divider — never solid block backgrounds.

## 5. Radius strategy

| Use | Radius |
|---|---|
| Inline pills, kbd | 4–6px |
| Buttons | 8px |
| Menu items | 8px |
| Cards | 12–16px |
| Composer card | 20–24px |
| Avatar | full |

Concentric rule: inner radius = outer radius − padding. The system enforces this implicitly via tokens.

## 6. Shadow strategy

- Page surfaces use **borders for elevation**, not shadows.
- Shadows appear only on **popovers, dropdowns, dialogs, tooltips** (i.e. floating overlays).
- All shadow colors are warm-tinted (mix of `oklch(18% 0.01 60)`), never neutral.
- Reserved use: focus ring is a soft 3px ring at `--color-ring`.

## 7. Border strategy

- Default border `--color-border` 1px hairline.
- `--color-border-strong` for input outlines and active separators.
- `--color-border-subtle` for very low-contrast inner dividers.
- Never use double borders or stacked borders.

## 8. Motion strategy

- Three duration tiers: `120ms` (hover), `220ms` (panel), `320ms` (drawer / composer focus expand).
- One easing curve almost everywhere: `cubic-bezier(0.2, 0.8, 0.2, 1)`.
- Streaming caret blinks at 1Hz — 2px tall, clay-colored, no fade-per-character.
- Thinking shimmer: sage→clay horizontal band sweeping a placeholder at 1.4s linear.
- Send→Stop button morph: icon swap in place at 120ms.
- `prefers-reduced-motion` collapses everything to instant.

## 9. The "high-end" checklist

A surface feels high-end when:

- It has at least one breathing band of ≥ 64px (let it be empty).
- It uses at most two type weights.
- It uses one accent.
- It is anchored in editorial type (a serif headline somewhere).
- It uses paper-warm backgrounds, not gray-blue.
- It avoids stock photography and 3D blob gradients.
- It has tight numerical hierarchy (one big number wins).
- It uses hairlines, not solid block dividers.
- It has no `box-shadow: 0 0 20px black`.

If a screen is missing three of these, it is not high-end yet.

## 10. The "breathing room" checklist

A surface breathes when:

- Hero margin ≥ 96px above and below.
- No card is touching the page edge except on mobile.
- Body line length is 60–80ch.
- Padding inside cards ≥ 24px.
- Lists have ≥ 8px between items and 16px between groups.
- Buttons have ≥ 16px horizontal padding at default size.
- Empty states aren't crammed: text wraps and has a top margin equal to the hero height.

## 11. Icon strategy

- Lucide React, stroke 1.5px, 16/18/20/24 sizes.
- Never mix stroke and filled glyphs.
- Inside buttons, decorative icons are `aria-hidden`.
- For semantic icons (warning/success), pair with text whenever it's the only marker.

## 12. Imagery strategy

- Hero illustrations are SVG only.
- Use composition / type / product chrome instead of photos.
- If a photo must appear, it is warm-toned, low-saturation, full-bleed.
