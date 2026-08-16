---
title: "Subsystem: Official Plugin Library (plugins/)"
description: "Catalog of official Lua plugins, including AI assistant, FiSH encryption, outbound webhooks, and automation utilities."
type: "subsystem"
tags:
  - "subsystem"
  - "plugins"
  - "catalog"
  - "lua"
---

# Subsystem: Official Plugin Library (`plugins/`)

`stugan` provides a rich library of official Lua scripts located in [`plugins/`](file:///Users/alex/GitHub/stugan/plugins). These scripts showcase the `stugan.*` Lua API and can be installed on-demand in chat using `/load <script-name>`.

## Curated Plugin Catalog

| Plugin | Script | Description & Commands |
| :--- | :--- | :--- |
| **AI Assistant** | [`ai.lua`](file:///Users/alex/GitHub/stugan/plugins/ai.lua) | Integrates with OpenAI, Claude, DeepSeek, Gemini, and Ollama. Commands: `/ask <prompt>`, `/summarize [N]`. |
| **FiSH Encryption** | [`fish.lua`](file:///Users/alex/GitHub/stugan/plugins/fish.lua) | Blowfish CBC/ECB encryption for private queries and secret channels. Commands: `/setkey <pass>`, `/delkey`. |
| **Outbound Webhooks** | [`webhooks.lua`](file:///Users/alex/GitHub/stugan/plugins/webhooks.lua) | Forwards highlights and mentions to Discord, Slack, Ntfy, or custom HTTP webhooks. |
| **URL Title Fetcher** | [`title.lua`](file:///Users/alex/GitHub/stugan/plugins/title.lua) | Automatically fetches and prints HTML titles for URLs pasted in chat channels. |
| **Typo Sed Correction** | [`sed.lua`](file:///Users/alex/GitHub/stugan/plugins/sed.lua) | Performs `s/find/replace/` regex substitutions on recent chat lines. |
| **Auto-Away** | [`away.lua`](file:///Users/alex/GitHub/stugan/plugins/away.lua) | Automatically toggles IRC `/away` status based on inactivity timers. |
| **NickServ Auto-Identify** | [`nickserv.lua`](file:///Users/alex/GitHub/stugan/plugins/nickserv.lua) | Identifies with NickServ upon connecting to networks that lack SASL support. |
| **URL Collector** | [`urls.lua`](file:///Users/alex/GitHub/stugan/plugins/urls.lua) | Maintains a recent history buffer of shared URLs with `/urls [N]`. |
| **Nick Watcher** | [`watch.lua`](file:///Users/alex/GitHub/stugan/plugins/watch.lua) | Monitors when specific friends or operators join, part, or change nick. |
| **Fun Commands** | [`fun.lua`](file:///Users/alex/GitHub/stugan/plugins/fun.lua) | Slap, roll dice, 8ball, coin flip, and ASCII art helpers. |

## Plugin Installation & Management

- **Installation**: Run `/load <name>` (e.g. `/load ai`) in the chat input. The daemon automatically downloads the script from the repository into `$STUGAN_HOME/scripts/` and activates it live.
- **Web UI Management**: Open **Settings → Plugins** in the Vue client to view installed and available plugins, inspect error counts, and configure script settings.
- **Local Script Development**: Any custom `*.lua` script dropped into `$STUGAN_HOME/scripts/` is automatically detected and hot-reloaded by the plugin host.

## Related Concepts

- [Plugin Runtime Subsystem (`internal/plugin`)](internal_plugin.md)
- [Core Domain & Interfaces](../architecture/core.md)
