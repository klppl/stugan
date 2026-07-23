# Configuration

stugan reads one TOML file, `config.toml`, from its home directory. Config,
scripts, and data all live under that root, resolved in order:

1. `$STUGAN_HOME`
2. `$XDG_CONFIG_HOME/stugan`
3. `~/.config/stugan`

`-home <path>` overrides the lookup. Under the root: `config.toml`, `scripts/`
(Lua plugins), and `data/` (the SQLite database and uploads); in multi-user
mode each user gets `users/<name>/data` and `users/<name>/scripts`. All fields
are optional — a missing config file is fine. A ready-to-edit example lives at
[config.example.toml](config.example.toml).

## `[server]`

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `listen` | string | `127.0.0.1:8080` | HTTP listen address. Use `0.0.0.0:8080` to expose it. |
| `public_url` | string | — | Absolute base URL, for push notifications and absolute links. |
| `static_dir` | string | `client/dist` | Directory the built client is served from. |
| `origin_patterns` | []string | — | Allowed WebSocket `Origin` patterns when serving from a non-localhost host. |
| `trusted_proxies` | []string | — | CIDRs (or bare IPs) of reverse proxies in front of the daemon. When the request's direct peer matches, the real client IP for auth rate-limiting is read from `CF-Connecting-IP` / `X-Forwarded-For`. Required behind a proxy (incl. Cloudflare Tunnel) so failed logins are throttled per visitor, not collapsed onto the proxy's address. |

## `[log]`

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `level` | string | `info` | `debug` \| `info` \| `warn` \| `error` |
| `format` | string | `text` | `text` \| `json` |

## `[history]`

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `retention_days` | int | `0` | Prune messages older than this many days from every user's history (search index included), hourly. `0` keeps history forever. |

## `[plugins]`

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `enabled` | bool | `true` | Load the Lua plugin host. |
| `sandbox` | bool | `true` | Restrict the Lua stdlib (removes `io`, `package`, `require`, `debug`, `os.execute`, …). Always on in multi-user; single-user may set `false` to opt into the full stdlib for trusted local scripts. |

Per-plugin settings are keyed by script basename and read via
`stugan.config(...)`:

```toml
[plugins.settings.highlight_reply]
word = "ping"
```

## `[[networks]]`

One block per IRC network (single-user mode). **These only seed the store on
first run** — after that, networks are managed from the GUI and the store is
authoritative.

| Key | Type | Meaning |
|-----|------|---------|
| `name` | string | Unique network id / display name. |
| `addr` | string | `host:port`. |
| `fallbacks` | []string | Additional `host:port` servers tried in order when `addr` fails to connect. |
| `tls` | bool | Use TLS. |
| `insecure` | bool | Skip TLS certificate verification (self-signed / LAN servers only). |
| `nick` / `user` / `realname` | string | Identity. `user` is the IRC ident and accepts only letters, digits and `-` `.` `_` `\` `[` `]` `{` `}` `^` `|`; it is **not** where a bouncer login goes (see [Bouncers](#bouncers-soju--znc)). |
| `channels` | []string | Auto-join after registration and Perform. |
| `monitor` | []string | Friends list watched via IRCv3 MONITOR (online/offline). Editable from the GUI thereafter. |
| `connect` | bool | Connect on startup (default true). |
| `sasl_user` / `sasl_pass` | string | SASL PLAIN credentials. |
| `sasl_external` | bool | Use SASL EXTERNAL (CertFP) instead of a password. |
| `cert_file` | string | PEM with certificate **and** private key concatenated (for CertFP). |
| `server_pass` | string | IRC `PASS` — for bouncers (ZNC/soju) or password-gated servers. |
| `perform` | []string | Command lines run after registration on every (re)connect. Supports the variables listed below. |

```toml
[[networks]]
name     = "libera"
addr     = "irc.libera.chat:6697"
tls      = true
nick     = "stuganuser"
channels = ["#stugan"]
# perform  = ["/mode $me +B", "/msg NickServ IDENTIFY hunter2", "/join #private secretkey"]
```

Perform variables are expanded when the command runs, after the server has
confirmed the connection:

| Variable | Value |
|----------|-------|
| `$me`, `$nick` | Your current nickname, including a fallback selected during registration. |
| `$network` | The network's display name (or id when it has no display name). |
| `$server` | The configured primary server address, including its port. |
| `$user` | The configured IRC username. |
| `$realname` | The configured IRC real name. |

Use `${name}` when text immediately follows a variable, for example
`${nick}_away`. Use `$$` for a literal `$`. Unknown variables are left unchanged.

Perform lines run in order with a one-second pause between commands. Configured
`channels` are joined one second after the final Perform command, allowing
service authentication and user modes (such as QuakeNet `+x`) to take effect
before JOIN. With no Perform commands, channels are joined immediately after
registration.

### Bouncers (soju / ZNC)

A bouncer multiplexes several upstream networks over one connection, so it
needs to know *which* account and *which* upstream network you want. Both
carry that selector in a `username/network` string — but it belongs in the
**authentication** fields, never in `user`:

| Field | Value |
|-------|-------|
| `nick` | Your nick on the upstream network. |
| `user` | Leave empty (defaults to the nick). The ident charset rejects `/` and `@`. |
| `sasl_user` | soju: `<username>/<network>` — optionally `@<client>` to give this client its own detached session and backlog, e.g. `anders/libera@stugan`. |
| `sasl_pass` | Your bouncer password. |
| `server_pass` | ZNC only: `<username>/<network>:<password>`. |

**soju needs SASL.** Its `PASS` path takes the account name from the `USER`
command and treats `PASS` as the password alone — it never splits a
`user:password` string — and the ident charset can't carry a `username/network`
selector, so `server_pass` is a dead end there. soju advertises `sasl=PLAIN`;
use `sasl_user` / `sasl_pass`. ZNC does split `PASS` on `:`, so `server_pass`
works for it.

In the GUI these are **SASL user** / **SASL pass** on the add-network form and
**Server pass** under *Advanced*. Leave **SASL EXTERNAL** unticked: it takes
precedence over the user/password and authenticates with a client certificate
instead, so a bouncer that only offers PLAIN answers `Unsupported SASL
mechanism` and drops the connection.

```toml
[[networks]]
name      = "soju"
addr      = "192.168.1.10:6697"
tls       = true
nick      = "anders"
sasl_user = "anders/libera@stugan"
sasl_pass = "hunter2"
channels  = ["#stugan"]
```

Putting the bouncer login in `user` instead fails before the socket is even
dialed, and the network retries with backoff forever:

```
level=WARN msg="connection ended; will retry" network=soju err="irc soju: invalid configuration: bad user/ident specified" backoff=8s
```

That is the ident charset rejecting `/` and `@` — move the value to
`sasl_user` (ZNC: `server_pass`) and clear `user`.

Getting the account name from somewhere else fails at registration instead. On
soju, a `server_pass` with no working `USER` name is rejected against whatever
ident was sent — here the nick, because `user` was empty:

```
downstream "10.0.0.14:60458": PASS authentication error for user "z": user not found
```

A plaintext bouncer on a LAN (`tls = false`, port 6667) sends the SASL password
in the clear; prefer TLS, and `insecure = true` if it uses a self-signed
certificate.

## `[highlight]`

| Key | Type | Meaning |
|-----|------|---------|
| `patterns` | []string | Case-insensitive regexes that trigger a highlight. |
| `exceptions` | []string | Regexes that suppress a highlight even if a pattern matched. |

Your nick is always a highlight (word-boundary, case-insensitive) regardless of
patterns. See [core.md](core.md#highlights).

## `[aliases]`

A `name → template` map. `$1`–`$9` are positional args, `$*` is all args, `$N-`
is args from N onward. The template starts with the slash-command it expands to.

```toml
[aliases]
j = "/join $*"
```

Also editable from the GUI (Settings → Aliases) and persisted per user; once
edited there, the stored set overrides this config block, the same way GUI
network and highlight changes take precedence.

## Multi-user — `[auth]` and `[[users]]`

With **no** `[[users]]` block the daemon is single-user and unauthenticated and
owns the top-level `[[networks]]`. Adding `[[users]]` requires login and gives
each user fully isolated connections, history, and plugins.

```toml
[auth]
session_hours = 720   # session lifetime (default 30 days)

[[users]]
name = "alice"
password_hash = "$2a$10$....."   # from `stugan -hashpw`
  [[users.networks]]
  name = "libera"
  addr = "irc.libera.chat:6697"
  tls  = true
  nick = "alice"
  channels = ["#stugan"]
```

Generate `password_hash` with `stugan -hashpw` (reads the password from stdin).

Each user may also carry `authorized_keys` for the SSH terminal UI (see
[`[ssh]`](#ssh)):

```toml
[[users]]
name = "alice"
password_hash = "$2a$10$....."
authorized_keys = ["ssh-ed25519 AAAA… alice@laptop"]
```

## `[ssh]`

Serves the same client as a terminal UI over SSH, so a network becomes usable
from a plain `ssh` command with no browser. Authentication is **public key
only** — a session's key must appear in the target user's `authorized_keys`.
Disabled unless `enabled = true`. See [ssh.md](ssh.md) for the full guide.

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `enabled` | bool | `false` | Turn the SSH listener on. |
| `listen` | string | `:2222` | Address to bind, e.g. `0.0.0.0:2222`. |
| `host_key` | string | `<data>/ssh_host_ed25519_key` | Server host key path; an ed25519 key is generated here on first run if absent. |
| `authorized_keys` | []string | — | OpenSSH public keys allowed to log in as the implicit **single** user. In multi-user mode use each `[[users]].authorized_keys` instead. |

```toml
[ssh]
enabled = true
listen  = "0.0.0.0:2222"
authorized_keys = [
  "ssh-ed25519 AAAA… me@laptop",
]
```

Connect with `ssh -p 2222 <user>@<host>` — the SSH username selects which
stugan user to open (in single-user mode it is `default`). A key registered
for one user can never open another user's session.

## Environment variables

| Var | Meaning |
|-----|---------|
| `STUGAN_HOME` | Config/data/scripts root (highest-priority lookup). |
| `STUGAN_WEB_PASSWORD` | Enables the site-wide password gate in front of the whole site (stacks with `[[users]]`). See [server.md](server.md#security-hardening). |

There is no built-in `.env` parser; `set -a; source .env; set +a; ./stugan`
loads one from a POSIX shell, or docker-compose's `env_file:` does it for
containers.
</content>
