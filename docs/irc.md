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
`standard-replies`, `draft/chathistory`, `draft/message-redaction`. The
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

Because translation is pure, it is exhaustively table-tested in
`translate_test.go` without a live server.
</content>
