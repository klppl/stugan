---
okf_version: "0.1"
title: "Stugan Knowledge Wiki"
description: "Comprehensive documentation index for the stugan codebase, covering architecture, subsystems, event flows, and plugin extensions."
---

# Stugan Knowledge Wiki

Welcome to the OpenWiki knowledge repository for **stugan**, a self-hosted, plugin-extensible web IRC client written in Go with a Vue 3 frontend, SSH Terminal UI, and Lua plugin runtime.

## Quickstart

- [Quickstart & Getting Started](quickstart.md) — High-level overview, feature summary, and developer workflows.

## Architecture

- [Architecture Overview](architecture/overview.md) — System boundaries, dependency inversion, and package import rules.
- [Core Domain & Interfaces](architecture/core.md) — Central `internal/core` domain models, interfaces (`IRCConn`, `PluginHost`, `Sink`), and state encapsulation.
- [Concurrency & Event Model](architecture/concurrency_event_model.md) — The two-sided bus, serial engine loop, synchronous plugin dispatch, and Sink fan-out.

## Subsystems

- [Subsystem: Composition Root (`cmd/stugan`)](subsystems/cmd_stugan.md) — Application startup, flag parsing, multi-tenant wiring, and graceful shutdown.
- [Subsystem: IRC Protocol & Capabilities (`internal/irc`)](subsystems/internal_irc.md) — `girc` connection encapsulation, SASL authentication, and IRCv3 capability negotiation.
- [Subsystem: Lua Plugin Runtime (`internal/plugin`)](subsystems/internal_plugin.md) — GopherLua engine, worker thread isolation, `stugan.*` sandboxed API, and hot reloading.
- [Subsystem: SQLite Persistence & FTS5 (`internal/store`)](subsystems/internal_store.md) — Embedded SQLite storage, WAL mode, FTS5 full-text search, and database isolation.
- [Subsystem: Server & Hub (`internal/server`)](subsystems/internal_server.md) — WebSocket routing, HTTP asset serving, bcrypt auth, session cookies, and reverse proxy trust.
- [Subsystem: SSH Terminal UI (`internal/tui`)](subsystems/internal_tui.md) — Wish SSH daemon, Bubble Tea full-screen interface, and keyboard shortcuts.
- [Subsystem: Wire Protocol & Event Synchronization](subsystems/internal_proto_client_proto.md) — Typed JSON envelope `{t, id, d}` and Go/TypeScript schema synchronization.
- [Subsystem: Vue 3 Frontend (`client/`)](subsystems/client.md) — Single-page app architecture, Pinia stores, virtualized lists, and PWA capabilities.
- [Subsystem: Official Plugin Library (`plugins/`)](subsystems/plugins.md) — Curated Lua script catalog (`ai.lua`, `fish.lua`, `webhooks.lua`, `away.lua`, etc.).
