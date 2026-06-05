-- expand.lua — a text expander for chat.
--
-- Type a short trigger and it becomes the full text when you send the line:
--   ;shrug      → ¯\_(ツ)_/¯
--   ;tableflip  → (╯°□°)╯︵ ┻━┻
-- Triggers expand anywhere in a line ("well ;shrug i guess"), and several
-- can appear at once. Tab-completion offers the trigger names.
--
-- Define your own, persisted in stugan.kv across reloads and restarts:
--   /exp                       list every trigger (yours + built-in)
--   /exp brb back in a moment  define ;brb
--   /exp del brb               remove it
--   /exp prefix !              change the trigger prefix (default ;)
-- A custom trigger shadows a built-in of the same name. No config.toml needed.

stugan.describe("Text expander: ;shrug, ;tableflip, … ; manage with /exp")

-- Expansion names are word chars; the prefix setting is stored under a key
-- that isn't a valid name (".prefix") so it never shows up as a trigger.
local function is_expansion_key(k)
  return k:match("^[%w_]+$") ~= nil
end

-- PFX/PAT are rebuilt whenever the prefix changes (via /exp prefix).
local PFX, PAT
local function set_prefix(p)
  PFX = p
  PAT = p:gsub("(%W)", "%%%1") .. "([%w_]+)"
end
set_prefix(stugan.kv.get(".prefix") or ";")

local BUILTIN = {
  shrug = [[¯\_(ツ)_/¯]],
  tableflip = "(╯°□°)╯︵ ┻━┻",
  unflip = "┬─┬ ノ( ゜-゜ノ)",
  lenny = "( ͡° ͜ʖ ͡°)",
  disapprove = "ಠ_ಠ",
  fingers = "┌( ಠ‿ಠ)┘",
  why = "ლ(ಠ益ಠლ)",
  facepalm = "(－‸ლ)",
}

-- A trigger name is a run of word characters after the prefix.
local function lookup(name)
  return stugan.kv.get(name) or BUILTIN[name]
end

-- gsub over PAT (prefix + name); the callback returns nil for unknown names,
-- which leaves the original text untouched.
stugan.hook_input(function(input, ctx)
  if input == "" then return input end
  return (input:gsub(PAT, function(name) return lookup(name) end))
end)

stugan.hook_completion(function(word, ctx)
  if word:sub(1, #PFX) ~= PFX then return end
  local frag = word:sub(#PFX + 1)
  local out, seen = {}, {}
  local function consider(name)
    if is_expansion_key(name) and not seen[name] and name:sub(1, #frag) == frag then
      seen[name] = true
      out[#out + 1] = PFX .. name
    end
  end
  for k in pairs(stugan.kv.all()) do consider(k) end
  for k in pairs(BUILTIN) do consider(k) end
  return out
end)

stugan.hook_command("exp", function(args, ctx)
  -- list
  if #args == 0 then
    local user = stugan.kv.all()
    local names, seen = {}, {}
    for k in pairs(user) do
      if is_expansion_key(k) then names[#names + 1] = k; seen[k] = true end
    end
    for k in pairs(BUILTIN) do if not seen[k] then names[#names + 1] = k end end
    table.sort(names)
    if #names == 0 then
      stugan.print(ctx, "exp: no expansions defined")
      return
    end
    stugan.print(ctx, "exp: expansions (prefix '" .. PFX .. "'):")
    for _, n in ipairs(names) do
      local tag = user[n] and "" or "  (built-in)"
      stugan.print(ctx, "  " .. PFX .. n .. " → " .. (user[n] or BUILTIN[n]) .. tag)
    end
    return
  end

  -- delete
  if args[1] == "del" then
    local name = args[2]
    if not name then
      stugan.print(ctx, "usage: /exp del <name>")
    elseif stugan.kv.get(name) then
      stugan.kv.delete(name)
      stugan.print(ctx, "exp: removed " .. PFX .. name)
    else
      stugan.print(ctx, "exp: no custom expansion " .. PFX .. name)
    end
    return
  end

  -- change the trigger prefix
  if args[1] == "prefix" then
    local p = args[2]
    if not p then
      stugan.print(ctx, "exp: prefix is '" .. PFX .. "'  (usage: /exp prefix <char>)")
    elseif p:lower() == "default" then
      stugan.kv.delete(".prefix")
      set_prefix(";")
      stugan.print(ctx, "exp: prefix reset to ';'")
    elseif #p == 1 and p:match("%p") then
      stugan.kv.set(".prefix", p)
      set_prefix(p)
      stugan.print(ctx, "exp: prefix is now '" .. p .. "'")
    else
      stugan.print(ctx, "exp: prefix must be a single punctuation character")
    end
    return
  end

  -- define
  local name = args[1]
  if not name:match("^[%w_]+$") then
    stugan.print(ctx, "exp: name must be letters/digits/underscore")
    return
  end
  if #args < 2 then
    stugan.print(ctx, "usage: /exp <name> <text…>")
    return
  end
  local value = table.concat(args, " ", 2)
  stugan.kv.set(name, value)
  stugan.print(ctx, "exp: " .. PFX .. name .. " → " .. value)
end)
