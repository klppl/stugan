# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

stugan is a self-hosted web IRC client: a persistent Go daemon that holds IRC connections 24/7 and buffers history, plus a Vue 3 browser frontend that talks to it over a typed-JSON WebSocket. Inspired by TheLounge (server↔browser model) and Halloy (GUI-independent core, IRCv3 depth). The headline feature is a weechat/irssi-style Lua plugin system.

## Commands

Go (run from the repo root):

```sh
go build ./...                       # build all packages
go build -o stugan ./cmd/stugan      # build the daemon binary (NOT from client/)
go vet ./...
gofmt -l .                           # must print nothing; CI fails on unformatted files
go test -race ./...                  # full suite
go test -race ./internal/core/       # one package
go test -race -run TestHookTimer ./internal/plugin/   # one test
./stugan                             # run (uses $STUGAN_HOME, else ~/.config/stugan)
./stugan -home ./dev                 # run with a disposable config/data dir
printf 'mypassword\n' | ./stugan -hashpw   # bcrypt hash for a [[users]] password_hash
```

Client (run from `client/`):

```sh
npm install
npm run build      # vue-tsc --noEmit (typecheck) then vite build → client/dist
npm run typecheck
npm run dev         # Vite dev server on :5173, proxies /ws to the daemon on :8080
```

The daemon serves the built client from `client/dist` (configurable via `server.static_dir`). A normal end-to-end build is: build client, then `go build`, then run.

CI (`.github/workflows/`): `ci.yml` runs gofmt/vet/build/test + client build; `docker.yml` builds and publishes the image to GHCR.

## Verifying changes

There is no mock IRC server. The established way to verify protocol/end-to-end behavior is to run the daemon against **Libera** (`irc.libera.chat:6697`, TLS) with a random nick into a low-traffic channel, then drive it with a throwaway **Node WebSocket client** (Node 18+ has a global `WebSocket`) that connects to `ws://127.0.0.1:8080/ws`, sends/reads `proto` frames, and asserts. Tests themselves are table-driven and use `-race`; the server package uses `httptest` + `coder/websocket` Dial with an in-process engine and a `fakeConn`.

## Architecture

The hard rule (see `docs/layout.md`): **`internal/core` imports none of the concrete libraries** (girc, gopher-lua, SQLite, the WebSocket lib). It defines the interfaces it consumes; the concrete packages implement them and import `core` (one-directional: `irc`/`store`/`plugin`/`server` → `core`, never the reverse). girc/lua/sqlite must never leak past their owning package — that's what makes them swappable.

Data flow, inbound: `irc.Conn` (girc) → translates a raw IRC line into a normalized `core.Event` (pure `toEvent` in `internal/irc/translate.go`) → `Engine.HandleEvent` enqueues it → the single **engine loop goroutine** (`internal/core/engine.go`) processes events serially: `handle` dispatches to the `PluginHost` (hooks may drop/mutate messages, claim commands, or be notified of signals), then `apply`/`applyLocked` mutates state and emits committed lines to every `Sink`.

Data flow, outbound: the browser sends a typed frame → `server.route` → `Engine.SendInput`/`AddNetworkLive`/etc. → state change + raw line to IRC.

Key seams:

- **`core.Engine`** owns the domain tree (`User → Network → Channel → Member/Message`). State is mutated only by the loop goroutine but read concurrently by server goroutines, so it is guarded by `e.mu` (RWMutex). Read snapshots via `Snapshot()` / `SnapshotNetwork()` (they deep-copy); never hand out live pointers. `conns` and run-state are also under `e.mu`.
- **`core.Sink`** is the read side of the bus (the s2c fan-out point): `Print`, `NetworkChanged`, `NetworkRemoved`, `ChannelList`, `Typing`. Implemented by `logSink` (terminal), `store.Store` (persistence), and `server`'s per-user `userSink`. **Adding a Sink method means updating every implementer**, including the test sinks (`captureSink`, `noopSink`).
- **`core.IRCConn`** (impl `internal/irc`), **`core.PluginHost`** (impl `internal/plugin`), **`core.Store`/`NetworkStore`** (impl `internal/store`), and **`core.Connector`** (builds connections at runtime; impl in `cmd/stugan`, wrapping `irc.New`) are the other injected seams.
- **`internal/proto`** is the single source of truth for the wire protocol: an `{t, id, d}` envelope plus one struct per event. The TypeScript mirror in **`client/src/proto/events.ts` must be kept in sync by hand.** Adding an event = proto struct + Go handler (`server.route` for c2s; a `userSink`/reply frame for s2c) + the TS type and a `connection.ts` handler.

### Plugins (`internal/plugin`)

A Lua host (gopher-lua) implementing `core.PluginHost`. **All Lua runs on one work-queue goroutine**, each script in its own `*lua.LState`; `Dispatch` blocks on that goroutine. Scripts get the `stugan.*` API, which calls back through `core.API` (so the plugin package never touches engine internals). Hooks run with a per-call timeout; Lua errors are recovered and a repeatedly-failing script is disabled — a bad script never kills the daemon. fsnotify hot-reloads a single script without dropping IRC connections. See `docs/plugins.md`; examples in `docs/examples/`.

### Multi-user (`internal/auth`, `cmd/stugan/hub.go`)

`server` is multi-tenant via a `Hub`: each connection resolves to a user (session cookie, or the implicit `default` user when auth is off) and bridges to *that user's* `Engine`. `cmd/stugan` builds one Engine + SQLite store (+ plugin host) per user. With no `[[users]]` in config the daemon is single-user and unauthenticated (back-compatible); with users, bcrypt login is required. **The store is the source of truth for networks** — config `[[networks]]` only seed it on first run; thereafter networks are managed from the GUI (`net:add`/`net:edit`/`net:remove`/`net:connect`) and persisted.

### IRCv3 notes

Caps live entirely in `internal/irc`. girc enables a baseline set by default; we additionally request `echo-message` and `draft/chathistory` via `SupportedCaps`. Gotchas worth knowing:
- girc delivers **echo-message events only to the `ALL_EVENTS` handler**, not command handlers — there's a dedicated `ALL_EVENTS` handler gated on `e.Echo` for this. An echo is translated as `EvMessageIn` with `Self` (an inbound display copy), never re-sent.
- When `echo-message` is negotiated, the engine **skips local echo** (`applyMessageOut`, and the plugin send helpers) so the server's echo is the only displayed copy.
- Member lists come from the NAMES reply (`353`), not just live JOINs; typing rides on `+typing` TAGMSG; away state from away-notify (`AWAY`).

## Conventions

- Go 1.26; standard layout with `internal/`. `context.Context` for lifecycles, errors wrapped with `%w`, no global state except config, goroutine-per-connection with clean shutdown via a shared cancelable context.
- Config/scripts/data live under one root resolved by `internal/config`: `$STUGAN_HOME`, else `$XDG_CONFIG_HOME/stugan`, else `~/.config/stugan`.
- Conventional commits; keep changes focused.
- `docs/`: `layout.md` (module/interface contract), `protocol.md` (wire schema), `plugins.md` (Lua API).
