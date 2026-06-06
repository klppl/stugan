---
name: new-plugin
description: Scaffold a new Lua plugin for stugan following the docs/examples conventions — describe(), the right hook(s), kv-backed user settings via stugan.setting() (GUI-configurable, never config.toml), and a load/verify pass. Use when adding a new example plugin or starting a user plugin.
disable-model-invocation: true
---

# Scaffold a stugan Lua plugin

stugan plugins are single Lua files dropped in the scripts directory; the host
loads each in its own `*lua.LState` and hot-reloads on save. The API is
`stugan.*` (reference: `docs/plugins.md`; worked examples: `docs/examples/*.lua`).
This skill scaffolds one that matches house conventions so it loads cleanly and
its settings are reachable from the GUI.

## Inputs

Ask the user (if `$ARGUMENTS` doesn't already say):
- **Name** — file basename, e.g. `pingpong` → `pingpong.lua`. Becomes
  `stugan.script_name`.
- **What it does** — one line (used for `stugan.describe()`).
- **Trigger(s)** — which hook(s): a slash command (`hook_command`), a content
  filter/responder on incoming messages (`hook_message`), input rewriting
  (`hook_input`), tab-completion (`hook_completion`), an IRC signal
  (`hook_signal`), or a timer (`hook_timer`).
- **User-tunable settings?** — anything a user might change at runtime. If yes,
  each gets a `stugan.setting()` declaration (see the hard rule below).
- **Target** — a shipped example (`docs/examples/`) or a user's own scripts
  dir. New shipped examples are also excerpted in plugins.md §3.8.

## Hard rules (project-specific)

- **Settings are kv/GUI-backed, never config.toml.** The user cannot SSH in to
  edit files, so anything configurable MUST be declared with `stugan.setting()`
  — it renders a field in Settings → Plugins, is persisted to the plugin's kv
  (key == setting name), and is applied live via its `apply` callback. Use
  `stugan.config()` (config.toml) only as a server-only fallback, never as the
  primary path. (See memory: plugins-no-config-toml.)
- **Never let a hook throw on bad input.** Guard `args`/`msg` fields; the host
  recovers Lua errors and disables a repeatedly-failing script, but a clean
  early return is better UX.
- **A message hook returns the msg to pass it, `nil` to drop it.** Don't fall
  off the end of a `hook_message` without returning `msg`.

## Recipe

### 1. Top matter — identity

```lua
-- <name>.lua — <one-line what it does>
stugan.describe("<one-line summary shown in Settings → Plugins>")
```

### 2. Settings (only if user-tunable)

Declare each near the top; capture the initial value in one line. `stugan.setting`
returns the current value (kv override, else default), and the host re-runs
`apply` on every change from the form:

```lua
local INTERVAL_MS
local function apply_interval(v) INTERVAL_MS = (tonumber(v) or 5) * 60000 end
apply_interval(stugan.setting("interval_min", {
  type = "number", default = 5, label = "Interval (min)", apply = apply_interval,
}))
```

Option keys: `type` (`"text"|"number"|"select"`), `default`, `label`, `help`,
`secret = true` (value never sent to the client — use for tokens/passwords),
`options = {…}` (choices when `type == "select"`), `apply`. Settings read live
from kv (`tonumber(stugan.kv.get("max"))`) need no `apply` at all.

### 3. The hook(s)

Pick by trigger. Mirror the closest example rather than inventing shape:
- **command** → `greet.lua` (`stugan.hook_command(name, fn)`; `args`, `ctx`).
- **content filter / responder** → `greet.lua`, `sed.lua` (`hook_message`;
  return `msg` to keep, `nil` to drop).
- **timer / idle** → `away.lua` (`hook_timer`; cancel with `stugan.unhook`).
- **signal** (JOIN, nick, etc.) → `watch.lua`, `nickserv.lua` (`hook_signal`).
- **auth / secrets / crypto** → `nickserv.lua`, `qauth.lua` (kv + `secret`
  settings; `stugan.crypto.*`).
- **completion** → `expand.lua` (`hook_completion`).

Acting on the network: `stugan.message/notice/action/join/part`,
`stugan.print(ctx, text)` for local buffer output, `stugan.send` for a raw
line. Reads: `stugan.networks/channels/members/nick`. Persistence:
`stugan.kv.get/set/delete/all`. Logging: `stugan.log.info/warn/error/debug`.

### 4. Load and verify

Per the plugin-iteration workflow, test against the **real persisted** scripts
dir and a normally-run daemon (not `-home ./dev`), so kv/settings persist:

```sh
go build -o stugan ./cmd/stugan
cp docs/examples/<name>.lua ~/.config/stugan/scripts/     # or the user's scripts dir
./stugan
```

Then in Settings → Plugins: confirm the description shows, **load** it, and if
it declared settings, open **configure** and confirm each field renders, saves,
and applies. For IRC-side behavior, drive it with the **irc-e2e-verify** skill.

### 5. If shipped as an example

- Keep it self-contained and commented in the style of the sibling files.
- Add/refresh its excerpt and one-line description in `docs/plugins.md` §3.8.
- If it exercises a `stugan.*` function not yet covered elsewhere, run the
  **lua-api-mirror-auditor** subagent to confirm impl/docs/examples agree.
