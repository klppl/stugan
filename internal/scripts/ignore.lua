-- ignore.lua — server-side per-nick ignore.
--
-- IRC has no native IGNORE, so this drops messages in the engine before they
-- are stored, counted, or turned into highlights/notifications — unlike a
-- client-side hide, an ignored nick leaves no trace. A hook_message handler
-- discards incoming PRIVMSG/NOTICE/ACTION from ignored senders; the ignore
-- list is persisted per-network in stugan.kv, so it survives restarts.
--
-- Commands:
--   /ignore            list the nicks ignored on this network
--   /ignore <nick> …   start ignoring one or more nicks
--   /unignore <nick> … stop ignoring one or more nicks
--
-- Scope is per-network: the same nickname on two networks is two people.
--
-- Web UI: right-click a member → "Ignore" / "Unignore" sends these commands
-- to the active buffer, so the daemon is the single source of truth.

stugan.describe("Server-side per-nick ignore (/ignore, /unignore)")

-- One kv entry per ignored (network, nick); value is "1". Keying each pair
-- separately keeps add/remove O(1) and lets hook_message do a single get().
local function key(network, nick)
  return network .. "\t" .. nick:lower()
end

local function is_ignored(network, nick)
  return stugan.kv.get(key(network, nick)) ~= nil
end

-- ignored_on returns the sorted nicks ignored on a network, by scanning the
-- flat kv namespace for this script and splitting back out the network prefix.
local function ignored_on(network)
  local prefix = network .. "\t"
  local out = {}
  for k in pairs(stugan.kv.all()) do
    if k:sub(1, #prefix) == prefix then
      out[#out + 1] = k:sub(#prefix + 1)
    end
  end
  table.sort(out)
  return out
end

-- Drop incoming conversational messages from an ignored sender. Our own
-- lines (echo-message) are never dropped, and non-message events (joins,
-- nick changes, …) don't reach hook_message at all.
stugan.hook_message(function(msg)
  if msg.self then return msg end
  if msg.kind ~= "privmsg" and msg.kind ~= "notice" and msg.kind ~= "action" then
    return msg
  end
  if msg.from ~= "" and is_ignored(msg.network, msg.from) then
    return nil
  end
  return msg
end)

stugan.hook_command("ignore", function(args, ctx)
  if #args == 0 then
    local nicks = ignored_on(ctx.network)
    if #nicks == 0 then
      stugan.print(ctx, "ignore: not ignoring anyone on " .. ctx.network)
    else
      stugan.print(ctx, "ignore: " .. table.concat(nicks, ", "))
    end
    return
  end
  for _, nick in ipairs(args) do
    stugan.kv.set(key(ctx.network, nick), "1")
    stugan.print(ctx, "ignore: now ignoring " .. nick)
  end
end)

stugan.hook_command("unignore", function(args, ctx)
  if #args == 0 then
    stugan.print(ctx, "usage: /unignore <nick> …")
    return
  end
  for _, nick in ipairs(args) do
    if is_ignored(ctx.network, nick) then
      stugan.kv.delete(key(ctx.network, nick))
      stugan.print(ctx, "ignore: no longer ignoring " .. nick)
    else
      stugan.print(ctx, "ignore: " .. nick .. " was not ignored")
    end
  end
end)
