# IRC server front-end (bouncer mode) — design

**Status:** draft / proposal. Nothing here is built yet.

**Goal:** let a normal IRC client (irssi, WeeChat, mIRC, Textual) connect *to
stugan* and use it as a bouncer/relay. stugan already holds the upstream IRC
connections 24/7 and buffers history; this exposes that state over the IRC
protocol so a desktop client can attach, see the current channels/members,
read missed backlog, and send/receive — exactly the role ZNC and soju fill.

This is **additive**. It does not change `internal/core`, `internal/irc`,
`internal/server`, or `internal/proto`. It adds one new package,
`internal/ircserver`, that consumes the existing engine the same way the
WebSocket server does, plus a few lines of wiring in `cmd/stugan`.

---

## 1. Why this fits stugan's architecture

The hard rule (see `docs/layout.md`) is that `core.Engine` is transport- and
GUI-independent: it owns the domain tree and broadcasts every committed line
to a set of `core.Sink`s. The browser server (`internal/server`) is just *one
consumer* of that bus. An IRC-server listener is a **second consumer of the
same bus**, mirror-image to the browser bridge:

| Direction | Browser (`internal/server`, exists) | IRC client (`internal/ircserver`, new) |
|-----------|--------------------------------------|-----------------------------------------|
| inbound (c2s) | WS JSON → `route()` → `Engine.SendInput()` | raw IRC line → parse → `Engine.SendInput()` |
| outbound (s2c) | `userSink` → `proto.Frame` → WS JSON | `ircSink` → IRC wire line → TCP socket |
| identity | session cookie → `Hub.Tenant(user)` | `PASS`/SASL → `Hub.Login` → `Hub.Tenant(user)` |
| snapshot | `proto.TInit` from `Engine.Snapshot()` | JOIN/NAMES/TOPIC replay from `Engine.SnapshotNetwork()` |
| backlog | `proto.TBacklog` from `History.Backlog` | CHATHISTORY / play-on-attach from `History.Backlog` |

Everything load-bearing already exists and is reused unchanged:

- **State tree** — `core.User → Network → Channel → Member` (`internal/core/types.go`).
  This *is* the bouncer's view of the world. Read via `Engine.Snapshot()` /
  `SnapshotNetwork()` (deep copies, race-safe from any goroutine).
- **Send path** — `Engine.SendInput(network, buffer, text)` is already
  goroutine-safe and handles slash-commands, aliases, `//` escaping, and
  plugin hooks. An irssi `PRIVMSG #x :hi` becomes the same `EvMessageOut` a
  browser send does.
- **Sink fan-out** — `core.Sink` (`Print`, `NetworkChanged`, `NetworkRemoved`,
  `ChannelList`, `Typing`). We add a *new implementer*, not a new method, so
  no existing sink changes.
- **History** — `server.History` (`Backlog`, `BacklogAround`, `Search`),
  implemented by `store.Store`, for play-on-attach.
- **Auth** — `auth.Users` (bcrypt) and the `server.Hub` interface
  (`cmd/stugan/hub.go`) already resolve a credential to a per-user `Tenant`
  (engine + history). The IRC listener takes the same `Hub`.
- **Plugins** — hooks fire inside the engine loop, so anything sent or
  received through a relayed client passes through the same `hook_message` /
  `hook_command` pipeline.

---

## 2. The one real design decision: mapping N networks onto IRC

A stugan user holds **many** upstream networks (`User.Networks`). A single IRC
client connection is, by protocol, attached to **one** network. We must pick
how an attaching client selects which network(s) it sees. Three options:

### Option A — ZNC-style `user/network` login on one port *(recommended)*

One listen port. The client authenticates as `username/networkname` (the
network is appended to the login name in `PASS` or the SASL authcid), and that
TCP connection is bound to exactly one stugan network. To follow three
networks, irssi opens three "server" entries, each a separate connection.

- **Pros:** preserves IRC's "one connection = one network" model exactly;
  channel names never collide across networks; client UIs (server tree,
  `/window`) work as users expect; identical to ZNC/soju, so existing client
  docs apply.
- **Cons:** a client must define one connection per network.
- **Login forms** (all map to a `(user, password, network)` triple):
  - `PASS networkname:password` then `NICK`/`USER`, or
  - `PASS username/network:password`, or
  - SASL PLAIN with authcid `username/network` (soju's convention).

### Option B — network-prefixed channels on one connection

A single connection shows every network at once; buffers are namespaced, e.g.
`libera/#go` and `oftc/#chan`. Status/query buffers likewise prefixed.

- **Pros:** one client connection for everything.
- **Cons:** non-standard buffer names confuse clients; nick/channel collisions
  across networks; private queries and numerics get messy; tab-completion and
  `/join` need rewriting. High friction, low payoff.

### Option C — one OS port per network

A distinct listen port per network. Simple to reason about, ugly to operate
(port sprawl, firewalling, config churn as networks are added/removed from the
GUI). Rejected.

**Recommendation: Option A.** It's the model every IRC client already
understands and keeps the rest of this design simple. The network selector
lives entirely in the registration handshake; the rest of the package is
network-scoped from that point on.

> Open question for the owner: which login encoding to *primarily* document —
> `PASS user/network:pass` (ZNC) vs SASL authcid `user/network` (soju). Suggest
> supporting both; document SASL as primary since it survives TLS-only setups
> cleanly.

---

## 3. Package layout

```
internal/ircserver/
  server.go      // Listener: TCP/TLS accept loop, ListenAndServe(ctx, addr), holds the Hub
  session.go     // per-connection state machine: registration, caps, attached network
  parse.go       // raw line ⇄ struct (prefix, command, params, tags) — IRC message codec
  inbound.go     // registered-client command → Engine call (the reverse of irc/translate.go)
  sink.go        // ircSink: core.Sink → IRC wire lines for one (user, network, session)
  replay.go      // on-attach state synthesis (welcome numerics, JOIN/NAMES/TOPIC) + backlog
  isupport.go    // 005 tokens, capability set we advertise downstream
```

Wiring lives in `cmd/stugan`: a new `[ircserver]` config block and one more
goroutine alongside `srv.ListenAndServe` in `main.go`.

The package depends on `core` (public API only), the `Hub`, and `server.History`
— never on girc, the WS lib, or proto. Same one-directional rule as everything
else.

---

## 4. Inbound: client → engine

A registered client's commands map mostly onto the existing input path, which
means we get plugin hooks, aliases, and built-in commands for free:

| Client sends | Action |
|--------------|--------|
| `PRIVMSG #chan :text` | `Engine.SendInput(net, "#chan", "text")` |
| `PRIVMSG #chan :\x01ACTION waves\x01` | `Engine.SendInput(net, "#chan", "/me waves")` |
| `NOTICE nick :text` | `Engine.SendInput(net, "nick", "/notice nick text")` |
| `JOIN #chan [key]` | `Engine.SendInput(net, "*status", "/join #chan [key]")` |
| `PART #chan [:reason]` | `Engine.SendInput(net, "*status", "/part #chan [reason]")` |
| `TOPIC #chan :new` | `/topic` via SendInput |
| `NICK newnick` | `/nick newnick` via SendInput (post-registration) |
| `MODE`, `WHOIS`, `WHO`, `KICK`, … | `/quote`-style passthrough via SendInput, or a dedicated raw bridge (see §8 gap) |
| `LIST [query]` | `Engine.ListChannels(net, query)` (structured result via the `ChannelList` sink) |
| `PING :x` | reply `PONG :x` **locally** — never forwarded upstream |
| `QUIT` | **detach this client only**; the upstream network stays connected (core bouncer behavior) |
| `CAP`, `AUTHENTICATE`, `PASS`, `USER` | registration handshake, handled locally (§6) |

Routing slash-commands through `SendInput` is the key simplification: `/join`,
`/part`, `/msg`, `/nick`, `/topic`, etc. are already built-ins (or plugin
commands), so `inbound.go` is mostly a thin "IRC verb → slash string"
translator, not a second command engine.

`QUIT` semantics are the single most important behavioral difference from a
real server: it tears down the *client session*, not the upstream link. That's
what makes it a bouncer.

---

## 5. Outbound: the `ircSink`

`Server.Sink(userID)` returns a `*userSink` for the browser; analogously
`ircserver` registers one `core.Sink` per **attached session**, scoped to that
session's `(user, network)`. It implements all five `Sink` methods:

- `Print(m)` — if `m.Network` != the session's bound network, **drop**.
  Otherwise format `m` into an IRC line addressed to the client and write it:
  - `MsgPrivmsg` → `:from PRIVMSG <buffer> :text`
  - `MsgAction` → `:from PRIVMSG <buffer> :\x01ACTION text\x01`
  - `MsgNotice` → `:from NOTICE <buffer> :text`
  - `MsgJoin/Part/Quit/Nick/Topic` → the corresponding membership verb
  - `MsgSystem` → `:server NOTICE <buffer-or-*> :text` (status/numeric text)
  - carry `m.Time` as a `@time=` tag when the client negotiated `server-time`
- `NetworkChanged(n)` — diff against the session's last-known snapshot and emit
  membership deltas the *raw* `Print` stream didn't already cover (e.g. mode
  changes, our own nick change). For v1 this can be minimal since most churn
  already arrives as `Print` lines with `MsgJoin/Part/...` kinds.
- `NetworkRemoved(id)` — if it's the bound network, send `ERROR` and close.
- `ChannelList(net, items)` — emit `321/322/323` (RPL_LIST*) to the client.
- `Typing(...)` — emit `+typing` TAGMSG if the client negotiated `message-tags`,
  else drop.

### Self / echo handling (subtle, call out explicitly)

When the attached client sends a PRIVMSG, the engine emits a committed copy on
the bus (`Self=true`, or the upstream `echo-message` echo). A real IRC server
does **not** echo your own PRIVMSG back unless you negotiated `echo-message`.
So the sink must:

- **Suppress** the echo to the *originating* session, unless that session
  negotiated `echo-message` (then send it with the same tags).
- **Forward** it to any *other* attached sessions of the same user+network, so
  a second device stays in sync (this is the ZNC `self-message` behavior).

The `core.Sink` fan-out doesn't carry "which client originated this," so the
session layer needs a small correlation mechanism — e.g. the session tags
outbound text it just sent and the sink matches-and-drops the echo for that
session only. **v1 simplification:** single-attach assumption — suppress all
`Self` lines to IRC clients; the client shows its own sent line locally. Revisit
for multi-device sync.

---

## 6. Registration & on-attach state replay (the hard part)

This is the chunk with no equivalent on the browser side. The browser receives
`proto.TInit` as one JSON snapshot; IRC has no snapshot frame — the client
only understands a *sequence of events*, so we must **play the present into
existence**.

**Registration handshake** (`session.go`), following RFC1459 + IRCv3:

1. Read `CAP LS`, `PASS`, `NICK`, `USER`, optional `AUTHENTICATE` (SASL).
2. Resolve credentials: parse `user/network` + password, call
   `Hub.Login(user, pass)` then `Hub.Tenant(user)`; bind the session to the
   named network (404 / `ERROR` if unknown). SASL PLAIN reuses the same
   `Hub.Login`.
3. Negotiate caps we support (§7), `CAP ACK`, `CAP END`.
4. Send welcome burst: `001`–`005` (with our `ISUPPORT`), `375/372/376` MOTD
   (synthetic, e.g. "stugan bouncer — network <name>").

**State replay** — from `Engine.SnapshotNetwork(net)`:

For each `Channel` of kind `KindChannel` in the snapshot:
- `:<ournick> JOIN <#chan>` (make the client believe it just joined)
- `332` topic + `333` topic-set metadata (if `Channel.Topic != ""`)
- `353` NAMES with prefixes from `Member.Modes`, then `366` end-of-names

The client now shows every channel the bouncer is in, fully populated.

**Backlog play-on-attach** — for each buffer, optionally pull
`History.Backlog(ctx, net, buffer, ...)` and replay as PRIVMSG/NOTICE lines
with `@time=` tags (requires the client negotiated `server-time`; otherwise
prefix `[HH:MM]` into the text or skip). Two delivery modes:
- **CHATHISTORY** (`draft/chathistory`) — if the client requests it, answer
  `CHATHISTORY LATEST/BEFORE` from `History.Backlog`. Preferred; lossless.
- **ZNC-style buffer dump** — replay last N lines per buffer right after each
  channel's NAMES burst. Simpler, works with dumber clients.

`Member` currently has no full `user@host`, so replayed/relayed prefixes use
`nick` (or a synthesized `nick!stugan@<network>`); see §8.

---

## 7. Capabilities & ISUPPORT we advertise *downstream*

We are now the *server* in the client's eyes, so we advertise our own cap set
(independent of what we negotiated upstream in `internal/irc`):

- **Caps:** `server-time` (essential for backlog), `message-tags`,
  `echo-message` (for self-sync), `multi-prefix`, `away-notify`,
  `account-notify` (if we track it), `batch` + `draft/chathistory` (for
  history), `sasl` (PLAIN).
- **ISUPPORT (`005`):** `CHANTYPES=#&`, `PREFIX=(ov)@+`, `NETWORK=<name>`,
  `CASEMAPPING=rfc1459`, `NICKLEN`, `CHANNELLEN`, etc. Mirror what we know of
  the upstream network where possible (we can stash upstream `005` in
  `internal/irc` later; v1 can advertise a safe baseline).

---

## 8. Known fidelity gaps (be honest about these)

These are limitations of relaying through a normalized core, not blockers, but
they should be documented for users:

1. **No full hostmasks.** `core.Member` tracks `Nick`, `Account`, `Modes`,
   `Away` — not `user@host`. Relayed/replayed lines will carry bare nicks or a
   synthesized host. Most clients tolerate this; WHOIS-quality host info is
   lost. *Fix later:* extend the inbound translator + `Member` to retain
   hostmasks.
2. **Numerics are flattened.** The engine converts WHOIS/WHO/WHOWAS replies to
   `MsgSystem` *text* lines (`applyNumeric` in `engine.go`), so we can't
   reconstruct structured `311/312/318` numerics for a relayed WHOIS. v1: relay
   them as `NOTICE` text to the status buffer. `LIST` is fine — it has a
   structured `ChannelList` sink path. *Fix later:* a structured numeric event.
3. **MODE/KICK/INVITE** that aren't built-in slash-commands need a raw
   passthrough (`/quote`) or dedicated handlers; verify which built-ins exist
   before relying on `SendInput`.
4. **One network per connection** (Option A) is a deliberate constraint, not a
   bug — but worth stating in user docs.

---

## 9. Concurrency & wiring

- `ircserver.Server` holds the `Hub`; `ListenAndServe(ctx, addr)` runs an
  accept loop, one goroutine per client connection (read pump), with a write
  pump per connection. Same shape as `internal/server`'s client.
- A session registers its `ircSink` on the tenant engine **at attach** and
  removes it **at detach**. ⚠️ `Engine.AddSink` is documented as *call before
  Run; not safe once the loop is running*. The IRC server attaches sinks at
  runtime, so we need either:
  - a small `Engine.AddSink`/`RemoveSink` made loop-safe (guard `e.sinks` with
    `e.mu`, snapshot under lock in `broadcast`/`notifyNetwork`), **or**
  - a single long-lived per-user `ircSink` (registered at startup like
    `userSink`) that fans out to that user's currently-attached sessions via
    the package's own registry (mirrors how `server` keeps
    `clients map[string]map[*client]struct{}` and `routeToUser`).

  **Recommended:** the second approach — one sink per user registered at
  startup, an internal session registry for fan-out. It needs **zero core
  changes** and matches the existing `server` pattern exactly. (The first
  approach is cleaner long-term but touches `core`.)
- `cmd/stugan/main.go`: after `hub.registerSinks(srv)`, also
  `ircsrv := ircserver.New(hub, ...)`, register its per-user sinks, and add
  `wg.Go(func(){ ircsrv.ListenAndServe(ctx, cfg.IRCServer.Listen) })`.
- Config: new block, e.g.
  ```toml
  [ircserver]
  listen = "127.0.0.1:6697"
  tls = true
  cert_file = "..."   # required for TLS
  key_file  = "..."
  ```
  Disabled when `listen` is empty (back-compatible default off).

---

## 10. Phasing

1. **Skeleton + handshake:** TCP listener, IRC line codec (`parse.go`),
   registration (`PASS`/`NICK`/`USER`/`CAP`), auth via `Hub`, bind to one
   network, welcome burst. Verify irssi connects and shows MOTD.
2. **State replay:** JOIN/TOPIC/NAMES from snapshot. irssi shows populated
   channels.
3. **Live relay:** `ircSink` Print → wire; inbound PRIVMSG/JOIN/PART via
   `SendInput`. Two-way chat works against a real upstream (test against Libera,
   per `CLAUDE.md`'s verification approach, with irssi as the downstream client).
4. **Backlog:** play-on-attach (ZNC-style), then `draft/chathistory`.
5. **Polish:** `server-time`, `echo-message`/self-sync for multi-attach,
   `LIST`, typing, `ISUPPORT` mirrored from upstream, TLS + SASL.

---

## 11. Verification

No mock IRC server exists (`CLAUDE.md`). Test the same way as the rest of the
project: run the daemon against Libera with a throwaway nick into a low-traffic
channel, then **attach a real irssi/WeeChat to stugan's IRC port** and assert
the round trip (join shows members, a message in irssi appears upstream and
vice-versa, reconnect replays backlog). Unit-test `parse.go` (codec) and
`inbound.go` (verb→slash mapping) table-driven with `-race`; the listener can
be tested in-process with a `net.Pipe`-backed fake client, mirroring how
`internal/server` uses `httptest` + an in-process engine.

---

## 12. Effort estimate

Phases 1–3 (a usable read/write bouncer for one network) are a focused few
days. Backlog + multi-attach polish (4–5) add roughly the same again. The
reason it's not larger: the state tree, event bus, history, auth, and plugin
system are reused wholesale — the new code is a protocol adapter, not a second
IRC core.
