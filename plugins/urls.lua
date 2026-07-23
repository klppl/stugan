-- urls.lua — remember the links posted in a buffer.
--
-- A persistent daemon is the right place to keep "what was that link someone
-- pasted yesterday?" This watches messages for http(s) URLs and keeps the
-- last few per buffer in stugan.kv, so the list survives reload and restart.
--
-- Commands (act on the active buffer):
--   /urls          list the recent links here
--   /urls <n>      list the last n
--   /urls clear    forget them
--
-- Settings live inside stugan (persisted in kv) — no config.toml:
--   /urls max <n>    links retained per buffer (default 50)
--   /urls show <n>   default number /urls prints (default 10)

stugan.describe("Remember links posted in a buffer (/urls, /urls clear)")

-- Setting keys have no tab, so they never collide with the per-buffer
-- "network\tbuffer" list keys.
local DEFAULT_MAX, DEFAULT_SHOW = 50, 10
local function get_max() return tonumber(stugan.kv.get("max")) or DEFAULT_MAX end
local function get_show() return tonumber(stugan.kv.get("show")) or DEFAULT_SHOW end

-- Declare them for the GUI form. No apply callback needed: get_max/get_show
-- read kv live, so a change from the form takes effect on the next message.
stugan.setting("max", { type = "number", default = DEFAULT_MAX, label = "Links kept per buffer" })
stugan.setting("show", { type = "number", default = DEFAULT_SHOW, label = "Shown by /urls" })

-- One kv entry per (network, buffer): records joined by "\n", each record
-- "epoch\tnick\turl". kv keys use \t, distinct from the field/record seps.
local function key(network, buffer)
  return network .. "\t" .. buffer
end

local function load(network, buffer)
  local raw = stugan.kv.get(key(network, buffer))
  local out = {}
  if raw then
    for line in (raw .. "\n"):gmatch("(.-)\n") do
      if line ~= "" then out[#out + 1] = line end
    end
  end
  return out
end

-- extract pulls http(s) URLs out of text, trimming trailing sentence
-- punctuation that almost never belongs to the link itself.
local function extract(text)
  local urls = {}
  for u in text:gmatch("https?://%S+") do
    u = u:gsub("[%.,!%?;:%)%]>]+$", "")
    if #u > 0 then urls[#urls + 1] = u end
  end
  return urls
end

stugan.hook_message(function(msg)
  if (msg.kind == "privmsg" or msg.kind == "action") and msg.buffer ~= "" then
    local found = extract(msg.text)
    if #found > 0 then
      local who = msg.from
      if msg.self then who = stugan.nick(msg.network) or "me" end
      local list = load(msg.network, msg.buffer)
      for _, u in ipairs(found) do
        list[#list + 1] = (msg.time or os.time()) .. "\t" .. who .. "\t" .. u
      end
      while #list > get_max() do table.remove(list, 1) end
      stugan.kv.set(key(msg.network, msg.buffer), table.concat(list, "\n"))
    end
  end
  return msg
end)

stugan.hook_command("urls", function(args, ctx)
  if ctx.buffer == "" then
    stugan.print(ctx, "urls: no active buffer")
    return
  end
  if args[1] == "clear" then
    stugan.kv.delete(key(ctx.network, ctx.buffer))
    stugan.print(ctx, "urls: cleared " .. ctx.buffer)
    return
  end
  if args[1] == "max" or args[1] == "show" then
    local n = tonumber(args[2])
    if not n or n < 1 then
      local cur = args[1] == "max" and get_max() or get_show()
      stugan.print(ctx, "urls: " .. args[1] .. " = " .. cur .. "  (usage: /urls " .. args[1] .. " <n>)")
    else
      stugan.kv.set(args[1], n)
      stugan.print(ctx, "urls: " .. args[1] .. " = " .. n)
    end
    return
  end

  local limit = tonumber(args[1]) or get_show()
  local list = load(ctx.network, ctx.buffer)
  if #list == 0 then
    stugan.print(ctx, "urls: none seen in " .. ctx.buffer)
    return
  end

  local from = math.max(1, #list - limit + 1)
  stugan.print(ctx, "urls: last " .. (#list - from + 1) .. " in " .. ctx.buffer .. ":")
  for i = from, #list do
    local t, nick, url = list[i]:match("^(.-)\t(.-)\t(.*)$")
    local when = (t and tonumber(t)) and os.date("%H:%M", tonumber(t)) or "--:--"
    stugan.print(ctx, "  " .. when .. " <" .. (nick or "?") .. "> " .. (url or list[i]))
  end
end)
