stugan.describe("Outbound Notification Webhook (Discord / Slack / Ntfy / Generic)")

local webhook_url = stugan.setting("webhook_url", {
  type = "text",
  secret = true,
  default = "",
  label = "Webhook Target URL",
  help = "Target HTTP/HTTPS URL for outbound notifications (e.g. Discord, Slack, Ntfy)"
})

local format = stugan.setting("format", {
  type = "select",
  default = "discord",
  options = { "discord", "slack", "ntfy", "generic" },
  label = "Payload Format",
  help = "Target platform JSON structure"
})

local notify_highlights = stugan.setting("notify_highlights", {
  type = "select",
  default = "true",
  options = { "true", "false" },
  label = "Notify Highlights / Mentions",
  help = "Send push notification when highlighted or mentioned"
})

local notify_all = stugan.setting("notify_all", {
  type = "select",
  default = "false",
  options = { "true", "false" },
  label = "Notify All Messages",
  help = "Relay every incoming message (for notification channels or logging bridges)"
})

local function build_payload(fmt, net, buf, sender, text)
  if fmt == "discord" then
    return {
      content = "💬 **[" .. (buf or "channel") .. " on " .. (net or "irc") .. "]** <" .. (sender or "user") .. "> " .. text
    }
  elseif fmt == "slack" then
    return {
      text = "💬 *[" .. (buf or "channel") .. " on " .. (net or "irc") .. "]* <" .. (sender or "user") .. "> " .. text
    }
  elseif fmt == "ntfy" then
    return {
      topic = "stugan",
      title = "Mention in " .. (buf or "") .. " (" .. (net or "") .. ")",
      message = "<" .. (sender or "") .. "> " .. text
    }
  else -- generic
    return {
      network = net,
      buffer = buf,
      from = sender,
      text = text,
      timestamp = os.time()
    }
  end
end

local function trim(s)
  if type(s) ~= "string" then return "" end
  return s:match("^%s*(.-)%s*$") or ""
end

local function send_webhook(net, buf, sender, text, cb)
  local target = trim(webhook_url)
  if target == "" then
    if cb then cb(false, "Webhook URL is not configured (set via /webhook url <URL> or Settings -> Plugins)") end
    return
  end

  local payload = build_payload(format, net, buf, sender, text)
  local body_str, err = stugan.json.encode(payload)
  if not body_str then
    if cb then cb(false, "Failed to encode JSON payload: " .. (err or "")) end
    return
  end

  stugan.http.request({
    method = "POST",
    url = target,
    headers = { ["Content-Type"] = "application/json" },
    body = body_str
  }, function(res)
    if not res.ok then
      if cb then cb(false, "HTTP transport error: " .. (res.error or "unknown")) end
      return
    end
    if res.status >= 200 and res.status < 300 then
      if cb then cb(true, "Webhook delivered successfully (HTTP " .. res.status .. ")") end
    else
      if cb then cb(false, "Webhook target returned status " .. res.status .. ": " .. (res.body or "")) end
    end
  end)
end

-- Monitor incoming messages for highlights / mentions
stugan.hook_message(function(msg)
  if not msg or msg.self then return msg end
  
  local is_hl = (msg.highlight == true)
  local is_all = (notify_all == "true")
  local is_enabled = (notify_highlights == "true")

  if (is_enabled and is_hl) or is_all then
    send_webhook(msg.network, msg.buffer, msg.from, msg.text, nil)
  end

  return msg
end, { priority = 900 })

stugan.hook_command("webhook", function(args, ctx)
  if #args == 0 then
    stugan.print(ctx.network, ctx.buffer, "Usage: /webhook <test|url|format|status>")
    stugan.print(ctx.network, ctx.buffer, "  /webhook test                   Send test notification payload")
    stugan.print(ctx.network, ctx.buffer, "  /webhook url <https://...>     Set webhook endpoint URL")
    stugan.print(ctx.network, ctx.buffer, "  /webhook format <discord|slack|ntfy|generic>")
    return
  end

  local cmd = args[1]:lower()
  if cmd == "test" then
    stugan.print(ctx.network, ctx.buffer, "🔔 Sending test webhook notification to format '" .. format .. "'…")
    send_webhook(ctx.network, ctx.buffer, ctx.nick or "stugan", "Test notification from stugan webhooks plugin!", function(ok, msg)
      if ok then
        stugan.print(ctx.network, ctx.buffer, "✅ " .. msg)
      else
        stugan.print(ctx.network, ctx.buffer, "❌ " .. msg)
      end
    end)
  elseif cmd == "url" and #args >= 2 then
    local new_url = args[2]
    stugan.kv.set("webhook_url", new_url)
    stugan.print(ctx.network, ctx.buffer, "Webhook target URL updated.")
  elseif cmd == "format" and #args >= 2 then
    local new_fmt = args[2]:lower()
    if new_fmt == "discord" or new_fmt == "slack" or new_fmt == "ntfy" or new_fmt == "generic" then
      stugan.kv.set("format", new_fmt)
      stugan.print(ctx.network, ctx.buffer, "Webhook payload format updated to: " .. new_fmt)
    else
      stugan.print(ctx.network, ctx.buffer, "Invalid format. Supported: discord, slack, ntfy, generic")
    end
  elseif cmd == "status" then
    local configured = trim(webhook_url) ~= "" and "Configured" or "Not configured"
    stugan.print(ctx.network, ctx.buffer, "Webhook Status: " .. configured .. " | Format: " .. format .. " | Highlights: " .. notify_highlights)
  else
    stugan.print(ctx.network, ctx.buffer, "Unknown subcommand. Usage: /webhook <test|url|format|status>")
  end
end)
