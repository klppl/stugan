# IRC Bouncer Server (Native Client Access)

stugan includes a built-in IRC bouncer server that allows standard IRC clients
(such as WeeChat, HexChat, irssi, Textual, mIRC, or mobile IRC apps) to connect
directly to stugan over TCP/TLS and use it as a persistent bouncer and relay.

Because stugan already manages 24/7 IRC connections, logs history in SQLite, and
executes Lua plugins inside its engine, connecting a native IRC client exposes
that exact persistent session over the IRC protocol with full state replay and
backlog sync.

---

## 1. Features

- **Multi-Network Multiplexing**: Connect to separate upstream networks on a single bouncer port using `user/network` selectors or SASL `PLAIN`.
- **State Replay on Attach**: Synthetic welcome numerics (`001`–`005`, `422`), live channel joins (`JOIN`), channel topics (`332`/`333`), and chunked membership lists (`353 RPL_NAMREPLY` / `366 RPL_ENDOFNAMES`) accurately populate client state.
- **Backlog & History Sync**: Replays recent history on attach with `@time=` tags and supports the IRCv3 `draft/chathistory` standard (`LATEST`, `BEFORE`, `AFTER`, `AROUND`, `BETWEEN`, `TARGETS`).
- **IRCv3 Support**:
  - `server-time`
  - `message-tags` (typing indicators, reactions, and deletions from upstream are delivered as `TAGMSG` with `+typing`, `+draft/react`/`+draft/reply`, and `+draft/delete` — see the note under [Client commands](#7-client-commands))
  - `echo-message` & `znc.in/self-message` (multi-client synchronization)
  - `batch`
  - `draft/chathistory`
  - `draft/read-marker` (`MARKREAD` read position synchronization across clients)
  - `soju.im/bouncer-networks` & `soju.im/bouncer-networks-notify`
  - `sasl` (`PLAIN`)
- **TLS by Default**: Optional custom certificates or automatic self-signed ECDSA certificate generation with SHA-256 fingerprint logging.
- **Plugin Integration**: Chat commands and messages sent from downstream IRC clients pass through stugan's Lua plugin hooks and alias system.
- **Slow-client isolation**: Each attached client gets its own bounded outbound queue (4096 lines) drained by a writer goroutine with a 30s write deadline, so a stalled client can never hold up the engine or other clients. A client that falls further behind is disconnected and can reattach for a fresh replay. Network connect/disconnect notices are sent only on state transitions.
- **Client Detach**: When an attached IRC client issues `/quit`, only the client's local session disconnects; upstream IRC networks remain connected 24/7 in stugan.

---

## 2. Configuration

Add the `[ircserver]` block to your `config.toml`:

```toml
[ircserver]
# Port to listen on (e.g. ":6697" or "0.0.0.0:6697").
# Omitted or empty disables the bouncer server.
listen = "0.0.0.0:6697"

# Enable TLS encryption (default: true).
tls = true

# Optional paths to custom PEM certificate and key files.
# If omitted and TLS is true, stugan auto-generates a self-signed ECDSA certificate
# in the data directory and logs its SHA-256 fingerprint on startup.
cert_file = ""
key_file  = ""

# Maximum number of historical messages to replay per buffer upon client attach (default: 50).
max_playback = 50
```

### Per-account access (web UI)

Once the listener is configured, each account controls whether it accepts IRC
clients under **Settings → IRC Clients**. Access is on by default; turning it
off refuses new bouncer logins for that account and immediately disconnects any
attached IRC clients (upstream networks stay connected). The same page shows
the server address, port, TLS fingerprint, and the exact username to use for
each network, with ready-made WeeChat and irssi commands.

---

## 3. Connecting IRC Clients

### Login Format

A single stugan account may own multiple IRC networks (e.g. `libera`, `oftc`). IRC protocol connections are bound to one network at a time.

When authenticating, provide your credentials in one of the following formats:

#### A. SASL PLAIN (Recommended)
Set SASL username to `<username>/<network>` (or simply `<username>` if only one network is configured), and password to your stugan account password:
- **SASL Username**: `alice/libera`
- **SASL Password**: `mypassword`

#### B. Server Password (`PASS`)
Provide `<username>/<network>:<password>` in the server password field:
- **Server Password**: `alice/libera:mypassword`

#### C. Username field + password
Most clients also work with the network in the plain username (ident) field and
just your password as the server password:
- **Username**: `alice/libera`
- **Server Password**: `mypassword`

Clients that support `soju.im/bouncer-networks` (Halloy, Goguma, gamja, senpai)
need only one server entry: log in as `alice` (SASL or `alice:mypassword`)
without a network and every network appears automatically.

#### D. Single-User Mode (No `[[users]]` configured)
If running stugan in unauthenticated single-user mode:
- **SASL Username / Server Password**: `<network>` (or leave blank if only 1 network exists).

---

## 4. Client Configuration Examples

### WeeChat

```sh
/server add stugan-libera your-server.com/6697 -ssl
/set irc.server.stugan-libera.ssl_fingerprint "<SHA256_FINGERPRINT>" # if using self-signed cert
/set irc.server.stugan-libera.sasl_mechanism plain
/set irc.server.stugan-libera.sasl_username "alice/libera"
/set irc.server.stugan-libera.sasl_password "mypassword"
/set irc.server.stugan-libera.autoconnect on
/connect stugan-libera
```

### irssi

```
/SERVER ADD -ssl -ssl_cert <optional> -network stugan-libera your-server.com 6697 alice/libera:mypassword
/CONNECT stugan-libera
```

### HexChat / Textual / Generic Desktop Clients
- **Server**: `your-server.com`
- **Port**: `6697`
- **Use SSL/TLS**: Enabled
- **Server Password**: `alice/libera:mypassword` (or use SASL PLAIN with username `alice/libera`)

---

## 5. TLS & Certificate Pinning

When TLS is enabled without explicit `cert_file` and `key_file` paths, stugan generates an ECDSA P-256 certificate valid for 10 years and writes `bouncer_cert.pem` and `bouncer_key.pem` to the data directory.

On startup, stugan logs the certificate's SHA-256 fingerprint:
```
INFO irc bouncer listening with TLS addr=0.0.0.0:6697 tls_source=self-signed fingerprint_sha256=AB:CD:EF:... (self-signed — pin this fingerprint in your IRC client)
```

Clients configured to verify certificates can pin this fingerprint directly.

---

## 6. Control Connections & Bouncer Networks

If a client negotiates the `soju.im/bouncer-networks` capability, it can connect without binding to a specific network:
- Running `BOUNCER LISTNETWORKS` returns a structured listing of all upstream networks and their real-time connection status.
- Real-time network state updates are broadcast downstream with `soju.im/bouncer-networks-notify`.
- A client then opens one connection per network with `BOUNCER BIND <netid>` before registering.
- `BOUNCER ADDNETWORK`, `CHANGENETWORK`, and `DELNETWORK` are refused with `FAIL BOUNCER UNKNOWN_COMMAND` — networks are managed in the stugan web UI (or the SSH UI).

---

## 7. Client Commands

Commands from an attached client are routed into the same engine path as the
web composer (`SendInput`), so aliases, plugin `hook_command`, and built-ins
all apply:

| Client sends | Handled as |
|--------------|------------|
| `PRIVMSG` / `NOTICE` | a message to the target (CTCP `ACTION` → `/me`); a line starting `/` runs as a stugan command |
| `JOIN`, `PART`, `TOPIC`, `NICK`, `AWAY` | the matching built-in command on the bound network |
| `LIST [mask]` | an upstream `LIST`, via the same path as the web channel browser |
| `WHOIS`, `WHO`, `MODE`, `KICK`, `INVITE` | passed upstream as `/quote`; replies are relayed back |
| `CHATHISTORY` | answered from stugan's SQLite history (`LATEST`, `BEFORE`, `AFTER`, `AROUND`, `BETWEEN`, `TARGETS`) |
| `MARKREAD` | advances the persisted read marker and notifies every client and browser |
| `BOUNCER` | `LISTNETWORKS` / `BIND` (see above) |
| `PING` / `PONG` / `QUIT` | handled locally; `QUIT` only detaches this client |
| anything else | passed upstream as a raw line (`/quote`) |

**Note:** the raw passthrough forwards the command and parameters but not
client message tags, so a client-sent `TAGMSG` (typing, reactions) or `REDACT`
does not reach the upstream network with its tags. Use the web UI to react to
or delete messages.
