-- away.lua — idle auto-away plus a one-time auto-reply to PMs while you're gone.
--
-- Merges what used to be two scripts (auto_away + awayreply) into one place
-- for "I'm away" behaviour:
--   * After <idle_minutes> with no input on a network you're marked AWAY.
--   * Your next line clears it.
--   * While away, the FIRST private message from each nick gets a one-time
--     NOTICE reply (a notice, not a privmsg, so it can't ping-pong with the
--     other side's own auto-replies). The next person, or the same person
--     after you return, starts fresh.
--   * /away [message] marks you away now (the message is a one-time override).
--   * /back clears it.
--
-- Everything is set from inside stugan (persisted in kv) — no config.toml:
--   * /awaymsg  [text]    your standing away status   (default "idle")
--   * /awayreply [text]   the auto-reply to PMs       (default below)
--   * /idle     [minutes] idle timeout, 0 disables    (default 10)
-- Each shows the current value with no argument and reverts with "default".

stugan.describe("Idle auto-away (/idle, /awaymsg, /awayreply) + auto-reply to PMs")

local DEFAULT_IDLE_MIN = 10
local DEFAULT_MSG = "idle"
local DEFAULT_REPLY = "I'm away; I'll see your message when I'm back."

-- Settings: declared so they show in the GUI plugin form, and read from kv with
-- an apply callback so a change (from the form or a command) takes effect live.
-- stugan.setting returns the current value (kv override or default) to init.
local IDLE_MS
local function apply_idle(v) IDLE_MS = (tonumber(v) or DEFAULT_IDLE_MIN) * 60 * 1000 end
apply_idle(stugan.setting("idle_minutes", {
  type = "number", default = DEFAULT_IDLE_MIN,
  label = "Idle timeout (min)", help = "0 disables auto-away",
  apply = apply_idle,
}))

local MSG
local function apply_msg(v) MSG = v end
apply_msg(stugan.setting("message", {
  default = DEFAULT_MSG, label = "Away status", apply = apply_msg,
}))

local REPLY
local function apply_reply(v) REPLY = v end
apply_reply(stugan.setting("reply", {
  default = DEFAULT_REPLY, label = "PM auto-reply", apply = apply_reply,
}))

local last = {} -- network -> unix seconds of last activity
local away = {} -- network -> true while marked away
local replied = {} -- network -> { nick -> true } already replied this away session

local function go_away(network, message)
  if away[network] then return end
  away[network] = true
  replied[network] = {}
  stugan.send(network, "AWAY :" .. (message or MSG))
end

local function come_back(network)
  last[network] = os.time()
  if not away[network] then return end
  away[network] = false
  replied[network] = nil
  stugan.send(network, "AWAY") -- empty AWAY clears the away state
end

-- Start the idle clock at connect so a freshly (re)connected network goes
-- away on schedule even before you've typed anything.
stugan.hook_signal("connect", function(s)
  last[s.network] = os.time()
  away[s.network] = false
end)

-- Any line we send is activity: clear away, reset the clock.
stugan.hook_input(function(input, ctx)
  come_back(ctx.network)
  return input
end)

-- Sweep for idle networks once a minute (IDLE_MS <= 0 disables auto-away).
stugan.hook_timer(60 * 1000, function()
  if IDLE_MS <= 0 then return end
  local now = os.time()
  for _, n in ipairs(stugan.networks()) do
    local t = last[n.name]
    if t and (now - t) * 1000 >= IDLE_MS then
      go_away(n.name)
    end
  end
end)

-- One-time auto-reply to private messages while away. A query is exactly when
-- the buffer is the sender's own nick (channels are #/&/+/! names).
stugan.hook_message(function(msg)
  if away[msg.network]
      and msg.kind == "privmsg"
      and not msg.self
      and msg.from ~= ""
      and msg.buffer == msg.from then
    local seen = replied[msg.network] or {}
    if not seen[msg.from] then
      seen[msg.from] = true
      replied[msg.network] = seen
      stugan.notice(msg.network, msg.from, REPLY)
    end
  end
  return msg
end)

stugan.hook_command("away", function(args, ctx)
  local m = #args > 0 and table.concat(args, " ") or MSG
  away[ctx.network] = false -- force a re-send even if already away
  go_away(ctx.network, m)
  stugan.print(ctx, "away: marked away on " .. ctx.network .. " (" .. m .. ")")
end)

stugan.hook_command("back", function(args, ctx)
  come_back(ctx.network)
  stugan.print(ctx, "back: cleared away on " .. ctx.network)
end)

stugan.hook_command("awaymsg", function(args, ctx)
  if #args == 0 then
    stugan.print(ctx, "awaymsg: away status is \"" .. MSG .. "\"")
    return
  end
  if args[1]:lower() == "default" then
    stugan.kv.delete("message")
    apply_msg(DEFAULT_MSG)
    stugan.print(ctx, "awaymsg: reverted to default (\"" .. MSG .. "\")")
    return
  end
  local v = table.concat(args, " ")
  stugan.kv.set("message", v)
  apply_msg(v)
  stugan.print(ctx, "awaymsg: away status set to \"" .. MSG .. "\"")
end)

stugan.hook_command("awayreply", function(args, ctx)
  if #args == 0 then
    stugan.print(ctx, "awayreply: auto-reply is \"" .. REPLY .. "\"")
    return
  end
  if args[1]:lower() == "default" then
    stugan.kv.delete("reply")
    apply_reply(DEFAULT_REPLY)
    stugan.print(ctx, "awayreply: reverted to default (\"" .. REPLY .. "\")")
    return
  end
  local v = table.concat(args, " ")
  stugan.kv.set("reply", v)
  apply_reply(v)
  stugan.print(ctx, "awayreply: auto-reply set to \"" .. REPLY .. "\"")
end)

stugan.hook_command("idle", function(args, ctx)
  if #args == 0 then
    if IDLE_MS <= 0 then
      stugan.print(ctx, "idle: auto-away disabled (manual /away still works)")
    else
      stugan.print(ctx, "idle: auto-away after " .. (IDLE_MS / 60000) .. " min")
    end
    return
  end

  if args[1]:lower() == "default" then
    stugan.kv.delete("idle_minutes")
    apply_idle(DEFAULT_IDLE_MIN)
    stugan.print(ctx, "idle: reverted to default (" .. (IDLE_MS / 60000) .. " min)")
    return
  end

  local m = tonumber(args[1])
  if not m or m < 0 then
    stugan.print(ctx, "usage: /idle <minutes>  (0 disables, 'default' resets)")
    return
  end
  stugan.kv.set("idle_minutes", m)
  apply_idle(m)
  if m == 0 then
    stugan.print(ctx, "idle: auto-away disabled")
  else
    stugan.print(ctx, "idle: auto-away after " .. m .. " min")
  end
end)
