-- fish.lua — FiSH-style Blowfish encryption for IRC.
--
-- Wire formats (matches weechat-fish / FiSHLiM / Mircryption):
--   CBC (default):  PRIVMSG #c :+OK *<std-base64(IV(8) || ciphertext)>
--   ECB (legacy):   PRIVMSG #c :+OK <fish-base64(ciphertext)>
--
-- Plaintext is null-terminated and random-padded to an 8-byte boundary; on
-- decrypt we trim at the first null. That's the convention every interop
-- target uses.
--
-- Scope:
--   * PRIVMSG (typed text) encrypted via hook_input with chunked send for
--     lines that would exceed IRC's 510-byte payload after expansion.
--   * /me, /notice, /topic claimed and either encrypted (when keyed) or
--     delegated to the engine's native behavior (when not).
--   * Incoming PRIVMSG / NOTICE / ACTION decrypted via hook_message.
--   * Manual key management: /setkey, /setkey-ecb, /delkey, /key.
--   * DH1080 key exchange via /keyx <nick> (private-message only — DH
--     doesn't extend to multi-party channels).
--
-- SECURITY: DH1080 has no authentication. An IRC operator or anyone with
-- access to the server can MITM the exchange and read everything. Treat
-- this as casual privacy among friends, not end-to-end encryption. Any
-- key shared out-of-band (manual /setkey) is only as private as the
-- channel you shared it through.
--
-- Requires echo-message (a baseline cap stugan negotiates). Without it,
-- you would see your own ciphertext echoed locally — the engine prints
-- the post-hook (encrypted) text when the server isn't echoing it back.
--
-- Web UI: right-click a channel or query in the sidebar → "Set encryption
-- key…" opens a dialog that sends /setkey, /setkey-ecb, or /delkey here.
-- The same slash commands work from the input box.

local crypto = stugan.crypto
local BLOCK   = 8
local PREFIX_CBC = "+OK *"
local PREFIX_ECB = "+OK "

-- ---------------------------------------------------------------------------
-- fish-base64 (used by ECB mode). 8 bytes <-> 12 chars; each 4-byte half is
-- emitted as 6 chars representing its 6-bit groups, low bits first.
-- ---------------------------------------------------------------------------
local FB64 = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
local FB64_IDX = {}
for i = 1, #FB64 do FB64_IDX[FB64:byte(i)] = i - 1 end

local function fb64_encode(bytes)
  if #bytes % BLOCK ~= 0 then return nil end
  local out = {}
  for i = 1, #bytes, BLOCK do
    local b1, b2, b3, b4, b5, b6, b7, b8 = bytes:byte(i, i + 7)
    local L = ((b1 * 256 + b2) * 256 + b3) * 256 + b4
    local R = ((b5 * 256 + b6) * 256 + b7) * 256 + b8
    for _ = 1, 6 do
      out[#out + 1] = FB64:sub(R % 64 + 1, R % 64 + 1)
      R = math.floor(R / 64)
    end
    for _ = 1, 6 do
      out[#out + 1] = FB64:sub(L % 64 + 1, L % 64 + 1)
      L = math.floor(L / 64)
    end
  end
  return table.concat(out)
end

local function fb64_decode(text)
  if #text == 0 or #text % 12 ~= 0 then return nil end
  local out = {}
  for i = 1, #text, 12 do
    local R, L, mul = 0, 0, 1
    for j = 0, 5 do
      local idx = FB64_IDX[text:byte(i + j)]
      if not idx then return nil end
      R = R + idx * mul
      mul = mul * 64
    end
    mul = 1
    for j = 6, 11 do
      local idx = FB64_IDX[text:byte(i + j)]
      if not idx then return nil end
      L = L + idx * mul
      mul = mul * 64
    end
    -- Emit L then R, big-endian. Mask to 8 bits per byte; the high bits
    -- of L / R cannot exceed 32 because we built them out of indices < 64.
    for shift = 24, 0, -8 do
      out[#out + 1] = string.char(math.floor(L / 2 ^ shift) % 256)
    end
    for shift = 24, 0, -8 do
      out[#out + 1] = string.char(math.floor(R / 2 ^ shift) % 256)
    end
  end
  return table.concat(out)
end

-- ---------------------------------------------------------------------------
-- Standard base64 (used by CBC mode). We accept both padded and unpadded
-- input on decode and emit unpadded on encode — that's what fish-CBC peers
-- expect in practice.
-- ---------------------------------------------------------------------------
local B64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
local B64_IDX = {}
for i = 1, #B64 do B64_IDX[B64:byte(i)] = i - 1 end

local function b64_char(v) return B64:sub(v + 1, v + 1) end

local function b64_encode(bytes)
  local out, n = {}, #bytes
  for i = 1, n, 3 do
    local b1 = bytes:byte(i)
    local b2 = bytes:byte(i + 1) or 0
    local b3 = bytes:byte(i + 2) or 0
    local w  = b1 * 65536 + b2 * 256 + b3
    out[#out + 1] = b64_char(math.floor(w / 262144) % 64)
    out[#out + 1] = b64_char(math.floor(w / 4096)   % 64)
    if i + 1 <= n then out[#out + 1] = b64_char(math.floor(w / 64) % 64) end
    if i + 2 <= n then out[#out + 1] = b64_char(w % 64) end
  end
  return table.concat(out)
end

local function b64_decode(text)
  text = text:gsub("=+$", "")
  local out, n, i = {}, #text, 1
  while i <= n do
    local c1 = B64_IDX[text:byte(i)]
    local c2 = B64_IDX[text:byte(i + 1)]
    local c3 = B64_IDX[text:byte(i + 2)]
    local c4 = B64_IDX[text:byte(i + 3)]
    if not c1 or not c2 then return nil end
    local w = c1 * 262144 + c2 * 4096 + (c3 or 0) * 64 + (c4 or 0)
    out[#out + 1] = string.char(math.floor(w / 65536) % 256)
    if c3 then out[#out + 1] = string.char(math.floor(w / 256) % 256) end
    if c4 then out[#out + 1] = string.char(w % 256) end
    i = i + 4
  end
  return table.concat(out)
end

-- ---------------------------------------------------------------------------
-- Keystore. Per-target, keyed by "<network>\t<target-lowercased>".
-- Channel names and nicks are treated as case-insensitive here; that's not
-- strictly correct for every RFC-1459 casemapping but it's good enough for
-- the kinds of buffers people set keys on. Value layout: "<mode>\t<key>".
-- ---------------------------------------------------------------------------
local function kv_key(network, target)
  return network .. "\t" .. target:lower()
end

local function get_key(network, target)
  local v = stugan.kv.get(kv_key(network, target))
  if not v then return nil, nil end
  local mode, key = v:match("^([^\t]+)\t(.+)$")
  return key, mode
end

local function set_key(network, target, key, mode)
  stugan.kv.set(kv_key(network, target), mode .. "\t" .. key)
  -- Publish per-buffer state so the client can render a lock icon. The
  -- key name "encrypted" is the contract Sidebar.vue reads; the value is
  -- the mode so future UIs could badge "CBC" vs "ECB" differently.
  stugan.set_buffer_state(network, target, { encrypted = mode })
end

local function del_key(network, target)
  stugan.kv.delete(kv_key(network, target))
  stugan.set_buffer_state(network, target, nil)
end

-- ---------------------------------------------------------------------------
-- Padding + encrypt / decrypt.
-- ---------------------------------------------------------------------------
local function pad(plaintext)
  -- Null-terminate, then random-fill up to the next 8-byte boundary so the
  -- ciphertext doesn't leak exact plaintext length and the receiver can
  -- trim at the first null.
  local body = plaintext .. "\0"
  local rem  = #body % BLOCK
  if rem ~= 0 then
    body = body .. crypto.random(BLOCK - rem)
  end
  return body
end

local function trim_at_null(s)
  local nul = s:find("\0", 1, true)
  if nul then return s:sub(1, nul - 1) end
  return s
end

local function encrypt(plaintext, key, mode)
  local padded = pad(plaintext)
  if mode == "ecb" then
    local ct = crypto.blowfish_ecb_encrypt(key, padded)
    return PREFIX_ECB .. fb64_encode(ct)
  end
  local iv = crypto.random(BLOCK)
  local ct = crypto.blowfish_cbc_encrypt(key, iv, padded)
  return PREFIX_CBC .. b64_encode(iv .. ct)
end

-- chunk_and_encrypt splits plaintext into ≤CHUNK_BYTES pieces and encrypts
-- each. The byte budget covers both CBC (≈2.7× expansion + 5-char prefix)
-- and ECB (1.5× expansion + 4-char prefix) so a single wire line never
-- exceeds IRC's 510-byte payload limit after the server prepends its
-- `:nick!user@host PRIVMSG #buf :` framing (worst case ≈100 bytes).
local CHUNK_BYTES = 220

local function chunk_and_encrypt(plaintext, key, mode)
  local out = {}
  if #plaintext == 0 then return out end
  for i = 1, #plaintext, CHUNK_BYTES do
    out[#out + 1] = encrypt(plaintext:sub(i, i + CHUNK_BYTES - 1), key, mode)
  end
  return out
end

-- Try to decrypt text with key. Returns the plaintext on success, nil if
-- text is not a recognised fish payload or decryption produced garbage.
local function decrypt(text, key)
  if text:sub(1, #PREFIX_CBC) == PREFIX_CBC then
    local raw = b64_decode(text:sub(#PREFIX_CBC + 1))
    if not raw or #raw < BLOCK * 2 or (#raw - BLOCK) % BLOCK ~= 0 then return nil end
    local iv  = raw:sub(1, BLOCK)
    local ct  = raw:sub(BLOCK + 1)
    local ok, pt = pcall(crypto.blowfish_cbc_decrypt, key, iv, ct)
    if not ok then return nil end
    return trim_at_null(pt)
  end
  if text:sub(1, #PREFIX_ECB) == PREFIX_ECB then
    -- Some implementations prefix the ECB body with "mcps " (Mircryption);
    -- strip that before fish-base64 decoding.
    local body = text:sub(#PREFIX_ECB + 1):gsub("^mcps ", "")
    local ct   = fb64_decode(body)
    if not ct or #ct == 0 then return nil end
    local ok, pt = pcall(crypto.blowfish_ecb_decrypt, key, ct)
    if not ok then return nil end
    return trim_at_null(pt)
  end
  return nil
end

-- ---------------------------------------------------------------------------
-- DH1080 key exchange.
--
-- The prime, generator g=2, and 1080-bit exponent size are taken verbatim
-- from the FiSH-irssi / hexchat-fishlim reference implementations — they
-- are the interop contract. Cross-checked from two independent sources.
--
-- Wire format (interop with weechat-fish / FiSHLiM / mIRC-FiSH 10):
--   NOTICE <peer> :DH1080_INIT  <dh-b64(g^x mod p)>
--   NOTICE <peer> :DH1080_FINISH <dh-b64(g^y mod p)>
-- The dh-b64 variant: standard base64 of the bytes, then `=` padding is
-- replaced with the literal `A` (an IRC-safe stand-in, since `=` carries
-- meaning in some message-tag contexts).
--
-- Security caveat: DH1080 has no authentication. A MITM on the IRC server
-- can substitute their own pubkeys and decrypt everything. It's "casual
-- privacy", not real E2E. Documented loudly here because users coming
-- from messaging apps will assume more than it provides.
-- ---------------------------------------------------------------------------

local function hex_to_bytes(hex)
  hex = hex:gsub("%s", "")
  local t = {}
  for i = 1, #hex, 2 do
    t[#t + 1] = string.char(tonumber(hex:sub(i, i + 1), 16))
  end
  return table.concat(t)
end

local DH1080_PRIME = hex_to_bytes([[
  FBE1022E23D213E8 ACFA9AE8B9DFADA3 EA6B7AC7A7B7E95A B5EB2DF858921FEA
  DE95E6AC7BE7DE6A DBAB8A783E7AF7A7 FA6A2B7BEB1E72EA E2B72F9FA2BFB2A2
  EFBEFAC868BADB3E 828FA8BADFADA3E4 CC1BE7E8AFE85E96 98A783EB68FA07A7
  7AB6AD7BEB618ACF 9CA2897EB28A6189 EFA07AB99A8A7FA9 AE299EFA7BA66DEA
  FEFBEFBF0B7D8B
]])

local DH1080_G        = "\2"  -- generator = 2
local DH1080_EXP_LEN  = 135   -- 1080-bit private exponent

-- dh_b64_encode wraps b64_encode with the DH1080 padding convention.
-- Our b64_encode is already unpadded (never emits `=`), so the only
-- adjustment is appending `A` when standard b64 would have had no
-- padding (i.e. when len(bytes) % 3 == 0). The decoder mirrors this.
local function dh_b64_encode(bytes)
  local s = b64_encode(bytes)
  if #bytes % 3 == 0 then return s .. "A" end
  return s
end

local function dh_b64_decode(text)
  if #text % 4 == 1 and text:sub(-1) == "A" then
    text = text:sub(1, -2)
  end
  return b64_decode(text)
end

-- crypto.modexp returns a fixed-width result (len = len(modulus)); the
-- reference impl uses OpenSSL's BN_bn2bin which strips leading zeros.
-- Trim before the wire and before SHA-256 to match.
local function trim_leading_zeros(s)
  local i = 1
  while i < #s and s:byte(i) == 0 do i = i + 1 end
  return s:sub(i)
end

-- dh_validate rejects degenerate peer pubkeys (y in {0, 1, p-1}). DH1080
-- doesn't define a known subgroup beyond this, so we match fishlim's
-- conservative bound: 1 < y < p-1, compared as big-endian bytes.
local function dh_validate(y_bytes)
  if not y_bytes or #y_bytes == 0 or #y_bytes > #DH1080_PRIME then return false end
  if #y_bytes == 1 and y_bytes:byte(1) <= 1 then return false end
  local padded = string.rep("\0", #DH1080_PRIME - #y_bytes) .. y_bytes
  for i = 1, #DH1080_PRIME do
    local a, b = padded:byte(i), DH1080_PRIME:byte(i)
    if a < b then return true end
    if a > b then return false end
  end
  return false -- y == p
end

local function derive_session_key(shared_bytes)
  -- Strip leading zeros to match BN_bn2bin, then SHA-256, then dh-b64.
  return dh_b64_encode(crypto.sha256(trim_leading_zeros(shared_bytes)))
end

-- pending tracks initiator-side state between INIT (we sent) and FINISH
-- (we received). Keyed by network + lowercased peer nick. Receiver side
-- needs no state — it derives + replies in one shot.
local pending = {}
local function pending_key(network, peer) return network .. "\t" .. peer:lower() end

local function dh_send_init(network, peer)
  local x  = crypto.random(DH1080_EXP_LEN)
  local pk = crypto.modexp(DH1080_G, x, DH1080_PRIME)
  pending[pending_key(network, peer)] = x
  stugan.send(network, "NOTICE " .. peer .. " :DH1080_INIT "
    .. dh_b64_encode(trim_leading_zeros(pk)))
end

local function dh_handle_init(network, peer, peer_pk_b64)
  local peer_pk = dh_b64_decode(peer_pk_b64)
  if not dh_validate(peer_pk) then return false end
  local y      = crypto.random(DH1080_EXP_LEN)
  local our_pk = crypto.modexp(DH1080_G, y, DH1080_PRIME)
  local shared = crypto.modexp(peer_pk, y, DH1080_PRIME)
  set_key(network, peer, derive_session_key(shared), "cbc")
  stugan.send(network, "NOTICE " .. peer .. " :DH1080_FINISH "
    .. dh_b64_encode(trim_leading_zeros(our_pk)))
  return true
end

local function dh_handle_finish(network, peer, peer_pk_b64)
  local k = pending_key(network, peer)
  local x = pending[k]
  if not x then return false end
  pending[k] = nil
  local peer_pk = dh_b64_decode(peer_pk_b64)
  if not dh_validate(peer_pk) then return false end
  local shared = crypto.modexp(peer_pk, x, DH1080_PRIME)
  set_key(network, peer, derive_session_key(shared), "cbc")
  return true
end

-- ---------------------------------------------------------------------------
-- Hooks.
-- ---------------------------------------------------------------------------

-- Encrypt outgoing PRIVMSG text if we have a key for this buffer. Runs at
-- low priority (= early) so any later input hook sees the ciphertext, not
-- the plaintext we're about to put on the wire. Long inputs are chunked:
-- the first encrypted piece is returned (so the engine's normal send path
-- handles ordering and local-echo), the rest are sent directly as raw
-- PRIVMSGs. Same order on the wire because Lua hooks run serially.
stugan.hook_input(function(input, ctx)
  if input == "" then return input end
  local key, mode = get_key(ctx.network, ctx.buffer)
  if not key then return input end
  local cts = chunk_and_encrypt(input, key, mode)
  for i = 2, #cts do
    stugan.send(ctx.network, "PRIVMSG " .. ctx.buffer .. " :" .. cts[i])
  end
  return cts[1]
end, { priority = 100 })

-- Intercept DH1080 handshake notices before the decrypt hook runs. They
-- ride on NOTICE so a peer's first INIT lands here regardless of whether
-- a key exists. self-echoes (our own outgoing INIT/FINISH) are ignored;
-- otherwise we'd loop on our own messages with echo-message on.
stugan.hook_message(function(msg)
  if msg.kind ~= "notice" or msg.self then return msg end
  local init_b64 = msg.text:match("^DH1080_INIT (.+)$")
  if init_b64 then
    if dh_handle_init(msg.network, msg.from, init_b64) then
      stugan.print(msg.network, msg.from,
        "fish: DH1080 key exchange with " .. msg.from .. " established")
    else
      stugan.log.warn("DH1080 INIT from " .. msg.from .. " failed validation")
    end
    return nil -- consume; don't show the raw handshake in the buffer
  end
  local finish_b64 = msg.text:match("^DH1080_FINISH (.+)$")
  if finish_b64 then
    if dh_handle_finish(msg.network, msg.from, finish_b64) then
      stugan.print(msg.network, msg.from,
        "fish: DH1080 key exchange with " .. msg.from .. " established")
    else
      stugan.log.warn("DH1080 FINISH from " .. msg.from
        .. " without a pending INIT or validation failed")
    end
    return nil
  end
  return msg
end, { priority = 50 })

-- Decrypt incoming PRIVMSG / NOTICE / ACTION if a key matches. Runs late so
-- the rest of the inbound pipeline (spam filters, mention detectors) sees
-- the decrypted text and can match on it.
stugan.hook_message(function(msg)
  if msg.kind ~= "privmsg" and msg.kind ~= "notice" and msg.kind ~= "action" then
    return msg
  end
  -- For channel messages the buffer IS the target. For queries the buffer
  -- is the other party — either msg.from on inbound, or the buffer name on
  -- our own echoed self-message. msg.buffer already holds the right value
  -- (translate.go assigns it accordingly), so the lookup is one-shot.
  local key, _ = get_key(msg.network, msg.buffer)
  if not key then return msg end
  local pt = decrypt(msg.text, key)
  if pt then msg.text = pt end
  return msg
end, { priority = 900 })

-- ---------------------------------------------------------------------------
-- Commands. /setkey [target] <key> sets a CBC key (the modern default);
-- /setkey-ecb does the same in legacy ECB mode for compatibility with old
-- bots and channels.
-- ---------------------------------------------------------------------------
local function parse_target_and_key(args, ctx)
  if #args == 1 then return ctx.buffer, args[1] end
  if #args >= 2 then return args[1], table.concat(args, " ", 2) end
  return nil, nil
end

local function set_cmd(mode)
  return function(args, ctx)
    local target, key = parse_target_and_key(args, ctx)
    if not target or not key or target == "" then
      stugan.print(ctx, "usage: /setkey" .. (mode == "ecb" and "-ecb" or "") .. " [target] <key>")
      return
    end
    set_key(ctx.network, target, key, mode)
    stugan.print(ctx, "fish: " .. mode:upper() .. " key set for " .. target)
  end
end

stugan.hook_command("setkey",     set_cmd("cbc"))
stugan.hook_command("setkey-ecb", set_cmd("ecb"))

stugan.hook_command("delkey", function(args, ctx)
  local target = args[1] or ctx.buffer
  del_key(ctx.network, target)
  stugan.print(ctx, "fish: key removed for " .. target)
end)

stugan.hook_command("key", function(args, ctx)
  local target = args[1] or ctx.buffer
  local key, mode = get_key(ctx.network, target)
  if key then
    stugan.print(ctx, "fish: " .. target .. " has a " .. mode:upper()
      .. " key (" .. #key .. " bytes)")
  else
    stugan.print(ctx, "fish: no key for " .. target)
  end
end)

-- /me, /notice, /topic — we have to claim these from the built-ins so the
-- engine doesn't transmit plaintext. When a key is set we send raw IRC
-- with the encrypted body; when there's no key we delegate to the same
-- helpers the built-ins use, so behavior is identical to no-plugin.
stugan.hook_command("me", function(args, ctx)
  local text = table.concat(args, " ")
  if text == "" then
    stugan.print(ctx, "usage: /me <action>")
    return
  end
  local key, mode = get_key(ctx.network, ctx.buffer)
  if not key then
    stugan.action(ctx.network, ctx.buffer, text)
    return
  end
  for _, ct in ipairs(chunk_and_encrypt(text, key, mode)) do
    -- CTCP ACTION carries the encrypted body between \x01 markers; the
    -- receiver's translate.go strips the framing, hook_message decrypts.
    stugan.send(ctx.network, "PRIVMSG " .. ctx.buffer .. " :\1ACTION " .. ct .. "\1")
  end
end)

stugan.hook_command("notice", function(args, ctx)
  local target = args[1]
  if not target or #args < 2 then
    stugan.print(ctx, "usage: /notice <target> <text>")
    return
  end
  local text = table.concat(args, " ", 2)
  local key, mode = get_key(ctx.network, target)
  if not key then
    stugan.notice(ctx.network, target, text)
    return
  end
  for _, ct in ipairs(chunk_and_encrypt(text, key, mode)) do
    stugan.send(ctx.network, "NOTICE " .. target .. " :" .. ct)
  end
end)

-- /topic uses the active buffer as the target (matching the built-in's
-- semantics). Empty body asks the server for the current topic. Topics
-- aren't chunked: IRC treats a topic as one string, and if the encrypted
-- form is too long the server will reject the change — surfacing that
-- failure is the server's job, not the plugin's.
stugan.hook_command("topic", function(args, ctx)
  local text = table.concat(args, " ")
  if text == "" then
    stugan.send(ctx.network, "TOPIC " .. ctx.buffer)
    return
  end
  local key, mode = get_key(ctx.network, ctx.buffer)
  if not key then
    stugan.send(ctx.network, "TOPIC " .. ctx.buffer .. " :" .. text)
    return
  end
  stugan.send(ctx.network, "TOPIC " .. ctx.buffer .. " :" .. encrypt(text, key, mode))
end)

-- /keyx <nick> initiates a DH1080 exchange with a peer. Pure private-message
-- protocol — channels need an out-of-band shared key (DH only handles two
-- parties). The peer must run a FiSH-compatible client; on success both
-- ends store a fresh CBC key under each other's nick.
stugan.hook_command("keyx", function(args, ctx)
  local target = args[1] or ctx.buffer
  if target == "" or target:sub(1, 1):match("[#&+!]") then
    stugan.print(ctx, "usage: /keyx <nick> (DH1080 is private-message only)")
    return
  end
  dh_send_init(ctx.network, target)
  stugan.print(ctx, "fish: DH1080 INIT sent to " .. target .. "; waiting for FINISH…")
end)

stugan.log.info("fish loaded (CBC default, ECB legacy, manual keys)")
