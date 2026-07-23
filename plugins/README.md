# Stugan Plugin Library

Welcome to the official **stugan** plugin library! This directory contains ready-to-use Lua plugins that extend stugan with rich IRC features, automation, encryption, AI assistants, webhooks, and custom slash commands.

---

## 🚀 Quick Start: Loading Plugins

Official plugins can be installed directly from within **stugan** without manually copying files!

### Installing a Plugin
In any channel or query buffer, run the `/load` command followed by the plugin name:

```text
/load <script-name>
```

#### Examples:
- `/load title` — Download and activate URL page title previewer
- `/load ai` — Download and activate AI assistant & conversation summarizer
- `/load away` — Download and activate idle auto-away + auto-reply
- `/load fish` — Download and activate FiSH Blowfish encryption

When you run `/load`, stugan automatically fetches the latest version from the official library (`https://raw.githubusercontent.com/klippelism/stugan/main/plugins/<script-name>.lua`), places it in your `$STUGAN_HOME/scripts/` directory, and hot-loads it immediately into the runtime.

---

## 🛠️ Managing Plugins

### Slash Commands
- `/load <script-name>` — Download (or update) and load a plugin from the official library.
- `/reload <script-name>` — Reload an existing local plugin script from disk.
- `/unload <script-name>` — Unload and disable a running plugin.

### Web UI
You can also view, configure, load, reload, or unload installed plugins visually:
1. Open stugan in your browser.
2. Go to **Settings → Plugins**.
3. Toggle, reload, or configure settings (e.g., API keys, trigger words) for any installed plugin.

---

## 📚 Official Plugin Catalog

| Plugin | Description | Commands & Features |
|---|---|---|
| **[`ai.lua`](ai.lua)** | AI Chat Assistant & Summarizer | `/ask <prompt>` to query OpenAI, DeepSeek, Claude, Gemini, or local Ollama.<br>`/summarize [N]` to summarize recent buffer history. |
| **[`away.lua`](away.lua)** | Idle Auto-Away & Auto-Reply | Automatically sets `/away` when idle and sends auto-replies to PMs.<br>Commands: `/idle`, `/awaymsg`, `/awayreply`, `/away`, `/back`. |
| **[`expand.lua`](expand.lua)** | Text Expander & Snippets | Expands custom shortcuts like `;shrug` → `¯\_(ツ)_/¯`. Tab-completion support.<br>Commands: `/exp`. |
| **[`fish.lua`](fish.lua)** | FiSH Blowfish Encryption | End-to-end Blowfish CBC/ECB encryption for PRIVMSG, NOTICE, and topic.<br>Commands: `/setkey`, `/setkey-ecb`, `/delkey`, `/key`, `/keyx`. Sidebar lock icons. |
| **[`fun.lua`](fun.lua)** | Dice Rolling & Toy Commands | Cryptographically fair dice rolls, magic 8-ball answers, and slap action.<br>Commands: `/roll`, `/8ball`, `/slap`. |
| **[`greet.lua`](greet.lua)** | Command & Message Filter Example | Example plugin demonstrating slash commands (`/greet`) and message filtering. |
| **[`highlight_reply.lua`](highlight_reply.lua)** | Trigger Word Auto-Reply | Configurable trigger word auto-reply system.<br>Commands: `/hlreply`. |
| **[`ignore.lua`](ignore.lua)** | Engine-Level Per-Nick Ignore | Server-side ignore filtering messages before storage or notifications.<br>Commands: `/ignore`, `/unignore`. Integrated into Web UI context menu. |
| **[`nickserv.lua`](nickserv.lua)** | NickServ Auto-Identify & Ghost | Automates NickServ authentication and nick reclaiming on connect.<br>Commands: `/nickserv`. |
| **[`qauth.lua`](qauth.lua)** | QuakeNet Q Authentication | QuakeNet Q CHALLENGEAUTH/HMAC-SHA256 authentication on connect.<br>Commands: `/qauth`. |
| **[`sed.lua`](sed.lua)** | Typo Correction (`s/foo/bar/`) | Re-sends your last line with substitutions applied using `s/pattern/replacement/` syntax. |
| **[`title.lua`](title.lua)** | URL Page Title Announcer | Fetches page titles for posted HTTP/HTTPS links and prints them locally.<br>Commands: `/title`. |
| **[`urls.lua`](urls.lua)** | Channel URL History & Logger | Scrapes and stores recent URLs per channel in persistent storage.<br>Commands: `/urls`. |
| **[`watch.lua`](watch.lua)** | Nick Watcher & Last-Seen | Tracks presence of watched nicks and logs when they join, part, or talk.<br>Commands: `/watch`, `/unwatch`. |
| **[`webhooks.lua`](webhooks.lua)** | Outbound Webhook Forwarder | Forwards highlights & mentions to Discord, Slack, Ntfy, or HTTP webhooks.<br>Commands: `/webhook`. |

---

## 📝 Writing Your Own Plugins

Plugins are written in Lua and live in `$STUGAN_HOME/scripts/`. For detailed guide and full Lua API documentation, see [docs/plugins.md](../docs/plugins.md).
