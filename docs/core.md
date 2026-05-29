# The core engine

`internal/core` is the GUI- and transport-independent brain. It owns the domain
state, runs the event bus, and defines every interface it consumes. It imports
none of the heavy libraries (see [layout.md](layout.md)).

## Domain types

The tree is `User → Network → Channel → Member`/`Message` (defined in
`types.go`):

```go
type User struct {
    ID       string
    Name     string
    Networks []*Network
}

type Network struct {
    ID       string         // stable id, e.g. "libera"
    Name     string         // display name
    Nick     string         // current nick (may differ from configured)
    State    ConnState      // disconnected | connecting | registered
    Channels []*Channel     // joined channels + open queries + status buffer
    Caps     []string       // negotiated IRCv3 caps (filled on snapshot)
    Params   NetworkParams  // the persisted connection config
}

type Channel struct {
    Name      string
    Kind      ChannelKind            // channel | query | status
    Topic     string
    Members   map[string]*Member
    Unread    int
    Highlight int
    State     map[string]string      // opaque plugin key/value bag (e.g. encryption)
}

type Member struct {
    Nick    string
    Account string   // from account-notify / WHOX, "" if unknown
    Modes   string   // channel prefixes: "@", "+", ...
    Away    bool
}

type Message struct {
    ID        string             // IRCv3 msgid if present, else generated
    Network   string
    Buffer    string             // channel or query name
    Time      time.Time          // server-time if present, else receipt time
    From      string             // nick / source
    Account   string
    Kind      MsgKind            // privmsg | notice | action | join | … | system
    Text      string
    Tags      map[string]string  // raw IRCv3 message-tags
    Self      bool               // echo-message: we sent this
    Highlight bool               // matched a highlight rule / nick mention
}
```

`MsgKind`: `privmsg`, `notice`, `action`, `join`, `part`, `quit`, `nick`,
`topic`, `system`. `ChannelKind`: `channel`, `query`, `status`. `ConnState`:
`disconnected`, `connecting`, `registered`.

These types are projected onto the wire as DTOs (see [protocol.md](protocol.md))
and exposed to Lua as tables (see [plugins.md](plugins.md)), so the domain model
stays decoupled from both.

## The Engine

`Engine` (`engine.go`) is responsible for:

- owning the complete domain tree,
- serializing all mutation onto a **single loop goroutine** fed by an event
  channel,
- managing connections (dial, connect/disconnect, reconnect) via a `Connector`,
- persisting network configs to a `NetworkStore`,
- dispatching mutable events to plugin hooks via the `PluginHost`,
- fanning out committed changes to every `Sink`.

### The loop goroutine

```go
func (e *Engine) HandleEvent(ev Event)   // enqueue from any goroutine (256-deep chan)

func (e *Engine) loop(ctx context.Context) {
    for {
        select {
        case <-ctx.Done(): return
        case ev := <-e.events: e.handle(ctx, ev)
        }
    }
}
```

Every event flows through this one goroutine in arrival order, so state
mutation never races. The pipeline per event is:

1. **`handle`** — routes by `ev.Type`. Internal events (`set_state`, `print`)
   bypass plugins. Mutable events (`message_in`, `message_out`, `command`) go
   to `PluginHost.Dispatch`, which may drop, rewrite, or (for commands) claim
   them. Everything else is a notify-only dispatch.
2. **`apply`** — commits the (possibly mutated) event. Specialized paths handle
   `message_out` (echo-message check), LIST accumulation, typing/react/redact,
   and numerics. Everything else calls `applyLocked`.
3. **`applyLocked`** — mutates the domain tree under the write lock and returns
   the buffer lines to emit. Highlight matching for inbound messages happens
   here.
4. **Sink fan-out** — happens *after* the lock is released, so I/O never blocks
   mutation.

### Concurrency

```go
mu sync.RWMutex   // guards the domain tree, conns, connCancels, listAccum
```

The loop write-locks only for the brief mutation phase; readers read-lock.
Snapshots are deep copies so a reader can traverse a stable view without
holding the lock:

```go
func (e *Engine) Snapshot() *User            // deep-copies the whole tree
func (e *Engine) SnapshotNetwork(id string) *Network
```

Both fill `Caps` from the live connection before returning.

## The event bus

```go
type Event struct {
    Type    EventType
    Network string
    Time    time.Time
    Message *Message          // message events

    Nick, NewNick string      // join/part/quit/nick/away
    Channel       string
    Account       string
    Text          string
    State         ConnState
    Target        string      // msgid (react / redact)

    Command string            // command: name without leading slash
    Args    []string          // command: whitespace-split arguments
    Members []Member          // names: listed channel members
    Away    bool              // away: whether Nick is now away
    Count   int               // list_item: user count; numeric: code
}
```

`EventType` values: `message_in`, `message_out` (mutable/droppable);
`command` (claimable); `join`, `part`, `quit`, `nick`, `topic`, `connect`,
`disconnect`, `names`, `away` (notify); `list_item`, `list_end` (channel
browser); `typing`, `react`, `redact` (ephemeral); `numeric` (WHOIS/WHO/errors);
`set_state`, `print` (internal, bypass plugins).

## Interfaces core defines

```go
// A connection to one IRC network. Implemented in internal/irc.
type IRCConn interface {
    Connect(ctx context.Context) error
    SendRaw(line string) error
    Message(target, text string) error
    Caps() []string
    CurrentNick() string
    Close() error
}

// Receiver of normalized inbound events (the Engine implements it).
type ConnHandler interface{ HandleEvent(ev Event) }

// The plugin runtime. Implemented in internal/plugin.
type PluginHost interface {
    Dispatch(ctx context.Context, ev Event) (out Event, keep bool)
    Commands() []string
    Close() error
}

// Builds an IRCConn from params. Implemented in cmd/stugan (wraps irc.New).
type Connector interface {
    Dial(p NetworkParams, h ConnHandler) (IRCConn, error)
}

// Persists a user's networks. Implemented by internal/store.
type NetworkStore interface {
    SaveNetwork(p NetworkParams) error
    DeleteNetwork(id string) error
}
```

### Sink — the read side of the bus

```go
type Sink interface {
    Print(m Message)                                       // a new buffer line
    NetworkChanged(n *Network)                             // structure changed
    NetworkRemoved(networkID string)                       // network dropped
    ChannelList(network string, items []ChannelListItem)   // LIST result
    Typing(network, buffer, nick, state string)            // typing notification
    React(network, buffer, target, nick, reaction string)  // emoji reaction
    Redact(network, buffer, target, nick, reason string)   // message removed
}
```

Sinks are called synchronously from the engine loop and **must not block** —
marshal/enqueue and return. Implemented by `logSink` (terminal), `store.Store`
(persistence), and `server`'s per-user `userSink`. **Adding a Sink method means
updating every implementer**, including the test sinks (`captureSink`,
`noopSink`).

### API — the surface exposed back to plugins

The Engine hands the `PluginHost` a `core.API` so `plugin` never touches engine
internals:

```go
type API interface {
    Send(network, raw string) error
    Message(network, target, text string) error
    Notice(network, target, text string) error
    Action(network, target, text string) error
    Join(network, channel string) error
    Part(network, channel string) error
    Print(network, buffer, text string)                    // local line, no IRC send
    SetBufferState(network, buffer string, state map[string]string)
    Networks() []NetworkInfo                               // snapshots
    Channels(network string) []ChannelInfo
    Members(network, channel string) []MemberInfo
    Nick(network string) string
}
```

`Message`/`Notice`/`Action` echo locally when the network has not negotiated
`echo-message` (otherwise the server's echo is the only displayed copy).

## Built-in commands

A `/command` event that no plugin claims falls back to `runBuiltinCommand`
(`command.go`):

- **Messaging:** `/me`, `/msg`, `/notice`, `/query`.
- **Membership:** `/join` (preserves keys), `/part`, `/nick`, `/topic`,
  `/away`, `/back`.
- **Moderation:** `/ban`, `/unban`, `/kick`, `/invite`,
  `/op` `/deop` `/voice` `/devoice` `/halfop` `/dehalfop`, `/mode`.
- **Lookups** (replies route back to the issuing buffer via `numeric` events):
  `/whois`, `/whowas`, `/who`, `/names`.
- **History:** `/chathistory [count]` (server-side, where supported).
- **Raw:** `/raw` (alias `/quote`). An unrecognized command is sent as a raw
  IRC line; a line beginning `//` is sent literally.

**Aliases** (configured under `[aliases]`) expand before built-ins, substituting
`$1`–`$9` (positional), `$*` (all), and `$N-` (from N on), loop-guarded to
depth 8.

## Highlights

`Highlighter` (`highlight.go`) compiles configured `patterns` and `exceptions`
into regexes. `Match(text, nick)` returns true when no exception matches and
either a word-boundary, case-insensitive nick mention or a pattern matches. It
is applied in `applyLocked` to inbound `privmsg`/`notice`/`action` lines that we
did not send, setting `Message.Highlight`. A highlight bumps the channel's
`Highlight` counter and can drive a desktop/Web Push notification.
</content>
