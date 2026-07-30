# SSH terminal UI

stugan can serve its client as a full-screen terminal UI over SSH, powered by
[wish](https://github.com/charmbracelet/wish) and
[Bubble Tea](https://github.com/charmbracelet/bubbletea). It bridges to the
*same* per-user engine the web client uses — same connections, same history,
same plugins — so the two are live views of one session. Read a channel in the
browser, answer it from a terminal, and both stay in sync.

It exists for the case where a browser is inconvenient: an SSH jump host, a
phone SSH app, a tmux pane on a server. No port-forwarding or web exposure
needed — just SSH.

## Enabling it

Authentication is **public-key only**; there are no SSH passwords. Add your
public key to the user that should own the session and turn the listener on:

```toml
[ssh]
enabled = true
listen  = "0.0.0.0:2222"
authorized_keys = ["ssh-ed25519 AAAA… me@laptop"]   # single-user mode
```

In multi-user mode, keys live on each account instead:

```toml
[[users]]
name = "alice"
password_hash = "$2a$10$…"
authorized_keys = ["ssh-ed25519 AAAA… alice@laptop"]
```

Then connect — the SSH username picks the stugan user (`default` in
single-user mode):

```sh
ssh -p 2222 alice@your-host
```

A host key (`ssh_host_ed25519_key`) is generated under the data directory on
first run; point `host_key` elsewhere to reuse an existing one. If `enabled`
is set but no user has any `authorized_keys`, the server logs a warning and
does not start (nothing could log in anyway).

## Keys

| Key | Action |
|-----|--------|
| type text, `Enter` | send a message; a line starting with `/` is a command (`/join`, `/msg`, `/me`, `/nick`, …), exactly as in the web input |
| `Ctrl-N` / `Ctrl-P` | next / previous buffer |
| `Alt-↑` / `Alt-↓` | previous / next network |
| `PgUp` / `PgDn` | scroll history |
| `Ctrl-K` | quick switcher (fuzzy jump to any buffer) |
| `Ctrl-O` | networks: connect/disconnect, add, edit, remove |
| `Ctrl-L` | channel list browser (LIST) for the current network |
| `Ctrl-G` | plugins: enable / disable / reload |
| `Ctrl-W` | toggle the member list |
| `Ctrl-X` | close the current buffer |
| `F1` | key help |
| `Ctrl-C` | disconnect the session (IRC connections stay up in the daemon) |

Quitting the TUI only ends *that* terminal session — the daemon keeps every
network connected and buffering, the same as closing a browser tab.

## How it fits

`internal/tui` is the only package that imports wish and Bubble Tea; like
`internal/irc` with girc, those libraries never leak past it. Each SSH session
attaches to a per-user fan-out sink registered on the engine at startup, so
committed lines reach every attached terminal without the engine knowing SSH
exists. The composition root (`cmd/stugan`) maps SSH public keys to stugan
users and hands the server each user's engine and history.
