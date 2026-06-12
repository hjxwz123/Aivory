# Product Spec — Aurelia

## 1. Positioning

**Aurelia** is a thoughtful AI companion for writing, reasoning, and exploration. It is positioned at the intersection of editorial calm and serious capability — a workspace where a careful person thinks, with an AI alongside them.

This repository implements the **frontend only**. There is no real backend, no real model call, no real authentication; every dynamic surface is wired to mock data, mock streaming, and mock state. Every backend touchpoint is named, typed, and prepared for future API integration.

## 2. Audience

| Segment | Need | How Aurelia serves it |
|---|---|---|
| Writers, researchers | Long-form conversation that respects reading | Generous chat column, serif voice on assistant moments, mark down rendering |
| Engineers | Quick code conversations + paste-and-go | Refined code block with copy + language label |
| Knowledge workers | Multi-thread context juggling | Sidebar with conversation groups, command palette |
| Casual users | Calm onboarding, no jargon | Suggestion chips, plain settings copy |

## 3. User stories (this iteration)

- As a visitor I can land on the marketing page, understand what Aurelia is, and click into the chat.
- As a user I can start a new conversation, write a message, watch a mocked stream reply, copy code, regenerate, edit my message.
- As a user I can browse my conversation history grouped by date, rename a conversation, delete it.
- As a user I can switch models (mock) and watch a placeholder confirm the choice.
- As a user I can attach a (mock) file and see it represented above the composer.
- As a user I can hit `⌘K` and search conversations / actions.
- As a user I can collapse the sidebar (`⌘B`), close any modal (`Esc`), send (`⌘Enter`).
- As a user I can sign in or register through a frontend-only form, with full validation states.
- As a user I can open settings and adjust theme (light / dark / system), font size, model defaults, etc.
- As a user I can toggle dark mode and see the entire app re-skin without flicker.

## 4. Page inventory

| Route | Purpose |
|---|---|
| `/` | Marketing landing page |
| `/chat` | Empty chat (welcome) |
| `/chat/:id` | Active conversation |
| `/login`, `/register`, `/forgot-password` | Auth flow (mocked) |
| `/settings/account` | Profile mock |
| `/settings/appearance` | Theme & density |
| `/settings/models` | Model preference mock |
| `/settings/privacy` | Data controls mock |
| `/settings/shortcuts` | Keyboard reference |
| `/settings/billing` | Plan & usage mock |
| `*` | 404 — "Lost the thread." |

## 5. Non-goals (this iteration)

- ❌ No real model API calls. Everything is `mockStream()`.
- ❌ No real authentication. Forms validate locally and "succeed" client-side.
- ❌ No file persistence. Attachment chips are visual only.
- ❌ No real payment integration.
- ❌ No real OAuth.
- ❌ No analytics, telemetry, or backend logging.
- ❌ No real-time multi-user features (presence, comments, share targets).
- ❌ No mobile app — web only, but fully responsive.

## 6. Quality bars

- All interactive elements keyboard accessible.
- All overlays self-themed — no `window.alert`, `window.confirm`, native `<select>`.
- Light + dark + system theme switching with no flicker on initial paint.
- ≥ 90 Lighthouse accessibility on Chat and Settings.
- First meaningful paint ≤ 1.5s on warm cache.
- Reduced-motion respected.

## 7. Future API surface (sketch, for forward compatibility)

```ts
// Drop-in real adapters can replace src/runtime/* without page rewrites.
interface ChatAdapter {
  listConversations(): Promise<Conversation[]>
  getConversation(id: string): Promise<Conversation>
  sendMessage(input: SendMessageInput): AsyncIterable<MessageChunk>
  stopStream(streamId: string): Promise<void>
}
```

The mock `aureliaMockAdapter` is wired into the runtime provider through this interface so the swap to a real backend later is one file.
