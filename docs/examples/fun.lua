-- fun.lua — the traditional IRC toy commands.
--
--   /roll 2d6        roll dice; also /roll d20, /roll 6 (= 1d6), /roll 3d6+2
--   /8ball <q>       ask the magic 8-ball
--   /slap <nick>     a fish-based grievance
--
-- Results are sent to the current channel/query (the point is to share them).
-- Randomness comes from stugan.crypto.random, sampled without modulo bias —
-- overkill for dice, but it's the RNG we have and it's honest.

stugan.describe("/roll dice, /8ball, /slap — IRC toy commands")

-- rand_below returns a uniform integer in [0, n) using rejection sampling over
-- a 32-bit value, so no result is very slightly favoured the way raw % would.
local function rand_below(n)
  if n <= 1 then return 0 end
  local span = 4294967296 -- 2^32
  local limit = span - (span % n)
  while true do
    local b = stugan.crypto.random(4)
    local x = b:byte(1) * 16777216 + b:byte(2) * 65536 + b:byte(3) * 256 + b:byte(4)
    if x < limit then return x % n end
  end
end

local function die(sides) return rand_below(sides) + 1 end

local function send(ctx, text)
  if ctx.buffer == "" then
    stugan.print(ctx, "fun: open a channel or query first")
    return
  end
  stugan.action(ctx.network, ctx.buffer, text)
end

stugan.hook_command("roll", function(args, ctx)
  local spec = (args[1] or "d6"):lower()
  local count, sides, sign, mod = spec:match("^(%d*)d(%d+)([%+%-]?)(%d*)$")
  if not sides then
    -- bare number: /roll 20 == /roll 1d20
    local n = tonumber(spec)
    if not n then
      stugan.print(ctx, "usage: /roll 2d6  (also d20, 6, 3d6+2)")
      return
    end
    count, sides = 1, n
  else
    count = tonumber(count) or 1
    sides = tonumber(sides)
  end

  if sides < 1 or sides > 1000000 or count < 1 or count > 100 then
    stugan.print(ctx, "roll: keep it to 1..100 dice of 1..1000000 sides")
    return
  end

  local modifier = 0
  if sign ~= "" and mod ~= "" then
    modifier = tonumber(mod) or 0
    if sign == "-" then modifier = -modifier end
  end

  local rolls, total = {}, modifier
  for i = 1, count do
    local r = die(sides)
    rolls[i] = r
    total = total + r
  end

  local label = count .. "d" .. sides
  if modifier ~= 0 then label = label .. (modifier > 0 and "+" or "") .. modifier end
  local detail = ""
  if count > 1 or modifier ~= 0 then
    detail = " (" .. table.concat(rolls, ", ")
      .. (modifier ~= 0 and ((modifier > 0 and " +" or " ") .. modifier) or "")
      .. ")"
  end
  send(ctx, "rolls " .. label .. ": " .. total .. detail)
end)

local EIGHTBALL = {
  "it is certain", "without a doubt", "yes, definitely", "you may rely on it",
  "most likely", "outlook good", "signs point to yes", "reply hazy, try again",
  "ask again later", "cannot predict now", "don't count on it", "my reply is no",
  "very doubtful", "outlook not so good",
}

stugan.hook_command("8ball", function(args, ctx)
  if #args == 0 then
    stugan.print(ctx, "usage: /8ball <question>")
    return
  end
  send(ctx, "🎱 " .. EIGHTBALL[rand_below(#EIGHTBALL) + 1])
end)

stugan.hook_command("slap", function(args, ctx)
  local who = args[1]
  if not who then
    stugan.print(ctx, "usage: /slap <nick>")
    return
  end
  send(ctx, "slaps " .. who .. " around a bit with a large trout")
end)
