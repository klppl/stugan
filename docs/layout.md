# Proposal 1 — Module & interface layout

This is the dependency contract for stugan. The goal is Halloy's discipline:
a core that knows nothing about IRC libraries, transports, or UIs, talking
to everything through interfaces. **Awaiting sign-off — nothing below is
implemented yet beyond `config`, `logging`, and `main`.**

## 1.1 Dependency direction

Arrows = "imports". Note that `core` imports nothing concrete; it sits at
the center and is depended upon.

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
 │    │           │                               │
 │  store        irc                              │
 └──────────────────────────────────────────────┘
```

Concretely:

| Package   | May import                          | Must NOT import                         |
|-----------|-------------------------------------|-----------------------------------------|
| `core`    | `proto` (types only), stdlib        | `server`, `irc`, `store`, `plugin`, girc, Lua, UI |
| `irc`     | girc, `core` (types it emits), stdlib | `server`, `plugin`                    |
| `store`   | modernc.org/sqlite, `core` (types), stdlib | `server`, `plugin`               |
| `plugin`  | gopher-lua, `core` (interfaces+types) | `server`, `irc` impl, `store` impl    |
| `server`  | `core`, `proto`, coder/websocket    | girc, Lua                               |
| `proto`   | stdlib only                         | everything else                         |
| `config`  | go-toml/v2, stdlib                  | everything else                         |

`core` defines the interfaces it consumes (`IRCConn`, `PluginHost`,
`Store`). The concrete packages implement them and so import `core` for the
interface and the event/domain types they produce — but the dependency is
strictly one-directional (`irc/store/plugin → core`, never the reverse), and
the heavy libraries (girc/lua/sqlite) point *away* from core. The rule that
matters: **core imports none of them, and girc/lua/sqlite never leak past
their owning package.** (Settled during Phase 1: `irc` imports `core`.)

> **Decision needed:** where do the interfaces live? I propose **core
> defines `IRCConn`, `PluginHost`, and `Store`** (consumer-defined
> interfaces). Concrete types (`irc.Conn`, `plugin.LuaHost`, `store.SQLite`)
> satisfy them structurally. Alternative: a separate `internal/iface`
> package. I prefer the former — fewer packages, idiomatic Go.

## 1.2 Domain types (in `core`)

Mirrors TheLounge's `ClientManager → Client → Network → Chan → Msg`, named
for clarity and with multi-user baked in as additive:

```go
// User is the account that owns networks. Single-user today: one implicit
// user. Multi-user later adds a UserManager and per-user auth without
// changing the shape below.
type User struct {
    ID       string
    Name     string
    Networks []*Network
}

type Network struct {
    ID       string            // stable id, e.g. "libera"
    Name     string            // display name
    Nick     string            // current nick (may differ from configured)
    State    ConnState         // Disconnected|Connecting|Registered
    Channels []*Channel        // joined channels + open queries
    conn     IRCConn           // the connection (interface)
}

type Channel struct {
    Name      string           // "#go-nuts" or a nick for a query
    Topic     string
    Members   map[string]*Member
    Kind      ChannelKind      // Channel|Query|Status
    Unread    int
    Highlight int
}

type Member struct {
    Nick    string
    Account string             // from account-notify / WHOX, "" if unknown
    Modes   string             // channel prefixes: "@", "+", ...
    Away    bool
}

type Message struct {
    ID      string             // IRCv3 msgid if present, else generated
    Network string
    Buffer  string             // channel or query name
    Time    time.Time          // server-time if present, else receipt time
    From    string             // nick / source
    Account string
    Kind    MsgKind            // Privmsg|Notice|Action|Join|Part|...|System
    Text    string
    Tags    map[string]string  // raw IRCv3 message-tags
    Self    bool               // echo-message: we sent this
}
```

These are the types `proto` events carry (in DTO form) and that `plugin`
exposes to Lua as tables.

## 1.3 The event bus (in `core`) — the spine

Every meaningful thing that happens becomes an `Event` published on the
bus. Two kinds of subscribers:

1. **Plugin hooks** (via `PluginHost`) run *synchronously, in priority
   order, before the event is committed*, and may **drop** or **mutate** it.
2. **Observers** (the `server`, the `store`) get the *final, committed*
   event asynchronously and react (persist it, push it to sockets).

```go
type EventType string

const (
    EvMessageIn  EventType = "message_in"   // mutable/droppable
    EvMessageOut EventType = "message_out"  // user input → IRC; mutable/droppable
    EvJoin       EventType = "join"
    EvPart       EventType = "part"
    EvNick       EventType = "nick"
    EvConnect    EventType = "connect"
    EvDisconnect EventType = "disconnect"
    EvCommand    EventType = "command"      // a /slash command
    // ... extended as IRCv3 features land
)

type Event struct {
    Type    EventType
    Network string
    Message *Message      // set for message events
    // signal-specific fields (nick, channel, args) as needed
}

// PluginHost is core's view of the plugin runtime. The Lua host (or a
// future WASM host) implements it. core calls Dispatch before committing
// a mutable event.
type PluginHost interface {
    // Dispatch runs registered hooks for ev in priority order. Returns the
    // possibly-mutated event and keep=false if a hook dropped it. Errors
    // from individual scripts are isolated and logged, not returned.
    Dispatch(ctx context.Context, ev Event) (out Event, keep bool)

    // Commands returns the set of /command names scripts have registered,
    // so core can route unknown slash-commands to the host.
    Commands() []string

    // Reload reloads scripts from disk (called by the hot-reload watcher).
    Reload(ctx context.Context) error

    Close() error
}
```

`core` exposes a small **API surface back to plugins** (send raw, print to
a buffer, read state, KV store). I propose this is a `core.API` struct
passed into the `PluginHost` at construction, so the Lua bindings call Go
methods on it. That keeps `plugin` free of business logic:

```go
type API interface {
    Send(network, raw string) error          // stugan.send
    Print(network, buffer, text string)       // stugan.print
    Networks() []NetworkInfo                   // read state (snapshots)
    Channel(network, name string) (ChannelInfo, bool)
    KV(plugin string) KVStore                  // per-plugin persistence
    Config(plugin string) map[string]any       // plugin-scoped settings
}
```

## 1.4 The IRCConn interface (in `core`, implemented in `irc`)

```go
type IRCConn interface {
    Connect(ctx context.Context) error
    SendRaw(line string) error                 // raw IRC line
    Caps() []string                            // negotiated IRCv3 caps
    // Inbound events are delivered to core via a callback/channel set at
    // construction, normalized into core.Event — girc types never escape.
    Close() error
}
```

`irc` translates girc's callbacks into normalized `core.Event`s and accepts
raw lines out. CAP/SASL/reconnect live inside `irc`. When we write a custom
IRCv3 core, only `irc` changes.

## 1.5 The Store interface (in `core`, implemented in `store`)

```go
type Store interface {
    AppendMessage(ctx context.Context, m Message) error
    Backlog(ctx context.Context, network, buffer string, before time.Time, limit int) ([]Message, error)
    Search(ctx context.Context, q SearchQuery) ([]Message, error)  // FTS5
    SaveNetwork(ctx context.Context, n NetworkState) error
    // ...
    Close() error
}
```

## 1.6 Startup wiring (in `cmd/stugan`)

```
config.Load → logging.New → store.Open
            → for each network: irc.New(cfg) implementing IRCConn
            → plugin.NewLuaHost(core.API) implementing PluginHost
            → core.New(store, host) ; core.AddNetwork(conn) per network
            → server.New(core) ; server.ListenAndServe(ctx)
            → block on ctx (SIGINT/SIGTERM) → graceful Close() of each
```

All share the root `context.Context`; cancellation cascades a clean
shutdown (connections drain, sockets close, store flushes).

## 1.7 Open questions for sign-off

1. Interfaces in `core` vs. a dedicated `iface` package? (I propose `core`.)
2. Plugin→core calls via a `core.API` interface passed to the host? (Yes,
   I propose.)
3. Is the type naming (`User/Network/Channel/Member/Message`) good, or do
   you want TheLounge's exact names (`Client/Chan/Msg`)? I lean toward the
   spelled-out names.
4. Should `Channel` model queries (DMs) and the server buffer as the same
   type with a `Kind`, or separate types? I propose one type + `Kind`.
