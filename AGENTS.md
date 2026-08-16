# Agent Guidance (AGENTS.md)

This file provides overarching guidance for all AI coding agents (Antigravity, Codex, Claude Code, Cursor, Copilot Workspace) working in this repository.

## What this is

`stugan` is a self-hosted web IRC client: a persistent Go daemon that holds IRC connections 24/7 and buffers history in SQLite, plus a Vue 3 browser frontend that talks to it over a typed-JSON WebSocket. The headline feature is a WeeChat/Irssi-style Lua plugin system.

## Key Commands

### Backend (Go - run from repo root)
```sh
go build ./...                                        # build all packages
go build -o stugan ./cmd/stugan                       # build the daemon binary
go vet ./...                                          # static analysis
gofmt -l .                                            # must print nothing (CI fails on unformatted files)
go test -race ./...                                   # full test suite
go test -race ./internal/core/                        # run one package
go test -race -run TestHookTimer ./internal/plugin/   # run single test
./stugan -home ./dev                                  # run with disposable config/data dir
printf 'mypassword\n' | ./stugan -hashpw              # bcrypt hash for config [[users]]
```

### Frontend (Vue 3 - run from `client/`)
```sh
cd client
npm install
npm run build                                         # vue-tsc --noEmit (typecheck) then vite build -> client/dist
npm run typecheck                                     # standalone TypeScript typecheck
npm run dev                                           # Vite dev server on :5173 (proxies /ws to :8080)
```

## Development Workflow: Feature Implementation & Bug Fixing

When implementing new features, modifying architecture, or fixing bugs in `stugan`:

1. **Consult OpenWiki First**: Reference the relevant documentation in `openwiki/` before planning or making modifications:
   - [openwiki/index.md](file:///Users/alex/GitHub/stugan/openwiki/index.md) - Master documentation index.
   - [openwiki/architecture/overview.md](file:///Users/alex/GitHub/stugan/openwiki/architecture/overview.md) & [openwiki/architecture/core.md](file:///Users/alex/GitHub/stugan/openwiki/architecture/core.md) - Module dependency contracts and interface boundaries.
   - [openwiki/architecture/concurrency_event_model.md](file:///Users/alex/GitHub/stugan/openwiki/architecture/concurrency_event_model.md) - Inbound/outbound event bus, single engine loop goroutine, and Sink fan-out.
   - [openwiki/subsystems/internal_irc.md](file:///Users/alex/GitHub/stugan/openwiki/subsystems/internal_irc.md) - IRC protocol translation and IRCv3 capability negotiation.
   - [openwiki/subsystems/internal_plugin.md](file:///Users/alex/GitHub/stugan/openwiki/subsystems/internal_plugin.md) & [openwiki/subsystems/plugins.md](file:///Users/alex/GitHub/stugan/openwiki/subsystems/plugins.md) - Lua plugin runtime, worker goroutine, and `stugan.*` API.
   - [openwiki/subsystems/internal_store.md](file:///Users/alex/GitHub/stugan/openwiki/subsystems/internal_store.md) - SQLite schema, FTS5 search, and per-user database isolation.
   - [openwiki/subsystems/internal_server.md](file:///Users/alex/GitHub/stugan/openwiki/subsystems/internal_server.md) - HTTP/WebSocket server, multi-tenant Hub, and authentication.
   - [openwiki/subsystems/internal_tui.md](file:///Users/alex/GitHub/stugan/openwiki/subsystems/internal_tui.md) - SSH Terminal UI (Wish + Bubble Tea).
   - [openwiki/subsystems/internal_proto_client_proto.md](file:///Users/alex/GitHub/stugan/openwiki/subsystems/internal_proto_client_proto.md) - WebSocket wire protocol envelope `{t, id, d}`.
   - [openwiki/subsystems/client.md](file:///Users/alex/GitHub/stugan/openwiki/subsystems/client.md) - Vue 3 frontend, Pinia state stores, and virtualization.

2. **Core Architectural Invariants**:
   - **`internal/core` Independence**: Never import concrete libraries (`girc`, `gopher-lua`, `modernc/sqlite`, `coder/websocket`, `wish`, `bubbletea`) into `internal/core`. `core` defines the interfaces; concrete packages implement them.
   - **Wire Protocol Synchronization**: If you add or modify a WebSocket event, you **must** update both the Go struct in `internal/proto/` and the TypeScript interface in `client/src/proto/events.ts`.
   - **Sink Updates**: If a new method is added to `core.Sink`, all implementers (`store.Store`, `server.userSink`, `tui.Server`, `logSink`, and test sinks) must be updated.
   - **Concurrency Safety**: Mutate domain state *only* on the engine loop goroutine under `e.mu`. Provide concurrent readers with deep copies via `Snapshot()` / `SnapshotNetwork()`.

3. **Validation Routine**:
   - Run Go test suite: `go test -race ./...`
   - Run Go formatting & vet: `gofmt -l . && go vet ./...`
   - Run Client typecheck: `cd client && npm run typecheck`

<!-- OPENWIKI:START -->

## OpenWiki Maintenance

This repository has a generated `openwiki/` evidence index. It is optional just-in-time context, not required startup reading.

- Treat source code and tests as authoritative. A brief's unknowns and review items are verification gaps, not automatic requirements.
- Prefer the narrowest quiet validation that proves the changed behavior. Preserve complete failure output.

The scheduled OpenWiki GitHub Actions workflow refreshes the repository wiki. Do not hand-edit generated OpenWiki pages unless explicitly asked; prefer updating source code/docs and letting OpenWiki regenerate.

<!-- OPENWIKI:END -->
