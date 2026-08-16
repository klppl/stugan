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
  - `message-tags` (cross-client tag delivery, typing indicators, reactions, deletions)
  - `echo-message` & `znc.in/self-message` (multi-client synchronization)
  - `batch`
  - `draft/chathistory`
  - `draft/read-marker` (`MARKREAD` read position synchronization across clients)
  - `soju.im/bouncer-networks` & `soju.im/bouncer-networks-notify`
  - `sasl` (`PLAIN`)
- **TLS by Default**: Optional custom certificates or automatic self-signed ECDSA certificate generation with SHA-256 fingerprint logging.
- **Plugin Integration**: Chat commands and messages sent from downstream IRC clients pass through stugan's Lua plugin hooks and alias system.
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

#### C. Single-User Mode (No `[[users]]` configured)
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
