# stugan

A self-hosted, plugin-extensible web IRC client written in Go.

stugan is a persistent daemon that holds your IRC connections 24/7 and
buffers history, plus a browser frontend that connects over WebSocket —
think [TheLounge](https://thelounge.chat/), rewritten in Go, with the
deep IRCv3 discipline of [Halloy](https://github.com/squidowl/halloy) and
a **weechat/irssi-style Lua plugin system** as the headline feature.

## Features

- Persistent IRC connections that survive browser disconnects; SQLite history
  with backlog replay on reconnect.
- Manage networks entirely from the web UI — add, edit, connect/disconnect, and
  remove servers (no config edits required), including a server password (for
  bouncers like ZNC/soju) and per-network "perform" commands run on every
  reconnect.
- Full-text message search (SQLite FTS5), a mentions view, per-channel mute, and
  unread/highlight counters with configurable highlight rules.
- Link previews + inline image/video (via a local image proxy), drag-drop/paste
  uploads, autocomplete (nicks, commands, channels, emoji), command aliases.
- IRCv3: SASL (PLAIN and EXTERNAL/CertFP via a client certificate), server-time,
  echo-message, away-notify, account-tag, multi-prefix, extended-join,
  message-tags, typing indicators, standard-replies, emoji reactions
  (`+draft/react`), message deletion (`draft/message-redaction`), a channel
  browser (LIST), and best-effort chathistory. See [docs/ircv3.md](docs/ircv3.md)
  for the full matrix and roadmap.
- Readable busy channels: consecutive join/part/quit/nick lines fold into one
  expandable summary, and nicks are colorized by a hash of the name (both
  toggleable in Settings).
- PWA: installable, mobile-responsive, with Web Push + desktop notifications.
- Installable custom themes, plus built-in dark/midnight/light.
- A weechat/irssi-style **Lua plugin system** (the headline feature).
- Multi-user with bcrypt auth and full per-user isolation.

## Architecture

```
cmd/stugan/        main(): flags, config, graceful shutdown
internal/
  config/          TOML config + $STUGAN_HOME resolution
  logging/         structured logging (slog)
  irc/             IRCConn interface + girc impl (only place girc is used)
  core/            GUI/transport-independent brain: state machine + event bus
  store/           SQLite history + FTS5 search + network persistence
  plugin/          PluginHost interface + Lua host (only place gopher-lua is used)
  auth/            bcrypt credentials + sessions (multi-user)
  server/          HTTP + WebSocket, typed event router, multi-tenant hub
  proto/           shared wire-protocol structs (TS mirror in client/)
client/            Vue 3 + TS + Vite frontend
docs/              design proposals, protocol + plugin API reference
```

`core` talks to the outside world only through interfaces (`IRCConn`,
`PluginHost`, the storage interface) and never imports `server`, the IRC
library, or UI code.

## Build & run

```sh
go build ./...
go test ./...

# Build the Vue client once (the daemon serves client/dist at /).
cd client && npm install && npm run build && cd ..

go run ./cmd/stugan            # uses $STUGAN_HOME or ~/.config/stugan
go run ./cmd/stugan -home ./dev-home
```

Then open the listen address (default `http://127.0.0.1:8080`).

### Frontend development

For live-reload while working on the client, run the Vite dev server; it
proxies the WebSocket to the Go daemon:

```sh
go run ./cmd/stugan &     # daemon on :8080
cd client && npm run dev  # Vite on :5173, open that
```

## Docker

Images are built and published to GHCR by CI (`ghcr.io/klppl/stugan`) for
`linux/amd64` and `linux/arm64`:

```sh
docker run -d --name stugan -p 8080:8080 -v stugan-data:/data \
  ghcr.io/klppl/stugan:latest
```

Config, history, scripts, and uploads live in the `/data` volume. Put a
`config.toml` there with `listen = "0.0.0.0:8080"` (so it's reachable
outside the container); set `origin_patterns` / `public_url` when serving
from a non-localhost host. Build locally with `docker build -t stugan .`.

## Configuration

Config, scripts, and data live under one root, resolved in order:
`$STUGAN_HOME`, then `$XDG_CONFIG_HOME/stugan`, then `~/.config/stugan`.

See [docs/config.md](docs/config.md) (to come) for the full reference.

## Multi-user

By default stugan runs single-user and unauthenticated (localhost). To require
login and isolate accounts, add `[[users]]` blocks — each user gets their own
networks, history, and plugins, fully separated. Generate a password hash:

```sh
stugan -hashpw            # prompts for a password, prints a bcrypt hash
```

```toml
[[users]]
name = "alice"
password_hash = "$2a$10$…"   # paste the hash from -hashpw
  [[users.networks]]
  name = "libera"
  addr = "irc.libera.chat:6697"
  tls  = true
  nick = "alice"
```

Sessions are bcrypt-verified with HttpOnly, SameSite=Strict cookies; the
plugin sandbox defaults on in multi-user mode.

## Site-wide password

For a quick, single-shared-password gate in front of the whole site —
useful when you're self-hosting on a public address and don't want to
set up `[[users]]` yet — set `STUGAN_WEB_PASSWORD`:

```sh
STUGAN_WEB_PASSWORD='hunter2' ./stugan
```

When set, every request to `/ws`, `/api/*`, and `/uploads/*` is blocked
behind a one-input prompt. Failed attempts are rate-limited per source
IP (8 fails per minute) and answered after a short delay; the login
forms also carry honeypot inputs that trip form-filling bots.

The password is bcrypt-hashed in memory at startup; the plaintext is
never retained. Grants live in an HttpOnly, SameSite=Strict cookie
(`stugan_magic`) for 30 days. The gate stacks with `[[users]]` — magic
word first, then per-user login — so it works in both single- and
multi-user setups.

Docker / docker-compose:

```yaml
services:
  stugan:
    image: ghcr.io/klppl/stugan:latest
    environment:
      STUGAN_WEB_PASSWORD: ${STUGAN_WEB_PASSWORD}
    # ...
```

There is no built-in `.env` parser; if you want one,
`set -a; source .env; set +a; ./stugan` loads it from a POSIX shell, or
docker-compose's `env_file:` does it for containers.

## Plugins

Drop a Lua script in `$STUGAN_HOME/scripts/*.lua` and it loads live
(hot-reloaded on save). Scripts register commands, filter/rewrite/drop
messages, hook signals, and run timers via a `stugan.*` API — the
weechat/irssi model. A crashing script is isolated and never takes down the
daemon. See [docs/plugins.md](docs/plugins.md) for the full API and
[docs/examples](docs/examples) for ready-to-use scripts (`greet`,
`highlight_reply`, `auto_away`).

```lua
-- scripts/greet.lua
stugan.hook_command("greet", function(args, ctx)
  stugan.message(ctx.network, args[1], "hello from a plugin!")
end)
```

## License

stugan is released under the [Lagom License](LICENSE) — not too much, not
too little.

## Design docs

- [docs/layout.md](docs/layout.md) — module & interface layout
- [docs/protocol.md](docs/protocol.md) — WebSocket event schema
- [docs/plugins.md](docs/plugins.md) — Lua plugin API
