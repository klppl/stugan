-- sed.lua — fix a typo in your last line with s/old/new/.
--
-- The classic IRC affordance: after sending a message, type
--   s/teh/the/        → resends the line with the first "teh" → "the"
--   s/teh/the/g       → replace every occurrence (g flag)
--   s/teh/the/i       → case-insensitive match (i flag)
--   s#a/b#c#          → any punctuation works as the delimiter
-- The s/// line itself is swallowed (never sent to the channel); the
-- corrected message is sent in its place and becomes the new "last line",
-- so corrections chain.
--
-- Matching is LITERAL substring, not Lua patterns — what people actually
-- want for typo fixes — so "." and "%" in the pattern mean themselves.
-- If the line doesn't parse as a substitution, or the pattern isn't found
-- in your last line, the text is sent through unchanged (so talking *about*
-- sed in a channel still works).
--
-- Last lines are tracked in memory per (network, buffer); they reset on
-- reload or restart, which is fine — you only ever sed something you just
-- said.

stugan.describe("Fix your last line with s/old/new/ (flags: g, i)")

-- network \t buffer -> the last plain line we sent there
local last = {}

local function key(network, buffer)
  return network .. "\t" .. buffer
end

-- parse_sed splits "s<d>pat<d>rep[<d>flags]" where <d> is any punctuation
-- delimiter. Returns pat, rep, flags or nil if the line isn't a substitution.
local function parse_sed(input)
  if input:sub(1, 1) ~= "s" then return nil end
  local d = input:sub(2, 2)
  if d == "" or not d:match("%p") then return nil end
  local de = "%" .. d -- a single % escapes any punctuation in a Lua pattern
  local pat, rep, flags = input:match("^s" .. de .. "(.-)" .. de .. "(.-)" .. de .. "(%a*)$")
  if not pat then
    -- no trailing delimiter: s/old/new
    pat, rep = input:match("^s" .. de .. "(.-)" .. de .. "(.*)$")
    flags = ""
  end
  if not pat or pat == "" then return nil end
  return pat, rep, flags
end

-- apply_sed does a literal substitution honouring g/i flags. Returns the new
-- string, or nil if the pattern wasn't found (count 0).
local function apply_sed(s, pat, rep, flags)
  local p = pat:gsub("([^%w])", "%%%1") -- escape magic chars: match literally
  if flags:find("i") then
    p = p:gsub("%a", function(c) return "[" .. c:lower() .. c:upper() .. "]" end)
  end
  local r = rep:gsub("%%", "%%%%") -- % is magic in gsub replacements
  local out, n
  if flags:find("g") then
    out, n = s:gsub(p, r)
  else
    out, n = s:gsub(p, r, 1)
  end
  if n == 0 then return nil end
  return out
end

stugan.hook_input(function(input, ctx)
  local pat, rep, flags = parse_sed(input)
  if pat and ctx.buffer ~= "" then
    local prev = last[key(ctx.network, ctx.buffer)]
    if prev then
      local fixed = apply_sed(prev, pat, rep, flags)
      if fixed then
        stugan.message(ctx.network, ctx.buffer, fixed)
        last[key(ctx.network, ctx.buffer)] = fixed
        return nil -- swallow the s/// line
      end
    end
    -- looked like sed but had nothing to fix: fall through, send literally
  end

  -- remember plain messages (not slash commands) as the correctable line
  if input ~= "" and input:sub(1, 1) ~= "/" and ctx.buffer ~= "" then
    last[key(ctx.network, ctx.buffer)] = input
  end
  return input
end)
