# The frontend

`client/` is a Vue 3 + TypeScript single-page app built with Vite. No UI
framework, no Vuex/Pinia — state lives in a handful of Vue `reactive()` objects
that components track directly. The daemon serves the built `client/dist` at
`/`; in development the Vite dev server (`:5173`) proxies `/ws` to the daemon
(`:8080`).

```sh
npm run dev        # Vite dev server with WS proxy
npm run build      # vue-tsc --noEmit (typecheck) then vite build → dist
npm run typecheck  # typecheck only
```

## WebSocket client (`connection.ts`)

The `Connection` singleton (exported as `connection`) is the heart of the app.
It owns the WebSocket and a single reactive `store`:

- `connect()` opens `ws(s)://<host>/ws`, auto-reconnecting ~1.5s after an
  unexpected close.
- `sendFrame<T>(t, d)` writes a typed `Envelope`; `onFrame` switches on
  `Envelope.t` and applies each s2c frame to the store.
- It mirrors `internal/proto` via `src/proto/events.ts` — the `T` constants and
  payload interfaces, **kept in sync with Go by hand**.

The reactive `store` holds: `status`, `server`/`caps`, `networks[]` (each with
`buffers[]` of messages), `active` (the focused buffer), `view`
(`chat`/`mentions`/`search`), `mentions[]`, `search`, `channelList`,
`netConfigs`, `typing`, `reactions`, and `jump`.

Key s2c handlers: `init` (authoritative snapshot, replaces local state), `msg`
(append + unread/highlight bookkeeping + the unread divider + desktop notify),
`net:update`/`net:remove`, `backlog` (paged or windowed merge), `search:result`,
`list:result`, `react`/`redact`/`typing`.

Key actions: `select`, `send`, `fetchBacklog`/`loadOlder`/`fetchAround`/
`backToLatest`, `search`, `addNetwork`/`editNetwork`/`removeNetwork`/
`setConnected`/`requestNetInfo`, `react`/`redact`, `sendTyping` (throttled),
`listChannels`, `upload`, and the cap helpers `hasCap`/`hasNetCap`/`nickOn`.

**Unread divider:** when an unfocused buffer goes from 0 unread to its first
live message, `unreadMarker` is pinned to the previous message; ChatView renders
a "new messages" line above it, cleared on navigation.

## State modules

| Module | Holds |
|--------|-------|
| `connection.store` | networks, buffers, messages, active buffer, reactions, typing — ephemeral, re-synced on reconnect |
| `settings.ts` | theme, custom themes, muted buffers, ignored nicks, fold-events, colored-nicks — **persisted to localStorage** |
| `auth.ts` | auth-enabled/authenticated, user, magic-word required/granted; `refresh()`, `login()`, `logout()`, `submitMagicWord()` |
| `ui.ts` | `sidebarOpen`, `membersOpen`, `isMobile` (responsive drawers, 720px breakpoint) |

Everything reacts through direct property assignment to these objects; no
explicit store library.

## Components

| Component | Role |
|-----------|------|
| `App.vue` | Root layout; gates splash → magic word → login → main app |
| `Sidebar.vue` | Network/buffer list, unread/highlight badges, lock icon, context menu |
| `ChatView.vue` | Message view: day grouping, event folding, unread divider, jump/scroll, drag-drop upload |
| `MessageItem.vue` | One line: colored nick, links, image/video embeds, OG preview cards, reactions, redact button |
| `ChatInput.vue` | Input with Tab autocomplete (nicks/emoji/commands), typing indicator, paste/drop upload |
| `TopBar.vue` | Menu/search/mentions/members toggles, network status, settings |
| `Settings.vue` | Theme + custom-theme installer, notifications/push, mutes/ignores, network list, logout |
| `NetworkSettings.vue` | Edit a network (host/TLS/nick/SASL/cert/perform/channels), connect/disconnect, channel browser, delete |
| `AddNetwork.vue` | Add-network form |
| `ChannelBrowser.vue` | LIST browser: filterable, sortable channel list; click to join |
| `Login.vue` / `MagicWord.vue` | Auth screens (with honeypot fields) |
| `EncryptionKey.vue` | FiSH key dialog → drives the bundled `fish.lua` (`/setkey`, `/delkey`) |

## Helper modules

- `links.ts` — split text into text/link segments, detect image/video URLs,
  route media through `/api/proxy`.
- `previews.ts` — fetch + cache Open Graph preview cards from `/api/preview`.
- `emoji.ts` — `:shortcode:` table, autocomplete matching, and replacement.
- `nickColor.ts` — deterministic per-nick HSL color from an FNV-1a hash of the
  canonicalized nick.
- `contextMenu.ts` — `useContextMenu` composable for right-click / long-press
  floating menus (viewport-clamped, click-outside/Escape dismiss).
- `pwa.ts` — register the service worker; subscribe to Web Push (fetch VAPID
  key, `pushManager.subscribe`, POST to `/api/push/subscribe`).

## PWA

- `manifest.webmanifest` — installable app metadata (name, icons incl. a
  maskable 512, standalone display, dark theme color).
- `public/sw.js` — a minimal service worker: it does **not** cache assets (an
  IRC client should always be fresh); it handles `push` events (shows a
  notification tagged by network/buffer) and `notificationclick` (focuses or
  opens a window).
</content>
