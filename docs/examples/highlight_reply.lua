-- highlight_reply.lua — auto-reply "pong" when a trigger word is mentioned.
-- Set the word from inside stugan (persisted in kv) — no config.toml:
--   /hlreply          show the current trigger word
--   /hlreply <word>   set it          (default "ping")
--   /hlreply default  reset to default
-- See docs/plugins.md.

stugan.describe("Auto-reply 'pong' when a trigger word is mentioned (/hlreply)")

local DEFAULT_WORD = "ping"
local word = stugan.kv.get("word") or DEFAULT_WORD

stugan.hook_message(function(msg)
  if msg.kind == "privmsg" and not msg.self
      and msg.text:lower():find(word, 1, true) then -- plain substring, not a pattern
    stugan.message(msg.network, msg.buffer, msg.from .. ": pong")
  end
  return msg
end)

stugan.hook_command("hlreply", function(args, ctx)
  if #args == 0 then
    stugan.print(ctx, "hlreply: trigger word is '" .. word .. "'")
    return
  end
  if args[1]:lower() == "default" then
    stugan.kv.delete("word")
    word = DEFAULT_WORD
    stugan.print(ctx, "hlreply: reset to '" .. word .. "'")
    return
  end
  word = args[1]:lower()
  stugan.kv.set("word", word)
  stugan.print(ctx, "hlreply: trigger word is now '" .. word .. "'")
end)
