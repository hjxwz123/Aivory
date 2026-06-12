# CLAUDE.md — Aurelia Project Conventions

> Read this file before editing any code in this repository. It encodes the rules the system was built around.

## 1. Mission

Aurelia is a frontend-only AI conversation web app — a thoughtful, editorial-feeling product. Every visual and interaction decision in this repo descends from `docs/design-research.md` and `docs/style-guide.md`. **Read those first.**

## 2. Hard rules

### 2.1 Tokens, not magic values

- **Never** write a raw hex, rgb, oklch, or pixel value inside a component. Use Tailwind tokens (e.g. `bg-bg`, `text-fg-muted`, `border-border`) which read from CSS variables defined in `src/styles/tokens.css`.
- New scales/colors require a token addition in `tokens.css` **and** a corresponding `@theme inline` mapping in `globals.css`.
- Numeric constants (max widths, breakpoints, durations) live in `src/lib/design-tokens.ts` or `tokens.css`.

### 2.2 No browser-native dialogs

- ❌ `window.alert`, `window.confirm`, `window.prompt`, native `<select>` dropdowns.
- ✅ Use `<Dialog>`, `<Drawer>`, `<Sheet>`, custom `<Select>`, `<Toast>`.

### 2.3 No default third-party styling

- shadcn/Radix primitives are **rethemed** before they ship. If a primitive looks like a shadcn demo, it's not done.
- No MUI, Ant Design, or Chakra. Bootstrap-derived classes forbidden.

### 2.4 One accent per screen

- Clay accent (`--color-accent`) appears ≤ 1 time per visible viewport as a *primary* element.
- Secondary (sage) is reserved for AI-status moments.
- Semantic colors (success/warning/danger) only when conveying state.

### 2.5 Single icon system

- `lucide-react`, stroke 1.5px. Don't mix in other icon libraries.

### 2.6 Accessibility is non-negotiable

- All interactive surfaces have a visible focus ring (clay, not blue).
- All Dialog/Sheet/DropdownMenu use Radix primitives.
- All form fields have labels and (when invalid) errors.
- All animations honor `prefers-reduced-motion`.

## 3. Code conventions

### 3.1 File naming

- React components: `PascalCase.tsx`.
- Hooks: `use-kebab.ts`.
- Utility modules: `kebab.ts`.
- Stores: `kebab.ts` exporting one default-named hook (`useConversationStore`).

### 3.2 Component shape

```tsx
import { type ComponentProps, type ReactNode } from 'react'
import { cn } from '@/lib/utils'

interface ButtonProps extends ComponentProps<'button'> {
  variant?: 'primary' | 'secondary' | 'ghost' | 'destructive'
  size?: 'sm' | 'md' | 'lg'
  leadingIcon?: ReactNode
  trailingIcon?: ReactNode
}

export function Button({ variant = 'primary', size = 'md', className, children, ...rest }: ButtonProps) {
  return (
    <button className={cn(/* token-driven classes */, className)} {...rest}>
      {children}
    </button>
  )
}
```

Rules:
- Always `interface` for props.
- Default props inline (`variant = 'primary'`), not `defaultProps`.
- Spread rest props **last** before `children`.
- Use `cn()` to merge classes.

### 3.3 Tailwind class ordering

Layout → Box → Background → Border → Typography → State → Custom.

### 3.4 Imports

- Absolute via `@/` alias (e.g. `@/components/ui/button`).
- Group: external, internal, relative.

### 3.5 No barrel files inside `ui/` or `chat/`

- Direct imports keep the dep graph readable.

### 3.6 No `any`

- Use `unknown` then narrow, or write proper types.

## 4. Adding a new primitive

1. Add tokens you need to `tokens.css` and `globals.css`.
2. Build the component in `src/components/ui/<name>.tsx`.
3. Cover: default / hover / focus-visible / active / disabled / (selected) / (error).
4. Validate keyboard: `tab` reaches it, `Enter`/`Space` activates it, `Esc` dismisses if overlay.
5. Validate dark mode visually.
6. Update `docs/ui-spec.md` if the primitive introduces a new pattern.

## 5. Adding a new page

1. Add route entry to `src/App.tsx`.
2. Page component in `src/pages/...`.
3. Decide: empty/loading/error states.
4. Responsive: mobile / tablet / desktop pass.
5. Light + dark pass.

## 6. Mock runtime contract

The runtime sits behind `src/runtime/adapter.ts`. When you need new mock behavior:
- Extend `mock-adapter.ts` only.
- Keep the public surface (`ChatAdapter`) stable so real backends drop in unchanged.

## 7. Forbidden

- Real model API calls (Anthropic, OpenAI, Google, etc.).
- Browser-native alerts/prompts/confirms.
- Pure white / pure black colors.
- 3D gradient blobs, neon, glassmorphism, parallax > 8px.
- Spinning loaders on AI thinking (use shimmer / caret).
- Inline magic colors / sizes.
- Importing component libraries that re-introduce default look (MUI/Ant/Chakra).
- Removing focus rings.
- `console.log` left in committed code.

## 8. Documentation flow

- `docs/design-research.md` — competitive intel
- `docs/product-spec.md` — what we ship
- `docs/ui-spec.md` — layouts and states
- `docs/style-guide.md` — visual rules
- `docs/frontend-architecture.md` — tech & file layout
- `docs/implementation-report.md` — what was done this iteration
- `docs/assumptions.md` — open questions

When you change something material, update the relevant doc.
