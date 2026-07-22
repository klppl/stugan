stugan.describe("AI Assistant & Conversation Summarizer (/ask <prompt>, /summarize [N])")

local provider = stugan.setting("provider", {
  type = "select",
  default = "openai",
  options = { "openai", "deepseek", "anthropic", "gemini", "ollama" },
  label = "AI Provider",
  help = "Select OpenAI, DeepSeek, Anthropic Claude, Google Gemini, or local Ollama"
})

local api_key = stugan.setting("api_key", {
  type = "text",
  secret = true,
  default = "",
  label = "API Key",
  help = "API Key for OpenAI, DeepSeek, Anthropic, or Gemini (leave blank for Ollama)"
})

local model_name = stugan.setting("model", {
  type = "text",
  default = "gpt-4o-mini",
  label = "Model Name",
  help = "e.g. gpt-4o-mini, deepseek-chat, deepseek-reasoner, claude-3-5-sonnet, gemini-1.5-flash, llama3"
})

local custom_endpoint = stugan.setting("endpoint", {
  type = "text",
  default = "",
  label = "Custom Endpoint URL",
  help = "Override default API endpoint URL if needed"
})

-- Ring buffer of recent messages per channel for /summarize
local history = {}
local MAX_HISTORY = 100

local function record_history(net, buf, sender, text, timestamp)
  if not net or not buf or not text or type(text) ~= "string" then return end
  if text:sub(1, 1) == "/" then return end -- ignore slash commands
  local key = (net:lower()) .. "/" .. (buf:lower())
  history[key] = history[key] or {}
  table.insert(history[key], {
    from = sender or "unknown",
    text = text,
    time = os.date("%H:%M", timestamp or os.time())
  })
  if #history[key] > MAX_HISTORY then
    table.remove(history[key], 1)
  end
end

stugan.hook_message(function(msg)
  if msg and msg.network and msg.buffer and msg.text then
    record_history(msg.network, msg.buffer, msg.from, msg.text, msg.time)
  end
  return msg
end)

stugan.hook_input(function(input, ctx)
  if input and ctx and ctx.network and ctx.buffer then
    record_history(ctx.network, ctx.buffer, ctx.nick or "me", input, os.time())
  end
  return input
end)

local function trim(s)
  if type(s) ~= "string" then return "" end
  return s:match("^%s*(.-)%s*$") or ""
end

local function get_endpoint()
  local custom = trim(custom_endpoint)
  if custom ~= "" then return custom end
  local prov = provider
  if prov == "deepseek" then
    return "https://api.deepseek.com/chat/completions"
  elseif prov == "anthropic" then
    return "https://api.anthropic.com/v1/messages"
  elseif prov == "gemini" then
    return "https://generativelanguage.googleapis.com/v1beta/models/" .. model_name .. ":generateContent?key=" .. api_key
  elseif prov == "ollama" then
    return "http://localhost:11434/api/generate"
  else
    return "https://api.openai.com/v1/chat/completions"
  end
end

local function call_ai(prompt, system_prompt, cb)
  local prov = provider
  local url = get_endpoint()
  local headers = { ["Content-Type"] = "application/json" }
  local req_body = {}

  if prov == "anthropic" then
    headers["x-api-key"] = api_key
    headers["anthropic-version"] = "2023-06-01"
    req_body = {
      model = model_name,
      max_tokens = 1024,
      system = system_prompt,
      messages = { { role = "user", content = prompt } }
    }
  elseif prov == "gemini" then
    req_body = {
      contents = {
        { parts = { { text = (system_prompt and (system_prompt .. "\n\n") or "") .. prompt } } }
      }
    }
  elseif prov == "ollama" then
    req_body = {
      model = model_name,
      prompt = (system_prompt and (system_prompt .. "\n\n") or "") .. prompt,
      stream = false
    }
  else -- openai default
    headers["Authorization"] = "Bearer " .. api_key
    req_body = {
      model = model_name,
      messages = {
        { role = "system", content = system_prompt or "You are a helpful IRC assistant." },
        { role = "user", content = prompt }
      }
    }
  end

  local encoded_body, err = stugan.json.encode(req_body)
  if not encoded_body then
    cb(false, "JSON encoding error: " .. (err or "unknown"))
    return
  end

  stugan.http.request({
    method = "POST",
    url = url,
    headers = headers,
    body = encoded_body
  }, function(res)
    if not res.ok then
      cb(false, "HTTP transport error: " .. (res.error or "unknown"))
      return
    end
    if res.status ~= 200 then
      cb(false, "API returned status " .. res.status .. ": " .. (res.body or ""))
      return
    end

    local data, err_dec = stugan.json.decode(res.body)
    if not data then
      cb(false, "Failed to decode response JSON: " .. (err_dec or ""))
      return
    end

    local reply = ""
    if prov == "anthropic" then
      if data.content and data.content[1] then reply = data.content[1].text or "" end
    elseif prov == "gemini" then
      if data.candidates and data.candidates[1] and data.candidates[1].content and data.candidates[1].content.parts then
        reply = data.candidates[1].content.parts[1].text or ""
      end
    elseif prov == "ollama" then
      reply = data.response or ""
    else -- openai
      if data.choices and data.choices[1] and data.choices[1].message then
        reply = data.choices[1].message.content or ""
      end
    end

    reply = reply:gsub("^%s+", ""):gsub("%s+$", "")
    if reply == "" then
      cb(false, "Empty AI response")
    else
      cb(true, reply)
    end
  end)
end

stugan.hook_command("ask", function(args, ctx)
  if #args == 0 then
    stugan.print(ctx.network, ctx.buffer, "Usage: /ask <question or prompt>")
    return
  end
  local prompt = table.concat(args, " ")
  stugan.print(ctx.network, ctx.buffer, "🤖 Asking AI (" .. provider .. " / " .. model_name .. ")…")

  call_ai(prompt, "You are a concise IRC AI bot. Keep answers direct, friendly, and concise.", function(ok, result)
    if ok then
      for line in result:gmatch("[^\r\n]+") do
        stugan.print(ctx.network, ctx.buffer, "🤖 " .. line)
      end
    else
      stugan.print(ctx.network, ctx.buffer, "❌ AI Error: " .. result)
    end
  end)
end)

stugan.hook_command("summarize", function(args, ctx)
  local count = tonumber(args[1]) or 30
  local key = (ctx.network or ""):lower() .. "/" .. (ctx.buffer or ""):lower()
  local msgs = history[key] or {}
  if #msgs == 0 then
    stugan.print(ctx.network, ctx.buffer, "No recent messages captured in this buffer to summarize.")
    return
  end

  local slice_start = math.max(1, #msgs - count + 1)
  local lines = {}
  for i = slice_start, #msgs do
    table.insert(lines, "[" .. msgs[i].time .. "] <" .. msgs[i].from .. "> " .. msgs[i].text)
  end

  local transcript = table.concat(lines, "\n")
  local prompt = "Summarize this recent IRC conversation from " .. ctx.buffer .. " into exactly 3 bullet points highlighting main discussion topics or decisions:\n\n" .. transcript

  stugan.print(ctx.network, ctx.buffer, "📊 Summarizing last " .. #lines .. " messages using AI…")

  call_ai(prompt, "You are an expert chat summarizer. Provide a clean 3-bullet point summary.", function(ok, result)
    if ok then
      stugan.print(ctx.network, ctx.buffer, "📊 Conversation Summary (" .. #lines .. " msgs):")
      for line in result:gmatch("[^\r\n]+") do
        stugan.print(ctx.network, ctx.buffer, line)
      end
    else
      stugan.print(ctx.network, ctx.buffer, "❌ Summarize Error: " .. result)
    end
  end)
end)
