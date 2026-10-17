# The IRC layer

`internal/irc` is the **only** package that imports girc
(`github.com/lrstanley/girc`). It implements `core.IRCConn` and translates
girc's wire events into normalized `core.Event`s. Nothing girc-shaped escapes
this package — that is what lets the IRC stack be swapped without touching
`core`. See the capability matrix and roadmap in [ircv3.md](ircv3.md).

## The connection (`conn.go`)

`Conn` implements `core.IRCConn`:

```go
func New(opts Options, handler core.ConnHandler) (*Conn, error)

func (c *Conn) Connect(ctx context.Context) error   // blocking; cancel ctx to close
func (c *Conn) SendRaw(line string) error
func (c *Conn) Message(target, text string) error   // PRIVMSG
func (c *Conn) Caps() []string                       // negotiated IRCv3 caps
func (c *Conn) CurrentNick() string
func (c *Conn) Close() error
```

`New` parses `Addr` (`host:port`, defaulting to 6697 with TLS, 6667 without)
and builds a `girc.Config` with the nick/user/realname, server password, and
the requested caps. Inbound girc callbacks are normalized and pushed to the
`core.ConnHandler` (the Engine); raw lines flow out via `SendRaw`/`Message`.

### TLS, SASL, and CertFP

- **TLS**: when enabled, a `tls.Config` is built with `MinVersion = TLS 1.2`
  and an explicit `ServerName` (girc needs it set).
- **SASL PLAIN**: a non-empty `SASLUser`/`SASLPass` configures
  `girc.SASLPlain`.
- **SASL EXTERNAL / CertFP**: `SASLExternal = true` uses `girc.SASLExternal`
  and presents a client certificate. `CertPEM` is a PEM bundle with the
  certificate **and** private key concatenated, loaded via
  `tls.X509KeyPair(certPEM, certPEM)`.

### Requested capabilities

The baseline girc enables, plus stugan's additions via `SupportedCaps`, cover:
`server-time`, `message-tags`, `echo-message`, `account-notify`,
`extended-join`, `account-tag`, `away-notify`, `multi-prefix`,
`userhost-in-names`, `chghost`, `setname`, `invite-notify`, `labeled-response`,
`standard-replies`, `draft/chathistory` (and the ratified `chathistory`),
`draft/message-redaction`, `draft/multiline`, and `draft/read-marker` /
`read-marker`. The
negotiated set is reported per-network via `Caps()` → `NetworkDTO.Caps`, so the
client can gate cap-dependent UI.

### echo-message handling

girc delivers **echo-message events only to the `ALL_EVENTS` handler**, not the
per-command handlers. `Conn` therefore registers a dedicated `ALL_EVENTS`
handler gated on `e.Echo` to capture our own sent PRIVMSG/NOTICE coming back.
An echo is translated as a `message_in` event with `Self = true` (a display
copy), never re-sent. When `echo-message` is negotiated the engine skips local
echo so the server's copy is the only one shown.

### Reconnect

The connection runs to completion and returns; reconnect, backoff, Perform,
and auto-join sequencing are driven by the Engine, which re-dials through the
`Connector`. Perform lines run one second apart; configured channels join one
second after the last line so service auth/modes settle first. IRC connections
are independent of browser sessions — they persist while no client is attached.

## stugan-engine relay

With `[engine]` configured (see [engine.md](engine.md)), `Options.Relay` routes
the connection through stugan-engine (`relay.go`). girc still runs normally,
but it dials a `*enginelink.Session` (a `net.Conn` backed by the engine link)
via `DialerConnect`, with `SSL` and STS off, because the engine owns the
socket and its TLS. Translation and every handler stay unchanged.

- **Fresh connect:** the engine dials and lines flow live. Each relayed line
  carries `@stugan/seq=N` and, if the server sent none, a `time` tag with the
  engine's receive time.
- **Attach:** the engine already holds a registered socket. The session feeds
  girc the recorded burst (CAP/SASL/001–005, with 001 addressed to the live
  nick), tagged `@stugan/replay`, then a marker `PING`. Until girc answers the
  marker, everything it writes (its own `CAP LS`/`NICK`/`USER`…) is swallowed,
  and the outbound queue holds core's writes. The marker handler emits one
  `EvConnect{Resumed: true, Args: channels}` ahead of any backlog line, re-arms
  MONITOR, and queues `NAMES`/`TOPIC` per channel at background priority. The
  replayed welcome's `CONNECTED` is skipped, so perform and autojoin don't
  re-run. If registration finished while no stugan was attached
  ("unattended"), the `CONNECTED` runs them as usual.
- **Acks:** handlers for one girc event finish before the next starts, so when
  line N reaches the `ALL_EVENTS` handler, line N-1 is fully handled. When girc
  has read everything, the session hands it an ack probe (`STUGANACK`). Acks go
  out through the engine's `Sync` barrier, i.e. only after core has committed
  the events. Replay and relay tags never reach `Message.Tags`.
- **Close vs Detach:** `Close` tells the engine to end the socket (remove,
  disconnect, edit). `Detach`, called by core on shutdown, and girc closing
  its "socket" both only detach. Rejected SASL credentials end the socket too.
- **Fingerprint:** a digest of the settings core reconnects for (address,
  fallbacks, TLS, skip-verify, user, realname, SASL, server password, cert).
  The engine re-attaches only on a match. The nick is deliberately excluded.

## Event translation (`translate.go`)

`toEvent(network string, e *girc.Event, self string) (core.Event, bool)` is a
**pure function** mapping a girc event to a `core.Event` (returning `false` for
events stugan does not model). Highlights:

- **PRIVMSG / NOTICE / CTCP ACTION** → `message_in`. The buffer is the channel
  for channel targets, the `status` buffer for server/pre-registration lines,
  otherwise a query keyed by the sender (inbound DM) or target (echoed DM). It
  carries `ID` (msgid tag), `From`, `Account` (account-tag), `Kind`, `Text`,
  `Tags`, and `Self` (`e.Echo`). A **non-ACTION CTCP** (VERSION/PING/TIME/… or a
  CTCP reply) is dropped rather than translated, so its raw `\x01`-framed payload
  never shows as garbled text; girc's built-in CTCP handler answers the standard
  requests.
- **JOIN / PART / QUIT / NICK / AWAY** → the matching membership/presence
  events; JOIN picks up the account from extended-join or the account tag.
- **NAMES (353)** → `names` with the parsed member list (prefixes split into
  modes). **TOPIC** → `topic`.
- **LIST (322) / LISTEND (366)** → `list_item` (channel, user count, topic) and
  `list_end`, accumulated by the engine for the channel browser.
- **WHOIS / WHOWAS / WHO and error numerics** → `numeric`, carrying the subject
  nick (for routing the reply back to the buffer that issued the command), a
  formatted line, and the numeric code.
- **Standard replies** (`FAIL`/`WARN`/`NOTE`) → rendered as `system` lines like
  `[COMMAND] code context: description`.
- **TAGMSG**: `+typing` → `typing`; `+draft/react` + `+draft/reply` → `react`
  (target msgid + emoji).
- **REDACT** (`draft/message-redaction`) → `redact` (target msgid + reason).
- **KICK / MODE / INVITE / ACCOUNT** → `kick`, `mode` (membership prefix
  changes split out from channel-setting flags), `invite`, and `account`.
- **MONITOR (730/731)** → `monitor` (one event per numeric, listing the nicks).
- **BATCH `draft/multiline`** → buffered and reassembled into a single
  `message_in` before translation (see [ircv3.md](ircv3.md)).

Because translation is pure, it is exhaustively table-tested in
`translate_test.go` without a live server.
</content>
