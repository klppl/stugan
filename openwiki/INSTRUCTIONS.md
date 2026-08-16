# Documentation Brief for Stugan

## Project Overview
`stugan` is a self-hosted, persistent 24/7 web IRC client written in Go with a modern Vue 3 frontend (communicating via a typed JSON WebSocket protocol), an SSH Terminal UI (Bubble Tea/Wish), and a Lua plugin runtime (gopher-lua).

## Key Architectural Principles & Areas to Document

1. **Strict Dependency Decoupling & Architecture (`internal/core`)**
   - Halloy-inspired core: `internal/core` defines abstract interfaces (`IRCConn`, `PluginHost`, `NetworkStore`, `Sink`, `Connector`, `API`) and MUST NOT import concrete libraries (girc, gopher-lua, modernc/sqlite, coder/websocket, wish/bubbletea).
   - Concrete implementations depend on `core`, never the reverse.
   - Document the interface contracts and explain why this architecture enables swappable transports and hosts.

2. **The Two-Sided Bus & Concurrency Model**
   - **Inbound**: IRC events (`internal/irc`) are translated to `core.Event`, enqueued to the serial Engine loop goroutine, passed through `PluginHost` hooks (which can drop/mutate/claim), applied to domain state under `e.mu` (RWMutex), and fanned out to registered `Sink` implementations (`logSink`, `store.Store`, `server.userSink`, `tui`).
   - **Outbound**: WebSocket client frames (`internal/server`) decode typed envelopes `{t, id, d}` and invoke Engine methods (`SendInput`, `AddNetworkLive`, etc.) to write raw IRC lines.
   - Explain deep-copied snapshots (`Snapshot()`, `SnapshotNetwork()`) for safe concurrent reads without leaking live pointers.

3. **Subsystems & Package Structure**
   - **`cmd/stugan`**: Application entry point, CLI flags, configuration loading, wiring tenants, graceful shutdown with shared root context.
   - **`internal/irc`**: Connection wrapper over `girc`, SASL (PLAIN, EXTERNAL/CertFP), and IRCv3 capability negotiation (`echo-message`, `draft/chathistory`, `typing`, `away-notify`, `account-tag`, `server-time`, `message-tags`, etc.).
   - **`internal/plugin`**: Lua plugin engine using `gopher-lua`. Dedicated worker goroutine, sandboxed `stugan.*` API, timeouts, error recovery, fsnotify script hot-reloading, and persistent KV store.
   - **`internal/store`**: Pure Go SQLite storage (`modernc.org/sqlite`) with WAL mode, FTS5 full-text message search, backlog queries, network persistence, and per-user database isolation.
   - **`internal/server`**: HTTP + WebSocket server (`coder/websocket`), multi-tenant `Hub` bridging session-authenticated users to their respective `Engine`, bcrypt auth, session cookie handling, rate limiting, and reverse proxy trust.
   - **`internal/tui`**: Full-featured SSH terminal UI powered by Wish and Bubble Tea.
   - **`internal/proto` & `client/src/proto`**: Typed protocol definition synchronized between Go and TypeScript (`events.ts`).
   - **`client/`**: Vue 3 + Vite + TypeScript frontend, Pinia state stores, virtualized message lists, link previews, inline media proxy, PWA support, and theme customization.
   - **`plugins/`**: Ready-to-use Lua script library (`ai.lua`, `webhooks.lua`, `fish.lua`, `away.lua`, `title.lua`, etc.).

4. **Diagrams to Include**
   - Module dependency flowchart showing the unidirectional flow towards `internal/core`.
   - Sequence diagram for inbound message processing (IRC -> Translator -> Engine Loop -> Plugin Hooks -> State Mutation -> Sink Fan-out).
   - Sequence diagram for outbound client actions over the WebSocket wire protocol.
   - Component diagram of the multi-tenant Hub architecture.
