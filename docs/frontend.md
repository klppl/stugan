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
  unexpected close. An app-level `ping`/`pong` heartbeat catches half-open
  sockets (a suspended mobile tab) that never fire `onclose`; a reconnect
  re-syncs the visible buffer's backlog.
- `sendFrame<T>(t, d)` writes a typed `Envelope`; `onFrame` switches on
  `Envelope.t` and applies each s2c frame to the store.
- It mirrors `internal/proto` via `src/proto/events.ts` — the `T` constants and
  payload interfaces, **kept in sync with Go by hand**.

The reactive `store` holds: `status`, `server`/`caps`, `networks[]` (each with
`buffers[]` of messages), `active` (the focused buffer), `view`
(`chat`/`mentions`/`search`), `mentions[]` + `mentionsUnseen`, `context`
(inline-expanded mention/search hits), `search`, `channelList`, `netConfigs`,
`typing`, `reactions`, `jump`, `toasts`, `plugins`/`curatedPlugins`,
`highlight`, `aliases`, `muted`, `ignored`, `missed` + `digestOpen` (the "What
you missed" digest), `recent` (recently visited buffers), and `readMarkers`.

Key s2c handlers: `init` (authoritative snapshot, replaces local state), `msg`
(append + unread/highlight bookkeeping + the unread divider + desktop notify),
`net:update`/`net:remove`/`net:reorder`, `backlog` (paged or windowed merge),
`context`, `search:result`, `list:result`, `missed:result`, `react`/`redact`/
`typing`, and the cross-tab broadcasts `settings`/`draft`/`read`/`mute`/
`highlight`/`aliases`.

Key actions: `select`, `send`, `fetchBacklog`/`loadOlder`/`fetchAround`/
`backToLatest`, `search`, `addNetwork`/`editNetwork`/`removeNetwork`/
`setConnected`/`requestNetInfo`, `react`/`redact`, `sendTyping` (throttled),
`listChannels`, `upload`, and the cap helpers `hasCap`/`hasNetCap`/`nickOn`.

**Unread divider:** when an unfocused buffer goes from 0 unread to its first
live message, `unreadMarker` is pinned to the previous message; ChatView renders
a "new messages" line above it, cleared on navigation.

**Muted buffers** don't count ordinary traffic, but highlights in them still
count, badge, and notify (desktop and Web Push).

**Ignore state** for the nick menu is learned from the bundled `ignore.lua`'s
replies and cached in localStorage, so the menu offers only Ignore *or*
Unignore.

## State modules

| Module | Holds |
|--------|-------|
| `connection.store` | networks, buffers, messages, active buffer, reactions, typing — ephemeral, re-synced on reconnect |
| `settings.ts` | theme, custom themes, font size, message layout, fold-events, colored nicks, link previews, reactions, typing (send/show), top bar, digest-on-return — cached in localStorage and **synced to the server** (`settings:set` → `prefs`), so they follow you across devices |
| `drafts.ts` | unsent composer text per buffer, debounced to the server (`draft:set`) and synced across tabs/devices |
| `auth.ts` | auth-enabled/authenticated, user, magic-word required/granted; `refresh()`, `login()`, `logout()`, `submitMagicWord()` |
| `ui.ts` | `sidebarOpen`, `membersOpen`, `isMobile` (720px breakpoint), the open image / channel browser / channel inspector / shortcut sheet, and buffer navigation (`bufferOrder`, `stepBuffer`, `selectNth`, next-unread) |

Per-browser conveniences stay in localStorage only: the last active buffer,
recently visited buffers (for the palette), last-seen time (for the digest),
and the ignore cache. Composer input history is kept in sessionStorage.

Everything reacts through direct property assignment to these objects; no
explicit store library.

## Components

| Component | Role |
|-----------|------|
| `App.vue` | Root layout; gates splash → magic word → login → main app; global shortcuts; tab title `(N) #chan — stugan` and a dotted favicon while there are unseen mentions |
| `Sidebar.vue` | Network/buffer list, per-network connection dot (channels dim while down), bold/highlight names for unread/mentioned buffers, inline "join channel" row, friends (MONITOR), context menu, drag reorder |
| `ChatView.vue` | Message view: day grouping, event folding, unread divider, jump/scroll, "↓ N new messages" pill while scrolled up, network-down strip with Connect, typing overlay, drag-drop upload |
| `MessageItem.vue` | One line: colored nick (click → nick menu, Shift-click → address), links, image/video embeds, OG preview cards, reactions, hover toolbar (react, reply, quote, redact), Classic/Cozy layout |
| `ChatInput.vue` | Composer (see below) |
| `CommandPalette.vue` | Cmd/Ctrl-K: recent-first buffer jump, commands, shortcut hints |
| `KeyboardShortcuts.vue` | The shortcut sheet (Cmd/Ctrl+/) |
| `Digest.vue` | "What you missed": highlights since the read markers, shown on return |
| `MentionRow.vue` | One mention/search hit, expandable to its surrounding context |
| `ImageViewerModal.vue` | In-app image lightbox with pinch/trackpad zoom and pan (`+`/`-`/`0`) |
| `UploadPreviewModal.vue` | Preview + optional comment before an upload is sent |
| `ChannelInspectorModal.vue` | Channel details (topic, modes, members) |
| `Toast.vue` | Transient notices and server errors |
| `TopBar.vue` | Menu/search/mentions/members toggles, network status, settings (hideable; Cmd-K covers it) |
| `Settings.vue` + `settings/*` | Appearance, chat behavior, highlights, aliases, notifications/account, uploads, plugins, IRC clients (`BouncerSettings.vue`: per-account bouncer access, connection details, client setup commands), backup/restore |
| `NetworkSettings.vue` | Edit a network (host/TLS/nick/SASL/cert/perform/channels), connect/disconnect, channel browser, delete |
| `AddNetwork.vue` | Add-network form |
| `ChannelBrowser.vue` | LIST browser: filterable, sortable channel list; click to join |
| `Login.vue` / `MagicWord.vue` | Auth screens (with honeypot fields) |
| `EncryptionKey.vue` | FiSH key dialog → drives the bundled `fish.lua` (`/setkey`, `/delkey`) |
| `Icon.vue` | Inline SVG icon set |

## The composer

- **Enter always sends**; Shift+Enter inserts a newline (sent as
  `draft/multiline` where supported).
- **Tab completion** is irssi-style: Tab completes a nick, `#channel`,
  `/command` or `:emoji` and cycles through matches, Shift+Tab goes back, Esc
  restores what you typed. Plugin `hook_completion` candidates are merged in.
- The as-you-type menu only appears for `/commands` and `:emoji`, and only
  takes Enter after you arrow into it — so Enter never completes by surprise.
- **Input history**: ↑/↓ (at the first/last line) recall lines sent in this
  buffer, per buffer, up to 100. Plain lines are kept in sessionStorage across
  reloads; `/commands` stay in memory only.
- While the network is offline the input is dashed and the placeholder reads
  "Reconnecting to X…". Sending your own line snaps the view back to the
  bottom.
- Moderation from the nick menu: Kick / Kick+Ban open a confirm popover with an
  optional reason.

## Keyboard shortcuts

On macOS, ⌘ replaces Ctrl and the numbered-buffer modifier is Ctrl (Option
types characters on many layouts).

| Keys | Action |
|------|--------|
| Cmd/Ctrl+K | Jump to a buffer or run a command |
| Alt+↑ / Alt+↓ | Previous / next buffer |
| Alt+Shift+↓ / Alt+Shift+↑ | Next / previous buffer with unread (mentions first) |
| Alt+A | Next unread buffer (irssi; not on macOS) |
| Alt+1–9, 0 (Ctrl on macOS) | Buffer 1–10 in sidebar order |
| Cmd/Ctrl+/ | Shortcut sheet |
| Shift+F10 | Buffer menu for the focused sidebar row |
| Enter in search | Run the search; Tab inserts a filter suggestion |

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
  opens a window and jumps to the buffer). Clicking a desktop notification
  jumps straight to the message.
</content>
