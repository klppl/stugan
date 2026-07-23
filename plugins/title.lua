-- title.lua — announce the <title> of links posted in a channel.
--
-- A worked example of stugan.http. When someone pastes an http(s) link, this
-- fetches the page off-thread and prints its title locally into the buffer
-- (stugan.print injects a line for you only — it is not sent to IRC, so there
-- is no echo and nobody else sees your client chattering).
--
-- Settings (persisted in kv; no config.toml):
--   /title on | off     toggle announcing (default on)
--   max length          longest title shown, set from the plugin settings form
--
-- This is intentionally simple: it scrapes <title> with a Lua pattern rather
-- than parsing HTML, and only follows the first link in a message.

stugan.describe("Announce the title of links posted in a channel")

local DEFAULT_MAX = 140
stugan.setting("enabled", { type = "select", default = "on", options = { "on", "off" },
  label = "Announce link titles" })
stugan.setting("max", { type = "number", default = DEFAULT_MAX, label = "Max title length" })

local function enabled() return (stugan.kv.get("enabled") or "on") == "on" end
local function maxlen() return tonumber(stugan.kv.get("max")) or DEFAULT_MAX end

-- first_url returns the first http(s) URL in text, trimmed of trailing
-- sentence punctuation, or nil.
local function first_url(text)
  local u = text:match("https?://%S+")
  if not u then return nil end
  u = u:gsub("[%.,!%?;:%)%]>]+$", "")
  return u ~= "" and u or nil
end

-- A few HTML entities show up in titles often enough to be worth decoding.
local ENTITIES = {
  ["&amp;"] = "&", ["&lt;"] = "<", ["&gt;"] = ">", ["&quot;"] = '"',
  ["&#39;"] = "'", ["&apos;"] = "'", ["&nbsp;"] = " ",
}

-- title_of extracts and tidies the <title> from an HTML body, or nil.
local function title_of(body)
  local t = body:match("<[Tt][Ii][Tt][Ll][Ee][^>]*>(.-)</[Tt][Ii][Tt][Ll][Ee]>")
  if not t then return nil end
  t = t:gsub("%s+", " "):gsub("^%s+", ""):gsub("%s+$", "")
  t = t:gsub("&#?%w+;", function(e) return ENTITIES[e] or e end)
  if t == "" then return nil end
  local lim = maxlen()
  if #t > lim then t = t:sub(1, lim - 1) .. "…" end
  return t
end

stugan.hook_message(function(msg)
  if enabled() and not msg.self and (msg.kind == "privmsg" or msg.kind == "action") then
    local url = first_url(msg.text)
    if url then
      -- Capture the buffer now; the callback fires later, off this message.
      local network, buffer = msg.network, msg.buffer
      stugan.http.get(url, function(res)
        if not res.ok then
          stugan.log.debug("title fetch failed: " .. (res.error or "?"))
          return
        end
        local ct = res.headers["content-type"] or ""
        if not ct:find("html", 1, true) then return end -- only HTML pages
        local title = title_of(res.body)
        if title then
          stugan.print(network, buffer, "↪ " .. title)
        end
      end)
    end
  end
  return msg
end)
