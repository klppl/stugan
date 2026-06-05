-- nickserv.lua — identify to NickServ on connect, and reclaim your nick.
--
-- On every (re)connect this identifies to services and, if you ended up on a
-- fallback nick because your real one was still ghosted, GHOSTs it and switches
-- back. Channels are restored by stugan itself, so this only touches identity.
--
-- Set it up from the network's status/query buffer (so nothing is echoed to a
-- channel):
--   /nickserv set <password>   save the password; remembers your current nick
--   /nickserv identify         re-run IDENTIFY now
--   /nickserv clear            forget it
--   /nickserv                  show status
--
-- The password lives in the plugin KV store (SQLite), in plaintext at rest —
-- the same trust level as any stored IRC password. If your network and stugan
-- support SASL, configuring that on the network is the stronger option; this is
-- the services-message fallback for when it isn't.

stugan.describe("Auto-identify to NickServ on connect; reclaim your nick")

local SERVICE = "NickServ" -- the services nick on most networks

local function pw_key(n) return "pw\t" .. n end
local function nick_key(n) return "nick\t" .. n end

-- Message a service via raw PRIVMSG (not stugan.message) so the password is
-- never echoed into a local buffer.
local function svc(network, text)
  stugan.send(network, "PRIVMSG " .. SERVICE .. " :" .. text)
end

-- One-shot timer: fire once, then unhook itself.
local function once(ms, fn)
  local h
  h = stugan.hook_timer(ms, function()
    stugan.unhook(h)
    fn()
  end)
end

local function identify(network)
  local pw = stugan.kv.get(pw_key(network))
  if not pw then return end
  local want = stugan.kv.get(nick_key(network))
  -- IDENTIFY <account> <password> works even while parked on a fallback nick.
  if want and want ~= "" then
    svc(network, "IDENTIFY " .. want .. " " .. pw)
  else
    svc(network, "IDENTIFY " .. pw)
  end
end

local function regain(network)
  local want = stugan.kv.get(nick_key(network))
  local pw = stugan.kv.get(pw_key(network))
  if not want or want == "" or not pw then return end
  if (stugan.nick(network) or "") == want then return end -- already have it
  svc(network, "GHOST " .. want .. " " .. pw)
  once(2000, function() stugan.send(network, "NICK " .. want) end)
end

stugan.hook_signal("connect", function(s)
  identify(s.network)
  regain(s.network)
end)

stugan.hook_command("nickserv", function(args, ctx)
  local sub = (args[1] or ""):lower()

  if sub == "set" then
    local pw = args[2]
    if not pw then
      stugan.print(ctx, "usage: /nickserv set <password>")
      return
    end
    stugan.kv.set(pw_key(ctx.network), pw)
    stugan.kv.set(nick_key(ctx.network), stugan.nick(ctx.network) or "")
    stugan.print(ctx, "nickserv: saved for " .. ctx.network
      .. " (nick " .. (stugan.nick(ctx.network) or "?") .. ")")
  elseif sub == "identify" then
    if stugan.kv.get(pw_key(ctx.network)) then
      identify(ctx.network)
      stugan.print(ctx, "nickserv: sent IDENTIFY on " .. ctx.network)
    else
      stugan.print(ctx, "nickserv: nothing saved for " .. ctx.network)
    end
  elseif sub == "clear" then
    stugan.kv.delete(pw_key(ctx.network))
    stugan.kv.delete(nick_key(ctx.network))
    stugan.print(ctx, "nickserv: cleared " .. ctx.network)
  else
    if stugan.kv.get(pw_key(ctx.network)) then
      stugan.print(ctx, "nickserv: " .. ctx.network .. " configured (nick "
        .. (stugan.kv.get(nick_key(ctx.network)) or "?") .. ")")
    else
      stugan.print(ctx, "nickserv: " .. ctx.network
        .. " not configured — /nickserv set <password>")
    end
    stugan.print(ctx, "  subcommands: set <password> | identify | clear")
  end
end)
