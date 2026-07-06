# Lua plugin API

The centerpiece. A user writes `$STUGAN_HOME/scripts/foo.lua`, saves, and it
loads live. Scripts extend stugan with commands, message filters, event
hooks, and timers — the weechat/irssi experience. Implemented in
`internal/plugin`; the surface below matches the running host.

Runtime: `github.com/yuin/gopher-lua` behind the `core.PluginHost`
interface (see [core.md](core.md#interfaces-core-defines)). Everything below is
exposed on a single global table `stugan`.

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

-- Rewrite an INCOMING channel topic before it reaches the topic bar. Fires
-- both for the topic delivered on join and for a live /topic change.
-- fn(t) -> string | { text = ... } | nil
--   t = { network, buffer, nick, text }   (nick is "" on join)
--   return a string or a table with `text` : replace the topic
--   return nil/nothing                    : leave it unchanged (not dropped)
stugan.hook_topic(fn)

-- Extend tab-completion. fn(word, ctx) -> array of candidate strings | nil
-- where `word` is the partial token under the cursor. Each candidate is a
-- full replacement token (the client appends a space). Results from every
-- completion hook are gathered and merged into the client's built-in
-- nick/channel/emoji/command menu.
stugan.hook_completion(fn)

-- Listen to signals. event is "join","part","kick","quit","nick","connect",
-- "disconnect","topic","mode","invite","account". fn(signal) -> ignored
-- (notify-only). The signal table carries network/nick/new_nick/channel/
-- account/text, plus kicker for "kick" (nick is the kicked member). For
-- "mode", nick is the setter and text the raw mode string. For "invite",
-- nick is the inviter and new_nick the invitee (possibly you). For
-- "account", nick logged in to services account (account "" = logged out).
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

Passed to `hook_command`, `hook_input`, and `hook_completion`, describing
where the user acted:

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

-- Publish an opaque per-buffer key/value bag. Carried through snapshots
-- and the wire protocol so the client (and any other Sink) can react to
-- plugin metadata. Pass nil or {} to clear. Plugin-defined keys; the
-- only consumer today is the sidebar lock indicator, which reads
-- state.encrypted = "cbc" | "ecb" (set by the bundled fish.lua).
stugan.set_buffer_state(network, buffer, state)  -- state: {[k]=v} or nil

-- Read state (returns plain Lua tables, snapshots — not live handles):
stugan.networks()                 -- -> array of {id,name,nick,state}
stugan.channels(network)          -- -> array of {name,kind,topic}
stugan.members(network, channel)  -- -> array of {nick,account,modes,away}
stugan.nick(network)              -- -> current nick string
```

`stugan.send` and friends validate the network exists and return
`ok, err`; errors are also logged. They never raise unless given the wrong
argument types.

## 3.6 Persistence, config, logging

```lua
-- Per-plugin key/value store. Persisted in SQLite (plugin_kv table) and
-- scoped to this script — entries survive both hot-reload and daemon
-- restart. Lazy-loaded on first access from this script.
stugan.kv.set("last_seen", os.time())
local t = stugan.kv.get("last_seen")        -- nil if unset
stugan.kv.delete("last_seen")
local all = stugan.kv.all()                  -- table of every key->value

-- Declare a user-facing setting. It renders as a field in the plugin's form
-- in Settings → Plugins, is backed by kv (key == name), and returns the
-- current value (kv override, else default) so you can initialize in one line.
-- opts: { type = "text"|"number"|"select", default, label, help,
--         secret = true,            -- value is never sent to the client
--         options = {"a","b"},      -- choices when type == "select"
--         apply = function(value) … end }  -- run when the value changes
local IDLE_MS
local function apply_idle(v) IDLE_MS = (tonumber(v) or 10) * 60000 end
apply_idle(stugan.setting("idle_minutes", {
  type = "number", default = 10, label = "Idle timeout (min)", apply = apply_idle,
}))
-- The host runs apply on changes from the form; settings that read kv live
-- (e.g. `tonumber(stugan.kv.get("max"))`) need no apply at all. Prefer
-- stugan.setting over stugan.config for anything a user might change —
-- config.toml can only be edited on the server.

-- Read plugin-scoped config from config.toml (a server-only fallback; prefer
-- stugan.setting above):
--   [plugins.settings.greet]
--   target = "#stugan"
local target = stugan.config("target")      -- this script's settings table
local target = stugan.config("target", "#default")  -- with fallback

-- Structured logging (goes to the daemon log, tagged with script name):
stugan.log.info("loaded")
stugan.log.warn("…"); stugan.log.error("…"); stugan.log.debug("…")

-- Identity:
stugan.script_name   -- this script's basename, e.g. "greet"

-- Self-description. Call at the top level to give the plugin a one-line
-- summary shown in the web client's plugin manager (Settings → Plugins).
stugan.describe("auto-reply 'pong' when someone says 'ping'")
```

The plugin manager in **Settings → Plugins** lists every script in the scripts
directory — loaded and not — with its description (or, lacking one, the
commands and hook count it registered) and buttons to **load**, **unload**, or
**reload** it at runtime. Reloading re-reads the file from disk without
restarting the daemon or dropping IRC connections (the same teardown the
fsnotify watcher uses on save). A loaded plugin that declared settings with
`stugan.setting()` also gets a **configure** toggle that opens a form to edit
them in place; a change is validated against the setting's type, persisted to
the plugin's kv, and applied live via its `apply` callback.

## 3.6.1 Crypto primitives

`stugan.crypto` exposes a few algorithms the Lua VM can't reasonably provide
on its own — secure RNG, modexp of big-integers, and the legacy Blowfish
cipher that IRC encryption schemes (FiSH ECB/CBC) still rely on. These are
primitives: padding, framing, key derivation, and ciphertext layout are
the script's job.

```lua
-- All bytes/strings below are 8-bit clean Lua strings.

-- Blowfish. The key must be 1..56 bytes; data must be a multiple of 8.
-- Padding is the caller's responsibility — fish-style "null-terminate
-- then random-pad" and PKCS-7 both compose cleanly on top.
stugan.crypto.blowfish_ecb_encrypt(key, data)     -> bytes
stugan.crypto.blowfish_ecb_decrypt(key, data)     -> bytes
stugan.crypto.blowfish_cbc_encrypt(key, iv, data) -> bytes   -- iv is 8 bytes
stugan.crypto.blowfish_cbc_decrypt(key, iv, data) -> bytes

-- SHA-256. Returns 32 raw bytes.
stugan.crypto.sha256(bytes)                       -> bytes

-- Cryptographically secure random bytes (crypto/rand). Capped at 4096.
stugan.crypto.random(n)                           -> bytes

-- Modular exponentiation of arbitrary-width big-endian integers. The
-- result is zero-padded to len(mod) so DH-style payloads have a stable
-- wire width without the caller stripping/re-adding leading zeros.
stugan.crypto.modexp(base, exp, mod)              -> bytes (len = #mod)
```

Argument validation raises a Lua error (which the host recovers and logs);
guard with `pcall` if you want to keep going on bad input.

See `internal/scripts/fish.lua` for a worked plugin that uses these to implement
FiSH-style Blowfish-CBC encryption for IRC.

## 3.6.2 HTTP

`stugan.http` lets a script reach the web — fetch a page title, hit a weather
or translation API, post to an LLM. Requests go through the daemon's
SSRF-guarded client (`internal/safehttp`): it resolves the host and refuses to
connect to private, loopback, link-local, or otherwise non-public addresses, so
a script cannot probe your internal network. Response bodies are capped at 1 MiB.

Both calls are **asynchronous**. All Lua runs on a single goroutine, so a
blocking fetch would stall every other script and the message hot path. Instead
the request runs off-thread and your callback is scheduled back onto the Lua
goroutine when it completes — the same model as `hook_timer`.

```lua
-- get(url, callback): the simple case.
stugan.http.get("https://example.com/", function(res)
  if res.ok then
    stugan.log.info("status " .. res.status .. ", " .. #res.body .. " bytes")
  else
    stugan.log.warn("fetch failed: " .. res.error)
  end
end)

-- request(opts, callback): method, headers, and body for POST/PUT/etc.
stugan.http.request({
  method  = "POST",
  url     = "https://api.example.com/v1/chat",
  headers = { ["Authorization"] = "Bearer " .. token,
              ["Content-Type"]  = "application/json" },
  body    = '{"prompt":"hello"}',
}, function(res) ... end)
```

The callback receives one table:

```lua
res = {
  ok      = true,            -- did the request complete (transport level)?
  status  = 200,             -- HTTP status; 0 on a transport error
  body    = "…",             -- response body (string, ≤ 1 MiB)
  headers = { ["content-type"] = "text/html; charset=utf-8", … }, -- keys lowercased
  error   = nil,             -- set (and ok=false, status=0) only on transport failure
}
```

`ok` reports a transport-level success, not a 2xx: a 404 is `ok=true` with
`status=404`. Check `res.status` yourself. `get`/`request` return `(true, nil)`
when the request was accepted, or `(false, reason)` if it could not even be
started — `"http: disabled"` (no client configured) or `"http: too many
concurrent requests"` (more than 4 in flight). In those two cases the callback
never fires, so handle the return value if you care about back-pressure.

A default `User-Agent: stugan-plugin/<script>` is sent unless you set your own.

See `docs/examples/title.lua` for a worked plugin that announces the title of
links posted in a channel.

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
- **Sandboxing:** `[plugins].sandbox` knob, **default `true`**. The sandbox is
  a restricted environment that removes the globals `io`, `package`, `require`,
  `load`, `loadstring`, `loadfile`, `dofile`, `debug`, and the
  process-affecting `os.*` functions (`execute`, `exit`, `remove`, `rename`,
  `setenv`, `tmpname`, `getenv`). The rest of the Lua stdlib stays available
  (`os.time`/`os.date`, `string`, `table`, `math`, …). Multi-user mode is
  **always** sandboxed regardless of the knob — tenants share the process.
  Single-user mode may set `sandbox = false` to opt into the full stdlib for
  its own trusted local scripts; each unsandboxed load is logged.
  > TODO(multi-user): for hard isolation, a WASM host (wazero) implements the
  > same `PluginHost` interface — no API changes for script authors using the
  > documented surface.

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

The trigger word lives in `kv` with a built-in default and a `/hlreply`
command, so it's set from inside stugan rather than config.toml:

```lua
local DEFAULT_WORD = "ping"
local word = stugan.kv.get("word") or DEFAULT_WORD

stugan.hook_message(function(msg)
  if msg.kind == "privmsg" and not msg.self
     and msg.text:lower():find(word, 1, true) then
    stugan.message(msg.network, msg.buffer, msg.from .. ": pong")
  end
  return msg
end)

stugan.hook_command("hlreply", function(args, ctx)
  if #args == 0 then
    stugan.print(ctx, "hlreply: trigger word is '" .. word .. "'")
    return
  end
  word = args[1]:lower()
  stugan.kv.set("word", word)
  stugan.print(ctx, "hlreply: trigger word is now '" .. word .. "'")
end)
```

### team_mentions.lua — extend tab-completion

```lua
-- Complete "@team" group mentions the client knows nothing about. Typing
-- "@de<Tab>" offers "@dev"; the candidates merge into the normal menu.
local GROUPS = { "@dev", "@ops", "@all", "@oncall" }

stugan.hook_completion(function(word, ctx)
  if word:sub(1, 1) ~= "@" then return end
  local out = {}
  for _, g in ipairs(GROUPS) do
    if g:sub(1, #word) == word then out[#out + 1] = g end
  end
  return out
end)
```

### away.lua — idle auto-away + auto-reply, on a timer

A `hook_timer` marks you away after an idle period and `hook_input` clears it
on your next line — the minimal timer pattern:

```lua
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

The shipped `docs/examples/away.lua` builds on this: it tracks the away state
per network (so `AWAY` is sent only on the transition, not every line), sends a
one-time NOTICE auto-reply to PMs while you're away, and adds `/away` / `/back`
plus runtime settings `/idle [minutes]` (the timeout; `0` disables),
`/awaymsg [text]` (the standing away status), and `/awayreply [text]` (the
auto-reply). Each persists in `kv` over a built-in default and accepts `default`
to revert — no config.toml. `/away [message]` stays a one-time override that
doesn't change the standing message. The timer reads `IDLE_MS` as an upvalue, so
`/idle` takes effect on the next sweep.

### sed.lua — fix a typo in your last line

The classic IRC affordance: after sending a message, type `s/teh/the/` to
resend it corrected (`/g` for all occurrences, `/i` for case-insensitive,
any punctuation as the delimiter). A `hook_input` remembers the last plain
line per buffer; a substitution swallows the `s///` line and sends the fixed
version in its place. Matching is **literal** (not Lua patterns) — what you
want for typo fixes — and a line that doesn't apply is sent through unchanged.
See `docs/examples/sed.lua`.

### urls.lua — remember links posted in a buffer

A persistent daemon is the right place to keep "what was that link from
yesterday?" A `hook_message` scrapes http(s) URLs and keeps the last few per
buffer in `stugan.kv` (so they survive restart); `/urls`, `/urls <n>`, and
`/urls clear` recall or forget them. See `docs/examples/urls.lua`.

### title.lua — announce the title of posted links

A worked use of `stugan.http`. A `hook_message` spots http(s) links, fetches
the page off-thread, scrapes its `<title>`, and prints it locally into the
buffer (via `stugan.print`, so nothing is sent to IRC). Toggle with
`/title on|off`; max length is set from the plugin settings form. See
`docs/examples/title.lua`.

### expand.lua — a text expander

Type `;shrug` and it becomes `¯\_(ツ)_/¯` when the line is sent; triggers
expand inline and tab-complete. A `hook_input` rewrites known triggers, a
`hook_completion` offers their names, and `/exp` manages your own (persisted
in `stugan.kv`, shadowing the built-ins). See `docs/examples/expand.lua`.

### nickserv.lua — identify and reclaim your nick on connect

A `hook_signal "connect"` that messages NickServ to IDENTIFY and, if you landed
on a fallback nick, GHOSTs your real one and switches back (the `NICK` is sent
from a self-unhooking one-shot timer, a moment after GHOST). The password lives
in `stugan.kv`, set via `/nickserv set <password>`; service messages go out as
raw PRIVMSG (not `stugan.message`) so the password is never echoed locally. See
`docs/examples/nickserv.lua`.

### watch.lua — the mirror of ignore

Where `ignore.lua` drops a nick, `watch.lua` surfaces one: a per-network list in
`stugan.kv` (same shape as ignore), a marker line in the channel when a watched
nick joins or parts, and a last-seen record so `/watch` doubles as a `/seen`.
Commands: `/watch` (list), `/watch <nick> …`, `/unwatch <nick> …`. See
`docs/examples/watch.lua`.

### qauth.lua — QuakeNet Q authentication

The QuakeNet counterpart to `nickserv.lua`. On `connect` it authenticates with
Q, defaulting to **CHALLENGEAUTH** — an HMAC-SHA-256 challenge/response built on
`stugan.crypto.sha256`, so the password never crosses the wire (a plain
`AUTH user pass` mode is available for networks that need it). Credentials live
in `stugan.kv`; service messages go out as raw PRIVMSG so nothing is echoed, and
`hidehost` sets `+x` once Q confirms login. `/qauth set <user> <pass>`, `/qauth`
(auth now), `/qauth show`, `/qauth method <challenge|plain>`, `/qauth clear`. See
`docs/examples/qauth.lua`.

### fun.lua — the toy commands

`/roll 2d6` (also `d20`, `6`, `3d6+2`), `/8ball <q>`, `/slap <nick>`. A small
worked use of `stugan.crypto.random` for unbiased dice (rejection sampling over
a 32-bit draw) with results sent to the buffer as actions. See
`docs/examples/fun.lua`.

### fish.lua — FiSH-style Blowfish encryption

A worked use of `stugan.crypto`. Implements the wire formats interoperable
clients ship (CBC `+OK *<b64(IV||ct)>`, legacy ECB `+OK <fish-b64(ct)>`),
a per-target keystore in `stugan.kv`, manual key management (`/setkey`,
`/setkey-ecb`, `/delkey`, `/key`), encrypted `/me` / `/notice` / `/topic`,
and DH1080 key exchange (`/keyx <nick>`). See `internal/scripts/fish.lua`
for the full plugin.

**Bundled and auto-installed.** The daemon embeds fish.lua via `go:embed`
(`internal/scripts/scripts.go`) and copies it to your scripts directory on
first run when the file isn't there — so the right-click "Set encryption
key…" affordance in the web UI just works without you copying anything by
hand. Edits and deletions are preserved across restarts: a missing file is
only re-installed if the script with that exact name doesn't exist.

### ignore.lua — server-side per-nick ignore

IRC has no native IGNORE, so this drops messages **in the engine** via
`hook_message` before they are stored, counted, or turned into
highlights/notifications — an ignored nick leaves no trace, unlike a
client-side hide. The list is persisted per-network in `stugan.kv`.
Commands: `/ignore` (list), `/ignore <nick> …` (add), `/unignore <nick> …`
(remove). See `internal/scripts/ignore.lua`.

**Bundled and auto-installed** the same way as fish.lua. The web UI's
right-click "Ignore" / "Unignore" on a member sends these commands, so the
daemon is the single source of truth — there is no separate client-side
ignore list.

## 3.9 Decisions (locked in Phase 5)

1. Global table name is `stugan`, no alias.
2. Command hooks use `fn(args, ctx)` with pre-split `args` and a structured
   `ctx` (network, buffer, kind, nick).
3. `hook_completion` extends the client's tab-completion (candidates are
   gathered server-side and merged into the menu over the wire). A separate
   weechat-style `hook_modifier` is **not** provided: `hook_message`
   (incoming) and `hook_input` (outgoing) already are the modifier hooks —
   they mutate or drop text in flight — so a named-modifier API would only
   duplicate them.
4. The KV store is per-script, SQLite-backed (`plugin_kv` table), and
   survives both hot-reload and daemon restart. The host caches values in
   memory for fast access and writes through on every set/delete.
5. The sandbox (`[plugins].sandbox`, default `true`) removes `io`, `package`,
   `require`, `dofile`, `loadfile`, `load`, `debug`, and the process-affecting
   `os.*` functions (`execute`, `exit`, `remove`, `rename`, `getenv`, …).
   Always on in multi-user; single-user may set `false` for the full stdlib
   (logged on load).

## 3.10 Built-in commands

A `/command` that no plugin claims falls back to built-ins: `/me`, `/msg`,
`/notice`, `/join`, `/part`, `/topic`, `/nick`, `/quit`, `/chathistory`
(server-side history where supported), and `/raw` (alias `/quote`).
Anything else unrecognized prints an "unknown command" notice. A line
starting with `//` is sent literally (an escaped leading slash).
