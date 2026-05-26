-- highlight_reply.lua — auto-reply when a configured word is mentioned.
-- Configure in config.toml:
--   [plugins.settings.highlight_reply]
--   word = "ping"
-- Proposed API (Phase 5); see docs/plugins.md.

local word = stugan.config("word", "ping")

stugan.hook_message(function(msg)
  if msg.kind == "privmsg" and not msg.self
     and msg.text:lower():find(word) then
    stugan.message(msg.network, msg.buffer, msg.from .. ": pong")
  end
  return msg
end)
