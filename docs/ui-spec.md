# UI Spec — Aurelia

## 1. Layout system

| Surface | Spec |
|---|---|
| Marketing shell | Transparent nav, edge padding 24/32/64, footer at 96px section spacing |
| App shell (chat) | 280px sidebar (collapsible to 56px) + 56px topbar + flex content + composer at bottom |
| Settings shell | Modal-style overlay on desktop OR full-page on `/settings`, with left tab rail |
| Auth shell | Centered card on full-screen warm wash |

Breakpoints: `sm 640 · md 768 · lg 1024 · xl 1280 · 2xl 1536`.

| Surface | mobile | tablet | desktop |
|---|---|---|---|
| Sidebar | Drawer (off-canvas) | Optionally collapsed icon rail | Full 280px |
| Composer | Fixed bottom, edge-to-edge | Centered max-w 720 | Centered max-w 800 |
| Settings rail | Vertical tab list at top | Side rail | Side rail |
| Marketing hero | Stacked, 32px gutters | Side-by-side, 48px | Side-by-side, 64px |

## 2. Component states

Every interactive component must define: **default**, **hover**, **focus-visible**, **active**, **disabled**, **loading**, optional **selected**, optional **error**.

- Focus rings: 2px clay outline + 2px offset, never browser default blue.
- Hover: ≤ 4% tint shift, never color change.
- Active: + 2px translate down OR scale 0.99, never both.
- Disabled: 50% opacity + `cursor-not-allowed`, no pointer events.

## 3. Page-by-page IA

### 3.1 Landing
- Sticky nav with logo + 4 links + sign-in + CTA. Transparent at top, hairline shadow once scrolled.
- Hero: serif headline + 1-line subhead + composer-as-CTA (clicking takes user to `/chat`).
- Sections: capabilities (3-col), use cases (4-card grid), model lineup, safety, CTA band, footer.
- All sections have ≥ 96px vertical breathing.
- Footer: 4-col link grid + brand + copyright.

### 3.2 Chat
- Sidebar: workspace header + new-chat button + search trigger + conversation groups + footer (user + settings).
- Topbar: collapse toggle + thread title (truncate) + model picker + share + more.
- Empty state: serif greeting ("Good evening, Astrid.") + 4 suggestion cards.
- Active conversation: message stream + composer.
- Composer: textarea + tool chips (attach, image, voice, mode pill) + send/stop button.
- Right edge optional: artifact drawer (reserved, not built this iteration).

### 3.3 Message bubble
- User: right-aligned, accentSoft pill background, 16px radius, 24px max paragraph width.
- Assistant: left-aligned, no bubble (bubbleless), full width, with `Aurelia` label and timestamp on hover.
- Actions on hover: copy, regenerate, edit (user only), like/dislike (assistant only), more.

### 3.4 Settings
- Left tab rail (Account, Appearance, Models, Privacy, Shortcuts, Billing).
- Right content panel with section headers + setting rows (label + control + description).
- Toggles, selects, sliders, segmented controls — all custom.

### 3.5 Auth
- Centered card max-w 400px.
- Logo at top, serif heading, fields, primary button, OAuth row, footer link to switch.

### 3.6 404
- Serif headline "Lost the thread." + body line + button to `/chat`.

## 4. Empty / loading / error states

Every list, fetch, and async action must define the three:

| State | Visual |
|---|---|
| Empty | Centered icon-illustration (SVG, never bitmap) + serif headline + body + optional CTA |
| Loading | Skeleton bars matching the final shape + shimmer (or spinner ONLY for non-AI work) |
| Error | Soft danger-tinted card + reason + retry button |

## 5. Toasts

- Position bottom-right desktop, top mobile.
- 4 variants (info, success, warning, danger) tinted in soft colors.
- Dismiss on click or after 5s.
- Live region `aria-live="polite"`.

## 6. Keyboard

| Shortcut | Action |
|---|---|
| `⌘/Ctrl + K` | Open Command Menu |
| `⌘/Ctrl + Enter` | Send message |
| `Shift + Enter` | Newline in composer |
| `⌘/Ctrl + B` | Toggle sidebar |
| `Esc` | Close any open overlay |
| `⌘/Ctrl + ,` | Open Settings |
| `⌘/Ctrl + /` | Show keyboard shortcuts |
| `⌘/Ctrl + Shift + O` | New chat |

## 7. Accessibility

- Every clickable surface has a tab-stop + visible focus ring + ARIA role/label.
- Dialog/Drawer/Sheet use Radix primitives (focus trap + restore + ESC + screen-reader announce).
- Tooltip has `aria-describedby` wired.
- Forms always pair `<Label>` with input + descriptive error.
- All icons inside `<button>` are decorated with `aria-hidden` and the button has a label or `aria-label`.
- All color pairs meet WCAG AA on text + states.
- `prefers-reduced-motion` collapses motion durations to 0ms.
- All routes have a `<h1>`.

## 8. Responsive checklist (per page)

- [ ] Sidebar collapses to Drawer ≤ md.
- [ ] Composer stays accessible above mobile keyboard.
- [ ] Message column max-width adapts.
- [ ] Marketing hero stacks ≤ md.
- [ ] Settings tab rail moves to top horizontal scroll ≤ md.
- [ ] Tap targets ≥ 44px on touch.

## 9. Visual rules consolidated

- Single accent visible per screen.
- Hairline borders for elevation.
- Shadows on popovers/dialogs only.
- Max two type weights in chrome.
- Serif limited to display + assistant voice moments.
- No pure black or pure white.
- All radii from the token set; no ad-hoc values.
