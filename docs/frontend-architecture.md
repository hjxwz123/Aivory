# Frontend Architecture — Aurelia

## 1. Tech selection

| Concern | Choice | Why |
|---|---|---|
| Runtime | React 19 + TypeScript 5.7 | Latest concurrent features (Activity, useOptimistic), strong typing |
| Bundler / dev server | Vite 6 | Fast HMR, good DX; the brief asked for Vite, Next, or current setup |
| Styling | Tailwind v4 + design tokens | `@theme inline` lets us declare tokens once in CSS |
| Routing | react-router 7 | Lightweight client routing for a frontend-only project |
| State | Zustand 5 | Minimal global state for conversations, theme, sidebar |
| Forms | Native + custom hooks | Avoid heavy form libs; controlled state is enough at this scale |
| Primitives | Radix UI | Accessibility ceiling for Dialog, DropdownMenu, etc. |
| Command palette | cmdk | Standard substrate |
| Icons | lucide-react | Single source, consistent stroke |
| Motion | framer-motion v12 | Subtle, controlled |
| Markdown | marked | Lightweight; we add our own renderer that maps to tokens |
| AI runtime | Mock-only `aureliaMockAdapter` | Brief explicitly forbids real model calls. Interface mirrors what assistant-ui / Vercel AI SDK expose, so swapping in real backends later is one file. |

### 1.1 Why not assistant-ui directly?

The brief asks us to prefer assistant-ui. We adopted its **architectural shape** (a Runtime layer + Composer + Thread + Message + ToolCall primitives) without installing the package, because:

1. **Mock-only constraint.** We can't run a real runtime; pulling assistant-ui in just to wrap its primitives in mock providers would add ~150 kB of unused code.
2. **Visual ownership.** Brief demands total visual ownership. assistant-ui's primitives are unstyled, which is fine, but we already need to fully build the visual layer; we'd save little.
3. **Future-proofing.** Our `ChatAdapter` interface is structurally compatible with assistant-ui's `RuntimeAdapter` — when we later attach a real backend, we can either keep our own primitives or wrap `@assistant-ui/react` and the data model survives.

This trade-off is documented and revisited in `docs/assumptions.md`.

## 2. Directory layout

```
src/
├── main.tsx                  # entry: theme bootstrap, router
├── App.tsx                   # top-level routes + providers
├── styles/
│   ├── tokens.css            # CSS variables (light + dark)
│   └── globals.css           # @theme bridge, reset, prose, utilities
├── lib/
│   ├── design-tokens.ts      # typed access to runtime token values
│   ├── utils.ts              # cn, sleep, copy, date bucket, etc.
│   ├── markdown.ts           # marked configuration + tokens
│   └── shortcuts.ts          # keyboard shortcut registry
├── components/
│   ├── ui/                   # Token-driven primitives
│   ├── chat/                 # Chat-specific composition
│   ├── sidebar/              # Sidebar composition
│   ├── command-menu/         # CommandPalette
│   ├── marketing/            # Landing-only composition
│   ├── settings/             # Settings shell
│   └── brand/                # Logo, LogoMark
├── pages/                    # Page-level components
│   ├── Landing.tsx
│   ├── chat/
│   │   ├── ChatLayout.tsx
│   │   ├── ChatHome.tsx
│   │   └── ChatThread.tsx
│   ├── auth/
│   │   ├── AuthLayout.tsx
│   │   ├── Login.tsx
│   │   ├── Register.tsx
│   │   └── ForgotPassword.tsx
│   ├── settings/
│   │   ├── SettingsLayout.tsx
│   │   ├── Account.tsx
│   │   ├── Appearance.tsx
│   │   ├── Models.tsx
│   │   ├── Privacy.tsx
│   │   ├── Shortcuts.tsx
│   │   └── Billing.tsx
│   └── NotFound.tsx
├── runtime/
│   ├── adapter.ts            # ChatAdapter interface
│   ├── mock-adapter.ts       # aureliaMockAdapter
│   └── RuntimeProvider.tsx   # React context exposing the adapter
├── hooks/
│   ├── use-theme.ts
│   ├── use-sidebar.ts
│   ├── use-command-menu.ts
│   ├── use-hotkeys.ts
│   ├── use-autosize-textarea.ts
│   ├── use-media-query.ts
│   └── use-toast.ts
├── store/
│   ├── conversations.ts      # Zustand: conversation list, active id
│   ├── theme.ts              # Zustand: theme + density + size
│   └── settings.ts           # Zustand: user prefs
├── data/
│   ├── conversations.ts      # seed conversations
│   ├── suggestions.ts        # empty-state prompts
│   ├── models.ts             # model registry
│   ├── replies.ts            # mock streaming text bank
│   └── user.ts               # seed user
├── types/
│   ├── chat.ts
│   ├── model.ts
│   ├── user.ts
│   └── settings.ts
└── icons/                    # any custom SVG glyphs
```

## 3. Component layering

- **Primitives (`ui/`)** — token-driven, opinionated, no business logic.
  - Examples: `Button`, `Input`, `Dialog`, `DropdownMenu`.
- **Composition (`chat/`, `sidebar/`, etc.)** — combine primitives, hold mini-logic.
  - Examples: `Composer`, `MessageRow`, `Sidebar`.
- **Pages (`pages/`)** — assemble compositions, wire to store/runtime.

Rule: a primitive never imports from a composition. A composition never imports from a page.

## 4. State

- **Zustand** for global concerns: conversations, theme, layout flags.
- **React local state** for ephemeral UI: dialog open, hover, etc.
- **URL** as a source of truth for active conversation (`/chat/:id`) and active settings tab.

Why Zustand? It avoids context churn for the conversation list, persists to localStorage trivially, and keeps the store testable.

## 5. Mock runtime

- `aureliaMockAdapter` implements `ChatAdapter`.
- `sendMessage` returns an `AsyncIterable<MessageChunk>` that emits text chunks at 30–80ms intervals, a tool-call cycle on certain prompts, then a stop chunk.
- `stopStream` flips an abort flag.
- The adapter is wrapped in `RuntimeProvider`, exposed via `useChat()` hook.
- Replacing this adapter with a real one (Anthropic, OpenAI, custom) is a single-file change.

## 6. Theming

- Bootstrapped pre-paint via a tiny inline script in `index.html` that reads `localStorage.aurelia.theme` and sets `data-theme` + `.dark` on `<html>`. No FOUC.
- The `useTheme()` hook drives runtime changes and persists to localStorage.
- "System" is honored through `matchMedia('(prefers-color-scheme: dark)')`.

## 7. Data flow at a glance

```
[User input in Composer]
        ↓ controlled state
[onSend(text)]
        ↓
[useConversationStore.appendMessage] (user)
        ↓
[adapter.sendMessage()] → AsyncIterable<MessageChunk>
        ↓ for-await
[useConversationStore.streamAssistant(chunks)]
        ↓
[MessageRow re-renders incrementally]
```

## 8. Future API hookup

Replace `mock-adapter.ts` with a real implementation that maps to backend endpoints:

```ts
async *sendMessage(input) {
  const res = await fetch('/api/chat', {
    method: 'POST',
    body: JSON.stringify(input),
  })
  // SSE / NDJSON / Vercel AI SDK stream → yield chunks
}
```

All UI code consumes only the `MessageChunk` shape, so nothing else needs to change.

## 9. Performance

- Tree-shaken Lucide imports.
- Code-split routes via lazy + Suspense for `chat`, `settings`, `auth`.
- Markdown rendered with token-level memoization on assistant messages.
- Virtualization not needed at this iteration (mock data <100 messages); easily added later.

## 10. Build / dev

- `npm run dev` — Vite at port 5173, host listening.
- `npm run build` — TS check then Vite build to `dist/`.
- `npm run preview` — local serve of build.
- `npm run typecheck` — TS without emit.
- `npm run lint` — ESLint flat config.
