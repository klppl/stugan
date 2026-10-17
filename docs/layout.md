# Architecture & module layout

stugan's design goal is Halloy's discipline: a **core that knows nothing about
IRC libraries, transports, or UIs**, talking to everything through interfaces.
This document is the dependency contract and a map of how data flows through
the system.

## Package map

```
cmd/stugan/        main(): flags, config, per-user wiring, graceful shutdown
cmd/stugan-engine/ the optional IRC-holding sidecar (engine.md)
internal/
  config/          TOML config + $STUGAN_HOME resolution
  logging/         structured logging (slog)
  core/            GUI/transport-independent brain: state machine + event bus
  irc/             IRCConn interface impl over girc (the only place girc lives)
  store/           SQLite history + FTS5 search + network/KV persistence
  plugin/          PluginHost impl: a Lua host (the only place gopher-lua lives)
  auth/            bcrypt credentials + sessions (multi-user)
  server/          HTTP + WebSocket, typed event router, multi-tenant hub
  ircserver/       IRC bouncer front end for native IRC clients (ircserver.md)
  tui/             SSH terminal UI (the only place wish + Bubble Tea live)
  proto/           shared wire-protocol structs (TS mirror in client/)
  backup/          backup archive build/extract (tar.gz / zip / raw .db)
  safehttp/        SSRF-guarded HTTP client (previews, proxy, stugan.http)
  scripts/         embedded bundled Lua plugins (fish, ignore, ai, webhooks)
  engine/          stugan-engine: holds IRC sockets, replays, buffers (engine.md)
  engineproto/     stugan ↔ engine wire frames (NDJSON), versioning
  enginelink/      stugan's side of the engine link: a net.Conn girc dials through
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
| `irc`     | girc, `core`, `enginelink`, stdlib      | `server`, `plugin` |
| `store`   | `modernc.org/sqlite`, `core`, stdlib    | `server`, `plugin` |
| `plugin`  | gopher-lua, fsnotify, `core`, `safehttp`, stdlib | `server`, `irc` impl, `store` impl |
| `server`  | `core`, `proto`, `auth`, `backup`, `safehttp`, `config`, `store` (import-mode type only), coder/websocket, webpush | girc, Lua |
| `ircserver` | `core`, `server` (tenant/hub + bouncer access), stdlib | girc, Lua, `store` impl |
| `tui`     | `core`, wish, Bubble Tea, Lip Gloss     | girc, Lua, `server`, `store` impl |
| `auth`    | bcrypt, stdlib                          | everything else |
| `proto`, `backup`, `safehttp`, `logging` | stdlib only     | everything else |
| `config`  | go-toml/v2, `engineproto` (address parsing), stdlib | everything else |
| `engine`  | `engineproto`, stdlib                   | everything else — it is shipped and versioned separately |
| `enginelink` | `engineproto`, stdlib                | everything else |
| `engineproto` | stdlib only                         | everything else |

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
*one* Sink-driven bridge; the IRC bouncer front end (`ircserver`, see
[ircserver.md](ircserver.md)) and the SSH terminal UI (`tui`, see
[ssh.md](ssh.md)) are two more, each registering its own `Sink` per user.

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
       install bundled scripts (fish, ignore, ai, webhooks) into <scripts>/
       (if [engine] url: one enginelink.Link for the whole process, shared
        by every user's connector; ids are "<user>\x00<network>")
       build core.Engine{ user, connector, highlighter, aliases }
       register store + plugin host as Sinks
       load networks from store (seed from config on first run)
       dial each network via the Connector, add to the engine
       wrap in server.Tenant{ Engine, History, Prefs, Store, ScriptsDir }
  → build the Hub over all tenants
  → if [ircserver] listen: ircserver.New(hub), register its Sinks
  → server.New(hub), register the userSinks
  → if [ssh] enabled:      tui.New(resolver), register its Sinks
  → start listeners (HTTP, bouncer, SSH) + the hourly retention prune
  → block on ctx (SIGINT/SIGTERM) → each engine detaches/closes its
    connections and drains its loop → close the engine link (flushing acks)
    → close the stores
```

All share the root `context.Context`; cancellation cascades a clean shutdown.
The `Connector` (`ircConnector` in `cmd/stugan`) wraps `irc.New` so `core`
never imports the IRC library.
</content>
