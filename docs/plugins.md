# Proposal 3 — Lua plugin API

The centerpiece. A user writes `$STUGAN_HOME/scripts/foo.lua`, saves, and it
loads live. Scripts extend stugan with commands, message filters, event
hooks, and timers — the weechat/irssi experience. **Awaiting sign-off; the
host lands in Phase 5, but the API surface is fixed now so `core`'s event
bus and `PluginHost` interface are designed to serve it.**

Runtime: `github.com/yuin/gopher-lua` behind the `core.PluginHost`
interface (§1.3). Everything below is exposed on a single global table
`stugan`.

## 3.1 Execution model

- Each script file is one plugin, loaded in its **own `*lua.LState`**. Plugins
  do not share Lua globals; they share state only through stugan APIs (KV
  store, sending to networks). Isolation means one script's crash or infinite
  global can't corrupt another.
- All Lua execution happens on a **single plugin goroutine** (one work queue),
  so hooks never race each other and the Lua VMs need no locking. Hooks must
  return promptly; long work belongs in a `hook_timer` or should call back.
  (A misbehaving hook is bounded by a per-call timeout — see §3.7.)
- Hooks fire **synchronously within `core`'s event commit path** for
  message/command events, so they can drop or mutate before anything is
  persisted or sent. Signal/timer hooks are notification-only.

## 3.2 Registration API

```lua
-- Add a slash-command /greet. fn(args, ctx) where args is an array of
-- the whitespace-split arguments and ctx describes the active buffer.
stugan.hook_command(name, fn)

-- Inspect/mutate/drop INCOMING messages. fn(msg) -> msg | nil
--   return msg        : pass through (optionally mutated)
--   return nil/false  : drop the message
stugan.hook_message(fn)

-- Inspect/mutate/drop OUTGOING user input before it hits IRC.
-- fn(input, ctx) -> input | nil   (input is the raw text the user sent)
stugan.hook_input(fn)

-- Listen to signals. event is "join","part","quit","nick","connect",
-- "disconnect","topic","mode", ... fn(signal) -> ignored (notify-only)
stugan.hook_signal(event, fn)

-- Periodic timer. fn() called every `ms` milliseconds until unhooked.
-- Returns a handle; stugan.unhook(handle) cancels it.
local h = stugan.hook_timer(ms, fn)
```

All `hook_*` functions return an opaque **handle**; `stugan.unhook(handle)`
removes it. On reload, a script's handles are all torn down automatically
before its file is re-run, so reloading is idempotent.

### Priorities

```lua
stugan.hook_message(fn, { priority = 100 })   -- lower runs earlier; default 500
```

Hooks run in ascending priority. A drop short-circuits later hooks for that
event.

## 3.3 The message table

What `hook_message` / `hook_input` receive and may mutate:

```lua
msg = {
  id      = "…",        -- IRCv3 msgid or generated (read-only)
  network = "libera",
  buffer  = "#go-nuts", -- channel or query
  from    = "alice",    -- sender nick
  account = "alice",    -- account-tag, or "" 
  kind    = "privmsg",  -- "privmsg"|"notice"|"action"
  text    = "hello",    -- MUTABLE
  self    = false,      -- did we send it (echo-message)
  time    = 1716750000, -- unix seconds (server-time)
  tags    = { ["+draft/reply"] = "…" },  -- raw message-tags
}
```

Mutating `text` (or `buffer`, to redirect) and returning the table rewrites
the message everywhere downstream — storage, UI, notifications. Returning
`nil` drops it before any of those see it.

## 3.4 The context table (`ctx`)

Passed to `hook_command` and `hook_input`, describing where the user acted:

```lua
ctx = {
  network = "libera",
  buffer  = "#go-nuts",
  kind    = "channel",   -- "channel"|"query"|"status"
  nick    = "me",         -- our current nick on this network
}
```

## 3.5 Action & state API

```lua
-- Send a raw IRC line on a network.
stugan.send(network, "PRIVMSG #x :hi")

-- Convenience wrappers (preferred over raw where they exist):
stugan.message(network, target, text)   -- PRIVMSG
stugan.notice(network, target, text)
stugan.action(network, target, text)    -- /me
stugan.join(network, channel)
stugan.part(network, channel)

-- Inject a line into a buffer's view WITHOUT sending to IRC (local print).
stugan.print(network, buffer, text)
-- Shorthand using the hook's ctx/msg buffer:
stugan.print(buffer_table, text)

-- Read state (returns plain Lua tables, snapshots — not live handles):
stugan.networks()                 -- -> array of {id,name,nick,state}
stugan.channels(network)          -- -> array of {name,kind,topic}
stugan.members(network, channel)  -- -> array of {nick,modes,away}
stugan.nick(network)              -- -> current nick string
```

`stugan.send` and friends validate the network exists and return
`ok, err`; errors are also logged. They never raise unless given the wrong
argument types.

## 3.6 Persistence, config, logging

```lua
-- Per-plugin key/value store (persisted in SQLite, scoped to this script).
stugan.kv.set("last_seen", os.time())
local t = stugan.kv.get("last_seen")        -- nil if unset
stugan.kv.delete("last_seen")

-- Read plugin-scoped config from config.toml:
--   [plugins.settings.greet]
--   target = "#stugan"
local target = stugan.config("target")      -- this script's settings table
local target = stugan.config("target", "#default")  -- with fallback

-- Structured logging (goes to the daemon log, tagged with script name):
stugan.log.info("loaded")
stugan.log.warn("…"); stugan.log.error("…"); stugan.log.debug("…")

-- Identity:
stugan.script_name   -- this script's basename, e.g. "greet"
```

## 3.7 Hot reload, isolation, sandboxing

- **Hot reload:** an fsnotify watcher on `$STUGAN_HOME/scripts`. On
  create/write/rename of `*.lua`, that single script is torn down (handles
  removed, `*lua.LState` closed) and re-loaded. IRC connections are never
  touched. Deleting a file unloads it. A debounce coalesces editor
  save-storms.
- **Error isolation:** every call into Lua (load, hook invocation, timer) is
  wrapped so a Lua error or panic is **recovered**, logged with the script
  name and traceback, and — for load errors or repeated runtime errors —
  the script is **disabled** (its hooks removed) rather than retried in a
  loop. The daemon never dies because of a script.
- **Per-call timeout:** hook invocations run with a context deadline; a hook
  that exceeds it is interrupted (gopher-lua `LState` context cancellation)
  and the script flagged. Protects the single plugin goroutine.
- **Sandboxing:** `[plugins].sandbox` knob. `false` (single-user default):
  full Lua stdlib, but each load is logged at WARN noting full-stdlib access.
  `true`: a restricted environment removing `os.execute`, `io`, `package`,
  raw `loadfile`/`dofile`, etc. (allowlist TBD in Phase 5).
  > TODO(multi-user): in multi-user mode sandbox defaults to `true`; for
  > hard isolation, a WASM host (wazero) implements the same `PluginHost`
  > interface — no API changes for script authors using the documented
  > surface.

## 3.8 Worked examples (shipped in docs/examples/)

### greet.lua — a command + a content filter

```lua
-- /greet <nick>  → say hello to <nick> from the current buffer's network.
stugan.hook_command("greet", function(args, ctx)
  if not args[1] then
    stugan.print(ctx, "usage: /greet <nick>")
    return
  end
  stugan.message(ctx.network, args[1], "hello from a plugin!")
end)

-- Drop any incoming message that mentions "spoiler".
stugan.hook_message(function(msg)
  if msg.text:lower():find("spoiler") then
    return nil
  end
  return msg
end)
```

### highlight_reply.lua — auto-reply when a word is mentioned

```lua
local word = stugan.config("word", "ping")
stugan.hook_message(function(msg)
  if msg.kind == "privmsg" and not msg.self
     and msg.text:lower():find(word) then
    stugan.message(msg.network, msg.buffer, msg.from .. ": pong")
  end
  return msg
end)
```

### auto_away.lua — a timer

```lua
-- After 10 min with no input from us, set away; clear on our next message.
local IDLE_MS = 10 * 60 * 1000
local last = {}  -- network -> unix seconds of our last sent line

stugan.hook_input(function(input, ctx)
  last[ctx.network] = os.time()
  stugan.send(ctx.network, "AWAY")     -- clear away
  return input
end)

stugan.hook_timer(60 * 1000, function()
  local now = os.time()
  for _, n in ipairs(stugan.networks()) do
    local t = last[n.name]
    if t and (now - t) * 1000 > IDLE_MS then
      stugan.send(n.name, "AWAY :idle")
    end
  end
end)
```

## 3.9 Open questions for sign-off

1. Global table name `stugan` — keep it, or a shorter alias (`s`, `irc`)?
   I propose `stugan` with no alias.
2. Hook signature for commands: `fn(args, ctx)` (proposed) vs. weechat's
   `fn(buffer, args_string)`? I prefer pre-split `args` + structured `ctx`.
3. Do you want a `hook_completion` (custom tab-completion) and
   `hook_modifier` (weechat-style named text transforms) in the v1 surface,
   or deferred? I propose deferring both to keep v1 tight.
4. KV store scope: per-script (proposed) vs. a shared namespace plugins opt
   into? Per-script is safer; shared can be added later.
5. Sandbox allowlist contents — fine to settle in Phase 5, or do you want to
   decide the exact removed modules now?
