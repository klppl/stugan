---
title: "Subsystem: internal/server (HTTP, WebSocket & Multi-Tenant Hub)"
description: "HTTP static asset delivery, WebSocket typed event routing, bcrypt session authentication, rate limiting, and reverse proxy trust."
type: "subsystem"
tags:
  - "subsystem"
  - "server"
  - "websocket"
  - "http"
  - "auth"
---

# Subsystem: `internal/server`

`internal/server` implements the HTTP web server and real-time WebSocket communication layer using [`github.com/coder/websocket`](https://github.com/coder/websocket). It bridges frontend browser sessions to their respective backend `core.Engine` instances.

## Multi-Tenant Hub Architecture

The server supports both single-user and multi-user configurations via `server.Hub`.

```mermaid
graph TD
    Client1["Browser Session A (User: Alice)"] -->|"WebSocket /ws"| Hub["server.Hub"]
    Client2["Browser Session B (User: Bob)"] -->|"WebSocket /ws"| Hub
    Client3["Browser Session C (User: Alice)"] -->|"WebSocket /ws"| Hub

    subgraph User Tenants
        Hub -->|"Cookie Auth / Session"| TenantAlice["Tenant: Alice<br/>Engine A + SQLite A"]
        Hub -->|"Cookie Auth / Session"| TenantBob["Tenant: Bob<br/>Engine B + SQLite B"]
    end

    TenantAlice --> FanoutAlice["userSink: Broadcast to Sessions A & C"]
    TenantBob --> FanoutBob["userSink: Broadcast to Session B"]
```

## Security & Authentication Features

- **Bcrypt Password Auth**: Users authenticate via `/api/login`, receiving secure `HttpOnly`, `SameSite=Lax` session cookies.
- **Optional Magic Word Gate (`$STUGAN_WEB_PASSWORD`)**: Configurable site-wide password gate required before accessing login forms or static assets.
- **Login Rate Limiting**: Exponential login throttling keyed by client IP.
- **Reverse Proxy Trust (`trusted_proxies`)**: Safely resolves real client IPs from `X-Forwarded-For` / `X-Real-IP` only when requests originate from configured CIDR proxy ranges.
- **File Upload & Inline Media Proxy**:
  - Secure drag-and-drop file upload endpoint (`/api/upload`) storing files in `<data>/uploads` or forwarding to external hosts.
  - Media preview proxy (`/api/media`) verifying URL schemes and content types with strict size limits to protect client privacy.
- **Web Push Notifications**: Web Push subscriptions (`/api/push`) and VAPID key distribution.

## WebSocket Wire Protocol Handler (`route.go`)

Incoming WebSocket frames are decoded as `{t: string, id: string, d: any}`:
1. Validates envelope structure and authenticates connection session.
2. Dispatches payload to target engine method (`SendInput`, `AddNetworkLive`, `EditNetworkLive`, `RemoveNetworkLive`, `SendReaction`, `RedactMessage`, `MarkRead`, `Search`).
3. Sends synchronous acknowledgment or asynchronous reply frames carrying matching request `id`.

## Related Concepts

- [Subsystem: `cmd/stugan`](cmd_stugan.md)
- [Wire Protocol (`internal/proto`)](internal_proto_client_proto.md)
- [Vue 3 Frontend (`client/`)](client.md)
