-- auto_away.lua — set AWAY after an idle period, clear it on our next line.
-- Proposed API (Phase 5); see docs/plugins.md.

stugan.describe("Set AWAY after 10 minutes idle; clear it on your next line")

local IDLE_MS = 10 * 60 * 1000
local last = {} -- network name -> unix seconds of our last sent line

stugan.hook_input(function(input, ctx)
  last[ctx.network] = os.time()
  stugan.send(ctx.network, "AWAY") -- clear away
  return input
end)

stugan.hook_timer(60 * 1000, function()
  local now = os.time()
  for _, n in ipairs(stugan.networks()) do
    local t = last[n.name]
    if t and (now - t) * 1000 > IDLE_MS then
      stugan.send(n.name, "AWAY :idle")
    end
  end
end)
