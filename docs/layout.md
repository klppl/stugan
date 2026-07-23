# Architecture & module layout

stugan's design goal is Halloy's discipline: a **core that knows nothing about
IRC libraries, transports, or UIs**, talking to everything through interfaces.
This document is the dependency contract and a map of how data flows through
the system.

## Package map

```
cmd/stugan/        main(): flags, config, per-user wiring, graceful shutdown
internal/
  config/          TOML config + $STUGAN_HOME resolution
  logging/         structured logging (slog)
  core/            GUI/transport-independent brain: state machine + event bus
  irc/             IRCConn interface impl over girc (the only place girc lives)
  store/           SQLite history + FTS5 search + network/KV persistence
  plugin/          PluginHost impl: a Lua host (the only place gopher-lua lives)
  auth/            bcrypt credentials + sessions (multi-user)
  server/          HTTP + WebSocket, typed event router, multi-tenant hub
  tui/             SSH terminal UI (the only place wish + Bubble Tea live)
  proto/           shared wire-protocol structs (TS mirror in client/)
  scripts/         embedded bundled Lua plugins (fish.lua)
client/            Vue 3 + TS + Vite frontend
docs/              this documentation
```

## Dependency direction

Arrows = "imports". `core` imports nothing concrete; it sits at the center and
is depended upon.

```
        cmd/stugan
            │  (wires everything together at startup)
            ▼
 ┌──────────────────────────────────────────────┐
 │  server  ──▶ proto                            │
 │    │                                          │
 │    ▼                                          │
 │  core  ◀── plugin   (plugin imports core types │
 │    ▲           │     only; core calls it via   │
 │    │           │     the PluginHost interface)  │
 │  store        irc                              │
 └──────────────────────────────────────────────┘
```

| Package   | May import                              | Must NOT import |
|-----------|-----------------------------------------|-----------------|
| `core`    | `proto` (types), stdlib                 | `server`, `irc`, `store`, `plugin`, girc, Lua, UI |
| `irc`     | girc, `core`, stdlib                    | `server`, `plugin` |
| `store`   | `modernc.org/sqlite`, `core`, stdlib    | `server`, `plugin` |
| `plugin`  | gopher-lua, `core`, stdlib              | `server`, `irc` impl, `store` impl |
| `server`  | `core`, `proto`, `auth`, coder/websocket | girc, Lua |
| `tui`     | `core`, wish, Bubble Tea, Lip Gloss     | girc, Lua, `server`, `store` impl |
| `proto`   | stdlib only                             | everything else |
| `config`  | go-toml/v2, stdlib                      | everything else |

**The rule that matters:** core imports none of the heavy libraries, and
girc/lua/sqlite/wish never leak past their owning package. That is what makes
them swappable — when we write a custom IRCv3 stack, only `irc` changes; a WASM
plugin host would only change `plugin`; the SSH front-end lives entirely in
`tui`, a second `Sink`-driven consumer of the engine alongside `server`.

`core` defines the interfaces it consumes (`IRCConn`, `PluginHost`,
`NetworkStore`, `Connector`, `Sink`, `API`). The concrete packages implement
them and import `core` for the interface and the domain/event types — strictly
one-directional (`irc`/`store`/`plugin` → `core`, never the reverse). See
[core.md](core.md) for the exact signatures.

## The two-sided bus

`core.Engine` owns the domain tree and is the hub of a bus with two sides:

- **Inbound (write side).** Connections and the browser feed `core.Event`s in.
  Plugin hooks run *synchronously, in priority order, before an event is
  committed*, and may drop or mutate it.
- **Outbound (read side, `core.Sink`).** Once an event is committed and state
  is mutated, the final result is fanned out to every registered `Sink`. The
  store persists it; each connected browser receives it as a wire frame; the
  terminal logger prints it.

Every transport is just another consumer of this bus. The WebSocket server is
*one* Sink-driven bridge; an IRC-server front-end (bouncer mode, see
[ircserver.md](ircserver.md)) would be a second, mirror-image one.

## Data flow

**Inbound (IRC → browser):**

```
girc callback
  → irc.toEvent()         normalize a raw line into a core.Event (pure fn)
  → Engine.HandleEvent()  enqueue onto the 256-deep event channel
  → engine loop goroutine (single, serial):
       handle()           dispatch to PluginHost (hooks may drop/mutate/claim)
       apply/applyLocked() mutate domain state under e.mu
       Sink fan-out       store persists; userSink → WS frame; logSink prints
```

**Outbound (browser → IRC):**

```
browser sends a typed frame
  → server.route()        switch on Envelope.T, decode payload
  → Engine.SendInput()/AddNetworkLive()/SendReaction()/…
       (slash-commands: alias expansion → plugin hook_command → built-ins)
  → state change + raw line written to the IRC connection
```

State is **mutated only by the loop goroutine** but **read concurrently** by
server goroutines, so it is guarded by `e.mu` (an `RWMutex`). Readers take
deep-copied snapshots via `Snapshot()` / `SnapshotNetwork()` — live pointers
never escape the lock.

## Multi-user

`server` is multi-tenant via a `Hub`: each connection resolves to a user (a
session cookie, or the implicit `default` user when auth is off) and bridges to
*that user's* `Engine`. `cmd/stugan` builds one `Engine` + SQLite store (+
plugin host) per user, fully isolated. With no `[[users]]` in config the daemon
is single-user and unauthenticated (back-compatible). **The store is the source
of truth for networks** — config `[[networks]]` only seed it on first run.

## Startup wiring (`cmd/stugan`)

```
config.Load → logging.New
  → for each effective user:
       open store (SQLite) at <data>/stugan.db
       install bundled scripts (fish.lua) into <scripts>/
       build core.Engine{ user, connector, highlighter, aliases }
       register store + plugin host as Sinks
       load networks from store (seed from config on first run)
       dial each network via the Connector, add to the engine
       wrap in server.Tenant{ Engine, History }
  → build the Hub over all tenants
  → server.New(hub) ; ListenAndServe(ctx)
  → block on ctx (SIGINT/SIGTERM) → graceful Close() of each engine + store
```

All share the root `context.Context`; cancellation cascades a clean shutdown.
The `Connector` (`ircConnector` in `cmd/stugan`) wraps `irc.New` so `core`
never imports the IRC library.
</content>
