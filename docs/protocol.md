# WebSocket wire protocol

The browser and daemon speak a **typed JSON protocol** over a single WebSocket
(`coder/websocket`), at `/ws`. Go structs in `internal/proto` are the single
source of truth; the TypeScript mirror in `client/src/proto/events.ts` is kept
in lockstep **by hand**. The current protocol version is `proto.Protocol = 1`
(bump on a breaking change; advertised in `hello`).

## Envelope

Every frame is one JSON object with a discriminator `t` (type) and a typed `d`
(data) payload. An optional `id` carries a client-chosen correlation id so a
request can be answered with a matching reply or error.

```go
type Envelope struct {
    T  string          `json:"t"`            // event type, e.g. "msg:send"
    ID string          `json:"id,omitempty"` // correlation id (req/reply)
    D  json.RawMessage `json:"d,omitempty"`  // typed payload, decoded by T
}
```

The server's router (`server.route`) switches on `T`, decodes `D` into the
matching struct, and dispatches; the client (`connection.ts`) does the same.
Unknown `T` is logged and ignored (forward-compat). Naming is `domain:verb`,
lowercase. `c2s` = client→server, `s2c` = server→client.

## Event catalogue

### Server → client

| `t`             | payload        | meaning |
|-----------------|----------------|---------|
| `hello`         | `Hello`        | sent on connect: protocol version, server name, caps |
| `init`          | `InitState`    | full snapshot: user + all networks/channels/members |
| `msg`           | `MessageDTO`   | a new committed line in a buffer |
| `net:update`    | `NetworkDTO`   | a network's state/nick/buffers/members changed |
| `net:remove`    | `NetRemove`    | a network was removed |
| `net:info`      | `NetConfig`    | answers a c2s `net:info` request (settings dialog) |
| `backlog`       | `BacklogResp`  | a page of history (answers `backlog:fetch`) |
| `context`       | `ContextResp`  | window of messages around an anchor, echoing its `id` (answers `context:fetch`) |
| `search:result` | `SearchResp`   | search results (answers `search`) |
| `list:result`   | `ListResp`     | channel-browser results (answers `list`) |
| `plugin:list`   | `PluginListResp` | the plugin manager list (answers `plugin:list` and `plugin:action`) |
| `complete:res`  | `CompleteRes`  | plugin tab-completion candidates (answers `complete:req`) |
| `highlight`     | `HighlightRules` | the normalized highlight ruleset, broadcast to all the user's tabs after a `highlight:set` |
| `pong`          | (none)         | answers a c2s `ping` (app-level liveness; see below) |
| `error`         | `WireError`    | `{code, message}`, correlated to a request `id` |

### Client → server

| `t`             | payload        | meaning |
|-----------------|----------------|---------|
| `msg:send`      | `MsgSend`      | send text/command to a buffer |
| `backlog:fetch` | `BacklogFetch` | request older/windowed history |
| `context:fetch` | `ContextFetch` | request the window around one message, to expand a mention/search hit inline |
| `search`        | `SearchReq`    | FTS5 search |
| `net:add`       | `NetAdd`       | add + connect a network |
| `net:edit`      | `NetConfig`    | apply settings changes to a network |
| `net:connect`   | `NetConnect`   | connect/disconnect a network |
| `net:info`      | (ref)          | request a network's full config |
| `plugin:list`   | (none)         | request the plugin manager list |
| `plugin:action` | `PluginAction` | load/unload/reload a plugin by name |
| `plugin:setting`| `PluginSettingReq` | set one declared setting of a plugin (replies with `plugin:list`) |
| `complete:req`  | `CompleteReq`  | ask plugins for tab-completion candidates (`seq`-correlated) |
| `read`          | `ReadMark`     | mark a buffer read up to now (advances the persisted read marker) |
| `highlight:set` | `HighlightRules` | replace the highlight ruleset (bad regex → `error`; success → `highlight` broadcast) |
| `buf:close`     | `BufClose`     | close a query/DM buffer (server drops it and re-broadcasts `net:update`; channels use `/part`) |
| `ping`          | (none)         | app-level liveness probe; answered with `pong` (see below) |

### Bidirectional

The actor field is omitted on c2s and filled by the server on s2c.

| `t`         | payload   | c2s | s2c |
|-------------|-----------|-----|-----|
| `typing`    | `Typing`  | send a typing state | someone is typing (adds `nick`) |
| `react`     | `React`   | toggle a reaction | someone reacted (adds `nick`) |
| `redact`    | `Redact`  | delete a message | a message was redacted (adds `by`) |
| `net:remove`| `NetRemove` | remove a network | confirm removal |
| `mute`      | `MuteSet` | set a buffer's muted state | the persisted state, broadcast to the user's tabs |

## State DTOs

Wire projections of the `core` domain types ([core.md](core.md)), decoupled so
the wire format can evolve independently:

```go
type UserDTO    struct { ID, Name string }

type NetworkDTO struct {
    ID, Name, Nick, State string
    Caps     []string                 // negotiated IRCv3 caps — gates cap-dependent UI
    Channels []ChannelDTO
}

type ChannelDTO struct {
    Name, Kind, Topic string
    Members           []MemberDTO
    Unread, Highlight int
    State             map[string]string  // plugin buffer state (e.g. encryption)
}

type MemberDTO  struct { Nick, Modes string; Away bool }

type MessageDTO struct {
    ID, Network, Buffer, Time, From, Kind, Text string  // Time is RFC3339 server-time
    Self, Highlight bool
    Tags            map[string]string
}
```

## Selected payloads

```go
type MsgSend struct { Network, Buffer, Text string }   // Text beginning "/" is a command

type BacklogFetch struct {
    Network, Buffer string
    Before string   // RFC3339 cursor; "" = latest page
    Around string   // RFC3339; window centered on T (jump-to-message)
    Limit  int
}

type BacklogResp struct {
    Network, Buffer string
    Messages []MessageDTO  // oldest → newest
    More     bool          // older history exists
    Around   string        // echoes the request when windowed
}

type SearchReq  struct { Query, Network, Buffer string; Limit int }
type SearchResp struct { Query string; Results []MessageDTO }

type NetAdd struct {
    Name, Addr, Nick, User, Realname string
    SASLUser, SASLPass, ServerPass, Channels, Perform, CertPEM string
    TLS, SASLExternal bool
}
type NetConnect struct { Network string; Connect bool }
type NetRemove  struct { Network string }

type Typing struct { Network, Buffer, Nick, State string }   // State: active|paused|done
type React  struct { Network, Buffer, Target, Nick, Reaction string }  // Target is a msgid
type Redact struct { Network, Buffer, Target, By, Reason string }

type PluginAction struct { Name, Action string }   // Action: load|unload|reload
type PluginSettingReq struct { Name, Key, Value string } // set Key=Value on plugin Name
type PluginSetting struct {
    Name, Type       string     // Type: text|number|select
    Label, Help      string
    Value, Default   string     // Value blank for Secret settings
    Secret           bool
    Options          []string   // allowed values when Type=="select"
}
type PluginInfo struct {
    Name, Description string
    Loaded, Disabled bool
    Errors, Hooks    int
    Commands         []string        // /command names it registered
    Settings         []PluginSetting // values declared via stugan.setting()
}
type PluginListResp struct { Plugins []PluginInfo }

// Highlight rules and mutes are server-persisted per user (store `prefs` table)
// and seeded into InitState (InitState.Highlight, InitState.Muted). A muted
// buffer is matched case-insensitively on Buffer; the set is checked both
// client-side (badges, in-app notify) and server-side (push, while away).
type HighlightRules struct { Patterns, Exceptions []string }  // case-insensitive regexes
type MuteRef        struct { Network, Buffer string }         // one muted buffer
type MuteSet        struct { Network, Buffer string; Muted bool }
```

`msg:send` whose `Text` begins with `/` is parsed server-side as a command
(alias expansion → plugin `hook_command` → built-ins), so commands and chat
share one path.

## Sync & ordering rules

- On (re)connect the server sends `hello` → `init` (a full, authoritative
  snapshot that replaces local state), then a live stream of incremental `s2c`
  events.
- Backlog is **pull**: the client asks via `backlog:fetch` and renders the
  `backlog` page; live messages arrive as `msg`. The client de-dupes by
  `MessageDTO.ID`. A windowed fetch (`around`) replaces buffer contents and
  enters a "windowed" mode that suppresses live appending until the user jumps
  back to the latest page.
- `id` correlation: requests expecting a definite answer (`backlog:fetch`,
  `search`, `net:add`, `net:info`) carry an `id` the matching reply echoes;
  fire-and-forget events (`typing`) omit it.
- **Liveness.** A browser `WebSocket` never exposes protocol ping/pong to JS and
  won't fire `onclose` on a half-open socket (a suspended mobile tab whose TCP
  flow died silently), so the client runs an app-level heartbeat: while open it
  sends `ping` periodically and treats any inbound frame (a `pong`, or ordinary
  traffic) as proof of life; sustained silence — or returning to a backgrounded
  tab / regaining the network — triggers an immediate reconnect. Independently,
  the server sends protocol-level pings and drops a client that stops ponging.
  On reconnect the client reloads each buffer's latest page, so messages that
  arrived while it was away fill in without a manual refresh.

## Adding an event

1. Add the struct + `T…` constant to `internal/proto/proto.go`.
2. Handle it: c2s in `server.route`; s2c emitted from a `userSink` method or a
   direct reply frame.
3. Mirror the type and constant in `client/src/proto/events.ts` and add a
   handler in `client/src/connection.ts`.
</content>
