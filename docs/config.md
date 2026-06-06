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
| `tls` | bool | Use TLS. |
| `insecure` | bool | Skip TLS certificate verification (self-signed / LAN servers only). |
| `nick` / `user` / `realname` | string | Identity. |
| `channels` | []string | Auto-join on connect. |
| `connect` | bool | Connect on startup (default true). |
| `sasl_user` / `sasl_pass` | string | SASL PLAIN credentials. |
| `sasl_external` | bool | Use SASL EXTERNAL (CertFP) instead of a password. |
| `cert_file` | string | PEM with certificate **and** private key concatenated (for CertFP). |
| `server_pass` | string | IRC `PASS` — for bouncers (ZNC/soju) or password-gated servers. |
| `perform` | []string | Command lines run after registration on every (re)connect. |

```toml
[[networks]]
name     = "libera"
addr     = "irc.libera.chat:6697"
tls      = true
nick     = "stuganuser"
channels = ["#stugan"]
# perform  = ["/msg NickServ IDENTIFY hunter2", "/join #private secretkey"]
```

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

## Environment variables

| Var | Meaning |
|-----|---------|
| `STUGAN_HOME` | Config/data/scripts root (highest-priority lookup). |
| `STUGAN_WEB_PASSWORD` | Enables the site-wide password gate in front of the whole site (stacks with `[[users]]`). See [server.md](server.md#security-hardening). |

There is no built-in `.env` parser; `set -a; source .env; set +a; ./stugan`
loads one from a POSIX shell, or docker-compose's `env_file:` does it for
containers.
</content>
