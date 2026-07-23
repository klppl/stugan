-- watch.lua — the mirror image of ignore.lua: surface specific nicks.
--
-- Where ignore drops a nick so they leave no trace, watch keeps a list of
-- people you care about and tells you when they show up: a marker line in the
-- channel when a watched nick joins or leaves, and a "last seen" record so
-- /watch doubles as a /seen for your own people. Like ignore, the list is
-- per-network in stugan.kv — the same nick on two networks is two people.
--
-- Commands:
--   /watch              list watched nicks on this network + when last seen
--   /watch <nick> …     start watching
--   /unwatch <nick> …   stop watching
--
-- Only join/part carry a channel to print into, so those are the alerts;
-- quit/nick are network-wide events with no buffer to surface them in.

stugan.describe("Watch specific nicks: join/part alerts + /watch last-seen")

-- Two flat kv namespaces, both keyed per (network, nick):
--   w<TAB>net<TAB>nick = "1"             membership
--   s<TAB>net<TAB>nick = "epoch<TAB>buf" last seen
local function wkey(n, nick) return "w\t" .. n .. "\t" .. nick:lower() end
local function skey(n, nick) return "s\t" .. n .. "\t" .. nick:lower() end

local function is_watched(n, nick) return stugan.kv.get(wkey(n, nick)) ~= nil end

local function watched_on(network)
  local prefix = "w\t" .. network .. "\t"
  local out = {}
  for k in pairs(stugan.kv.all()) do
    if k:sub(1, #prefix) == prefix then out[#out + 1] = k:sub(#prefix + 1) end
  end
  table.sort(out)
  return out
end

local function mark_seen(network, nick, buffer)
  stugan.kv.set(skey(network, nick), os.time() .. "\t" .. (buffer or ""))
end

-- Quietly record activity from watched nicks (no print — the message is
-- already in the buffer); this feeds the /watch last-seen column.
stugan.hook_message(function(msg)
  if not msg.self and msg.from ~= "" and is_watched(msg.network, msg.from) then
    mark_seen(msg.network, msg.from, msg.buffer)
  end
  return msg
end)

stugan.hook_signal("join", function(s)
  if is_watched(s.network, s.nick) then
    mark_seen(s.network, s.nick, s.channel)
    stugan.print(s.network, s.channel, "● watch: " .. s.nick .. " joined " .. s.channel)
  end
end)

stugan.hook_signal("part", function(s)
  if is_watched(s.network, s.nick) then
    stugan.print(s.network, s.channel, "● watch: " .. s.nick .. " left " .. s.channel)
  end
end)

stugan.hook_command("watch", function(args, ctx)
  if #args == 0 then
    local nicks = watched_on(ctx.network)
    if #nicks == 0 then
      stugan.print(ctx, "watch: not watching anyone on " .. ctx.network)
      return
    end
    stugan.print(ctx, "watch: " .. #nicks .. " on " .. ctx.network .. ":")
    for _, nick in ipairs(nicks) do
      local rec = stugan.kv.get(skey(ctx.network, nick))
      local info = "never seen"
      if rec then
        local t, buf = rec:match("^(%d+)\t(.*)$")
        if t then
          info = "last seen " .. os.date("%Y-%m-%d %H:%M", tonumber(t))
          if buf ~= "" then info = info .. " in " .. buf end
        end
      end
      stugan.print(ctx, "  " .. nick .. " — " .. info)
    end
    return
  end
  for _, nick in ipairs(args) do
    stugan.kv.set(wkey(ctx.network, nick), "1")
    stugan.print(ctx, "watch: now watching " .. nick)
  end
end)

stugan.hook_command("unwatch", function(args, ctx)
  if #args == 0 then
    stugan.print(ctx, "usage: /unwatch <nick> …")
    return
  end
  for _, nick in ipairs(args) do
    if is_watched(ctx.network, nick) then
      stugan.kv.delete(wkey(ctx.network, nick))
      stugan.kv.delete(skey(ctx.network, nick))
      stugan.print(ctx, "watch: no longer watching " .. nick)
    else
      stugan.print(ctx, "watch: " .. nick .. " was not watched")
    end
  end
end)
