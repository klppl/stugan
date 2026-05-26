# stugan

A self-hosted, plugin-extensible web IRC client written in Go.

stugan is a persistent daemon that holds your IRC connections 24/7 and
buffers history, plus a browser frontend that connects over WebSocket —
think [TheLounge](https://thelounge.chat/), rewritten in Go, with the
deep IRCv3 discipline of [Halloy](https://github.com/squidowl/halloy) and
a **weechat/irssi-style Lua plugin system** as the headline feature.

> Status: **Phase 0** — foundations and design proposals. Not yet usable.

## Architecture

```
cmd/stugan/        main(): flags, config, graceful shutdown
internal/
  config/          TOML config + $STUGAN_HOME resolution
  logging/         structured logging (slog)
  irc/             IRCConn interface + girc impl (only place girc is used)
  core/            GUI/transport-independent brain: state machine + event bus
  store/           SQLite history + FTS5 search
  plugin/          PluginHost interface + Lua host (only place gopher-lua is used)
  server/          HTTP + WebSocket, typed event router
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
go run ./cmd/stugan            # uses $STUGAN_HOME or ~/.config/stugan
go run ./cmd/stugan -home ./dev-home
```

## Configuration

Config, scripts, and data live under one root, resolved in order:
`$STUGAN_HOME`, then `$XDG_CONFIG_HOME/stugan`, then `~/.config/stugan`.

See [docs/config.md](docs/config.md) (to come) for the full reference.

## Design docs

- [docs/layout.md](docs/layout.md) — module & interface layout
- [docs/protocol.md](docs/protocol.md) — WebSocket event schema
- [docs/plugins.md](docs/plugins.md) — Lua plugin API
