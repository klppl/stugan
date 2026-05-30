-- greet.lua — a slash-command plus a content filter.
-- Proposed API (Phase 5); see docs/plugins.md.

stugan.describe("/greet <nick> to say hello; drops messages mentioning 'spoiler'")

-- /greet <nick>  → say hello to <nick> from the current buffer's network.
stugan.hook_command("greet", function(args, ctx)
  if not args[1] then
    stugan.print(ctx, "usage: /greet <nick>")
    return
  end
  stugan.message(ctx.network, args[1], "hello from a plugin!")
end)

-- Drop any incoming message that mentions "spoiler".
stugan.hook_message(function(msg)
  if msg.text:lower():find("spoiler") then
    return nil
  end
  return msg
end)
