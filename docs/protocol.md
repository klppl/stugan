# Proposal 2 — WebSocket event schema

The browser and daemon speak a **typed JSON protocol** over a single
WebSocket (via `coder/websocket`). No Socket.IO. Go structs in
`internal/proto` are the **single source of truth**; the TypeScript mirror
in `client/src/proto/events.ts` is kept in lockstep (hand-written now, a
small Go→TS generator is a Phase 6 nice-to-have). **Awaiting sign-off.**

## 2.1 Envelope

Every frame is one JSON object with a discriminator `t` (type) and a typed
`d` (data) payload. An optional `id` carries a client-chosen correlation id
so a request can be answered with a matching ack/error.

```go
// Envelope is the single framing for all messages in both directions.
type Envelope struct {
    T  string          `json:"t"`            // event type, e.g. "msg:send"
    ID string          `json:"id,omitempty"` // correlation id (req/ack)
    D  json.RawMessage `json:"d,omitempty"`  // typed payload, decoded by T
}
```

Routing: the server's event router switches on `T`, decodes `D` into the
matching Go struct, and dispatches. The client does the same. Unknown `T`
is logged and ignored (forward-compat).

Naming convention: `domain:verb`, lowercase. `c2s` = client→server,
`s2c` = server→client.

## 2.2 Connection lifecycle

| `t`            | dir | payload          | meaning |
|----------------|-----|------------------|---------|
| `hello`        | s2c | `Hello`          | sent on connect: protocol version, server caps |
| `auth`         | c2s | `Auth`           | (multi-user, Phase 7) token/credentials |
| `auth:ok`      | s2c | `AuthOK`         | session established |
| `init`         | s2c | `InitState`      | full snapshot: networks, channels, members |
| `ping`/`pong`  | both| `Ping`           | app-level keepalive (browser sleep detection) |
| `error`        | s2c | `WireError`      | correlated to a `c2s` `id` when applicable |

```go
type Hello struct {
    Protocol int      `json:"protocol"`     // bump on breaking change
    Server   string   `json:"server"`       // "stugan/dev"
    Caps     []string `json:"caps"`          // e.g. ["search","uploads","push"]
}

type InitState struct {
    User     UserDTO       `json:"user"`
    Networks []NetworkDTO  `json:"networks"`
}

type WireError struct {
    Code    string `json:"code"`             // "bad_request","not_found",...
    Message string `json:"message"`
}
```

## 2.3 State snapshot DTOs

These are wire projections of the `core` domain types (§1.2), decoupled so
the wire format can evolve independently.

```go
type UserDTO struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}

type NetworkDTO struct {
    ID       string       `json:"id"`
    Name     string       `json:"name"`
    Nick     string       `json:"nick"`
    State    string       `json:"state"`     // "disconnected"|"connecting"|"registered"
    Channels []ChannelDTO `json:"channels"`
}

type ChannelDTO struct {
    Name      string      `json:"name"`
    Kind      string      `json:"kind"`       // "channel"|"query"|"status"
    Topic     string      `json:"topic"`
    Members   []MemberDTO `json:"members,omitempty"`
    Unread    int         `json:"unread"`
    Highlight int         `json:"highlight"`
}

type MemberDTO struct {
    Nick  string `json:"nick"`
    Modes string `json:"modes"`               // "@","+",""
    Away  bool   `json:"away"`
}

type MessageDTO struct {
    ID      string            `json:"id"`
    Network string            `json:"network"`
    Buffer  string            `json:"buffer"`
    Time    string            `json:"time"`    // RFC3339 (server-time)
    From    string            `json:"from"`
    Kind    string            `json:"kind"`    // "privmsg"|"notice"|"action"|"join"|...
    Text    string            `json:"text"`
    Self    bool              `json:"self"`
    Tags    map[string]string `json:"tags,omitempty"`
}
```

## 2.4 Client → server events

| `t`              | payload         | meaning |
|------------------|-----------------|---------|
| `msg:send`       | `MsgSend`       | send text/command to a buffer |
| `buffer:open`    | `BufferRef`     | open a query / focus a buffer |
| `buffer:close`   | `BufferRef`     | close a query or part a channel |
| `buffer:read`    | `BufferRead`    | mark read up to a message (read-marker) |
| `backlog:fetch`  | `BacklogFetch`  | request older history for a buffer |
| `search`         | `SearchReq`     | FTS5 search |
| `net:add`        | `NetworkConfigDTO` | add+connect a network |
| `net:connect`    | `NetworkRef`    | connect/disconnect a configured network |
| `typing`         | `TypingReq`     | IRCv3 typing indicator out |

```go
type MsgSend struct {
    Network string `json:"network"`
    Buffer  string `json:"buffer"`           // channel/query; "/cmd ..." allowed
    Text    string `json:"text"`             // may be a slash-command
}

type BufferRef struct {
    Network string `json:"network"`
    Buffer  string `json:"buffer"`
}

type BacklogFetch struct {
    Network string `json:"network"`
    Buffer  string `json:"buffer"`
    Before  string `json:"before"`           // RFC3339 cursor; "" = latest
    Limit   int    `json:"limit"`
}

type SearchReq struct {
    Query   string `json:"query"`
    Network string `json:"network,omitempty"` // optional scope
    Buffer  string `json:"buffer,omitempty"`
}
```

`msg:send` whose `Text` begins with `/` is parsed as a command (server-side
alias expansion + plugin `hook_command` + built-ins), so commands and chat
share one path.

## 2.5 Server → client events

| `t`              | payload         | meaning |
|------------------|-----------------|---------|
| `msg`            | `MessageDTO`    | a new (committed) message in a buffer |
| `msg:ack`        | `MsgAck`        | echo-message confirmation for a sent line |
| `buffer:add`     | `ChannelDTO`    | a channel/query opened |
| `buffer:remove`  | `BufferRef`     | a buffer closed/parted |
| `buffer:update`  | `ChannelDTO`    | topic/unread/highlight/members changed |
| `net:update`     | `NetworkDTO`    | connection state / nick changed |
| `member:join`    | `MemberEvent`   | someone joined |
| `member:part`    | `MemberEvent`   | someone left/quit |
| `member:nick`    | `NickEvent`     | nick change |
| `backlog`        | `BacklogResp`   | a page of history (answers `backlog:fetch`) |
| `search:result`  | `SearchResp`    | search results |
| `typing`         | `TypingEvent`   | someone is typing |
| `notify`         | `Notify`        | highlight/mention worthy of a push/notification |

```go
type BacklogResp struct {
    Network  string       `json:"network"`
    Buffer   string       `json:"buffer"`
    Messages []MessageDTO `json:"messages"`   // oldest→newest
    More     bool         `json:"more"`        // older history exists
}

type MemberEvent struct {
    Network string    `json:"network"`
    Buffer  string    `json:"buffer"`
    Member  MemberDTO `json:"member"`
}

type Notify struct {
    Network string `json:"network"`
    Buffer  string `json:"buffer"`
    From    string `json:"from"`
    Text    string `json:"text"`
}
```

## 2.6 Sync & ordering rules

- On (re)connect the server sends `hello` → `init` (full snapshot), then a
  live stream of incremental `s2c` events. The client never assumes it
  missed nothing; `init` is authoritative and replaces local state.
- Backlog is **pull**: the client asks via `backlog:fetch` and renders the
  `backlog` page. Live messages arrive as `msg`. The client de-dupes by
  `MessageDTO.ID`.
- `id` correlation: `c2s` events that expect a definite answer
  (`backlog:fetch`, `search`, `net:add`) carry an `id`; the matching `s2c`
  reply echoes it. Fire-and-forget events (`typing`) omit it.

## 2.7 TypeScript mirror (shape)

```ts
// client/src/proto/events.ts — kept in sync with internal/proto
export interface Envelope<T = unknown> { t: string; id?: string; d?: T }

export interface MessageDTO {
  id: string; network: string; buffer: string; time: string;
  from: string; kind: MsgKind; text: string; self: boolean;
  tags?: Record<string, string>;
}
export type MsgKind = "privmsg" | "notice" | "action" | "join" | "part" | "system";
// ...one interface per payload above, plus a discriminated union of all events.
```

## 2.8 Open questions for sign-off

1. Single multiplexed socket (proposed) vs. one socket per network?
   I propose **one socket**, network is a field on every event.
2. Envelope shape: terse `{t,id,d}` (proposed) vs. verbose
   `{type,correlationId,data}`? Terse keeps frames small.
3. `protocol` integer version in `hello` — OK as the compatibility gate?
4. Should backlog be pull-based (proposed) or should the server push a
   fixed last-N on `buffer:open`? I lean pull + an initial small page.
