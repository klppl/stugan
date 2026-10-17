# Agent Guidance (AGENTS.md)

This file provides overarching guidance for all AI coding agents (Antigravity, Codex, Claude Code, Cursor, Copilot Workspace) working in this repository.

## What this is

`stugan` is a self-hosted web IRC client: a persistent Go daemon that holds IRC connections 24/7 and buffers history in SQLite, plus a Vue 3 browser frontend that talks to it over a typed-JSON WebSocket. The headline feature is a WeeChat/Irssi-style Lua plugin system.

## Key Commands

### Backend (Go - run from repo root)
```sh
go build ./...                                        # build all packages
go build -o stugan ./cmd/stugan                       # build the daemon binary
go build -o stugan-engine ./cmd/stugan-engine         # optional IRC-holding sidecar (docs/engine.md)
go build -o stugan-previews ./cmd/stugan-previews     # optional link-preview sidecar (docs/previews.md)
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

1. **Consult the docs first**: Read the relevant page in `docs/` before planning or making modifications (e.g. `docs/core.md`, `docs/protocol.md`, `docs/plugins.md`, `docs/storage.md`, `docs/server.md`, `docs/frontend.md`). Treat source code and tests as authoritative.

2. **Core Architectural Invariants**:
   - **`internal/core` Independence**: Never import concrete libraries (`girc`, `gopher-lua`, `modernc/sqlite`, `coder/websocket`, `wish`, `bubbletea`) into `internal/core`. `core` defines the interfaces; concrete packages implement them.
   - **Wire Protocol Synchronization**: If you add or modify a WebSocket event, you **must** update both the Go struct in `internal/proto/` and the TypeScript interface in `client/src/proto/events.ts`.
   - **Sink Updates**: If a new method is added to `core.Sink`, all implementers (`store.Store`, `server.userSink`, `ircserver.ircSink`, the `tui` hub sink, `logSink`, and test sinks) must be updated.
   - **Concurrency Safety**: Mutate domain state *only* on the engine loop goroutine under `e.mu`. Provide concurrent readers with deep copies via `Snapshot()` / `SnapshotNetwork()`.
   - **stugan-engine stays dumb and compatible**: `internal/engine` imports only `internal/engineproto` + stdlib and understands as little IRC as possible — every change to it restarts users' engines, which drops IRC. Wire changes go in `internal/engineproto`: adding an optional field/op is a `ProtocolMinor` bump (and stugan must check the engine's minor before relying on it); renaming or re-meaning one is a `ProtocolMajor` bump (also update `ENGINE_MAJOR` in `.github/workflows/docker-engine.yml`).
   - **stugan-previews never falls back in-process**: when `[previews]` is set, `server` forwards to the sidecar and returns 502 if it is down. Site fetching belongs in `internal/previews` providers, which must fetch only through the `*Fetcher` (SSRF guard) and read keys via `Setting()`.

3. **Validation Routine**:
   - Run Go test suite: `go test -race ./...`
   - Run Go formatting & vet: `gofmt -l . && go vet ./...`
   - Run Client typecheck: `cd client && npm run typecheck`
