---
name: add-proto-event
description: Scaffold a new wire-protocol event across all four hand-synced surfaces (proto.go struct + discriminator, events.ts mirror, server.route c2s handler and/or connection.ts onFrame s2c handler). Use when adding a new message type to the daemon↔browser protocol.
disable-model-invocation: true
---

# Add a wire-protocol event

stugan's protocol is defined once in Go and mirrored by hand in three other
places. Adding an event touches all of them. Follow this recipe exactly; the
order matters because `internal/proto/proto.go` is the source of truth.

## Inputs

Ask the user (if `$ARGUMENTS` doesn't already say):
- **Event name** (Go-ish, e.g. `PinMessage`).
- **Discriminator** string, `domain:verb` convention (e.g. `pin:set`).
- **Direction**: c2s (browser→daemon), s2c (daemon→browser), or both.
- **Fields**: name, type, whether optional.

## Steps

### 1. `internal/proto/proto.go` — source of truth

- Add a `T*` const to the discriminator block, with a `// c2s` / `// s2c`
  comment matching the direction (the auditor and dispatch checks rely on
  these comments).
- Add the payload struct, one field per input, with `json:"..."` tags.
  Optional fields get `,omitempty`. Match the existing DTO style (see
  `MessageDTO`, `NetConfig`).

### 2. `client/src/proto/events.ts` — TS mirror

- Add the discriminator to the `T` const object (same string value).
- Add an `interface` mirroring the struct: every Go field keyed by its json
  tag; `omitempty` → optional `?`; types map as
  `[]string`→`string[]`, `map[string]string`→`Record<string,string>`,
  `int`→`number`, `bool`→`boolean`.

### 3. Wire the handlers

- **c2s**: add a `case proto.T<Name>:` in `func (s *Server) route(...)` in
  `internal/server/server.go`. Decode `env.D` into the struct (follow the
  sibling cases), validate, then call into the engine
  (`Engine.SendInput`/`AddNetworkLive`/etc.). If it produces an s2c reply,
  build a frame carrying `env.ID` to correlate.
- **s2c**: if the daemon originates this event, decide its source — a
  `core.Sink` method (committed, fan-out to all of a user's connections) or a
  direct reply frame. Then add a `case T.<Name>:` in `onFrame(env)` in
  `client/src/connection.ts` that decodes `env.d` and updates the reactive
  store. (If it's a new Sink method, that's a bigger change — see
  CLAUDE.md's "Adding a Sink method means updating every implementer";
  consider running the core-boundary-reviewer subagent after.)

### 4. Verify

```sh
gofmt -w internal/proto/proto.go internal/server/server.go
go build ./... && go vet ./...
cd client && npm run typecheck
```

Then run the **proto-mirror-auditor** subagent to confirm all four surfaces
agree, and document the event in `docs/protocol.md`.
