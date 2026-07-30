# SSH Terminal UI

stugan can serve its client as a full-screen terminal UI over SSH, powered by
[wish](https://github.com/charmbracelet/wish) and
[Bubble Tea](https://github.com/charmbracelet/bubbletea). It bridges to the
*same* per-user engine the web client uses — same connections, same history,
same plugins — so the two are live views of one session. Read a channel in the
browser, answer it from a terminal, and both stay in sync.

It exists for cases where a browser is inconvenient: an SSH jump host, a
mobile SSH terminal app (e.g. Termux, Blink), or a tmux pane on a server. No port-forwarding or web exposure needed — just SSH.

## Enabling it

Authentication is **public-key only**; there are no SSH passwords. Add your
public key to the target user and turn the listener on:

```toml
[ssh]
enabled = true
listen  = "0.0.0.0:2222"
authorized_keys = ["ssh-ed25519 AAAA… me@laptop"]   # single-user mode
```

In multi-user mode (`[auth]` enabled), keys live on each user account:

```toml
[[users]]
name = "alice"
password_hash = "$2a$10$…"
authorized_keys = ["ssh-ed25519 AAAA… alice@laptop"]
```

Connect using SSH — the SSH username selects the stugan user (`default` in single-user mode):

```sh
# Single-user mode:
ssh -p 2222 default@your-host

# Multi-user mode:
ssh -p 2222 alice@your-host
```

A server host key (`ssh_host_ed25519_key`) is generated under the data directory on first run; set `host_key` in config to reuse an existing key. If `enabled = true` is set but no user has any `authorized_keys`, the server logs a warning and will not start (preventing unauthenticated access).

---

## Mouse Support & Navigation

The TUI supports mouse cell motion in terminal emulators:
- **Mouse Wheel**: Scroll chat history up and down smoothly.
- **Mouse Click**: Click any channel or query buffer in the sidebar to jump directly to it.

---

## Key Shortcuts

| Key | Action |
|-----|--------|
| type text, `Enter` | Send a message; a line starting with `/` is a command (`/join`, `/msg`, `/me`, `/nick`, `/load`, …), exactly as in the web input |
| `Ctrl-N` / `Ctrl-P` | Next / Previous buffer |
| `Alt-↑` / `Alt-↓` | Previous / Next network |
| `PgUp` / `PgDn` | Scroll chat history |
| `Ctrl-K` | Quick switcher (fuzzy search jump to any buffer) |
| `Ctrl-O` | Networks overlay (connect/disconnect, add, edit, remove) |
| `Ctrl-L` | Channel list browser (`LIST`) for current network |
| `Ctrl-G` | Plugin manager & curated plugin store library |
| `Ctrl-W` | Toggle the member list panel |
| `Ctrl-X` | Close current buffer (part channel or close query) |
| `F1` | Key help overlay |
| `Ctrl-C` | Disconnect terminal session (IRC connections remain active in daemon) |

---

## Plugin Management & Store (`Ctrl-G`)

Press `Ctrl-G` to open the Plugin Overlay:
* **`[ Installed ]` Tab**: View local Lua scripts, toggle load/unload (`Enter` / `Space`), and hot-reload scripts (`r`).
* **`[ Library ]` Tab**: Browse official curated plugins from the Stugan plugin repository, download/install scripts with one keypress (`Enter` / `Space`), and check for updates (`u`).
* **Tab Switch**: Press `Tab`, `←`, or `→` to toggle between Installed and Library tabs.

---

## How it fits in the architecture

`internal/tui` is the only package that imports wish and Bubble Tea; like `internal/irc` with girc, those terminal UI libraries never leak into the core engine or server. Each SSH session attaches to a per-user fan-out sink registered on the engine at startup, so committed lines reach every attached terminal without the engine needing to know SSH exists. The composition root (`cmd/stugan`) maps SSH public keys to stugan users and hands the TUI server each user's engine and history.
