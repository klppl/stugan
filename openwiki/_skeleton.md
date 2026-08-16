---
type: "Reference"
title: "OpenWiki Skeleton for Stugan Documentation"
openwiki_generated: true
---

# OpenWiki Skeleton for Stugan Documentation

## Overview
This skeleton outlines the structure of the documentation to be written for the `stugan` project. It establishes a comprehensive guide to its architecture, subsystems, and workflows, ensuring developers and agents can move efficiently and effectively.

## Sections
- **Quickstart**: High-level introduction to the repository, including main sections, concepts, APIs, and a map of how to navigate the wiki.
- **Architecture**
  - Overview
    - Describe the high-level architectural concepts: strict dependency decoupling, the two-sided bus, concurrency model, and other principles.
    - Include diagrams explaining module dependency flow, inbound/outbound processing, and hub architecture.
  - Core (`internal/core`)
    - Document the core interfaces (`IRCConn`, `NetworkStore`, etc.) and their contracts.
    - Principles of dependency inversion.
  - Concurrency and Event System
    - Explain the engine loop, hooks, state mutations, and Sink implementations.
- **Subsystems**
  - **cmd/stugan**: Document application entry, CLI flags, and context-based graceful shutdown.
  - **internal/irc**: Describe IRC-specific modules, SASL authentication, IRCv3 capabilities, and girc integration.
  - **internal/plugin**: Coverage of the Lua plugin runtime, sandboxing, scripting interface, and persistence.
  - **internal/store**: SQLite database handling, WAL mode, FTS5, backlog queries, and multi-tenant isolation.
  - **internal/server**: Explain the HTTP/WebSocket server, authentication, rate-limiting, and multi-tenant hub mechanics.
  - **internal/tui**: SSH terminal UI powered by Wish/Bubble Tea, its commands, and design considerations.
  - **internal/proto and client/src/proto**: Protocol definition synchronization between Go and TypeScript.
  - **client/**: Vue 3 frontend architecture, virtualized message lists, Pinia state management, and customization features.
  - **plugins/**: Library of ready-to-use Lua scripts.

## Diagrams
1. Module dependency flowchart: Visualize unidirectional dependability focused on `internal/core`.
2. Inbound message processing sequence.
3. Outbound WebSocket action sequence.
4. Hub multi-tenant architecture component diagram.

## Deliverables
Each page noted in the directory structure below will include clear documentation covering responsibilities, owning entrypoints, important symbols, relationships, invariants, tests, and lifecycle ordering. The following subsections outline the pages and their anticipated content.

---

## Directory Structure and Content Plan

### /openwiki/quickstart.md
- **Description**: High-level entry point to the repository's wiki documentation. Summarizes major architectural principles, systems, and workflows.

### /openwiki/architecture/
#### overview.md
- **Description**: High-level architectural concepts, strict dependency decoupling, and concurrency model.
- **Diagrams**: Dependency flowchart.
#### core.md
- **Description**: Core interfaces (`IRCConn`, `NetworkStore`) and contracts. Details the principles of dependency inversion.
#### concurrency_event_model.md
- **Description**: The engine loop's mechanics, hooks, state mutations, and Sink implementations.
- **Diagrams**: Sequence diagram for inbound and outbound flows.

### /openwiki/subsystems/
#### cmd_stugan.md
- **Description**: Application entry point, CLI flags, and graceful shutdown with the root context.
#### internal_irc.md
- **Description**: IRC modules, SASL (PLAIN, CertFP), IRCv3 capabilities, and `girc` integration.
#### internal_plugin.md
- **Description**: Lua plugin engine, sandboxing, hot-reloading, and persistent KV store.
#### internal_store.md
- **Description**: SQLite storage, WAL mode, FTS5, backlog queries, and database isolation.
#### internal_server.md
- **Description**: HTTP/WebSocket server functionalities, authentication, and hub architecture.
- **Diagrams**: Hub multi-tenant component diagram.
#### internal_tui.md
- **Description**: SSH terminal UI functionalities, commands, and design.
#### internal_proto_client_proto.md
- **Description**: Typed protocol definition synchronization between Go and TypeScript.
#### client.md
- **Description**: Vue frontend, message list virtualization, Pinia state stores, and customization.
#### plugins.md
- **Description**: Document Lua script library.

---

Pending skeleton review before drafting. Next step involves invoking the `skeleton_critic` subagent to validate this plan.