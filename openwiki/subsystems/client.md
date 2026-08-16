---
title: "Subsystem: client (Vue 3 Single Page Application)"
description: "Vue 3 frontend architecture, Pinia state stores, virtualized message lists, link previews, inline media proxy, and theming."
type: "subsystem"
tags:
  - "subsystem"
  - "frontend"
  - "vue"
  - "typescript"
  - "pinia"
---

# Subsystem: `client` (Vue 3 Frontend)

The `client/` directory contains the single-page web application written in Vue 3, TypeScript, and Vite. The frontend communicates with the Go backend exclusively over WebSocket via typed protocol frames.

## Frontend Directory Structure

```
client/src/
├── assets/          # CSS themes, icons, fonts
├── components/      # Reusable UI components
│   ├── chat/        # MessageList, MessageItem, ChatInput, NickList, TopicBar
│   ├── sidebar/     # NetworkTree, BufferItem, UserStatus, NetworkModal
│   ├── settings/    # SettingsModal, PluginManager, ThemePicker
│   └── modals/      # SearchModal, ChannelBrowser, HelpModal
├── composables/     # Vue composables (useAutocomplete, useMediaPreview, useNotifications)
├── proto/           # TypeScript wire protocol mirror (events.ts)
├── services/        # WebSocket client (connection.ts), Audio/Push notification service
├── stores/          # Pinia stores (chat, networks, ui, settings, plugins)
├── views/           # Top-level views (ChatView, LoginView)
├── App.vue          # Root component
└── main.ts          # Application bootstrap
```

## Key State Management Stores (Pinia)

- **`useNetworksStore`**: Manages network configurations, channel lists, user nicknames, connection states, and unread counts.
- **`useChatStore`**: Buffers messages per active buffer, handles infinite scrolling backlog requests, and tracks live typing/reaction states.
- **`useUIStore`**: Manages modals (Channel list, Search, Settings), mobile drawer toggles, active buffer selection, and sidebar state.
- **`useSettingsStore`**: Manages user preferences, sound alerts, highlight phrases, timestamp formats, and active theme CSS.
- **`usePluginStore`**: Manages plugin listings, settings forms, and installation state.

## Core UI Subsystems

### 1. Message Backlog & Virtualization
To ensure 60fps performance with tens of thousands of messages, message buffers utilize windowed rendering. As users scroll up, `useChatStore` dispatches `backlog` requests to fetch previous chunks from the backend SQLite store.

### 2. Auto-Complete Engine (`useAutocomplete`)
Provides intelligent inline tab completions for:
- Channel member nicknames (weighted by recent chat activity).
- Slash commands (built-ins + active Lua plugin commands).
- Channel names across the active network.
- Emoji shortcodes (`:smile:`, `:rocket:`).

### 3. Media & Link Previews
URLs detected in messages are passed through the local `/api/media` backend proxy to fetch OpenGraph metadata and image/video previews safely without leaking user IP addresses to third parties.

### 4. Custom Themes & PWA
Themes are defined with CSS custom properties. The client is fully installable as a Progressive Web App (PWA) with offline shell caching and desktop/Web Push notification support.

## Related Concepts

- [Wire Protocol & Schema Synchronization](internal_proto_client_proto.md)
- [Server & WebSockets (`internal/server`)](internal_server.md)
