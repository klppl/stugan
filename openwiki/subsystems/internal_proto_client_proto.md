---
title: "Subsystem: Wire Protocol & Event Synchronization"
description: "Specification of the JSON WebSocket wire envelope and bidirectional schema synchronization between internal/proto and client/src/proto."
type: "subsystem"
tags:
  - "subsystem"
  - "protocol"
  - "websocket"
  - "typescript"
---

# Subsystem: Wire Protocol & Event Synchronization

The communication between the Go daemon backend and the Vue 3 frontend is defined by a strictly typed JSON wire protocol. The canonical schema is defined in Go in [`internal/proto`](file:///Users/alex/GitHub/stugan/internal/proto) and mirrored in TypeScript in [`client/src/proto/events.ts`](file:///Users/alex/GitHub/stugan/client/src/proto/events.ts).

## The Wire Envelope

Every WebSocket message exchanged over `/ws` uses a three-field envelope:

```json
{
  "t": "msg",
  "id": "req-12345",
  "d": {
    "network": "libera",
    "buffer": "#go",
    "seq": 1042,
    "time": 1773752400,
    "from": "alice",
    "kind": "privmsg",
    "text": "Hello world!"
  }
}
```

- **`t` (`string`)**: Message type discriminator.
- **`id` (`string`, optional)**: Correlation ID matching client requests to server responses.
- **`d` (`any`)**: Type-specific data payload.

## Event Schema Mapping

| Direction | Event Type (`t`) | Description | Payload Struct (Go / TS) |
| :--- | :--- | :--- | :--- |
| **s2c** | `init` | Initial full state snapshot upon connecting. | `InitPayload` |
| **s2c** | `msg` | Real-time chat message broadcast. | `MsgPayload` |
| **s2c** | `net:changed` | Network connection or structural change. | `NetworkPayload` |
| **s2c** | `net:removed` | Network removed from user session. | `NetworkRemovedPayload` |
| **s2c** | `typing` | Ephemeral typing indicator. | `TypingPayload` |
| **s2c** | `react` | Message emoji reaction added/removed. | `ReactionPayload` |
| **s2c** | `redact` | Message redaction event. | `RedactPayload` |
| **c2s** | `input` | User chat line or slash command. | `InputPayload` |
| **c2s** | `backlog` | Request historical message page. | `BacklogReq` / `BacklogResp` |
| **c2s** | `search` | Full-text message search query. | `SearchReq` / `SearchResp` |
| **c2s** | `mark_read` | Updates read marker position. | `MarkReadPayload` |
| **c2s** | `net:add` / `edit` / `remove` | Network management mutations. | `NetAddReq` / `NetEditReq` |

## Schema Synchronization Rule

When introducing a new event type or modifying fields:
1. Define the Go struct in [`internal/proto`](file:///Users/alex/GitHub/stugan/internal/proto).
2. Update the TypeScript interfaces in [`client/src/proto/events.ts`](file:///Users/alex/GitHub/stugan/client/src/proto/events.ts).
3. Implement the Go router in [`internal/server/route.go`](file:///Users/alex/GitHub/stugan/internal/server/route.go).
4. Implement the frontend dispatcher in [`client/src/services/connection.ts`](file:///Users/alex/GitHub/stugan/client/src/services/connection.ts).

## Related Concepts

- [Server & Hub (`internal/server`)](internal_server.md)
- [Vue 3 Client Architecture](client.md)
