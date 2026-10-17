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
    MonitorOnline map[string]bool  // friends-list presence (lowercased nick → online), per connection
}

type Channel struct {
    Name        string
    Kind        ChannelKind          // channel | query | status
    Topic       string
    TopicSetter string               // from 333 RPL_TOPICWHOTIME
    TopicTime   time.Time
    Mode        string               // channel modes, e.g. "+nt"
    Members     map[string]*Member
    Unread      int
    Highlight   int
    State       map[string]string    // opaque plugin key/value bag (e.g. encryption)
}

type Member struct {
    Nick    string
    Account string   // from account-notify / WHOX, "" if unknown
    Modes   string   // channel prefixes: "@", "+", ...
    Away    bool
}

type Message struct {
    ID        string             // IRCv3 msgid if present, else generated
    Seq       int64              // store rowid (history paging cursor); 0 for live lines
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
        case <-ctx.Done(): drain(); return   // commit what is queued, then exit
        case ev := <-e.events:
            e.handle(ctx, ev)
            if len(e.events) == 0 || time.Since(lastFlush) >= maxNotifyDelay {
                e.flushDirty()                // one snapshot per changed network
            }
        case <-e.kick: e.flushDirty()          // off-loop changes (plugin prints…)
        }
    }
}
```

Every event flows through this one goroutine in arrival order, so state
mutation never races.

`Sync(fn)` queues a barrier: the loop runs `fn` once every event handed to
`HandleEvent` before it has been committed. The stugan-engine relay uses it to
acknowledge a line only after its events are committed (see
[engine.md](engine.md)).

**Shutdown order.** When `Run`'s context ends, the engine sets `stopping`, then
cancels the connections' own context and **detaches** each connection that
implements `Detacher`, or closes the ones that don't. It waits for the
connection goroutines to return, and only then stops the loop, which first
commits whatever is still queued: the last lines read, a relay's ack barrier.
`disconnect` events seen while stopping are dropped, so a shutdown doesn't
record a disconnect in history. The next start connects again, or resumes
through the engine.

The pipeline per event is:

1. **`handle`** — routes by `ev.Type`. The internal `set_state` and `sync`
   events bypass plugins. Mutable events (`message_in`, `message_out`, `command`) go
   to `PluginHost.Dispatch`, which may drop, rewrite, or (for commands) claim
   them. Everything else is a notify-only dispatch.
2. **`apply`** — commits the (possibly mutated) event. Specialized paths handle
   `message_out` (echo-message check), LIST accumulation, typing/react/redact,
   and numerics. Everything else calls `applyLocked`.
3. **`applyLocked`** — mutates the domain tree under the write lock and returns
   the buffer lines to emit. Highlight matching for inbound messages happens
   here.
4. **Sink fan-out** — happens *after* the lock is released, so I/O never blocks
   mutation. Buffer lines (`Print`) go out immediately; event-driven structural changes only
   mark the network **dirty**.

**Snapshot coalescing.** `NetworkChanged` is not sent once per event (explicit
network add/edit/connect calls still notify directly). Changed
network ids accumulate in a dirty set, and `flushDirty` pushes one fresh
deep-copied snapshot per dirty network when the event queue drains, or at least
every `maxNotifyDelay` (250ms) under sustained load. A netsplit or join burst
therefore costs one deep copy and one `net:update` frame per network, not one
per QUIT. Changes made off the loop (plugin prints, background goroutines) wake
an idle loop through `kick`.

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

    Nick, NewNick string      // join/part/quit/nick/away; invite: inviter/invitee
    Buffer        string      // the channel or query the event applies to
    Account       string
    Text          string      // reason / topic / mode flags / numeric line
    State         ConnState
    TopicMeta     bool        // topic: 333 setter/time only, not a new topic
    Target        string      // msgid (react / redact)
    Kicker        string      // kick: who kicked (Nick is the removed member)

    Command     string        // command: name without leading slash
    Args        []string      // command: whitespace-split arguments; monitor: nicks
    Members     []Member      // names: listed channel members
    MemberModes []MemberMode  // mode: membership prefix changes (+o bob, …)
    ChanModes   string        // mode: channel-setting part of the flags
    Away        bool          // away: whether Nick is now away
    Online      bool          // monitor: whether the Args nicks are now online
    Count       int           // list_item: user count; numeric: code
    Resumed     bool          // connect: re-attached via stugan-engine; Args = channels
}
```

A **resumed** `connect` reports a connection stugan-engine kept registered
while stugan was down. Core recreates its channel buffers from `Args` without
join lines, prints `resumed <network> as <nick>`, and does **not** run perform
or autojoin. It does cancel any perform sequence still pending from an earlier
registration. Plugins see it as the `resume` signal, not `connect`.

`EventType` values: `message_in`, `message_out` (mutable/droppable);
`command` (claimable); `join`, `part`, `kick`, `quit`, `nick`, `topic`,
`connect`, `disconnect`, `names`, `away`, `account`, `invite`, `mode`
(notify); `list_item`, `list_end` (channel browser); `typing`, `react`,
`redact` (ephemeral); `numeric` (WHOIS/WHO/errors); `monitor` (MONITOR
730/731 presence); `set_state` and `sync` (internal, bypass plugins).

## Interfaces core defines

```go
// A connection to one IRC network. Implemented in internal/irc.
type IRCConn interface {
    Connect(ctx context.Context) error
    SendRaw(line string) error
    Message(target, text string) error
    Caps() []string
    CurrentNick() string
    Autojoin()                   // join configured channels; called after Perform
    Close() error
}

// Optional IRCConn extension for connections that can outlive the process
// (the stugan-engine relay): Detach stops using it without ending it. Run
// detaches instead of closing on shutdown; Close still means "end it".
type Detacher interface{ Detach() error }

// Receiver of normalized inbound events (the Engine implements it). The
// Engine also offers Sync(fn), which irc.Conn discovers by type assertion.
type ConnHandler interface{ HandleEvent(ev Event) }

// The plugin runtime. Implemented in internal/plugin.
type PluginHost interface {
    Dispatch(ctx context.Context, ev Event) (out Event, keep bool)
    Commands() []string
    Complete(word, network, buffer string) []string   // hook_completion candidates

    // Plugin manager (Settings → Plugins, the TUI overlay, /load etc.)
    Plugins() []PluginInfo
    CuratedPlugins() []CuratedPluginInfo
    LoadPlugin(name string) error
    UnloadPlugin(name string) error
    UninstallPlugin(name string) error
    ReloadPlugin(name string) error
    DownloadPlugin(ctx context.Context, name string) error        // from the official library
    ImportPlugin(ctx context.Context, rawURL, name string) error  // from any URL
    UpdatePlugin(ctx context.Context, name string) error
    CheckPluginUpdates(ctx context.Context, name string) error    // "" = all
    SetPluginSetting(script, key, value string) error             // stugan.setting values

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

// Stored history, read back for plugins (stugan.backlog). Implemented by
// internal/store; beforeSeq is a Message.Seq cursor (0 = latest page).
type HistoryReader interface {
    Backlog(ctx context.Context, network, buffer string, beforeSeq int64, limit int) ([]Message, bool, error)
}
```

### Sink — the read side of the bus

```go
type Sink interface {
    Print(m Message)                                       // a new buffer line
    NetworkChanged(n *Network)                             // structure changed
    NetworkRemoved(networkID string)                       // network dropped
    NetworksReordered(networkIDs []string)                 // manual sidebar order
    ChannelList(network string, items []ChannelListItem)   // LIST result
    Typing(network, buffer, nick, state string)            // typing notification
    React(network, buffer, target, nick, reaction string)  // emoji reaction
    Redact(network, buffer, target, nick, reason string)   // message removed
    ReadMarker(network, buffer string, ts time.Time)       // read marker advanced
}
```

Sinks are called synchronously from the engine loop and **must not block** —
marshal/enqueue and return. The heavier sinks each sit behind a bounded
queue: the store's batched writer ([storage.md](storage.md)), each bouncer
session's outbound line queue (a client that falls 4096 lines behind is
disconnected), and each SSH session's message queue (killed on overflow; the
user reconnects to a fresh snapshot). Implemented by `logSink` (terminal),
`store.Store` (persistence), `server`'s per-user `userSink`, the bouncer
(`ircserver`), and the SSH UI (`tui`). **Adding a Sink method means
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
    Backlog(network, buffer string, limit int) []MessageInfo   // stored history
    HoldJoins(network string) error      // defer autojoin until service auth…
    ReleaseJoins(network string) error   // …completes (or join_hold_timeout fires)
}
```

`Message`/`Notice`/`Action` echo locally when the network has not negotiated
`echo-message` (otherwise the server's echo is the only displayed copy).

## Built-in commands

A `/command` event that no plugin claims falls back to `runBuiltinCommand`
(`command.go`):

- **Messaging:** `/me`, `/msg`, `/notice`, `/query`.
- **Membership:** `/join` (preserves keys), `/part`, `/nick`, `/topic`,
  `/away`, `/back`, `/quit [reason]` (sends `QUIT`; use `/disconnect` to take
  the network offline and keep it there).
- **Moderation:** `/ban`, `/unban`, `/kick`, `/invite`,
  `/op` `/deop` `/voice` `/devoice` `/halfop` `/dehalfop`, `/mode`.
- **Lookups** (replies route back to the issuing buffer via `numeric` events):
  `/whois`, `/whowas`, `/who`, `/names`.
- **History:** `/chathistory [count]` (server-side, where supported).
- **Connection:** bare `/connect` (re)dials the current network — e.g. after an
  authentication failure parked it — and `/disconnect` drops it without
  forgetting it. `/connect` with arguments is passed through as the IRC
  operator `CONNECT` command.
- **Plugins:** `/load <name>` (download a curated script from the official
  library and load it) or `/load <URL> [name]` (import a script from any URL),
  `/unload <name>`, `/reload <name>`, `/update <name>` (re-download a
  curated/imported script and reload it).
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
