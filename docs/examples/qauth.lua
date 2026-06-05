-- qauth.lua — authenticate with QuakeNet's Q (CServe) on connect.
--
-- Like nickserv.lua, but for QuakeNet, whose Q bot uses its own scheme. Two
-- methods are supported:
--   * challenge (default) — CHALLENGEAUTH, an HMAC-SHA-256 challenge/response.
--     Your password never goes over the wire, only a one-time digest of it.
--   * plain — the old AUTH <user> <pass> (what the ZNC qauth module did).
--     Simple, but the password crosses the network in clear; use only if a
--     network blocks challengeauth.
--
-- On connect this authenticates automatically; it also re-auths if Q asks, and
-- (with hidehost) sets +x to mask your host once Q confirms login.
--
-- Everything is configured from inside stugan — no config.toml needed. Set it
-- up from Q's query or the status buffer so nothing leaks to a channel:
--   /qauth set <username> <password>   save credentials for this network
--   /qauth                             authenticate now
--   /qauth show                        username, method, hidehost (no password)
--   /qauth method <challenge|plain>    choose the scheme (default challenge)
--   /qauth hidehost <on|off>           +x to mask your host after login (def on)
--   /qauth clear                       forget credentials
--
-- The password is held in the plugin KV (SQLite), plaintext at rest — same
-- trust level as any stored IRC password. With the default challenge method it
-- is never transmitted. We can only match the service by nick ("Q"), since the
-- message table carries no host; on QuakeNet "Q" is a reserved service nick, so
-- that is safe in practice.

stugan.describe("Authenticate with QuakeNet Q on connect (challenge or plain)")

local Q = "Q@CServe.quakenet.org"

local pending = {} -- network -> true while a CHALLENGE is outstanding

local function ukey(n) return "user\t" .. n end
local function pkey(n) return "pass\t" .. n end

local function creds(network)
  local u = stugan.kv.get(ukey(network))
  local p = stugan.kv.get(pkey(network))
  if u and p then return u, p end
  return nil
end

local function use_challenge()
  return stugan.kv.get("method") ~= "plain" -- challenge unless set to plain
end

local function hidehost_enabled()
  return stugan.kv.get("hidehost") ~= "off" -- on unless set to off
end

-- Declared for the GUI form. Both are read live (above), so no apply callback
-- is needed. The credentials are deliberately NOT settings — they're set per
-- network with /qauth set and never exposed to the form.
stugan.setting("method", {
  type = "select", options = { "challenge", "plain" }, default = "challenge",
  label = "Auth method", help = "challenge never sends your password",
})
stugan.setting("hidehost", {
  type = "select", options = { "on", "off" }, default = "on",
  label = "Hide host (+x after login)",
})

-- Raw PRIVMSG to Q with no local echo, so the password is never shown.
local function toq(network, text)
  stugan.send(network, "PRIVMSG " .. Q .. " :" .. text)
end

-- ---- HMAC-SHA-256 over stugan.crypto.sha256 (verified against crypto/hmac) ----

local function bxor(a, b)
  local r, p = 0, 1
  for _ = 1, 8 do
    local x, y = a % 2, b % 2
    if x ~= y then r = r + p end
    a, b, p = (a - x) / 2, (b - y) / 2, p * 2
  end
  return r
end

local function tohex(s)
  return (s:gsub(".", function(c) return string.format("%02x", c:byte()) end))
end

local function hmac_sha256(key, msg)
  if #key > 64 then key = stugan.crypto.sha256(key) end
  key = key .. string.rep("\0", 64 - #key)
  local o, i = {}, {}
  for n = 1, 64 do
    local b = key:byte(n)
    o[n] = string.char(bxor(b, 0x5c))
    i[n] = string.char(bxor(b, 0x36))
  end
  return stugan.crypto.sha256(table.concat(o) .. stugan.crypto.sha256(table.concat(i) .. msg))
end

-- QuakeNet CHALLENGEAUTH (HMAC-SHA-256):
--   key      = sha256_hex( lower(user) .. ":" .. sha256_hex(pass[1..10]) )
--   response = hmac_sha256_hex( key, challenge )
-- Q truncates passwords to 10 characters, so we hash only the first 10.
local function challenge_response(user, pass, challenge)
  local sha_pw = tohex(stugan.crypto.sha256(pass:sub(1, 10)))
  local key = tohex(stugan.crypto.sha256(user:lower() .. ":" .. sha_pw))
  return tohex(hmac_sha256(key, challenge))
end

-- ---- auth flow ----

local function authenticate(network)
  local u, p = creds(network)
  if not u then return false end
  if use_challenge() then
    pending[network] = true
    toq(network, "CHALLENGE") -- Q replies with a CHALLENGE notice (handled below)
  else
    toq(network, "AUTH " .. u .. " " .. p)
  end
  return true
end

stugan.hook_signal("connect", function(s)
  authenticate(s.network)
end)

stugan.hook_message(function(msg)
  if msg.from ~= "Q" or (msg.kind ~= "notice" and msg.kind ~= "privmsg") then
    return msg
  end
  local net = msg.network

  -- Challenge step 2: Q sent us the challenge; answer it.
  if pending[net] then
    local chal = msg.text:match("^CHALLENGE%s+(%S+)")
    if chal then
      pending[net] = nil
      local u, p = creds(net)
      if u then
        toq(net, "CHALLENGEAUTH " .. u .. " "
          .. challenge_response(u, p, chal) .. " HMAC-SHA-256")
      end
      return msg
    end
  end

  local low = msg.text:lower()
  -- Plain path: Q prompts us to authenticate (e.g. after a netsplit).
  if not use_challenge() and low:find("auth") and low:find("authenticate") then
    authenticate(net)
  end
  -- Mask our host once Q confirms the login.
  if hidehost_enabled() and low:find("you are now logged in") then
    stugan.send(net, "MODE " .. (stugan.nick(net) or "") .. " +x")
  end
  return msg
end)

stugan.hook_command("qauth", function(args, ctx)
  local sub = (args[1] or ""):lower()

  if sub == "set" then
    local u, p = args[2], args[3]
    if not u or not p then
      stugan.print(ctx, "usage: /qauth set <username> <password>")
      return
    end
    stugan.kv.set(ukey(ctx.network), u)
    stugan.kv.set(pkey(ctx.network), p)
    stugan.print(ctx, "qauth: saved Q login for " .. ctx.network .. " (user " .. u .. ")")
  elseif sub == "show" then
    local u = stugan.kv.get(ukey(ctx.network))
    local opts = "method " .. (use_challenge() and "challenge" or "plain")
      .. ", hidehost " .. (hidehost_enabled() and "on" or "off")
    if u then
      stugan.print(ctx, "qauth: " .. ctx.network .. " user " .. u
        .. ", password set, " .. opts)
    else
      stugan.print(ctx, "qauth: nothing saved for " .. ctx.network .. " (" .. opts .. ")")
    end
  elseif sub == "method" then
    local m = (args[2] or ""):lower()
    if m == "plain" or m == "challenge" then
      stugan.kv.set("method", m)
      stugan.print(ctx, "qauth: method = " .. m)
    else
      stugan.print(ctx, "usage: /qauth method <challenge|plain>")
    end
  elseif sub == "hidehost" then
    local v = (args[2] or ""):lower()
    if v == "on" or v == "off" then
      stugan.kv.set("hidehost", v)
      stugan.print(ctx, "qauth: hidehost = " .. v)
    else
      stugan.print(ctx, "usage: /qauth hidehost <on|off>")
    end
  elseif sub == "clear" then
    stugan.kv.delete(ukey(ctx.network))
    stugan.kv.delete(pkey(ctx.network))
    stugan.print(ctx, "qauth: cleared " .. ctx.network)
  elseif sub == "" then
    if authenticate(ctx.network) then
      stugan.print(ctx, "qauth: authenticating with Q…")
    else
      stugan.print(ctx, "qauth: nothing saved for " .. ctx.network
        .. " — /qauth set <user> <pass>")
    end
  else
    stugan.print(ctx, "qauth: set <user> <pass> | show | method <challenge|plain>"
      .. " | hidehost <on|off> | clear | (no arg) auth now")
  end
end)
